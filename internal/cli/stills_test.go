package cli

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"image/color"

	"github.com/fabiotnt/musafoz/internal/testimages"
)

func phoneJPEG() []byte {
	return testimages.JPEG(testimages.Halves(64, 32, color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}),
		testimages.PhoneEXIF(binary.BigEndian, 6))
}

func writeFile(t *testing.T, p string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestThumbCommand(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "photo one.jpg"), phoneJPEG())
	out := filepath.Join(dir, "thumbs")
	r := run(t, nil, "thumb", src, "--size", "20x20", "--output", out)
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := filepath.Join(out, "photo one_thumb.jpg")
	if strings.TrimSpace(r.stdout) != want {
		t.Errorf("stdout %q, want %q", r.stdout, want)
	}
	// Running again keeps the output and names the conflict.
	r = run(t, nil, "thumb", src, "--size", "20x20", "--output", out)
	if r.err == nil || !strings.Contains(r.err.Error(), "already exist") || !strings.Contains(r.err.Error(), want) {
		t.Errorf("second run: %v", r.err)
	}
	r = run(t, nil, "thumb", src, "--size", "20x20", "--output", out, "--overwrite", "--format", "png", "--mode", "fill", "--prefix", "p_")
	if r.err != nil || strings.TrimSpace(r.stdout) != filepath.Join(out, "p_photo one.png") {
		t.Errorf("png: %v %q", r.err, r.stdout)
	}
}

func TestThumbFlagErrors(t *testing.T) {
	src := writeFile(t, filepath.Join(t.TempDir(), "a.jpg"), phoneJPEG())
	cases := map[string][]string{
		"--size is required":              {"thumb", src},
		"not in WIDTHxHEIGHT":             {"thumb", src, "--size", "20"},
		"[alongside output]":              {"thumb", src, "--size", "2x2", "--output", "x", "--alongside"},
		"[prefix suffix]":                 {"thumb", src, "--size", "2x2", "--prefix", "a", "--suffix", "b"},
		"is not fit or fill":              {"thumb", src, "--size", "2x2", "--mode", "crop"},
		"is not jpeg or png":              {"thumb", src, "--size", "2x2", "--format", "gif"},
		"file does not exist":             {"thumb", src + ".missing", "--size", "2x2"},
		"[alongside output] were all set": {"thumb", "batch", "list.txt", "--size", "2x2", "--output", "x", "--alongside"},
	}
	for want, args := range cases {
		r := run(t, nil, args...)
		if r.err == nil || !strings.Contains(r.err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, r.err, want)
		}
	}
}

func TestThumbBatchCommandReportsLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "good one.jpg"), phoneJPEG())
	list := writeFile(t, filepath.Join(dir, "list.txt"), []byte("good one.jpg\n\nmissing.jpg\n"))
	r := run(t, nil, "thumb", "batch", list, "--size", "10x10", "--output", filepath.Join(dir, "out"))
	if r.err == nil || r.err.Error() != "1 of 2 list lines failed" {
		t.Errorf("err = %v", r.err)
	}
	if !strings.Contains(r.stderr, "error: line 3: missing.jpg: file does not exist") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	if strings.TrimSpace(r.stdout) != filepath.Join(dir, "out", "good one_thumb.jpg") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestImageFlagErrors(t *testing.T) {
	cases := map[string][]string{
		"give times or --random":    {"image", "clip.mp4", "00:00:01", "--random", "2"},
		"--random must be 1":        {"image", "clip.mp4", "--random", "0"},
		"--mode needs --size":       {"image", "clip.mp4", "--mode", "fill"},
		"not in HH:MM:SS":           {"image", "clip.mp4", "1:00"},
		"is negative":               {"image", "clip.mp4", "--", "-00:00:01"},
		"file does not exist":       {"image", "missing.mp4", "00:00:01"},
		"[output alongside]":        {"image", "clip.mp4", "--output", "x", "--alongside"},
		"accepts between 1 and 2":   {"cover", "clip.mp4", "00:00:01", "00:00:02"},
		"cover: timestamp \"1:00\"": {"cover", "clip.mp4", "1:00"},
	}
	for want, args := range cases {
		r := run(t, nil, args...)
		if r.err == nil || !strings.Contains(r.err.Error(), strings.TrimPrefix(want, "cover: ")) {
			t.Errorf("%v: err = %v, want %q", args, r.err, want)
		}
	}
}

