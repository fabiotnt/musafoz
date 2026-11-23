// Package preview generates the cover, MP4 preview, and mosaic for one
// video. It does not depend on any user interface.
package preview

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/fabiotnt/musafoz/internal/fslock"
	"github.com/fabiotnt/musafoz/internal/media"
)

// Frame count limits. The mosaic grid is fixed at 10 by 10.
const (
	DefaultFrames = 100
	MaxFrames     = 100
)

// Output name suffixes, placed beside the input.
const (
	CoverSuffix  = "_cover.jpg"
	VideoSuffix  = "_preview.mp4"
	MosaicSuffix = "_preview.jpg"
)

// Steps reported in errors.
const (
	StepValidateInput   = "validate input"
	StepValidateOptions = "validate options"
	StepLockOutputs     = "lock outputs"
	StepCheckOutputs    = "check outputs"
	StepResolveFFprobe  = "resolve ffprobe"
	StepInspect         = "inspect"
	StepResolveFFmpeg   = "resolve ffmpeg"
	StepFindFont        = "find font"
	StepPrepare         = "prepare"
	StepFaststart       = "faststart"
	StepExtractFrames   = "extract frames"
	StepCover           = "cover"
	StepPreviewVideo    = "preview video"
	StepMosaic          = "mosaic"
	StepPublish         = "publish"
	StepReplaceOriginal = "replace original"
)

// Faststart outcomes.
const (
	FaststartNotRequested = "not_requested"
	FaststartApplied      = "applied"
	FaststartAlready      = "already_faststart"
)

// StepError reports the failing step and input.
type StepError struct {
	Path string
	Step string
	Err  error
	// Kept is a directory left in place because it holds backups that could
	// not be restored. It is empty in the normal case.
	Kept string
}

func (e *StepError) Error() string {
	msg := fmt.Sprintf("preview %s: %s: %v", e.Path, e.Step, e.Err)
	if e.Kept != "" {
		msg += "; previous outputs are kept in " + e.Kept
	}
	return msg
}

func (e *StepError) Unwrap() error { return e.Err }

// Outputs are the three generated files.
type Outputs struct {
	Cover  string `json:"cover"`
	Video  string `json:"preview_video"`
	Mosaic string `json:"mosaic"`
}

// List returns the outputs in publishing order.
func (o Outputs) List() []string { return []string{o.Cover, o.Video, o.Mosaic} }

// OutputsFor returns the output paths for an input. Inputs that share a
// directory and base name, such as clip.mp4 and clip.mov, share outputs.
func OutputsFor(input string) Outputs {
	dir, base := splitInput(input)
	return Outputs{
		Cover:  filepath.Join(dir, base+CoverSuffix),
		Video:  filepath.Join(dir, base+VideoSuffix),
		Mosaic: filepath.Join(dir, base+MosaicSuffix),
	}
}

func splitInput(input string) (dir, base string) {
	name := filepath.Base(input)
	return filepath.Dir(input), strings.TrimSuffix(name, filepath.Ext(name))
}

// Options configures a preview run.
type Options struct {
	Input string
	// Frames is the number of frames, from 1 to MaxFrames.
	Frames           int
	Overwrite        bool
	FaststartInPlace bool
	// Font is an explicit overlay font. Empty means media.DefaultFonts.
	Font string
	// Accel selects hardware acceleration. Empty means media.AccelAuto.
	Accel media.Accel
	// Platform is the operating system, as runtime.GOOS. Empty means the
	// current one.
	Platform string

	// ResolveFFprobe and ResolveFFmpeg return executable paths. They are
	// called only after the earlier checks pass.
	ResolveFFprobe func(context.Context) (string, error)
	ResolveFFmpeg  func(context.Context) (string, error)

	// Logf receives progress messages and Warnf receives warnings. Both
	// may be nil.
	Logf  func(format string, args ...any)
	Warnf func(format string, args ...any)

	// Random returns a number in [0, 1) for the cover choice. Nil uses
	// math/rand/v2.
	Random func() float64
	// Concurrency bounds parallel frame extraction. Zero means the CPU count.
	Concurrency int
}

// Result describes a successful run.
type Result struct {
	Input           string  `json:"input"`
	Outputs         Outputs `json:"outputs"`
	DurationSeconds float64 `json:"duration_seconds"`
	Frames          int     `json:"frames"`
	CoverAtSeconds  float64 `json:"cover_at_seconds"`
	Faststart       string  `json:"faststart"`
	// FrameDecoder and PreviewEncoder name the backend that did the work,
	// "cpu" or "videotoolbox", after any fallback.
	FrameDecoder   string `json:"frame_decoder"`
	PreviewEncoder string `json:"preview_encoder"`
}

