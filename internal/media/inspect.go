// Package media holds the media operations of musafoz. It does not depend
// on any user interface.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNoVideoStream reports an input without a real video track.
var ErrNoVideoStream = errors.New("no video stream found")

// ErrUnknownDuration reports a video whose duration is unknown or not
// positive. Inspect accepts such files; processing operations do not.
var ErrUnknownDuration = errors.New("duration is unknown or not positive")

// RequireDuration returns the duration of info, or ErrUnknownDuration.
func RequireDuration(info *Info) (float64, error) {
	if info.DurationSeconds == nil || *info.DurationSeconds <= 0 {
		return 0, ErrUnknownDuration
	}
	return *info.DurationSeconds, nil
}

// InspectError wraps a failure with the operation and the input path.
type InspectError struct {
	Path string
	Err  error
}

func (e *InspectError) Error() string {
	return fmt.Sprintf("inspect %s: %v", e.Path, e.Err)
}

func (e *InspectError) Unwrap() error { return e.Err }

// Info is the result of inspecting a video file.
type Info struct {
	Path            string       `json:"path"`
	SizeBytes       int64        `json:"size_bytes"`
	Format          string       `json:"format"`
	FormatLongName  string       `json:"format_long_name"`
	DurationSeconds *float64     `json:"duration_seconds"`
	DurationSource  string       `json:"duration_source"`
	BitRate         *int64       `json:"bit_rate"`
	Video           VideoStream  `json:"video"`
	Audio           *AudioStream `json:"audio"`
	Streams         StreamCounts `json:"streams"`
	// CodecTags lists each stream's codec tag in index order, for example
	// "avc1" or "mebx". It is used to check remuxes and is not printed.
	CodecTags []string `json:"-"`
	// Tags are the container tags, such as creation_time and the QuickTime
	// creation date and location. Raw is the whole FFprobe JSON. Neither is
	// printed by inspect; web metadata files keep them.
	Tags map[string]string `json:"-"`
	Raw  json.RawMessage   `json:"-"`
}

// Duration sources.
const (
	DurationFromContainer = "container"
	DurationFromVideo     = "video_stream"
	DurationUnknown       = "unknown"
)

// VideoStream describes the primary video stream.
type VideoStream struct {
	Index         int    `json:"index"`
	Codec         string `json:"codec"`
	CodecLongName string `json:"codec_long_name"`
	Profile       string `json:"profile,omitempty"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Rotation      int    `json:"rotation"`
	// SampleAspectRatio is the pixel aspect ratio as num:den, "1:1" for
	// square pixels.
	SampleAspectRatio string `json:"sample_aspect_ratio"`
	// DisplayWidth and DisplayHeight are the frame size as shown: the
	// width scaled by the sample aspect ratio, then swapped for 90 or 270
	// degree rotations.
	DisplayWidth  int      `json:"display_width"`
	DisplayHeight int      `json:"display_height"`
	PixelFormat   string   `json:"pixel_format,omitempty"`
	FrameRate     *float64 `json:"frame_rate"`
	FrameRateRaw  string   `json:"frame_rate_raw,omitempty"`
	FrameCount    *int64   `json:"frame_count"`
	BitRate       *int64   `json:"bit_rate"`
	// DurationSeconds is the video stream duration, which can be shorter
	// than the container duration.
	DurationSeconds *float64 `json:"duration_seconds"`
	// TimeBase is the stream time base, for example "1/60".
	TimeBase string `json:"time_base,omitempty"`
	// ColorTransfer, ColorPrimaries, and ColorSpace are the coded color
	// description, such as "arib-std-b67", "bt2020", and "bt2020nc" for HLG
	// HDR video. They are not printed by inspect.
	ColorTransfer  string `json:"-"`
	ColorPrimaries string `json:"-"`
	ColorSpace     string `json:"-"`
}

// HDR reports a PQ (smpte2084) or HLG (arib-std-b67) transfer.
func (v VideoStream) HDR() bool {
	return v.ColorTransfer == "smpte2084" || v.ColorTransfer == "arib-std-b67"
}

// Timescale returns the denominator of a 1/N time base, or 0.
func (v VideoStream) Timescale() int {
	num, den, ok := strings.Cut(v.TimeBase, "/")
	if !ok || num != "1" {
		return 0
	}
	n, err := strconv.Atoi(den)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// DisplayAspect returns the width to height ratio of the video as shown,
// after applying the sample aspect ratio and the rotation metadata. It is 0
// when the dimensions are unknown.
func (v VideoStream) DisplayAspect() float64 {
	if v.Width <= 0 || v.Height <= 0 {
		return 0
	}
	num, den := parseSAR(v.SampleAspectRatio)
	aspect := float64(v.Width) * float64(num) / (float64(v.Height) * float64(den))
	if v.Rotation%180 != 0 {
		aspect = 1 / aspect
	}
	return aspect
}

// parseSAR reads a num:den sample aspect ratio. Missing, zero, or invalid
// values mean square pixels.
func parseSAR(s string) (int, int) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return 1, 1
	}
	num, err1 := strconv.Atoi(a)
	den, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || num <= 0 || den <= 0 {
		return 1, 1
	}
	return num, den
}

// LastFrameTime estimates the start time of the last video frame: the video
// stream duration (or the container duration) minus one frame. Seeking past
// it decodes nothing.
func (info *Info) LastFrameTime() float64 {
	end := 0.0
	if info.DurationSeconds != nil {
		end = *info.DurationSeconds
	}
	if d := info.Video.DurationSeconds; d != nil && (end == 0 || *d < end) {
		end = *d
	}
	step := 0.1
	if fps := info.Video.FrameRate; fps != nil && *fps > 0 {
		step = 1 / *fps
	}
	return math.Max(0, end-step)
}

// AudioStream describes the first audio stream.
type AudioStream struct {
	Index         int    `json:"index"`
	Codec         string `json:"codec"`
	CodecLongName string `json:"codec_long_name"`
	Channels      int    `json:"channels"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	SampleRate    int    `json:"sample_rate"`
}