func TestImageAndCoverCommands(t *testing.T) {
	f := newPreviewFixture(t)
	ffprobe, ffmpeg := f.args[1], f.args[3]
	out := filepath.Join(filepath.Dir(f.input), "frames")
	r := run(t, nil, "--ffprobe", ffprobe, "--ffmpeg", ffmpeg, "image", f.input, "00:00:00", "00:00:01.500", "--output", out, "--size", "4x4")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	want := filepath.Join(out, "clip_00-00-00.jpg") + "\n" + filepath.Join(out, "clip_00-00-01-500.jpg") + "\n"
	if r.stdout != want {
		t.Errorf("stdout %q, want %q", r.stdout, want)
	}
	r = run(t, nil, "--ffprobe", ffprobe, "--ffmpeg", ffmpeg, "image", f.input, "--random", "3", "--alongside")
	if r.err != nil || strings.Count(r.stdout, filepath.Join(filepath.Dir(f.input), "clip_")) != 3 {
		t.Errorf("random: %v\n%s", r.err, r.stdout)
	}

	r = run(t, nil, "--ffprobe", ffprobe, "--ffmpeg", ffmpeg, "cover", f.input, "00:00:01.250", "--json")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	var res struct {
		Cover   string  `json:"cover"`
		CoverAt float64 `json:"cover_at_seconds"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil || res.CoverAt != 1.25 || res.Cover != strings.TrimSuffix(f.input, ".mp4")+"_cover.jpg" {
		t.Errorf("cover result %s (%v)", r.stdout, err)
	}
	if _, err := os.Stat(strings.TrimSuffix(f.input, ".mp4") + "_preview.mp4"); err == nil {
		t.Error("cover wrote a preview video")
	}
	r = run(t, nil, "--ffprobe", ffprobe, "--ffmpeg", ffmpeg, "cover", f.input)
	if r.err == nil || !strings.Contains(r.err.Error(), "check outputs") || !strings.Contains(r.err.Error(), "_cover.jpg") {
		t.Errorf("second cover: %v", r.err)
	}
	r = run(t, nil, "--ffprobe", ffprobe, "--ffmpeg", ffmpeg, "cover", f.input, "--overwrite")
	if r.err != nil || !strings.Contains(r.stdout, "_cover.jpg (at ") {
		t.Errorf("random cover: %v %s", r.err, r.stdout)
	}
	r = run(t, nil, "--ffprobe", ffprobe, "--ffmpeg", ffmpeg, "cover", f.input, "00:00:02", "--overwrite")
	if r.err == nil || !strings.Contains(r.err.Error(), "cover time 00:00:02 is outside the video") {
		t.Errorf("late cover: %v", r.err)
	}
}

func TestInspectImage(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "phone.jpg"), phoneJPEG())
	before, _ := os.ReadFile(src)
	// No ffprobe is resolved for images: a bad --ffprobe does not matter.
	r := run(t, nil, "--ffprobe", "/does/not/exist", "inspect", src)
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"Format:      JPEG image", "Dimensions:  64x32", "Orientation: 6 (rotated 90 degrees clockwise)",
		"Display:     32x64", "Camera:      Apple iPhone 15 Pro", "Exposure:    1/120 s, f/1.78, ISO 64", "GPS:         -23.555000, -46.630000", "EXIF:        "} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("text lacks %q:\n%s", want, r.stdout)
		}
	}
	r = run(t, nil, "inspect", "--json", src)
	if r.err != nil {
		t.Fatal(r.err)
	}
	var doc struct {
		Kind        string `json:"kind"`
		Orientation int    `json:"orientation"`
		EXIF        struct {
			IFD0 []map[string]any `json:"ifd0"`
			GPS  []map[string]any `json:"gps"`
			Exif []map[string]any `json:"exif"`
		} `json:"exif"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	if doc.Kind != "image" || doc.Orientation != 6 || len(doc.EXIF.GPS) != 7 || len(doc.EXIF.Exif) != 14 {
		t.Errorf("doc %+v", doc)
	}
	found := false
	for _, tag := range doc.EXIF.IFD0 {
		if tag["id"] == "0xC5D8" && tag["name"] == nil {
			found = true
		}
	}
	if !found {
		t.Error("unknown tag 0xC5D8 missing from JSON")
	}
	if after, _ := os.ReadFile(src); string(after) != string(before) {
		t.Error("inspect changed the image")
	}
}

func TestInspectImageURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/clip.mp4" {
			w.Write([]byte("\x00\x00\x00\x18ftypisom not an image"))
			return
		}
		w.Write(phoneJPEG())
	}))
	defer srv.Close()
	r := run(t, nil, "inspect", "--json", srv.URL+"/p/photo.jpg?token=SECRET")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Contains(r.stdout+r.stderr, "SECRET") || !strings.Contains(r.stdout, `"path": "`+srv.URL+`/p/photo.jpg"`) {
		t.Errorf("stdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
	}
	r = run(t, nil, "inspect", srv.URL+"/clip.mp4?token=SECRET")
	if r.err == nil || !strings.Contains(r.err.Error(), "videos are inspected from local files only") || strings.Contains(r.err.Error(), "SECRET") {
		t.Errorf("video URL: %v", r.err)
	}
}

