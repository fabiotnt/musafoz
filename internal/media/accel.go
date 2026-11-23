package media

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
)

// Accel selects how media is decoded and encoded.
type Accel string

const (
	// AccelAuto uses hardware acceleration where it is implemented and a
	// probe confirms that it works for the input, and the CPU otherwise.
	AccelAuto Accel = "auto"
	// AccelCPU forces software decoding and encoding.
	AccelCPU Accel = "cpu"
)

// ParseAccel validates an acceleration mode name.
func ParseAccel(s string) (Accel, error) {
	switch Accel(s) {
	case AccelAuto, AccelCPU:
		return Accel(s), nil
	}
	return "", fmt.Errorf("unknown acceleration mode %q; use auto or cpu", s)
}

// Hardware backends. VideoToolbox is the only one implemented; other
// platforms use the CPU.
const (
	BackendCPU          = "cpu"
	BackendVideoToolbox = "videotoolbox"
)

// BackendLabel returns a display name for a backend.
func BackendLabel(b string) string {
	if b == BackendVideoToolbox {
		return "VideoToolbox"
	}
	return "CPU"
}

// DecodePath describes how frames are decoded.
type DecodePath struct {
	Backend string
	// Reason explains the choice, for the log.
	Reason string
	// swFormat is the pixel format hardware frames are downloaded to.
	swFormat string
	// rotation is the clockwise display rotation, applied explicitly on the
	// hardware path because FFmpeg does not rotate hardware frames.
	rotation int
}

// CPUDecode returns the software decode path with a reason.
func CPUDecode(reason string) DecodePath {
	return DecodePath{Backend: BackendCPU, Reason: reason}
}

// EncodePath describes how the preview video is encoded.
type EncodePath struct {
	Backend string
	Reason  string
	// bitrate is the target bit rate of the hardware encoder.
	bitrate int
}

// CPUEncode returns the libx264 encode path with a reason.
func CPUEncode(reason string) EncodePath {
	return EncodePath{Backend: BackendCPU, Reason: reason}
}

// Encoder returns the FFmpeg encoder name for the path.
func (e EncodePath) Encoder() string {
	if e.Backend == BackendVideoToolbox {
		return "h264_videotoolbox"
	}
	return "libx264"
}

// hardwareSWFormat maps a decoded pixel format to the format VideoToolbox
// frames are downloaded to. Formats it cannot carry return "".
func hardwareSWFormat(pixFmt string) string {
	switch pixFmt {
	case "yuv420p", "yuvj420p", "nv12":
		return "nv12"
	case "yuv420p10le", "p010le":
		return "p010le"
	}
	return ""
}

// ChooseDecode picks the frame decode path. In auto mode on macOS it first
// checks that FFmpeg lists VideoToolbox, then decodes two frames of the
// input with hardware frames kept on the GPU and downloaded explicitly. That
// probe fails instead of silently falling back to software, so success means
// the hardware decoder handled this input.
func ChooseDecode(ctx context.Context, ffmpeg, input string, info *Info, mode Accel, goos string) DecodePath {
	if mode == AccelCPU {
		return CPUDecode("forced by --accel cpu")
	}
	if goos != "darwin" {
		return CPUDecode("hardware decoding is only implemented on macOS")
	}
	accels, err := listHWAccels(ctx, ffmpeg)
	if err != nil {
		return CPUDecode("cannot list FFmpeg hardware acceleration methods: " + err.Error())
	}
	if !accels[BackendVideoToolbox] {
		return CPUDecode("FFmpeg does not list VideoToolbox")
	}
	sw := hardwareSWFormat(info.Video.PixelFormat)
	if sw == "" {
		return CPUDecode(fmt.Sprintf("VideoToolbox decoding is not used for %s %s", info.Video.Codec, info.Video.PixelFormat))
	}
	p := DecodePath{Backend: BackendVideoToolbox, swFormat: sw, rotation: info.Video.Rotation}
	err = runFFmpeg(ctx, ffmpeg, append(p.inputArgs(-1, input),
		"-map", "0:v:0", "-frames:v", "2", "-vf", p.hardwareFilters(), "-f", "null", "-")...)
	if err != nil {
		if ctx.Err() != nil {
			return CPUDecode("canceled")
		}
		return CPUDecode(fmt.Sprintf("VideoToolbox could not decode this %s input: %v", info.Video.Codec, err))
	}
	p.Reason = fmt.Sprintf("VideoToolbox decoded a %s test sample", info.Video.Codec)
	return p
}

