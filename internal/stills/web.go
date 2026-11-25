package stills

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fabiotnt/musafoz/internal/fslock"
	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
)

// WebOptions say how web copies are made. Outputs go to Runner.Output.Dir
// and are named after the content: <uuid>.jpg and <uuid>_thumb.jpg for
// images; <uuid>.mp4, <uuid>_cover.jpg, <uuid>_preview.mp4, and
// <uuid>_preview.jpg for videos. The record <uuid>.json goes to
// MetadataDir, which may be the output directory itself.
type WebOptions struct {
	// ImageMax bounds the long side of web images. Images are never scaled
	// up.
	ImageMax int
	// ThumbShort is the short side of thumbnails, which keep the aspect
	// ratio of the image.
	ThumbShort int
	// ImageQuality and ThumbQuality are JPEG qualities, 1 to 100.
	ImageQuality, ThumbQuality int
	// Video is the format of web videos.
	Video media.WebVideo
	// MetadataDir receives the record of each content, <uuid>.json, with
	// every metadata field musafoz reads, including GPS, and the outputs
	// with their SHA-256. Keep these files private. The record is written
	// last, so it marks a finished content. Empty writes none.
	MetadataDir string
	// SkipExisting reuses a content whose record has the same settings and
	// whose outputs are all in the output directory of this run, unchanged.
	// It finishes a content that an earlier run left incomplete, replacing
	// only files that run is known to have written. It needs MetadataDir.
	SkipExisting bool
	// Preview writes the cover, preview video, and mosaic beside a web
	// video and returns their paths. Nil writes no previews. PreviewFrames
	// is recorded in the settings; set it to the frames Preview uses.
	Preview       func(ctx context.Context, video string, overwrite bool) (cover, clip, mosaic string, err error)
	PreviewFrames int
}

// DefaultWebOptions are 2560 pixel images, 480 pixel thumbnails, and 1080p
// video.
func DefaultWebOptions() WebOptions {
	return WebOptions{ImageMax: 2560, ThumbShort: 480, ImageQuality: 85, ThumbQuality: 80, Video: media.DefaultWebVideo}
}

// WebPipeline numbers the way musafoz makes web outputs. It changes when a
// change to musafoz changes the outputs for the same settings, so records
// of the earlier version are reported as made with other settings.
const WebPipeline = 1

// WebConfig is the part of the settings that shapes the outputs of one
// kind of content. Settings that do not apply to the kind are zero, so
// changing the image size does not invalidate videos.
type WebConfig struct {
	Pipeline      int  `json:"pipeline"`
	ImageMax      int  `json:"image_max,omitempty"`
	ThumbShort    int  `json:"thumb_short,omitempty"`
	ImageQuality  int  `json:"image_quality,omitempty"`
	ThumbQuality  int  `json:"thumb_quality,omitempty"`
	VideoMaxLong  int  `json:"video_max_long,omitempty"`
	VideoMaxShort int  `json:"video_max_short,omitempty"`
	VideoBitrate  int  `json:"video_bitrate,omitempty"`
	VideoCRF      int  `json:"video_crf,omitempty"`
	AudioBitrate  int  `json:"audio_bitrate,omitempty"`
	Preview       bool `json:"preview,omitempty"`
	PreviewFrames int  `json:"preview_frames,omitempty"`
}

// config returns the settings that apply to a kind of content.
func (o *WebOptions) config(kind string) WebConfig {
	c := WebConfig{Pipeline: WebPipeline}
	if kind == KindImage {
		c.ImageMax, c.ThumbShort, c.ImageQuality, c.ThumbQuality = o.ImageMax, o.ThumbShort, o.ImageQuality, o.ThumbQuality
		return c
	}
	v := o.Video
	c.VideoMaxLong, c.VideoMaxShort, c.VideoBitrate, c.VideoCRF, c.AudioBitrate = v.MaxLong, v.MaxShort, v.Bitrate, v.CRF, v.AudioBitrate
	if o.Preview != nil {
		c.Preview, c.PreviewFrames = true, o.PreviewFrames
	}
	return c
}

