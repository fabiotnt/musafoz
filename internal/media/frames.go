package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// CoverTime picks a random cover timestamp. Videos longer than 10 seconds
// get a point between 15 and 85 percent of the duration; shorter videos get
// a point anywhere from the start to last, the start of the last decodable
// frame. The result is capped at last and rounded to milliseconds. r returns
// a number in [0, 1).
func CoverTime(duration float64, last float64, r func() float64) float64 {
	lo, hi := 0.0, last
	if duration > 10 {
		lo, hi = duration*0.15, duration*0.85
	}
	t := math.Min(lo+r()*(hi-lo), last)
	return math.Max(0, math.Floor(t*1000)/1000)
}

// ParseTimestamp reads a timestamp written as HH:MM:SS with an optional
// fraction of a second, such as 00:01:15.500. Hours may have any number of
// digits; minutes and seconds have two digits below 60. The result is in
// seconds, rounded to milliseconds.
func ParseTimestamp(s string) (float64, error) {
	if strings.HasPrefix(strings.TrimSpace(s), "-") {
		return 0, fmt.Errorf("timestamp %q is negative", s)
	}
	m := timestampRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("timestamp %q is not in HH:MM:SS or HH:MM:SS.fff form", s)
	}
	h, _ := strconv.ParseFloat(m[1], 64)
	mins, _ := strconv.ParseFloat(m[2], 64)
	sec, _ := strconv.ParseFloat(m[3], 64)
	if mins >= 60 || sec >= 60 {
		return 0, fmt.Errorf("timestamp %q has minutes or seconds of 60 or more", s)
	}
	v := h*3600 + mins*60 + sec
	return math.Round(v*1000) / 1000, nil
}

var timestampRE = regexp.MustCompile(`^([0-9]+):([0-9]{2}):([0-9]{2}(?:\.[0-9]+)?)$`)

// Timestamp formats seconds as HH:MM:SS, followed by milliseconds when the
// value has a fraction, such as 00:01:15.500.
func Timestamp(sec float64) string {
	ms := int64(math.Round(sec * 1000))
	s := Clock(float64(ms / 1000))
	if ms%1000 != 0 {
		s += fmt.Sprintf(".%03d", ms%1000)
	}
	return s
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
	// WorkDir receives frame_NNN.jpg files.
	WorkDir string
	// FontFile is the overlay font. It must be inside WorkDir with a plain
	// name, because filters refer to it relative to WorkDir.
	FontFile string
	Times    []float64
	// Overlay sets the timestamp size. The zero value uses the sizes for a
	// 1920x1080 frame.
	Overlay Overlay
	// Decode selects CPU or hardware decoding. The zero value is the CPU.
	Decode DecodePath
	// Concurrency bounds parallel ffmpeg runs. Zero means the CPU count.
	Concurrency int
	// OnFrame, when set, is called after each frame is written. Calls may
	// come from several goroutines at once.
	OnFrame func()
}

// ExtractFrames writes one JPEG per timestamp with an HH:MM:SS overlay. It
// fails if any frame cannot be decoded, naming the first failing timestamp.
func ExtractFrames(ctx context.Context, o FrameOptions) error {
	font, err := workFile(o.WorkDir, o.FontFile)
	if err != nil {
		return err
	}
	o.FontFile = font
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
			} else if o.OnFrame != nil {
				o.OnFrame()
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
	err := runFFmpegIn(ctx, o.WorkDir, o.FFmpeg, args...)
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
		return fmt.Errorf("cover at %s (%ss): %w", Timestamp(at), seconds(at), err)
	}
	return nil
}