// inputArgs returns the input options for one decode, seeking to at when
// at is not negative.
func (p DecodePath) inputArgs(at float64, input string) []string {
	var args []string
	if p.Backend == BackendVideoToolbox {
		args = append(args, "-noautorotate", "-hwaccel", "videotoolbox", "-hwaccel_output_format", "videotoolbox_vld")
	}
	if at >= 0 {
		args = append(args, "-ss", seconds(at))
	}
	return append(args, "-i", ffmpegPath(input))
}

// hardwareFilters downloads hardware frames and applies the rotation that
// FFmpeg applies automatically on the CPU path. It is empty for the CPU.
func (p DecodePath) hardwareFilters() string {
	if p.Backend != BackendVideoToolbox {
		return ""
	}
	f := "hwdownload,format=" + p.swFormat
	switch p.rotation {
	case 90:
		f += ",transpose=clock"
	case 180:
		f += ",hflip,vflip"
	case 270:
		f += ",transpose=cclock"
	}
	return f
}

// Workers returns the number of parallel ffmpeg processes for frame
// extraction: concurrency when positive, otherwise the CPU count, or half of
// it for VideoToolbox, whose decoder is shared and gained nothing from more
// processes in measurements.
func (p DecodePath) Workers(concurrency int) int {
	if concurrency > 0 {
		return concurrency
	}
	if p.Backend == BackendVideoToolbox {
		return max(1, runtime.NumCPU()/2)
	}
	return runtime.NumCPU()
}

// hardwareBitsPerPixel sets the VideoToolbox target bit rate. libx264 with
// its default quality used about 0.62 bits per pixel per frame for a 4K
// portrait phone clip at 4 fps.
const hardwareBitsPerPixel = 0.62

// ChooseEncode picks the preview encode path, independently of the decode
// path. In auto mode on macOS it checks that FFmpeg lists h264_videotoolbox
// and encodes a short test clip at the preview size with it; the encoder
// refuses to run without hardware by default. Any failed check selects
// libx264.
func ChooseEncode(ctx context.Context, ffmpeg string, mode Accel, goos string, width, height int) EncodePath {
	if mode == AccelCPU {
		return CPUEncode("forced by --accel cpu")
	}
	if goos != "darwin" {
		return CPUEncode("hardware encoding is only implemented on macOS")
	}
	encoders, err := listFFmpeg(ctx, ffmpeg, "-encoders")
	if err != nil || !encoders["h264_videotoolbox"] {
		return CPUEncode("FFmpeg does not list h264_videotoolbox")
	}
	// Test at the real preview size: the encoder rejects some sizes, such
	// as 256x256 on Intel Macs, that it would never see in practice.
	w, h := max(2, width/2*2), max(2, height/2*2)
	err = runFFmpeg(ctx, ffmpeg, "-f", "lavfi", "-i", fmt.Sprintf("color=c=gray:s=%dx%d:r=4:d=1", w, h),
		"-c:v", "h264_videotoolbox", "-pix_fmt", "yuv420p", "-f", "null", "-")
	if err != nil {
		if ctx.Err() != nil {
			return CPUEncode("canceled")
		}
		return CPUEncode("h264_videotoolbox failed a test encode: " + err.Error())
	}
	bitrate := int(math.Round(hardwareBitsPerPixel * float64(width*height) * 4))
	return EncodePath{
		Backend: BackendVideoToolbox,
		Reason:  "h264_videotoolbox passed a test encode",
		bitrate: max(bitrate, 500_000),
	}
}

func (e EncodePath) codecArgs() []string {
	if e.Backend == BackendVideoToolbox {
		return []string{"-c:v", "h264_videotoolbox", "-b:v", strconv.Itoa(e.bitrate)}
	}
	return []string{"-c:v", "libx264"}
}

// listHWAccels returns the methods listed by "ffmpeg -hwaccels".
func listHWAccels(ctx context.Context, ffmpeg string) (map[string]bool, error) {
	out, err := commandOutput(ctx, ffmpeg, "-hide_banner", "-hwaccels")
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.TrimSpace(line); f != "" && !strings.HasSuffix(f, ":") {
			names[f] = true
		}
	}
	return names, nil
}