// diff names the settings that differ, as "image_max 2560 -> 2048".
func (c WebConfig) diff(d WebConfig) []string {
	var a, b map[string]any
	ja, _ := json.Marshal(c)
	jb, _ := json.Marshal(d)
	json.Unmarshal(ja, &a)
	json.Unmarshal(jb, &b)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var out []string
	for k := range keys {
		if fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
			out = append(out, fmt.Sprintf("%s %v -> %v", k, orNone(a[k]), orNone(b[k])))
		}
	}
	sort.Strings(out)
	return out
}

func orNone(v any) any {
	if v == nil {
		return "none"
	}
	return v
}

// ConfigMismatchError reports a content recorded with other settings.
type ConfigMismatchError struct {
	Record  string
	Changes []string
}

func (e *ConfigMismatchError) Error() string {
	return fmt.Sprintf("%s was made with other settings (%s); use --overwrite to make it again with the current ones",
		e.Record, strings.Join(e.Changes, ", "))
}

// Web result kinds and statuses.
const (
	KindImage = "image"
	KindVideo = "video"

	StatusOK        = "ok"
	StatusDuplicate = "duplicate"
	StatusExists    = "exists"
	// StatusResumed is a content that an earlier run left incomplete and
	// this run finished.
	StatusResumed = "resumed"
)