func syntheticHEIC(t *testing.T) []byte {
	t.Helper()
	hevc, err := os.ReadFile(testdata("image", "quadrants-64x48.hevc"))
	if err != nil {
		t.Fatal(err)
	}
	return testimages.HEIF(testimages.HEIFOptions{HEVC: hevc, Width: 64, Height: 48, Grid: true, Rotation: 270, Mirror: -1,
		EXIF: testimages.PhoneEXIF(binary.BigEndian, 6)})
}

func TestInspectHEIC(t *testing.T) {
	data := syntheticHEIC(t)
	src := writeFile(t, filepath.Join(t.TempDir(), "IMG_0001.HEIC"), data)
	// No FFmpeg or FFprobe is needed to inspect a HEIC file.
	r := run(t, nil, "--ffprobe", "/does/not/exist", "--ffmpeg", "/does/not/exist", "inspect", src)
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"Format:      HEIF/HEIC image", "Dimensions:  128x48", "Transforms:  rotated 90 degrees clockwise (HEIF irot and imir, applied)",
		"Orientation: 6 (rotated 90 degrees clockwise) in EXIF, not applied to HEIF", "Display:     48x128", "Camera:      Apple iPhone 15 Pro", "GPS:         "} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("text lacks %q:\n%s", want, r.stdout)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer srv.Close()
	r = run(t, nil, "inspect", "--json", srv.URL+"/photos/IMG_0001.HEIC?sig=SECRET")
	if r.err != nil {
		t.Fatal(r.err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["format"] != "heif" || doc["display_width"] != 48.0 || doc["exif"] == nil || doc["path"] != srv.URL+"/photos/IMG_0001.HEIC" {
		t.Errorf("doc %v", doc)
	}
	if _, ok := doc["transparent_pixels"]; ok || strings.Contains(r.stdout+r.stderr, "SECRET") {
		t.Errorf("unexpected transparent_pixels or query in output:\n%s\n%s", r.stdout, r.stderr)
	}

	// Other images keep transparent_pixels in their JSON.
	jpg := writeFile(t, filepath.Join(t.TempDir(), "a.jpg"), phoneJPEG())
	r = run(t, nil, "inspect", "--json", jpg)
	if r.err != nil || !strings.Contains(r.stdout, `"transparent_pixels": false`) || strings.Contains(r.stdout, "heif_transforms") {
		t.Errorf("jpeg JSON changed: %v\n%s", r.err, r.stdout)
	}
}

// thumb batch --jsonl writes one record per list line, in order, downloads
// each URL once for the thumbnail and its record, keeps going after a
// failed line, and leaves the query, credentials, and EXIF details out.
func TestThumbBatchJSONL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(phoneJPEG())
	}))
	defer srv.Close()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "General", "phone one.jpg"), phoneJPEG())
	writeFile(t, filepath.Join(dir, "Screenshots", "screen.png"),
		testimages.PNG(testimages.Halves(30, 60, color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}), nil))
	writeFile(t, filepath.Join(dir, "notes.txt"), []byte("not an image"))
	remote := strings.Replace(srv.URL, "http://", "http://user:pass@", 1) + "/remote/pic.jpg?sig=SECRET#frag"
	list := writeFile(t, filepath.Join(dir, "list.txt"), []byte(
		"General/phone one.jpg\n"+
			"missing.jpg\n"+
			"\n"+
			remote+"\n"+
			"notes.txt\n"+
			"Screenshots/screen.png\n"))
	out := filepath.Join(dir, "out")
	r := run(t, nil, "thumb", "batch", list, "--size", "32x32", "--quality", "80", "--output", out, "--jsonl")
	if r.err == nil || r.err.Error() != "2 of 5 list lines failed" {
		t.Errorf("err = %v", r.err)
	}
	if hits.Load() != 1 {
		t.Errorf("the URL was requested %d times", hits.Load())
	}
	for _, leak := range []string{"SECRET", "frag", "user", "pass", "GPS", "gps", "iPhone", "Apple", "exif"} {
		if strings.Contains(r.stdout, leak) {
			t.Errorf("report contains %q:\n%s", leak, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "error: line 2: missing.jpg: file does not exist") ||
		!strings.Contains(r.stderr, "error: line 5: notes.txt") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d records:\n%s", len(lines), r.stdout)
	}
	want := []string{
		`{"line":1,"source":"General/phone one.jpg","status":"ok","output":"` + filepath.Join(out, "phone one_thumb.jpg") +
			`","source_width":32,"source_height":64,"output_width":16,"output_height":32,"date":"2026-10-31T17:51:24-03:00","date_source":"DateTimeOriginal"}`,
		`{"line":2,"source":"missing.jpg","status":"error","error":"file does not exist"}`,
		`{"line":4,"source":"` + srv.URL + `/remote/pic.jpg","status":"ok","output":"` + filepath.Join(out, "pic_thumb.jpg") +
			`","source_width":32,"source_height":64,"output_width":16,"output_height":32,"date":"2026-10-31T17:51:24-03:00","date_source":"DateTimeOriginal"}`,
		"",
		`{"line":6,"source":"Screenshots/screen.png","status":"ok","output":"` + filepath.Join(out, "screen_thumb.jpg") +
			`","source_width":30,"source_height":60,"output_width":16,"output_height":32,"date":null,"date_source":null}`,
	}
	for i, l := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Errorf("record %d is not JSON: %v", i, err)
		}
		if i == 3 {
			if rec["line"] != 5.0 || rec["status"] != "error" || !strings.Contains(rec["error"].(string), "not a supported image") {
				t.Errorf("record 4: %s", l)
			}
			continue
		}
		if l != want[i] {
			t.Errorf("record %d:\n got %s\nwant %s", i, l, want[i])
		}
	}

	// Without --jsonl the output stays the list of written paths.
	r = run(t, nil, "thumb", "batch", list, "--size", "32x32", "--output", filepath.Join(dir, "plain"))
	if strings.Contains(r.stdout, "{") || len(strings.Split(strings.TrimSpace(r.stdout), "\n")) != 3 {
		t.Errorf("plain stdout:\n%s", r.stdout)
	}
}

