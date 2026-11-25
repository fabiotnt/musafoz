package media

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// WebVideo describes the H.264 MP4 that EncodeWeb writes.
type WebVideo struct {
	// MaxLong and MaxShort bound the displayed size: 1920 and 1080 give
	// 1080p, also for portrait video. Videos are never scaled up.
	MaxLong, MaxShort int
	// Bitrate caps the video bitrate in bits per second; quality is chosen
	// by CRF below that cap.
	Bitrate int
	// CRF is the x264 constant rate factor, 0 to 51.
	CRF int
	// AudioBitrate is the AAC bitrate in bits per second.
	AudioBitrate int
}

// DefaultWebVideo is 1080p H.264 at up to 5 Mbps with 128 kbps AAC, as
// streaming sites deliver 1080p.
var DefaultWebVideo = WebVideo{MaxLong: 1920, MaxShort: 1080, Bitrate: 5_000_000, CRF: 21, AudioBitrate: 128_000}

// WebSize returns the output size for a video displayed at w x h: scaled
// down to fit MaxLong x MaxShort in its own orientation, never up, with
// even sides as yuv420p requires (rounded down).
func (o WebVideo) WebSize(w, h int) (int, int) {
	long, short := max(w, h), min(w, h)
	scale := math.Min(1, math.Min(float64(o.MaxLong)/float64(long), float64(o.MaxShort)/float64(short)))
	// Round down to even, so an odd side is never enlarged.
	even := func(v int) int { return max(2, int(float64(v)*scale+1e-9)/2*2) }
	return even(w), even(h)
}

// WebFilter returns the FFmpeg video filter that turns the decoded,
// already rotated frames of v into w x h BT.709 SDR frames with square
// pixels, tagged as BT.709 (setparams, because encoders take the color
// tags of the frames). PQ and HLG video is tone mapped from BT.2020 to BT.709 with the
// Hable curve; other video is scaled and converted to the BT.709 matrix.
func WebFilter(v VideoStream, w, h int) string {
	scale := fmt.Sprintf("scale=%d:%d:flags=lanczos", w, h)
	if !v.HDR() {
		return scale + ":out_color_matrix=bt709:out_range=tv,setsar=1,format=yuv420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv"
	}
	prim := v.ColorPrimaries
	if prim == "" || prim == "unknown" {
		prim = "bt2020"
	}
	matrix := v.ColorSpace
	if matrix == "" || matrix == "unknown" {
		matrix = "bt2020nc"
	}
	return scale + ",setsar=1," +
		fmt.Sprintf("zscale=tin=%s:pin=%s:min=%s:t=linear:npl=100,format=gbrpf32le,", v.ColorTransfer, prim, matrix) +
		"zscale=p=bt709,tonemap=tonemap=hable:desat=0," +
		"zscale=t=bt709:m=bt709:r=tv,format=yuv420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv"
}

// EncodeWeb writes in as an MP4 that plays in every browser: H.264 High
// profile in yuv420p with BT.709 color tags, the first audio track as
// stereo AAC, the index at the start of the file (faststart), and no
// metadata, chapters, subtitles, or data tracks, so camera and location
// tags do not reach the copy. info is the inspection of in. progress, when
// set, receives the encoded time in microseconds.
func EncodeWeb(ctx context.Context, ffmpeg, in, out string, info *Info, o WebVideo, progress func(us int64)) error {
	w, h := o.WebSize(info.Video.DisplayWidth, info.Video.DisplayHeight)
	kbps := o.Bitrate / 1000
	args := []string{"-i", ffmpegPath(in),
		"-map", fmt.Sprintf("0:%d", info.Video.Index), "-map", "0:a:0?",
		"-vf", WebFilter(info.Video, w, h),
		"-c:v", "libx264", "-preset", "medium", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-crf", strconv.Itoa(o.CRF), "-maxrate", fmt.Sprintf("%dk", kbps), "-bufsize", fmt.Sprintf("%dk", 2*kbps),
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", o.AudioBitrate/1000), "-ac", "2",
		"-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn",
		"-movflags", "+faststart", "-f", "mp4", out}
	var report func(string, string)
	if progress != nil {
		report = progressInt("out_time_us", progress)
	}
	if err := runFFmpegProgress(ctx, "", ffmpeg, report, args...); err != nil {
		return err
	}
	return checkNonEmpty(out)
}

var (
	quickTimeDateRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d+)?([+-]\d{2}):?(\d{2})$`)
	utcDateRe       = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d+)?Z$`)
)

// VideoDate returns the recording date of a video from its container
// tags: the QuickTime creation date that phones write, local time with its
// UTC offset, or else creation_time, which is UTC. source names the tag.
// ok is false when neither tag holds a date.
func VideoDate(tags map[string]string) (value, source string, ok bool) {
	if m := quickTimeDateRe.FindStringSubmatch(strings.TrimSpace(tags["com.apple.quicktime.creationdate"])); m != nil {
		return m[1] + m[3] + ":" + m[4], "QuickTime creationdate", true
	}
	if m := utcDateRe.FindStringSubmatch(strings.TrimSpace(tags["creation_time"])); m != nil && !strings.HasPrefix(m[1], "1904-01-01") && !strings.HasPrefix(m[1], "1970-01-01") {
		return m[1] + "Z", "creation_time", true
	}
	return "", "", false
}
