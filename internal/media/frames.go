package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// FramePattern is the FFmpeg input pattern of extracted frames in a work
// directory.
const FramePattern = "frame_%03d.jpg"

// FrameTimes returns n evenly spaced timestamps in seconds, i * duration /
// n, each capped at last, the start of the last decodable frame. Very short
// clips can therefore repeat a timestamp.
func FrameTimes(duration float64, n int, last float64) []float64 {
	times := make([]float64, n)
	for i := range times {
		times[i] = math.Min(float64(i)*duration/float64(n), last)
	}
	return times
}

// CoverTime picks the cover timestamp. Videos longer than 10 seconds get a
// random point between 15 and 85 percent of the duration; shorter videos
// use the midpoint. The result is capped at last. r returns a number in
// [0, 1).
func CoverTime(duration float64, last float64, r func() float64) float64 {
	t := duration / 2
	if duration > 10 {
		lo, hi := duration*0.15, duration*0.85
		t = lo + r()*(hi-lo)
	}
	return math.Min(t, last)
}

// Clock formats whole seconds as HH:MM:SS, truncating fractions.
func Clock(sec float64) string {
	s := int64(math.Floor(sec))
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

func seconds(t float64) string {
	return strconv.FormatFloat(t, 'f', 3, 64)
}

// FrameOptions configures frame extraction.
type FrameOptions struct {
	FFmpeg string
	Input  string
	// WorkDir receives frame_NNN.jpg files. Its path must be filter safe.
	WorkDir string
	// FontFile is a filter-safe path to the overlay font.
	FontFile string
	Times    []float64
	// Overlay sets the timestamp size. The zero value uses the sizes for a
	// 1920x1080 frame.
	Overlay Overlay
	// Decode selects CPU or hardware decoding. The zero value is the CPU.
	Decode DecodePath
	// Concurrency bounds parallel ffmpeg runs. Zero means the CPU count.
	Concurrency int
}

// ExtractFrames writes one JPEG per timestamp with an HH:MM:SS overlay. It
// fails if any frame cannot be decoded, naming the first failing timestamp.
func ExtractFrames(ctx context.Context, o FrameOptions) error {
	if err := checkFilterPath(o.WorkDir); err != nil {
		return err
	}
	if err := checkFilterPath(o.FontFile); err != nil {
		return err
	}
	workers := o.Decode.Workers(o.Concurrency)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make([]error, len(o.Times))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, t := range o.Times {
		sem <- struct{}{}
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := extractFrame(ctx, o, i, t); err != nil {
				errs[i] = err
				cancel()
			}
		}()
	}
	wg.Wait()
	// Report the earliest real failure; cancellations follow from it.
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return ctx.Err()
}

func extractFrame(ctx context.Context, o FrameOptions, i int, t float64) error {
	out := filepath.Join(o.WorkDir, fmt.Sprintf(FramePattern, i))
	text := Clock(t)
	escaped := text[0:2] + `\:` + text[3:5] + `\:` + text[6:8]
	ov := o.Overlay
	if ov.FontSize == 0 {
		ov = Overlay{FontSize: 64, Margin: 25, Border: 8}
	}
	vf := squarePixels + ",drawtext=fontfile=" + o.FontFile + ":text='" + escaped + "'" +
		fmt.Sprintf(":x=%d:y=%d:fontsize=%d:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=%d",
			ov.Margin, ov.Margin, ov.FontSize, ov.Border)
	if hw := o.Decode.hardwareFilters(); hw != "" {
		vf = hw + "," + vf
	}
	args := append(o.Decode.inputArgs(t, o.Input),
		"-frames:v", "1", "-update", "1", "-q:v", "2", "-vf", vf, out)
	err := runFFmpeg(ctx, o.FFmpeg, args...)
	if ctx.Err() != nil {
		return context.Canceled
	}
	if err == nil {
		err = checkNonEmpty(out)
	}
	if err != nil {
		return fmt.Errorf("frame %d at %s (%ss): no frame decoded: %w", i+1, text, seconds(t), err)
	}
	return nil
}

// squarePixels converts a frame to square pixels at its displayed aspect
// ratio by scaling the width by the sample aspect ratio. It is a no-op for
// square-pixel sources. FFmpeg has already applied the rotation metadata,
// including the matching change to the sample aspect ratio.
const squarePixels = "scale=w=round(iw*sar):h=ih,setsar=1"

// ExtractCover writes a clean frame at source resolution in its displayed
// orientation and aspect ratio, with square pixels.
func ExtractCover(ctx context.Context, ffmpeg, input string, at float64, out string) error {
	err := runFFmpeg(ctx, ffmpeg,
		"-ss", seconds(at), "-i", ffmpegPath(input),
		"-vf", squarePixels, "-frames:v", "1", "-update", "1", "-q:v", "2", out)
	if err == nil {
		err = checkNonEmpty(out)
	}
	if err != nil {
		return fmt.Errorf("cover at %s (%ss): %w", Clock(at), seconds(at), err)
	}
	return nil
}

