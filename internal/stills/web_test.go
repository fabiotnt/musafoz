package stills

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/testimages"
)

func solidImage(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// p3JPEG is a 3000x1000 phone-style JPEG in Display P3 with EXIF and GPS.
func p3JPEG(c color.NRGBA) []byte {
	icc := testimages.ICC("Display P3", testimages.DisplayP3Colorants)
	segs := []struct {
		Marker  byte
		Payload []byte
	}{}
	for _, p := range testimages.JPEGICC(icc, 60000) {
		segs = append(segs, struct {
			Marker  byte
			Payload []byte
		}{0xE2, p})
	}
	return testimages.WithJPEGSegments(testimages.JPEG(solidImage(3000, 1000, c), testimages.PhoneEXIF(binary.BigEndian, 1)), segs...)
}

func screenshotPNG() []byte {
	return testimages.WithPNGChunks(testimages.PNG(solidImage(100, 200, color.NRGBA{10, 20, 30, 255}), nil),
		[2][]byte{[]byte("iTXt"), testimages.PNGXMP(testimages.ScreenshotXMP("2025-01-31T09:35:03"))})
}

func TestContentUUID(t *testing.T) {
	a, b := ContentUUID(strings.Repeat("ab", 32)), ContentUUID(strings.Repeat("cd", 32))
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(a) || !re.MatchString(b) || a == b || a != ContentUUID(strings.Repeat("ab", 32)) {
		t.Errorf("uuids %s %s", a, b)
	}
}

func TestWebImages(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	p3 := write(t, filepath.Join(src, "General", "IMG_0001.JPG"), p3JPEG(color.NRGBA{200, 100, 50, 255}))
	shot := write(t, filepath.Join(src, "Screenshots", "shot.png"), screenshotPNG())
	again := write(t, filepath.Join(src, "Favorites", "IMG_0001.JPG"), p3JPEG(color.NRGBA{200, 100, 50, 255}))
	o := DefaultWebOptions()
	o.MetadataDir = filepath.Join(dir, "meta")
	out := Output{Dir: filepath.Join(dir, "web")}
	r := newRunner(t, out)

	res, err := r.Web(context.Background(), p3, "line 1", o)
	must(t, err)
	if res.Status != StatusOK || res.Kind != KindImage || res.SourceWidth != 3000 || res.Width != 2560 || res.Height != 853 ||
		res.ThumbWidth != 1441 || res.ThumbHeight != 480 || res.ColorFrom != "Display P3" || res.Screenshot {
		t.Errorf("p3 result %+v", res)
	}
	if res.Date == nil || *res.Date != "2026-10-31T17:51:24-03:00" || *res.DateSource != "DateTimeOriginal" {
		t.Errorf("p3 date %v %v", res.Date, res.DateSource)
	}
	if res.UUID != ContentUUID(res.SHA256) || filepath.Base(res.Files["image"]) != res.UUID+".jpg" || filepath.Base(res.Files["thumb"]) != res.UUID+"_thumb.jpg" {
		t.Errorf("names %v for %s", res.Files, res.UUID)
	}
	for role, p := range res.Files {
		img, data := decode(t, p)
		if img.EXIF != nil || img.ICC != nil || bytes.Contains(data, []byte("iPhone")) || bytes.Contains(data, []byte("Display P3")) {
			t.Errorf("%s keeps metadata", role)
		}
		// P3 (200, 100, 50) is about (219, 94, 37) in sRGB.
		r, g, b, _ := img.Pixels.At(10, 10).RGBA()
		if r>>8 < 212 || g>>8 > 101 || b>>8 > 45 {
			t.Errorf("%s: pixel %d %d %d, colors not converted to sRGB", role, r>>8, g>>8, b>>8)
		}
	}
	var meta map[string]any
	data, err := os.ReadFile(res.Metadata)
	must(t, err)
	must(t, json.Unmarshal(data, &meta))
	img := meta["image"].(map[string]any)
	if meta["uuid"] != res.UUID || img["color_profile"] != "Display P3" || img["exif"].(map[string]any)["gps"] == nil {
		t.Errorf("metadata %s", data)
	}

	// The screenshot is not enlarged, and its date comes from XMP.
	res2, err := r.Web(context.Background(), shot, "line 2", o)
	must(t, err)
	if res2.Width != 100 || res2.Height != 200 || res2.ThumbWidth != 100 || !res2.Screenshot || res2.ColorFrom != "" ||
		res2.Date == nil || *res2.Date != "2025-01-31T09:35:03" || *res2.DateSource != "XMP photoshop:DateCreated" {
		t.Errorf("screenshot %+v", res2)
	}

	// The same content again is a duplicate with the same UUID and files.
	res3, err := r.Web(context.Background(), again, "line 3", o)
	must(t, err)
	if res3.Status != StatusDuplicate || res3.UUID != res.UUID || res3.Files["image"] != res.Files["image"] {
		t.Errorf("duplicate %+v", res3)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "web"))
	if len(entries) != 4 {
		t.Errorf("%d files written", len(entries))
	}

	// A new run keeps existing files: an error without --skip-existing, the
	// recorded result with it, and nothing is written again.
	before, _ := os.Stat(res.Files["image"])
	var ce *ConflictError
	if _, err := newRunner(t, out).Web(context.Background(), p3, "line 1", o); !errors.As(err, &ce) {
		t.Errorf("second run: %v", err)
	}
	o.SkipExisting = true
	res4, err := newRunner(t, out).Web(context.Background(), p3, "line 1", o)
	must(t, err)
	after, _ := os.Stat(res.Files["image"])
	if res4.Status != StatusExists || res4.UUID != res.UUID || res4.Width != 2560 || *res4.Date != *res.Date || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("resumed %+v", res4)
	}
	// A missing output means the content is made again.
	os.Remove(res2.Files["thumb"])
	o.SkipExisting = true
	if _, err := newRunner(t, out).Web(context.Background(), shot, "line 2", o); !errors.As(err, &ce) {
		t.Errorf("partial outputs: %v", err)
	}
}