// Run generates the preview outputs for one video. On any failure the
// original video and any earlier outputs are left as they were.
func Run(ctx context.Context, opts Options) (*Result, error) {
	r := &run{opts: opts}
	return r.do(ctx)
}

type run struct {
	opts    Options
	input   string
	staging string
	keep    bool
}

func (r *run) logf(format string, args ...any) {
	if r.opts.Logf != nil {
		r.opts.Logf(format, args...)
	}
}

func (r *run) warnf(format string, args ...any) {
	if r.opts.Warnf != nil {
		r.opts.Warnf(format, args...)
	}
}

func (r *run) fail(step string, err error) error {
	se := &StepError{Path: r.opts.Input, Step: step, Err: err}
	if r.keep {
		se.Kept = r.staging
	}
	return se
}

func (r *run) do(ctx context.Context) (*Result, error) {
	runStart := time.Now()
	o := r.opts
	if o.Frames < 1 || o.Frames > MaxFrames {
		return nil, r.fail(StepValidateOptions, fmt.Errorf("frames must be between 1 and %d, got %d", MaxFrames, o.Frames))
	}
	abs, err := filepath.Abs(o.Input)
	if err != nil {
		return nil, r.fail(StepValidateInput, err)
	}
	r.input = abs
	if err := media.ValidateInput(abs); err != nil {
		return nil, r.fail(StepValidateInput, unwrapInspect(err))
	}
	if o.FaststartInPlace {
		if _, err := media.FaststartMuxer(abs); err != nil {
			return nil, r.fail(StepValidateOptions, err)
		}
	}

	// One lock per output set, held until publishing and any restore end.
	dir, base := splitInput(abs)
	outputs := OutputsFor(abs)
	lockPath := filepath.Join(dir, "."+base+".musafoz.lock")
	unlock, err := fslock.Lock(ctx, lockPath, func() {
		r.logf("waiting for another musafoz run that writes %s outputs", base)
	})
	if err != nil {
		return nil, r.fail(StepLockOutputs, err)
	}
	defer unlock()

	r.reportInterrupted(dir, base)

	if !o.Overwrite {
		var conflicts []string
		for _, p := range outputs.List() {
			if _, err := os.Lstat(p); err == nil {
				conflicts = append(conflicts, p)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, r.fail(StepCheckOutputs, err)
			}
		}
		if len(conflicts) > 0 {
			return nil, r.fail(StepCheckOutputs, &ConflictError{Paths: conflicts})
		}
	}

	ffprobe, err := o.ResolveFFprobe(ctx)
	if err != nil {
		return nil, r.fail(StepResolveFFprobe, err)
	}
	r.logf("inspecting %s", abs)
	info, err := media.Inspect(ctx, ffprobe, abs)
	if err != nil {
		return nil, r.fail(StepInspect, unwrapInspect(err))
	}
	duration, err := media.RequireDuration(info)
	if err != nil {
		return nil, r.fail(StepInspect, err)
	}
	if o.FaststartInPlace {
		if tags := unsupportedFaststartTags(info); len(tags) > 0 {
			return nil, r.fail(StepFaststart, &UnsupportedTracksError{Tags: tags})
		}
	}
	last := info.LastFrameTime()
	origStat, err := os.Stat(abs)
	if err != nil {
		return nil, r.fail(StepInspect, err)
	}

	ffmpeg, err := o.ResolveFFmpeg(ctx)
	if err != nil {
		return nil, r.fail(StepResolveFFmpeg, err)
	}
	if err := media.CheckFFmpegFeatures(ctx, ffmpeg); err != nil {
		return nil, r.fail(StepResolveFFmpeg, err)
	}
	font, err := media.FindFont(o.Font)
	if err != nil {
		return nil, r.fail(StepFindFont, err)
	}

	r.staging, err = os.MkdirTemp(dir, "."+base+".musafoz-")
	if err != nil {
		return nil, r.fail(StepPrepare, err)
	}
	defer func() {
		if !r.keep {
			os.RemoveAll(r.staging)
		}
	}()
	work, err := os.MkdirTemp("", "musafoz-preview-")
	if err != nil {
		return nil, r.fail(StepPrepare, err)
	}
	defer os.RemoveAll(work)
	fontLink := filepath.Join(work, "font"+strings.ToLower(filepath.Ext(font)))
	if err := os.Symlink(font, fontLink); err != nil {
		return nil, r.fail(StepPrepare, err)
	}
	header := filepath.Join(work, "header.txt")
	if err := os.WriteFile(header, []byte(filepath.Base(abs)), 0o644); err != nil {
		return nil, r.fail(StepPrepare, err)
	}

	res := &Result{Input: abs, Outputs: outputs, DurationSeconds: duration, Frames: o.Frames, Faststart: FaststartNotRequested}
	source := abs
	remuxed := ""
	if o.FaststartInPlace {
		already, err := media.IsFastStart(abs)
		if err != nil {
			return nil, r.fail(StepFaststart, err)
		}
		if already {
			res.Faststart = FaststartAlready
			r.logf("faststart: already applied, leaving the original as is")
		} else {
			r.logf("faststart: remuxing into a temporary file")
			remuxed = filepath.Join(r.staging, "faststart"+strings.ToLower(filepath.Ext(abs)))
			if err := r.remux(ctx, ffprobe, ffmpeg, info, remuxed); err != nil {
				return nil, r.fail(StepFaststart, err)
			}
			source = remuxed
			res.Faststart = FaststartApplied
		}
	}

	staged := Outputs{
		Cover:  filepath.Join(r.staging, "cover.jpg"),
		Video:  filepath.Join(r.staging, "preview.mp4"),
		Mosaic: filepath.Join(r.staging, "mosaic.jpg"),
	}
	mode := o.Accel
	if mode == "" {
		mode = media.AccelAuto
	}
	platform := o.Platform
	if platform == "" {
		platform = runtime.GOOS
	}

	dec := media.ChooseDecode(ctx, ffmpeg, source, info, mode, platform)
	if ctx.Err() != nil {
		return nil, r.fail(StepExtractFrames, ctx.Err())
	}
	r.logf("frame decoding: %s (%s)", media.BackendLabel(dec.Backend), dec.Reason)
	frameOpts := media.FrameOptions{
		FFmpeg: ffmpeg, Input: source, WorkDir: work, FontFile: fontLink,
		Times:       media.FrameTimes(duration, o.Frames, last),
		Overlay:     media.OverlayFor(info.Video.DisplayWidth, info.Video.DisplayAspect()),
		Concurrency: o.Concurrency,
	}
	extract := func(d media.DecodePath) error {
		frameOpts.Decode = d
		r.logf("extracting %d frames on %s (%d parallel ffmpeg processes)",
			o.Frames, media.BackendLabel(d.Backend), min(d.Workers(o.Concurrency), o.Frames))
		return media.ExtractFrames(ctx, frameOpts)
	}
	stepStart := time.Now()
	err = extract(dec)
	if err != nil && dec.Backend != media.BackendCPU && ctx.Err() == nil {
		r.logf("frame extraction on %s failed (%v); retrying once on CPU", media.BackendLabel(dec.Backend), err)
		dec = media.CPUDecode("fallback after a hardware failure")
		err = extract(dec)
	}
	if err != nil {
		return nil, r.fail(StepExtractFrames, err)
	}
	res.FrameDecoder = dec.Backend
	r.logf("extracted %d frames on %s in %.1f s", o.Frames, media.BackendLabel(dec.Backend), time.Since(stepStart).Seconds())

	random := o.Random
	if random == nil {
		random = rand.Float64
	}
	res.CoverAtSeconds = media.CoverTime(duration, last, random)
	r.logf("generating cover at %s on CPU", media.Clock(res.CoverAtSeconds))
	if err := media.ExtractCover(ctx, ffmpeg, source, res.CoverAtSeconds, staged.Cover); err != nil {
		return nil, r.fail(StepCover, err)
	}
	enc := media.ChooseEncode(ctx, ffmpeg, mode, platform,
		info.Video.DisplayWidth, info.Video.DisplayHeight)
	if ctx.Err() != nil {
		return nil, r.fail(StepPreviewVideo, ctx.Err())
	}
	r.logf("preview encoding: %s with %s (%s)", media.BackendLabel(enc.Backend), enc.Encoder(), enc.Reason)
	stepStart = time.Now()
	err = media.EncodePreview(ctx, ffmpeg, work, staged.Video, enc)
	if err != nil && enc.Backend != media.BackendCPU && ctx.Err() == nil {
		r.logf("preview encoding with %s failed (%v); retrying once with libx264", enc.Encoder(), err)
		enc = media.CPUEncode("fallback after a hardware failure")
		err = media.EncodePreview(ctx, ffmpeg, work, staged.Video, enc)
	}
	if err != nil {
		return nil, r.fail(StepPreviewVideo, err)
	}
	res.PreviewEncoder = enc.Backend
	r.logf("encoded preview video with %s in %.1f s", enc.Encoder(), time.Since(stepStart).Seconds())
	r.logf("building mosaic")
	if err := media.BuildMosaic(ctx, media.MosaicOptions{
		FFmpeg: ffmpeg, WorkDir: work, FontFile: fontLink, HeaderFile: header, Out: staged.Mosaic,
		Aspect: info.Video.DisplayAspect(),
	}); err != nil {
		return nil, r.fail(StepMosaic, err)
	}

	// Publish the three outputs, then replace the original if requested.
	// Any failure restores the earlier set.
	pub := &publisher{backupDir: filepath.Join(r.staging, "backup"), overwrite: o.Overwrite}
	stagedList, finalList := staged.List(), outputs.List()
	for i := range finalList {
		if err := pub.publish(stagedList[i], finalList[i]); err != nil {
			return nil, r.fail(StepPublish, r.restore(pub, err))
		}
	}
	if remuxed != "" {
		if err := replaceOriginal(remuxed, abs, origStat); err != nil {
			return nil, r.fail(StepReplaceOriginal, r.restore(pub, err))
		}
	}
	r.logf("finished in %.1f s", time.Since(runStart).Seconds())
	return res, nil
}