// EncodePreview encodes the frames in workDir as H.264 at 4 fps with the
// yuv420p pixel format. The frames already have square pixels at the
// displayed aspect ratio, so the video keeps that aspect ratio. JPEG frames are full range, so they are converted
// to limited range; otherwise current FFmpeg writes yuvj420p. Odd
// dimensions are rounded down to even values, which libx264 requires for
// yuv420p.
func EncodePreview(ctx context.Context, ffmpeg, workDir, out string, enc EncodePath) error {
	if err := checkFilterPath(workDir); err != nil {
		return err
	}
	args := []string{"-framerate", "4", "-i", filepath.Join(workDir, FramePattern),
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2:out_range=tv,setsar=1"}
	args = append(args, enc.codecArgs()...)
	args = append(args, "-pix_fmt", "yuv420p", out)
	err := runFFmpeg(ctx, ffmpeg, args...)
	if err == nil {
		err = checkNonEmpty(out)
	}
	return err
}

// MosaicOptions configures the mosaic image.
type MosaicOptions struct {
	FFmpeg   string
	WorkDir  string
	FontFile string
	// Aspect is the displayed width to height ratio of the source. Zero
	// means 16:9.
	Aspect float64
	// HeaderFile holds the header text, read without expansion.
	HeaderFile string
	Out        string
}

// MosaicCell returns the cell size for a displayed aspect ratio. The 10 by
// 10 grid is the largest one with that aspect ratio that fits in 4K in the
// video's orientation: 3840x2160 for landscape and square sources, 2160x3840
// for portrait ones. The cell is therefore at most 384x216 or 216x384, with
// the free side rounded to an even number: 16:9 gives 384x216, 9:16 gives
// 216x384, 4:3 gives 288x216, 3:4 gives 216x288, and 1:1 gives 216x216. The
// source resolution does not matter.
func MosaicCell(aspect float64) (w, h int) {
	if aspect <= 0 || math.IsNaN(aspect) || math.IsInf(aspect, 0) {
		aspect = 16.0 / 9.0
	}
	even := func(x float64) int { return max(2, 2*int(math.Round(x/2))) }
	boxW, boxH := 384.0, 216.0
	if aspect < 1 {
		boxW, boxH = 216.0, 384.0
	}
	if aspect >= boxW/boxH {
		return int(boxW), min(int(boxH), even(boxW/aspect))
	}
	return min(int(boxW), even(boxH*aspect)), int(boxH)
}

// Overlay holds the timestamp overlay sizes, in pixels of the extracted
// frame.
type Overlay struct {
	FontSize int
	Margin   int
	Border   int
}

// Overlay sizes in mosaic pixels. They are the reference values (font 64,
// margin 25, border 8) for a 1920x1080 frame shown in a 384x216 cell.
const (
	overlayFontInCell   = 12.8
	overlayMarginInCell = 5.0
	overlayBorderInCell = 1.6
)

// OverlayFor sizes the timestamp overlay of a frame so that it has the same
// size in the mosaic for every source resolution and aspect ratio. The frame
// is frameWidth pixels wide at the displayed aspect ratio, and its mosaic
// cell is MosaicCell(aspect). The preview video uses the same frames, so the
// overlay keeps the same size relative to the picture there as well.
func OverlayFor(frameWidth int, aspect float64) Overlay {
	cw, _ := MosaicCell(aspect)
	scale := float64(frameWidth) / float64(cw)
	if frameWidth <= 0 {
		scale = 1
	}
	return Overlay{
		FontSize: max(1, int(math.Round(overlayFontInCell*scale))),
		Margin:   int(math.Round(overlayMarginInCell * scale)),
		Border:   max(1, int(math.Round(overlayBorderInCell*scale))),
	}
}

// MosaicSize returns the mosaic image size for a displayed aspect ratio:
// the 10 by 10 grid, which fits in 4K, plus the 40-pixel header above it.
func MosaicSize(aspect float64) (w, h int) {
	cw, ch := MosaicCell(aspect)
	return 10 * cw, 10*ch + 40
}

// BuildMosaic tiles the frames into a 10 by 10 grid whose aspect ratio
// matches the displayed aspect ratio of the source, with empty cells left
// black, then adds a 40-pixel header above the grid. Each frame is scaled
// to its cell before tiling, which keeps the intermediate image small: a
// grid of full-size 4K frames would exceed FFmpeg's image size limit.
func BuildMosaic(ctx context.Context, o MosaicOptions) error {
	for _, p := range []string{o.WorkDir, o.FontFile, o.HeaderFile} {
		if err := checkFilterPath(p); err != nil {
			return err
		}
	}
	cw, ch := MosaicCell(o.Aspect)
	grid := filepath.Join(o.WorkDir, "grid.jpg")
	if err := runFFmpeg(ctx, o.FFmpeg,
		"-i", filepath.Join(o.WorkDir, FramePattern),
		"-filter_complex", fmt.Sprintf("scale=%d:%d,setsar=1,", cw, ch)+
			"tile=10x10:margin=0:padding=0",
		"-frames:v", "1", "-update", "1", grid); err != nil {
		return fmt.Errorf("tile frames: %w", err)
	}
	err := runFFmpeg(ctx, o.FFmpeg,
		"-i", grid,
		"-filter_complex", "pad=iw:ih+40:0:40:color=black@1.0,"+
			"drawtext=fontfile="+o.FontFile+":textfile="+o.HeaderFile+
			":expansion=none:x=20:y=10:fontsize=24:fontcolor=white",
		"-update", "1", o.Out)
	if err == nil {
		err = checkNonEmpty(o.Out)
	}
	if err != nil {
		return fmt.Errorf("add header: %w", err)
	}
	return nil
}

func checkNonEmpty(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("output %s was not written", filepath.Base(path))
	}
	if fi.Size() == 0 {
		return fmt.Errorf("output %s is empty", filepath.Base(path))
	}
	return nil
}

// checkFilterPath rejects paths that would need escaping inside an FFmpeg
// filter graph. Only work directory paths and fixed file names are placed
// in filters, so a failure points at an unusual temporary directory.
func checkFilterPath(p string) error {
	for _, r := range p {
		ok := r == '/' || r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("path %q contains characters that FFmpeg filters cannot take safely; set TMPDIR to a plain directory", p)
		}
	}
	return nil
}