func TestWebRefusals(t *testing.T) {
	dir := t.TempDir()
	r := newRunner(t, Output{Dir: filepath.Join(dir, "web")})
	o := DefaultWebOptions()
	txt := write(t, filepath.Join(dir, "notes.txt"), []byte("hello"))
	if _, err := r.Web(context.Background(), txt, "x", o); err == nil || !strings.Contains(err.Error(), "not a supported image, and not a video") {
		t.Errorf("text: %v", err)
	}
	for name, mod := range map[string]func(*WebOptions){
		"skip without metadata": func(o *WebOptions) { o.SkipExisting = true },
		"thumb over image":      func(o *WebOptions) { o.ThumbShort = 4000 },
		"quality":               func(o *WebOptions) { o.ImageQuality = 0 },
		"tiny":                  func(o *WebOptions) { o.ImageMax = 8 },
	} {
		bad := DefaultWebOptions()
		mod(&bad)
		if _, err := r.Web(context.Background(), txt, "x", bad); err == nil || strings.Contains(err.Error(), "not a supported") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "web")); err == nil {
		t.Error("the output directory was created for refused input")
	}
}

func TestWebBatch(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "one.jpg"), p3JPEG(color.NRGBA{1, 2, 3, 255}))
	write(t, filepath.Join(dir, "b", "one.jpg"), p3JPEG(color.NRGBA{1, 2, 3, 255}))
	write(t, filepath.Join(dir, "shot.png"), screenshotPNG())
	list := write(t, filepath.Join(dir, "list.txt"), []byte("a/one.jpg\nmissing.jpg\nb/one.jpg\n\nshot.png\nx\ty\n"))
	r := newRunner(t, Output{Dir: filepath.Join(dir, "web")})
	var got []string
	var failed []int
	err := r.WebBatch(context.Background(), list, DefaultWebOptions(), WebHooks{
		OnItem:  func(line int, source string, res *WebResult) { got = append(got, source+" "+res.Status) },
		OnError: func(e *LineError) { failed = append(failed, e.Line) },
	})
	var be *BatchError
	if !errors.As(err, &be) || be.Failed != 2 || be.Total != 5 {
		t.Errorf("err = %v", err)
	}
	if strings.Join(got, ",") != "a/one.jpg ok,b/one.jpg duplicate,shot.png ok" || len(failed) != 2 || failed[0] != 2 || failed[1] != 6 {
		t.Errorf("items %v, failed %v", got, failed)
	}
	_ = imaging.OutputJPEG
}