// --quality is checked before the list is read or any output is written.
func TestThumbQualityFlag(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "a.jpg"), phoneJPEG())
	list := writeFile(t, filepath.Join(dir, "list.txt"), []byte("a.jpg\n"))
	out := filepath.Join(dir, "out")
	cases := map[string][]string{
		"--quality: quality 0 is outside 1 to 100":   {"thumb", src, "--size", "8x8", "--quality", "0", "--output", out},
		"--quality: quality 101 is outside 1 to 100": {"thumb", "batch", list, "--size", "8x8", "--quality", "101", "--output", out, "--jsonl"},
		"--quality applies to JPEG output only":      {"thumb", "batch", list, "--size", "8x8", "--quality", "80", "--format", "png", "--output", out},
		"invalid argument":                           {"thumb", src, "--size", "8x8", "--quality", "high"},
	}
	for want, args := range cases {
		r := run(t, nil, args...)
		if r.err == nil || !strings.Contains(r.err.Error(), want) || r.stdout != "" {
			t.Errorf("%v: err = %v, stdout %q, want %q", args, r.err, r.stdout, want)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("an output directory was created before the flags were checked")
	}
	r := run(t, nil, "thumb", src, "--size", "8x8", "--quality", "1", "--output", out)
	if r.err != nil {
		t.Errorf("quality 1: %v", r.err)
	}
	r = run(t, nil, "image", "clip.mp4", "--quality", "80")
	if r.err == nil || !strings.Contains(r.err.Error(), "unknown flag: --quality") {
		t.Errorf("image --quality: %v", r.err)
	}
}