// WebResult describes the web copy of one content.
type WebResult struct {
	Kind string `json:"kind"`
	// UUID names the outputs. It is derived from SHA256, so the same
	// content always gets the same UUID.
	UUID   string `json:"uuid"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	// Format is the source format, such as "heif" or "mov,mp4,m4a,...".
	Format string `json:"format"`
	// Files maps roles to paths in the output directory of the run: image
	// and thumb, or video, cover, preview, and mosaic.
	Files map[string]string `json:"files"`
	// SourceWidth and SourceHeight are the displayed size of the source.
	SourceWidth  int `json:"source_width"`
	SourceHeight int `json:"source_height"`
	// Width and Height are the size of the main output, and ThumbWidth and
	// ThumbHeight of the thumbnail.
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	ThumbWidth  int     `json:"thumb_width,omitempty"`
	ThumbHeight int     `json:"thumb_height,omitempty"`
	Duration    float64 `json:"duration_seconds,omitempty"`
	// Date and DateSource are null when the source has no date (see
	// imaging.SelectDate and media.VideoDate).
	Date       *string `json:"date"`
	DateSource *string `json:"date_source"`
	// ColorFrom names the color profile converted to sRGB, and HDR reports
	// a video tone mapped to SDR.
	ColorFrom string `json:"color_converted_from,omitempty"`
	HDR       bool   `json:"hdr_tone_mapped,omitempty"`
	// Screenshot reports an image that its XMP marks as a screenshot.
	Screenshot bool `json:"screenshot,omitempty"`
	// Metadata is the path of the record, when one is written.
	Metadata string `json:"metadata,omitempty"`
	// Timings of this run, in seconds: reading the source for its SHA-256,
	// making the outputs, and checking existing outputs.
	Timings *WebTimings `json:"timings,omitempty"`
	// Status is StatusOK, StatusResumed, StatusDuplicate for content
	// already handled in this run, or StatusExists for content reused with
	// SkipExisting.
	Status string `json:"-"`
}

// WebTimings are the durations of the steps of one content, in seconds.
type WebTimings struct {
	Hash    float64 `json:"hash"`
	Convert float64 `json:"convert,omitempty"`
	Verify  float64 `json:"verify,omitempty"`
}

// webOutput is one output file of a content, by name in the output
// directory.
type webOutput struct {
	Role   string `json:"role"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// webRecordFormat identifies record files.
const webRecordFormat = "musafoz-web-record/1"

// webRecord is the content of <uuid>.json: the finished state of a content
// and all the metadata read from its source.
type webRecord struct {
	Format  string      `json:"format"`
	UUID    string      `json:"uuid"`
	SHA256  string      `json:"sha256"`
	Source  string      `json:"source"`
	Config  WebConfig   `json:"config"`
	Outputs []webOutput `json:"outputs"`
	// Result has file names, not paths: outputs are looked up in the output
	// directory of each run.
	Result *WebResult `json:"result"`
	// Image is the full inspection of an image source; Probe is the
	// FFprobe output of a video source, with its container tags.
	Image *imaging.Info   `json:"image,omitempty"`
	Probe json.RawMessage `json:"probe,omitempty"`
}

// webPending is written to the output directory, as
// .<uuid>.musafoz-pending.json, before outputs are placed, and removed after
// the record is written. It holds no metadata of the source: only the names
// and SHA-256 of the outputs, so a later run can tell the files this run
// placed from files of another origin.
type webPending struct {
	Format  string      `json:"format"`
	UUID    string      `json:"uuid"`
	Config  WebConfig   `json:"config"`
	Outputs []webOutput `json:"outputs"`
}

const webPendingFormat = "musafoz-web-pending/1"

// uuidNamespace is the UUID version 5 namespace of musafoz content ids.
var uuidNamespace = [16]byte{0x2b, 0x8e, 0x5f, 0x0c, 0x6d, 0x1a, 0x4e, 0x3b, 0x9a, 0x77, 0x41, 0xc2, 0x5e, 0x90, 0x13, 0xd4}

// ContentUUID returns the UUID version 5 (RFC 9562) of "sha256:<hex>" in
// the musafoz namespace.
func ContentUUID(sha256Hex string) string {
	h := sha1.New()
	h.Write(uuidNamespace[:])
	h.Write([]byte("sha256:" + sha256Hex))
	u := h.Sum(nil)[:16]
	u[6] = u[6]&0x0f | 0x50
	u[8] = u[8]&0x3f | 0x80
	x := hex.EncodeToString(u)
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (o *WebOptions) check() error {
	switch {
	case o.ImageMax < 16 || o.ImageMax > imaging.MaxSide:
		return fmt.Errorf("the image size %d is outside 16 to %d", o.ImageMax, imaging.MaxSide)
	case o.ThumbShort < 16 || o.ThumbShort > o.ImageMax:
		return fmt.Errorf("the thumbnail size %d is outside 16 to the image size %d", o.ThumbShort, o.ImageMax)
	case o.SkipExisting && o.MetadataDir == "":
		return errors.New("skipping existing outputs needs a metadata directory")
	}
	for _, q := range []int{o.ImageQuality, o.ThumbQuality} {
		if _, err := imaging.ParseQuality(q); err != nil {
			return err
		}
	}
	return nil
}

// stagingDir is the hidden directory, inside the output directory, where
// outputs are made before they are placed. Being on the same file system,
// outputs are placed with a link or rename.
const stagingDir = ".musafoz-staging"

// webItem is the work on one content.
type webItem struct {
	r       *Runner
	o       WebOptions
	id, sum string
	outDir  string
	// metaPath is the record path, or "" without MetadataDir.
	metaPath    string
	pendingPath string
	// ours holds output names that an earlier run of musafoz wrote, with
	// their SHA-256, as read from its record or pending file. Only these
	// may be replaced when a content is finished.
	ours map[string]string
	// replaceRecord allows replacing an existing record.
	replaceRecord bool
	// format is the image format of the source, or "" for a video, whose
	// inspection is video.
	format string
	video  *media.Info
}

// webAfterPlace, when set by tests, runs after each output is placed; an
// error stops the content there, as an interrupted run would.
var webAfterPlace func(name string) error

// Web makes the web copy of one source, a local file or a URL. Content
// already handled in this run is not converted again: the result is the
// earlier one with StatusDuplicate. owner names the item in messages.
func (r *Runner) Web(ctx context.Context, raw, owner string, o WebOptions) (*WebResult, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	s, err := r.resolveSource(ctx, raw)
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	sum, n, err := hashFile(s.path)
	if err != nil {
		return nil, err
	}
	timings := &WebTimings{Hash: since(t0)}
	r.mu.Lock()
	prev := r.web[sum]
	r.mu.Unlock()
	if prev != nil {
		dup := *prev
		dup.Status, dup.Timings = StatusDuplicate, timings
		return &dup, nil
	}
	// Know the kind of content before creating anything.
	format, err := imaging.SniffFile(s.path)
	if err != nil {
		return nil, err
	}
	var video *media.Info
	switch {
	case format != "" && !imaging.Decodable(format) && format != imaging.FormatHEIF:
		return nil, &imaging.UnsupportedError{Format: format}
	case format == "":
		if video, err = r.inspect(ctx, s); err != nil {
			return nil, fmt.Errorf("not a supported image, and not a video: %w", err)
		}
	}
	outDir, err := r.webDir()
	if err != nil {
		return nil, err
	}
	it := &webItem{r: r, o: o, id: ContentUUID(sum), sum: sum, outDir: outDir, ours: map[string]string{}, format: format, video: video}
	it.pendingPath = filepath.Join(outDir, "."+it.id+".musafoz-pending.json")
	if o.MetadataDir != "" {
		metaDir, err := filepath.Abs(o.MetadataDir)
		if err != nil {
			return nil, err
		}
		if err := r.ensureDir(metaDir); err != nil {
			return nil, err
		}
		it.metaPath = filepath.Join(metaDir, it.id+".json")
	}

	// Two processes on the same output directory never work on the same
	// content at once. Locks are shared by UUIDs with the same first two
	// hex digits, so at most 256 lock files exist.
	if err := r.ensureDir(filepath.Join(outDir, stagingDir)); err != nil {
		return nil, err
	}
	unlock, err := fslock.Lock(ctx, filepath.Join(outDir, stagingDir, it.id[:2]+".lock"), func() {
		r.logf("%s: waiting for another musafoz process working on %s", s.label, it.id)
	})
	if err != nil {
		return nil, err
	}
	defer unlock()

	res, err := it.run(ctx, s, n, timings)
	if err != nil {
		return nil, err
	}
	r.remember(sum, res)
	return res, nil
}

func since(t time.Time) float64 { return math.Round(time.Since(t).Seconds()*1000) / 1000 }

func (it *webItem) run(ctx context.Context, s *source, n int64, timings *WebTimings) (*WebResult, error) {
	r, o := it.r, it.o
	// An existing record: reuse it, finish it, or refuse.
	if it.metaPath != "" {
		rec, err := readRecord(it.metaPath, it.id)
		switch {
		case err == nil:
			if !r.Output.Overwrite && !o.SkipExisting {
				return nil, &ConflictError{Paths: []string{it.metaPath}}
			}
			if o.SkipExisting {
				if d := rec.Config.diff(o.config(rec.Result.Kind)); len(d) > 0 {
					return nil, &ConfigMismatchError{Record: it.metaPath, Changes: d}
				}
				t := time.Now()
				missing := it.verify(rec.Outputs)
				timings.Verify = since(t)
				if len(missing) == 0 {
					// A run that died between the record and the removal of
					// its pending file left only that file behind.
					if _, err := readPending(it.pendingPath, it.id); err == nil {
						os.Remove(it.pendingPath)
					}
					res := it.resultFrom(rec)
					res.Status, res.Timings = StatusExists, timings
					return res, nil
				}
				r.logf("%s: finishing %s: %s", s.label, it.id, strings.Join(missing, "; "))
			}
			for _, out := range rec.Outputs {
				it.ours[out.Name] = out.SHA256
			}
			it.replaceRecord = true
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	// A pending file: an earlier run stopped while placing outputs.
	if p, err := readPending(it.pendingPath, it.id); err == nil {
		if !o.SkipExisting && !r.Output.Overwrite {
			return nil, fmt.Errorf("an earlier run did not finish %s (%s); use --skip-existing to finish it", it.id, it.pendingPath)
		}
		for _, out := range p.Outputs {
			it.ours[out.Name] = out.SHA256
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	// Staging directories of this content left by a run that died are
	// removed: they are only ever written under the lock held here.
	if old, _ := filepath.Glob(filepath.Join(it.outDir, stagingDir, it.id+"-*")); len(old) > 0 {
		for _, d := range old {
			os.RemoveAll(d)
		}
	}
	stage, err := os.MkdirTemp(filepath.Join(it.outDir, stagingDir), it.id+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	t := time.Now()
	res := &WebResult{UUID: it.id, SHA256: it.sum, Bytes: n, Status: StatusOK}
	rec := &webRecord{Format: webRecordFormat, UUID: it.id, SHA256: it.sum, Source: s.label}
	staged, err := it.make(ctx, s, stage, res, rec)
	if err != nil {
		return nil, err
	}
	timings.Convert = since(t)
	rec.Config = o.config(res.Kind)

	// Record the staged outputs, then place them, then write the record.
	for i := range staged {
		if staged[i].SHA256, staged[i].Bytes, err = hashFile(filepath.Join(stage, staged[i].Name)); err != nil {
			return nil, err
		}
	}
	if len(it.ours) > 0 && res.Status == StatusOK {
		res.Status = StatusResumed
	}
	pend := webPending{Format: webPendingFormat, UUID: it.id, Config: rec.Config, Outputs: staged}
	for name, sum := range it.ours {
		if !hasOutput(staged, name) {
			// Keep knowing earlier files across a second interruption.
			pend.Outputs = append(pend.Outputs, webOutput{Name: name, SHA256: sum})
		}
	}
	if err := writeJSON(it.pendingPath, pend, true); err != nil {
		return nil, err
	}
	for _, out := range staged {
		if err := it.place(filepath.Join(stage, out.Name), out); err != nil {
			return nil, err
		}
		if webAfterPlace != nil {
			if err := webAfterPlace(out.Name); err != nil {
				return nil, err
			}
		}
	}
	res.Files = map[string]string{}
	for _, out := range staged {
		res.Files[out.Role] = filepath.Join(it.outDir, out.Name)
	}
	res.Timings = timings
	if it.metaPath != "" {
		res.Metadata = it.metaPath
		rec.Outputs = staged
		rec.Result = it.namesOnly(res)
		if err := writeJSON(it.metaPath, rec, it.replaceRecord || r.Output.Overwrite); err != nil {
			return nil, err
		}
	}
	os.Remove(it.pendingPath)
	return res, nil
}

func hasOutput(outs []webOutput, name string) bool {
	for _, o := range outs {
		if o.Name == name {
			return true
		}
	}
	return false
}

// place puts a staged output in the output directory. An existing file is
// replaced when --overwrite is given or when an earlier run of musafoz
// wrote it (its SHA-256 matches); a file with the same content is kept;
// any other file is a conflict and is not touched.
func (it *webItem) place(staged string, out webOutput) error {
	dst := filepath.Join(it.outDir, out.Name)
	if it.r.Output.Overwrite {
		return PlaceFile(staged, dst, true)
	}
	err := PlaceFile(staged, dst, false)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		return err
	}
	cur, _, herr := hashFile(dst)
	switch {
	case herr != nil:
		return herr
	case cur == out.SHA256:
		return nil
	case it.ours[out.Name] != "" && cur == it.ours[out.Name]:
		return PlaceFile(staged, dst, true)
	}
	return &ConflictError{Paths: []string{dst}}
}

// verify checks that every output is in the output directory of this run
// with its recorded SHA-256, and describes the ones that are not.
func (it *webItem) verify(outs []webOutput) []string {
	var problems []string
	for _, out := range outs {
		p := filepath.Join(it.outDir, out.Name)
		sum, _, err := hashFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			problems = append(problems, out.Name+" is missing")
		case err != nil:
			problems = append(problems, out.Name+": "+err.Error())
		case sum != out.SHA256:
			problems = append(problems, out.Name+" changed")
		}
	}
	if len(outs) == 0 {
		problems = append(problems, "the record lists no outputs")
	}
	return problems
}

// namesOnly returns res with file names instead of paths, for the record.
func (it *webItem) namesOnly(res *WebResult) *WebResult {
	c := *res
	c.Files = map[string]string{}
	for role, p := range res.Files {
		c.Files[role] = filepath.Base(p)
	}
	c.Metadata, c.Timings = "", nil
	return &c
}

// resultFrom returns the recorded result with paths in this run.
func (it *webItem) resultFrom(rec *webRecord) *WebResult {
	c := *rec.Result
	c.Files = map[string]string{}
	for role, name := range rec.Result.Files {
		c.Files[role] = filepath.Join(it.outDir, filepath.Base(name))
	}
	c.Metadata = it.metaPath
	return &c
}

func readRecord(path, id string) (*webRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec webRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.Format != webRecordFormat || rec.UUID != id || rec.Result == nil {
		return nil, fmt.Errorf("%s is not a musafoz web record for %s; it was left as it is", path, id)
	}
	return &rec, nil
}

func readPending(path, id string) (*webPending, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p webPending
	if err := json.Unmarshal(data, &p); err != nil || p.Format != webPendingFormat || p.UUID != id {
		return nil, fmt.Errorf("%s is not a musafoz pending file for %s; it was left as it is", path, id)
	}
	return &p, nil
}

func writeJSON(path string, v any, replace bool) error {
	return WriteFile(path, replace, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	})
}

func (r *Runner) remember(sum string, res *WebResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.web == nil {
		r.web = map[string]*WebResult{}
	}
	r.web[sum] = res
}

// make writes the outputs of a content to the staging directory and fills
// the result and the record metadata.
func (it *webItem) make(ctx context.Context, s *source, stage string, res *WebResult, rec *webRecord) ([]webOutput, error) {
	if it.format != "" {
		info, outs, err := it.r.webImage(ctx, s, it.format, it.id, it.o, res, stage)
		rec.Image = info
		return outs, err
	}
	rec.Probe = it.video.Raw
	return it.r.webVideo(ctx, s, it.video, it.id, it.o, res, stage)
}

// fitWithin scales w x h down so its long side is side, never up. The long
// side is exact and the short side is rounded.
func fitWithin(w, h, side int) (int, int) {
	if w <= side && h <= side {
		return w, h
	}
	if w >= h {
		return side, max(1, int(math.Round(float64(h)*float64(side)/float64(w))))
	}
	return max(1, int(math.Round(float64(w)*float64(side)/float64(h)))), side
}

func (r *Runner) webImage(ctx context.Context, s *source, format, id string, o WebOptions, res *WebResult, stage string) (*imaging.Info, []webOutput, error) {
	l, err := r.loadImage(ctx, s, format)
	if err != nil {
		return nil, nil, err
	}
	b := l.pixels.Bounds()
	res.Kind, res.Format = KindImage, format
	res.SourceWidth, res.SourceHeight = b.Dx(), b.Dy()
	var main image.Image = l.pixels
	if w, h := fitWithin(b.Dx(), b.Dy(), o.ImageMax); w != b.Dx() || h != b.Dy() {
		// Fill at the computed size: the aspect ratios differ by less than
		// a pixel, and fit would recompute and round the sides again.
		main = imaging.Resize(l.pixels, imaging.Size{Width: w, Height: h}, imaging.ModeFill)
	}
	mb := main.Bounds()
	res.Width, res.Height = mb.Dx(), mb.Dy()
	scale := math.Min(1, float64(o.ThumbShort)/float64(min(mb.Dx(), mb.Dy())))
	tw, th := max(1, int(math.Round(float64(mb.Dx())*scale))), max(1, int(math.Round(float64(mb.Dy())*scale)))
	thumb := imaging.Resize(main, imaging.Size{Width: tw, Height: th}, imaging.ModeFill)
	res.ThumbWidth, res.ThumbHeight = tw, th
	outs := []webOutput{{Role: "image", Name: id + ".jpg"}, {Role: "thumb", Name: id + "_thumb.jpg"}}
	if err := writeJPEG(l.color.ToSRGB(main), filepath.Join(stage, outs[0].Name), o.ImageQuality); err != nil {
		return nil, nil, err
	}
	if err := writeJPEG(l.color.ToSRGB(thumb), filepath.Join(stage, outs[1].Name), o.ThumbQuality); err != nil {
		return nil, nil, err
	}
	if d := imaging.SelectDate(l.exif, l.xmp); d != nil {
		res.Date, res.DateSource = &d.Value, &d.Source
	}
	if l.color != nil {
		res.ColorFrom = l.color.Name
	}
	res.Screenshot = strings.EqualFold(l.xmp["exif:UserComment"], "Screenshot")
	return l.info, outs, nil
}

func (r *Runner) webVideo(ctx context.Context, s *source, info *media.Info, id string, o WebOptions, res *WebResult, stage string) ([]webOutput, error) {
	ffmpeg, err := r.tool(ctx, &r.ffmpeg, r.ResolveFFmpeg)
	if err != nil {
		return nil, err
	}
	if info.Video.HDR() {
		r.logf("%s: tone mapping %s HDR video to SDR", s.label, info.Video.ColorTransfer)
	}
	video := filepath.Join(stage, id+".mp4")
	if err := media.EncodeWeb(ctx, ffmpeg, s.path, video, info, o.Video, nil); err != nil {
		return nil, fmt.Errorf("convert the video: %w", err)
	}
	res.Kind, res.Format = KindVideo, info.Format
	res.SourceWidth, res.SourceHeight = info.Video.DisplayWidth, info.Video.DisplayHeight
	res.Width, res.Height = o.Video.WebSize(info.Video.DisplayWidth, info.Video.DisplayHeight)
	res.HDR = info.Video.HDR()
	if info.DurationSeconds != nil {
		res.Duration = math.Round(*info.DurationSeconds*1000) / 1000
	}
	if v, src, ok := media.VideoDate(info.Tags); ok {
		res.Date, res.DateSource = &v, &src
	}
	outs := []webOutput{{Role: "video", Name: id + ".mp4"}}
	if o.Preview != nil {
		// The preview writes beside the staged video, so its files are
		// staged too and placed with the others.
		cover, clip, mosaic, err := o.Preview(ctx, video, false)
		if err != nil {
			return nil, fmt.Errorf("preview of the web video: %w", err)
		}
		for role, p := range map[string]string{"cover": cover, "preview": clip, "mosaic": mosaic} {
			if filepath.Dir(p) != stage {
				return nil, fmt.Errorf("the preview wrote %s outside the staging directory", p)
			}
			outs = append(outs, webOutput{Role: role, Name: filepath.Base(p)})
		}
		sort.Slice(outs[1:], func(i, j int) bool { return outs[1+i].Role < outs[1+j].Role })
	}
	return outs, nil
}

// webDir returns the absolute output directory, created when missing.
func (r *Runner) webDir() (string, error) {
	dir := r.Output.Dir
	if dir == "" {
		dir = "."
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return dir, r.ensureDir(dir)
}

func writeJPEG(img image.Image, dst string, quality int) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := imaging.Encode(f, img, imaging.OutputJPEG, quality); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WebHooks receive the outcome of each list line.
type WebHooks struct {
	// OnItem receives each line that gave a result, with the source as
	// printed in logs.
	OnItem func(line int, source string, res *WebResult)
	// OnError receives each failed line.
	OnError func(*LineError)
}

// WebBatch makes web copies of the sources of a list, one per line. A
// failed line is reported and the others continue; the result is a
// *BatchError when any line failed.
func (r *Runner) WebBatch(ctx context.Context, listPath string, o WebOptions, h WebHooks) error {
	if err := o.check(); err != nil {
		return err
	}
	entries, err := ReadList(listPath)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s lists no sources", listPath)
	}
	dir := filepath.Dir(listPath)
	failed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fail := func(err error) {
			failed++
			if h.OnError != nil {
				h.OnError(&LineError{Line: e.Line, Source: label(e.Fields[0]), Err: err})
			}
		}
		if len(e.Fields) > 1 {
			fail(errors.New("the line has a TAB; web lists take one source per line"))
			continue
		}
		res, err := r.Web(ctx, ResolveListSource(dir, e.Fields[0]), fmt.Sprintf("line %d", e.Line), o)
		if err != nil {
			fail(err)
			continue
		}
		if h.OnItem != nil {
			h.OnItem(e.Line, label(e.Fields[0]), res)
		}
	}
	if failed > 0 {
		return &BatchError{Failed: failed, Total: len(entries)}
	}
	return nil
}