// StreamCounts counts streams by kind. Attached pictures, such as cover
// art, are not counted as video.
type StreamCounts struct {
	Video           int `json:"video"`
	Audio           int `json:"audio"`
	Subtitle        int `json:"subtitle"`
	AttachedPicture int `json:"attached_picture"`
	Other           int `json:"other"`
}

// ValidateInput checks that path names a readable regular file.
func ValidateInput(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &InspectError{Path: path, Err: errors.New("file does not exist")}
		}
		return &InspectError{Path: path, Err: err}
	}
	if fi.IsDir() {
		return &InspectError{Path: path, Err: errors.New("is a directory, expected a video file")}
	}
	if !fi.Mode().IsRegular() {
		return &InspectError{Path: path, Err: errors.New("is not a regular file")}
	}
	f, err := os.Open(path)
	if err != nil {
		return &InspectError{Path: path, Err: err}
	}
	return f.Close()
}

// Inspect runs ffprobe on path and returns its video information.
func Inspect(ctx context.Context, ffprobePath, path string) (*Info, error) {
	if err := ValidateInput(path); err != nil {
		return nil, err
	}
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"--", path)
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, &InspectError{Path: path, Err: ctx.Err()}
		}
		msg := strings.TrimSpace(errOut.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, &InspectError{Path: path, Err: fmt.Errorf("ffprobe could not read the file: %s", msg)}
	}
	info, err := ParseProbeJSON(out.Bytes())
	if err != nil {
		return nil, &InspectError{Path: path, Err: err}
	}
	info.Path = path
	if fi, err := os.Stat(path); err == nil {
		info.SizeBytes = fi.Size()
	}
	return info, nil
}

type probeOutput struct {
	Streams []probeStream `json:"streams"`
	Format  *probeFormat  `json:"format"`
}

type probeFormat struct {
	Tags           map[string]string `json:"tags"`
	FormatName     string            `json:"format_name"`
	FormatLongName string            `json:"format_long_name"`
	Duration       string            `json:"duration"`
	Size           string            `json:"size"`
	BitRate        string            `json:"bit_rate"`
}

