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
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
)

// WebOptions say how web copies are made. Outputs go to Runner.Output.Dir
// and are named after the content: <uuid>.jpg and <uuid>_thumb.jpg for
// images; <uuid>.mp4, <uuid>_cover.jpg, <uuid>_preview.mp4, and
// <uuid>_preview.jpg for videos; <uuid>.json in MetadataDir.
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
	// MetadataDir receives one JSON file per content with every metadata
	// field musafoz reads, including GPS. Keep it private. Empty writes
	// none.
	MetadataDir string
	// SkipExisting reuses the recorded result of a content whose metadata
	// file and outputs all exist, so an interrupted job can run again. It
	// needs MetadataDir.
	SkipExisting bool
	// Preview writes the cover, preview video, and mosaic beside a web
	// video and returns their paths. Nil writes no previews.
	Preview func(ctx context.Context, video string, overwrite bool) (cover, clip, mosaic string, err error)
}

// DefaultWebOptions are 2560 pixel images, 480 pixel thumbnails, and 1080p
// video.
func DefaultWebOptions() WebOptions {
	return WebOptions{ImageMax: 2560, ThumbShort: 480, ImageQuality: 85, ThumbQuality: 80, Video: media.DefaultWebVideo}
}

// Web result kinds and statuses.
const (
	KindImage = "image"
	KindVideo = "video"

	StatusOK        = "ok"
	StatusDuplicate = "duplicate"
	StatusExists    = "exists"
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
	// Files maps roles to paths: image and thumb, or video, cover,
	// preview, and mosaic.
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
	// Metadata is the path of the metadata file, when one is written.
	Metadata string `json:"metadata,omitempty"`
	// Status is StatusOK, StatusDuplicate for content already handled in
	// this run, or StatusExists for content reused with SkipExisting.
	Status string `json:"-"`
}

// webMetadata is the content of a metadata file.
type webMetadata struct {
	UUID   string     `json:"uuid"`
	SHA256 string     `json:"sha256"`
	Source string     `json:"source"`
	Result *WebResult `json:"result"`
	// Image is the full inspection of an image source; Probe is the
	// FFprobe output of a video source, with its container tags.
	Image *imaging.Info   `json:"image,omitempty"`
	Probe json.RawMessage `json:"probe,omitempty"`
}

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

// Web makes the web copy of one source, a local file or a URL. Content
// already handled in this run is not converted again: the result is the
// earlier one with StatusDuplicate. owner names the item in collision
// messages.
func (r *Runner) Web(ctx context.Context, raw, owner string, o WebOptions) (*WebResult, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	s, err := r.resolveSource(ctx, raw)
	if err != nil {
		return nil, err
	}
	sum, n, err := hashFile(s.path)
	if err != nil {
		return nil, err
	}
	id := ContentUUID(sum)
	r.mu.Lock()
	prev := r.web[sum]
	r.mu.Unlock()
	if prev != nil {
		dup := *prev
		dup.Status = StatusDuplicate
		return &dup, nil
	}
	if o.SkipExisting {
		if res := r.existingWeb(o, id); res != nil {
			r.remember(sum, res)
			return res, nil
		}
	}
	res := &WebResult{UUID: id, SHA256: sum, Bytes: n, Status: StatusOK}
	format, err := imaging.SniffFile(s.path)
	if err != nil {
		return nil, err
	}
	var meta webMetadata
	switch {
	case format != "":
		if !imaging.Decodable(format) && format != imaging.FormatHEIF {
			return nil, &imaging.UnsupportedError{Format: format}
		}
		info, err := r.webImage(ctx, s, format, id, o, res, owner)
		if err != nil {
			return nil, err
		}
		meta.Image = info
	default:
		info, err := r.inspect(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("not a supported image, and not a video: %w", err)
		}
		if err := r.webVideo(ctx, s, info, id, o, res, owner); err != nil {
			return nil, err
		}
		meta.Probe = info.Raw
	}
	if o.MetadataDir != "" {
		res.Metadata = filepath.Join(o.MetadataDir, id+".json")
		meta.UUID, meta.SHA256, meta.Source, meta.Result = id, sum, s.label, res
		if err := r.ensureDir(o.MetadataDir); err != nil {
			return nil, err
		}
		if err := r.claim([]string{res.Metadata}, owner); err != nil {
			return nil, err
		}
		err := WriteFile(res.Metadata, r.Output.Overwrite, func(w io.Writer) error {
			enc := json.NewEncoder(w)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			return enc.Encode(meta)
		})
		if err != nil {
			return nil, err
		}
	}
	r.remember(sum, res)
	return res, nil
}