// restore rolls back published outputs. If that fails, the staging
// directory is kept because it holds the remaining backups.
func (r *run) restore(pub *publisher, cause error) error {
	if err := pub.rollback(); err != nil {
		r.keep = true
		return fmt.Errorf("%w; %v", cause, err)
	}
	return cause
}

// unsupportedFaststartTags returns the codec tags of tracks that a
// stream-copy remux is known not to preserve.
func unsupportedFaststartTags(info *media.Info) []string {
	var found []string
	for _, tag := range info.CodecTags {
		if tag == "mebx" && !slices.Contains(found, tag) {
			found = append(found, tag)
		}
	}
	return found
}

// UnsupportedTracksError reports tracks that faststart cannot keep. It is
// raised from inspection, before any remux.
type UnsupportedTracksError struct {
	Tags []string
}

func (e *UnsupportedTracksError) Error() string {
	return fmt.Sprintf("the file has %s tracks (Apple timed metadata) that a remux cannot preserve; "+
		"faststart was refused and the original was preserved; run preview without --faststart-in-place to generate the outputs",
		strings.Join(e.Tags, ", "))
}

// remux writes a faststart copy and checks it against the original. The
// check compares, from FFprobe output: the number of video, audio,
// subtitle, attached picture, and other streams; the codec tag of every
// stream in index order; the container duration, within 0.1 seconds; and
// that the moov box precedes mdat. It does not compare packet data or
// timestamps; stream copy keeps packets unchanged and the original video
// timescale is passed to the muxer.
func (r *run) remux(ctx context.Context, ffprobe, ffmpeg string, orig *media.Info, dst string) error {
	if err := media.Remux(ctx, ffmpeg, r.input, dst, orig.Video.Timescale()); err != nil {
		return err
	}
	info, err := media.Inspect(ctx, ffprobe, dst)
	if err != nil {
		return fmt.Errorf("remuxed file is unreadable: %w", unwrapInspect(err))
	}
	if info.Streams != orig.Streams {
		return fmt.Errorf("remuxed file has streams %+v, original has %+v", info.Streams, orig.Streams)
	}
	// The muxer can rewrite tracks it does not understand with placeholder
	// values; mebx is refused earlier, and this catches other such cases.
	if !slices.Equal(info.CodecTags, orig.CodecTags) {
		return fmt.Errorf("remux would not preserve every track (codec tags %v became %v); the original was kept",
			orig.CodecTags, info.CodecTags)
	}
	d1, _ := media.RequireDuration(orig)
	d2, err := media.RequireDuration(info)
	if err != nil || math.Abs(d1-d2) > 0.1 {
		return fmt.Errorf("remuxed duration %v differs from original %.3f", info.DurationSeconds, d1)
	}
	ok, err := media.IsFastStart(dst)
	if err != nil || !ok {
		return fmt.Errorf("remuxed file does not start with the moov box")
	}
	return nil
}

// replaceOriginal renames the remuxed file over the original, keeping its
// permissions, after checking that the original did not change meanwhile.
func replaceOriginal(remuxed, original string, before os.FileInfo) error {
	now, err := os.Stat(original)
	if err != nil {
		return err
	}
	if now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime()) {
		return errors.New("the original changed during processing; it was not replaced")
	}
	if err := os.Chmod(remuxed, before.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(remuxed, original)
}

// reportInterrupted warns about staging directories left by runs that were
// killed. They may hold backups of earlier outputs, so they stay in place.
// The caller holds the output-set lock, so no live run owns them.
func (r *run) reportInterrupted(dir, base string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := "." + base + ".musafoz-"
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			r.warnf("found files from an interrupted run at %s; it may hold backups of earlier outputs and was left in place",
				filepath.Join(dir, e.Name()))
		}
	}
}

// unwrapInspect drops the "inspect <path>:" prefix, since StepError already
// names the input and step.
func unwrapInspect(err error) error {
	var ie *media.InspectError
	if errors.As(err, &ie) {
		return ie.Err
	}
	return err
}