// EncodePreview encodes the frames in workDir as H.264 at 4 fps with the
// yuv420p pixel format. The frames already have square pixels at the
// displayed aspect ratio, so the video keeps that aspect ratio. JPEG frames are full range, so they are converted
// to limited range; otherwise current FFmpeg writes yuvj420p. Odd
// dimensions are rounded down to even values, which libx264 requires for
// yuv420p.
//
// progress, when set, receives the number of frames encoded so far.
func EncodePreview(ctx context.Context, ffmpeg, workDir, out string, enc EncodePath, progress func(frames int64)) error {
	args := []string{"-framerate", "4", "-i", filepath.Join(workDir, FramePattern),
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2:out_range=tv,setsar=1"}
	args = append(args, enc.codecArgs()...)
	args = append(args, "-pix_fmt", "yuv420p", out)
	err := runFFmpegProgress(ctx, "", ffmpeg, progressInt("frame", progress), args...)
	if err == nil {
		err = checkNonEmpty(out)
	}
	return err
}

// MosaicOptions configures the mosaic image.
type MosaicOptions struct {
	FFmpeg  string
	WorkDir string
	// FontFile and HeaderFile must be inside WorkDir with plain names; see
	// FrameOptions.
	FontFile string
	// Aspect is the displayed width to height ratio of the source. Zero
	// means 16:9.
	Aspect float64
	// Frames is the number of extracted frames, which sets the grid.
	Frames int
	// HeaderFile holds the header text, read without expansion.
	HeaderFile string
	Out        string
}

// MosaicHeader is the height of the header above the grid, in pixels.
const MosaicHeader = 40

// MosaicCell returns the cell size of a 10 by 10 grid for a displayed aspect
// ratio. That grid is the largest one with the aspect ratio that fits in 4K
// in the video's orientation: 3840x2160 for landscape and square sources,
// 2160x3840 for portrait ones. The cell is therefore at most 384x216 or
// 216x384, with the free side rounded to an even number: 16:9 gives 384x216,
// 9:16 gives 216x384, 4:3 gives 288x216, 3:4 gives 216x288, and 1:1 gives
// 216x216. The source resolution does not matter. Ten times this cell is
// the grid area for every frame count; see MosaicLayoutFor.
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

// MosaicGrid returns the grid for n frames: ceil(sqrt(n)) columns and
// ceil(n / columns) rows, so 25 frames give 5x5, 98 give 10x10 with two
// empty cells, and 7 give 3x3.
func MosaicGrid(n int) (cols, rows int) {
	n = max(1, n)
	cols = int(math.Ceil(math.Sqrt(float64(n))))
	// Guard against floating-point error for perfect squares.
	for (cols-1)*(cols-1) >= n {
		cols--
	}
	for cols*cols < n {
		cols++
	}
	return cols, (n + cols - 1) / cols
}

// MosaicLayout describes where frames go in the mosaic grid.
type MosaicLayout struct {
	Cols, Rows int
	// GridW and GridH are the grid area, ten times MosaicCell for every
	// frame count, so the area keeps the displayed aspect ratio and the 4K
	// limit.
	GridW, GridH int
	// CellW and CellH are the cell size: the grid area divided by the
	// columns and rows, rounded down. Leftover pixels are split around the
	// tiled cells as a black border.
	CellW, CellH int
	// ThumbW and ThumbH are the frame size inside a cell. The frame keeps
	// its aspect ratio and is centered with black bars when the cell has
	// another shape, as in a 3 by 2 grid.
	ThumbW, ThumbH int
}

// MosaicLayoutFor returns the layout for a displayed aspect ratio and frame
// count. Smaller grids get larger cells in the same area.
func MosaicLayoutFor(aspect float64, frames int) MosaicLayout {
	if aspect <= 0 || math.IsNaN(aspect) || math.IsInf(aspect, 0) {
		aspect = 16.0 / 9.0
	}
	cw, ch := MosaicCell(aspect)
	l := MosaicLayout{GridW: 10 * cw, GridH: 10 * ch}
	l.Cols, l.Rows = MosaicGrid(frames)
	l.CellW, l.CellH = l.GridW/l.Cols, l.GridH/l.Rows
	// Fit the frame in the cell. A difference of up to 2 pixels from the
	// cell is the even rounding of MosaicCell, so the frame fills the cell
	// then, as in the 10 by 10 grid.
	l.ThumbW, l.ThumbH = l.CellW, l.CellH
	if w := int(math.Round(float64(l.CellH) * aspect)); w < l.CellW-2 {
		l.ThumbW = w
	} else if h := int(math.Round(float64(l.CellW) / aspect)); h < l.CellH-2 {
		l.ThumbH = h
	}
	return l
}

// Overlay holds the timestamp overlay sizes, in pixels of the extracted
// frame.
type Overlay struct {
	FontSize int
	Margin   int
	Border   int
}

// Overlay sizes in mosaic pixels for a 10 by 10 grid. They are the
// reference values (font 64, margin 25, border 8) for a 1920x1080 frame
// shown in a 384x216 cell.
const (
	overlayFontInCell   = 12.8
	overlayMarginInCell = 5.0
	overlayBorderInCell = 1.6
)

// OverlayFor sizes the timestamp overlay of a frame from its width and
// displayed aspect ratio only. The size is the one that shows as 12.8-pixel
// text in a cell of the 10 by 10 grid, MosaicCell(aspect), for every source
// resolution. It does not depend on the frame count: the preview video uses
// the same frames, so its timestamps keep the same size relative to the
// picture for every --frames value. Smaller grids have larger cells, so
// their mosaic timestamps are larger than 12.8 pixels, never smaller.
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
// the grid area, which fits in 4K, plus the header above it. It is the same
// for every frame count.
func MosaicSize(aspect float64) (w, h int) {
	l := MosaicLayoutFor(aspect, 100)
	return l.GridW, l.GridH + MosaicHeader
}

// BuildMosaic scales each frame to fit its cell, keeping its aspect ratio,
// centers it in the cell on black, and tiles the cells into the grid of
// MosaicLayoutFor. Empty cells stay black. It then centers the tiled cells
// in the grid area and adds the header above it. Scaling before tiling
// keeps the intermediate image small: a grid of full-size 4K frames would
// exceed FFmpeg's image size limit.
func BuildMosaic(ctx context.Context, o MosaicOptions) error {
	font, err := workFile(o.WorkDir, o.FontFile)
	if err != nil {
		return err
	}
	header, err := workFile(o.WorkDir, o.HeaderFile)
	if err != nil {
		return err
	}
	l := MosaicLayoutFor(o.Aspect, o.Frames)
	grid := filepath.Join(o.WorkDir, "grid.jpg")
	if err := runFFmpeg(ctx, o.FFmpeg,
		"-i", filepath.Join(o.WorkDir, FramePattern),
		"-filter_complex", fmt.Sprintf("scale=%d:%d,setsar=1,", l.ThumbW, l.ThumbH)+
			fmt.Sprintf("pad=%d:%d:%d:%d:color=black,", l.CellW, l.CellH, (l.CellW-l.ThumbW)/2, (l.CellH-l.ThumbH)/2)+
			fmt.Sprintf("tile=%dx%d:margin=0:padding=0", l.Cols, l.Rows),
		"-frames:v", "1", "-update", "1", grid); err != nil {
		return fmt.Errorf("tile frames: %w", err)
	}
	offX := (l.GridW - l.Cols*l.CellW) / 2
	offY := (l.GridH - l.Rows*l.CellH) / 2
	err = runFFmpegIn(ctx, o.WorkDir, o.FFmpeg,
		"-i", grid,
		"-filter_complex", fmt.Sprintf("pad=%d:%d:%d:%d:color=black@1.0,",
			l.GridW, l.GridH+MosaicHeader, offX, MosaicHeader+offY)+
			"drawtext=fontfile="+font+":textfile="+header+
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

// workFile returns the name of p relative to workDir, for use inside an
// FFmpeg filter graph. FFmpeg runs with workDir as its current directory,
// so filters never see an absolute path: Windows drive letters, backslashes,
// and spaces in the temporary directory would otherwise need escaping.
func workFile(workDir, p string) (string, error) {
	rel, err := filepath.Rel(workDir, p)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not inside the work directory %s", p, workDir)
	}
	rel = filepath.ToSlash(rel)
	if err := checkFilterPath(rel); err != nil {
		return "", err
	}
	return rel, nil
}

// checkFilterPath rejects names that would need escaping inside an FFmpeg
// filter graph. Only fixed file names inside the work directory are placed
// in filters.
func checkFilterPath(p string) error {
	for _, r := range p {
		ok := r == '/' || r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("file name %q contains characters that FFmpeg filters cannot take safely", p)
		}
	}
	return nil
}