func (r *Runner) remember(sum string, res *WebResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.web == nil {
		r.web = map[string]*WebResult{}
	}
	r.web[sum] = res
}

// existingWeb returns the recorded result of a content whose metadata file
// and outputs all exist, or nil.
func (r *Runner) existingWeb(o WebOptions, id string) *WebResult {
	data, err := os.ReadFile(filepath.Join(o.MetadataDir, id+".json"))
	if err != nil {
		return nil
	}
	var m webMetadata
	if json.Unmarshal(data, &m) != nil || m.Result == nil || m.UUID != id || len(m.Result.Files) == 0 {
		return nil
	}
	for _, p := range m.Result.Files {
		if _, err := os.Stat(p); err != nil {
			return nil
		}
	}
	m.Result.Status = StatusExists
	return m.Result
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

func (r *Runner) webImage(ctx context.Context, s *source, format, id string, o WebOptions, res *WebResult, owner string) (*imaging.Info, error) {
	dir, err := r.webDir()
	if err != nil {
		return nil, err
	}
	mainPath, thumbPath := filepath.Join(dir, id+".jpg"), filepath.Join(dir, id+"_thumb.jpg")
	if err := r.claim([]string{mainPath, thumbPath}, owner); err != nil {
		return nil, err
	}
	l, err := r.loadImage(ctx, s, format)
	if err != nil {
		return nil, err
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
	if err := r.writeJPEG(l.color.ToSRGB(main), mainPath, o.ImageQuality); err != nil {
		return nil, err
	}
	if err := r.writeJPEG(l.color.ToSRGB(thumb), thumbPath, o.ThumbQuality); err != nil {
		return nil, err
	}
	res.Files = map[string]string{"image": mainPath, "thumb": thumbPath}
	if d := imaging.SelectDate(l.exif, l.xmp); d != nil {
		res.Date, res.DateSource = &d.Value, &d.Source
	}
	if l.color != nil {
		res.ColorFrom = l.color.Name
	}
	res.Screenshot = strings.EqualFold(l.xmp["exif:UserComment"], "Screenshot")
	return l.info, nil
}

func (r *Runner) webVideo(ctx context.Context, s *source, info *media.Info, id string, o WebOptions, res *WebResult, owner string) error {
	dir, err := r.webDir()
	if err != nil {
		return err
	}
	out := filepath.Join(dir, id+".mp4")
	if err := r.claim([]string{out}, owner); err != nil {
		return err
	}
	ffmpeg, err := r.tool(ctx, &r.ffmpeg, r.ResolveFFmpeg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+id+".mp4.musafoz-*")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if info.Video.HDR() {
		r.logf("%s: tone mapping %s HDR video to SDR", s.label, info.Video.ColorTransfer)
	}
	if err := media.EncodeWeb(ctx, ffmpeg, s.path, tmp.Name(), info, o.Video, nil); err != nil {
		return fmt.Errorf("convert the video: %w", err)
	}
	if err := PlaceFile(tmp.Name(), out, r.Output.Overwrite); err != nil {
		return err
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
	res.Files = map[string]string{"video": out}
	if o.Preview != nil {
		cover, clip, mosaic, err := o.Preview(ctx, out, r.Output.Overwrite)
		if err != nil {
			return fmt.Errorf("preview of the web video: %w", err)
		}
		res.Files["cover"], res.Files["preview"], res.Files["mosaic"] = cover, clip, mosaic
	}
	return nil
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

func (r *Runner) writeJPEG(img image.Image, dst string, quality int) error {
	return WriteFile(dst, r.Output.Overwrite, func(w io.Writer) error {
		return imaging.Encode(w, img, imaging.OutputJPEG, quality)
	})
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