type probeStream struct {
	Index          int               `json:"index"`
	CodecType      string            `json:"codec_type"`
	CodecName      string            `json:"codec_name"`
	CodecLongName  string            `json:"codec_long_name"`
	CodecTag       string            `json:"codec_tag_string"`
	Profile        string            `json:"profile"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	PixFmt         string            `json:"pix_fmt"`
	SampleAspect   string            `json:"sample_aspect_ratio"`
	AvgFrameRate   string            `json:"avg_frame_rate"`
	RFrameRate     string            `json:"r_frame_rate"`
	TimeBase       string            `json:"time_base"`
	Duration       string            `json:"duration"`
	NbFrames       string            `json:"nb_frames"`
	BitRate        string            `json:"bit_rate"`
	SampleRate     string            `json:"sample_rate"`
	Channels       int               `json:"channels"`
	ChannelLayout  string            `json:"channel_layout"`
	Disposition    map[string]int    `json:"disposition"`
	Tags           map[string]string `json:"tags"`
	ColorTransfer  string            `json:"color_transfer"`
	ColorPrimaries string            `json:"color_primaries"`
	ColorSpace     string            `json:"color_space"`
	SideDataList   []struct {
		SideDataType string   `json:"side_data_type"`
		Rotation     *float64 `json:"rotation"`
	} `json:"side_data_list"`
}

// stillImageFormats are demuxers that read single images, not video.
// Animated formats such as GIF are accepted as video.
var stillImageFormats = map[string]bool{
	"image2":      true,
	"webp_pipe":   true,
	"png_pipe":    true,
	"jpeg_pipe":   true,
	"bmp_pipe":    true,
	"tiff_pipe":   true,
	"jpegxl_pipe": true,
}

// ParseProbeJSON converts ffprobe JSON output into Info. It fails with
// ErrNoVideoStream when there is no real video track.
func ParseProbeJSON(data []byte) (*Info, error) {
	var p probeOutput
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	if p.Format == nil {
		return nil, errors.New("ffprobe output has no format section")
	}
	info := &Info{
		Format:         p.Format.FormatName,
		FormatLongName: p.Format.FormatLongName,
		BitRate:        parseInt(p.Format.BitRate),
		Tags:           p.Format.Tags,
		Raw:            append(json.RawMessage(nil), data...),
	}
	if size := parseInt(p.Format.Size); size != nil {
		info.SizeBytes = *size
	}

	var video *probeStream
	for i := range p.Streams {
		s := &p.Streams[i]
		info.CodecTags = append(info.CodecTags, s.CodecTag)
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 {
				info.Streams.AttachedPicture++
				continue
			}
			info.Streams.Video++
			if video == nil {
				video = s
			}
		case "audio":
			info.Streams.Audio++
			if info.Audio == nil {
				info.Audio = &AudioStream{
					Index:         s.Index,
					Codec:         s.CodecName,
					CodecLongName: s.CodecLongName,
					Channels:      s.Channels,
					ChannelLayout: s.ChannelLayout,
				}
				if sr := parseInt(s.SampleRate); sr != nil {
					info.Audio.SampleRate = int(*sr)
				}
			}
		case "subtitle":
			info.Streams.Subtitle++
		default:
			info.Streams.Other++
		}
	}
	if video == nil || stillImageFormats[firstFormatName(info.Format)] {
		return nil, ErrNoVideoStream
	}

	v := VideoStream{
		Index:           video.Index,
		Codec:           video.CodecName,
		CodecLongName:   video.CodecLongName,
		Profile:         video.Profile,
		Width:           video.Width,
		Height:          video.Height,
		Rotation:        rotation(video),
		PixelFormat:     video.PixFmt,
		FrameCount:      parseInt(video.NbFrames),
		BitRate:         parseInt(video.BitRate),
		DurationSeconds: parseDuration(video.Duration),
		TimeBase:        video.TimeBase,
		ColorTransfer:   video.ColorTransfer,
		ColorPrimaries:  video.ColorPrimaries,
		ColorSpace:      video.ColorSpace,
	}
	num, den := parseSAR(video.SampleAspect)
	v.SampleAspectRatio = fmt.Sprintf("%d:%d", num, den)
	v.DisplayWidth = int(math.Round(float64(v.Width) * float64(num) / float64(den)))
	v.DisplayHeight = v.Height
	if v.Rotation%180 != 0 {
		v.DisplayWidth, v.DisplayHeight = v.DisplayHeight, v.DisplayWidth
	}
	for _, raw := range []string{video.AvgFrameRate, video.RFrameRate} {
		if fps := parseRational(raw); fps != nil {
			v.FrameRate, v.FrameRateRaw = fps, raw
			break
		}
	}
	info.Video = v

	info.DurationSource = DurationUnknown
	if d := parseDuration(p.Format.Duration); d != nil {
		info.DurationSeconds, info.DurationSource = d, DurationFromContainer
	} else if d := parseDuration(video.Duration); d != nil {
		info.DurationSeconds, info.DurationSource = d, DurationFromVideo
	}
	return info, nil
}

func firstFormatName(names string) string {
	if i := strings.IndexByte(names, ','); i >= 0 {
		return names[:i]
	}
	return names
}

// rotation returns the clockwise display rotation in degrees, normalized
// to 0, 90, 180, or 270. FFprobe reports the display matrix rotation as
// counterclockwise degrees; older files may carry a "rotate" tag instead.
func rotation(s *probeStream) int {
	for _, sd := range s.SideDataList {
		if sd.SideDataType == "Display Matrix" && sd.Rotation != nil {
			return normalizeRotation(-int(math.Round(*sd.Rotation)))
		}
	}
	if r, err := strconv.Atoi(s.Tags["rotate"]); err == nil {
		return normalizeRotation(r)
	}
	return 0
}

func normalizeRotation(deg int) int {
	deg %= 360
	if deg < 0 {
		deg += 360
	}
	return deg
}

func parseDuration(s string) *float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return nil
	}
	return &f
}

func parseInt(s string) *int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

func parseRational(s string) *float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		return nil
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || n <= 0 || d <= 0 {
		return nil
	}
	f := n / d
	return &f
}
