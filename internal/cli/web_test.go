package cli

import (
	"encoding/binary"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabiotnt/musafoz/internal/testimages"
)

func TestParseBitrate(t *testing.T) {
	for in, want := range map[string]int{"5M": 5_000_000, "4500k": 4_500_000, "2.5M": 2_500_000, "800000": 800_000} {
		if got, err := parseBitrate(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "fast", "-1M", "50k", "500M"} {
		if _, err := parseBitrate(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestWebFlagErrors(t *testing.T) {
	src := writeFile(t, filepath.Join(t.TempDir(), "a.jpg"), phoneJPEG())
	cases := map[string][]string{
		"--output is required":             {"web", src},
		"--skip-existing needs --metadata": {"web", src, "--output", "o", "--skip-existing"},
		"--video-bitrate":                  {"web", src, "--output", "o", "--video-bitrate", "fast"},
		"--video-size is LONGxSHORT":       {"web", src, "--output", "o", "--video-size", "1080x1920"},
		"--frames must be 1 to 100":        {"web", src, "--output", "o", "--frames", "0"},
		"--thumb-quality":                  {"web", "batch", "list.txt", "--output", "o", "--thumb-quality", "101"},
		"[overwrite skip-existing]":        {"web", src, "--output", "o", "--metadata", "m", "--overwrite", "--skip-existing"},
	}
	for want, args := range cases {
		r := run(t, nil, args...)
		if r.err == nil || !strings.Contains(r.err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, r.err, want)
		}
	}
}

// web batch --jsonl reports each line with its UUID and leaves GPS and
// camera data to the private metadata files.
func TestWebBatchJSONL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "General", "phone.jpg"), phoneJPEG())
	writeFile(t, filepath.Join(dir, "Favorites", "phone.jpg"), phoneJPEG())
	shot := testimages.WithPNGChunks(testimages.PNG(testimages.Halves(40, 80, color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}), nil),
		[2][]byte{[]byte("iTXt"), testimages.PNGXMP(testimages.ScreenshotXMP("2024-09-27T08:37:55"))})
	writeFile(t, filepath.Join(dir, "Screenshots", "shot.png"), shot)
	list := writeFile(t, filepath.Join(dir, "list.txt"), []byte("General/phone.jpg\nmissing.heic\nFavorites/phone.jpg\nScreenshots/shot.png\n"))
	out, meta := filepath.Join(dir, "web"), filepath.Join(dir, "meta")
	r := run(t, nil, "web", "batch", list, "--output", out, "--metadata", meta, "--jsonl")
	if r.err == nil || r.err.Error() != "1 of 4 list lines failed" {
		t.Errorf("err = %v", r.err)
	}
	for _, leak := range []string{"GPS", "gps", "iPhone", "Apple"} {
		if strings.Contains(r.stdout, leak) {
			t.Errorf("report contains %q", leak)
		}
	}
	var recs []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%v: %s", err, l)
		}
		recs = append(recs, m)
	}
	if len(recs) != 4 {
		t.Fatalf("%d records:\n%s", len(recs), r.stdout)
	}
	status := func(i int) string { return recs[i]["status"].(string) }
	if status(0) != "ok" || status(1) != "error" || status(2) != "duplicate" || status(3) != "ok" {
		t.Errorf("statuses %s %s %s %s", status(0), status(1), status(2), status(3))
	}
	if recs[0]["uuid"] != recs[2]["uuid"] || recs[0]["kind"] != "image" || recs[0]["source_width"] != 32.0 || recs[0]["date_source"] != "DateTimeOriginal" {
		t.Errorf("phone records %v %v", recs[0], recs[2])
	}
	if recs[3]["date"] != "2024-09-27T08:37:55" || recs[3]["date_source"] != "XMP photoshop:DateCreated" || recs[3]["screenshot"] != true {
		t.Errorf("screenshot record %v", recs[3])
	}
	data, err := os.ReadFile(filepath.Join(meta, recs[0]["uuid"].(string)+".json"))
	if err != nil || !strings.Contains(string(data), `"gps"`) || !strings.Contains(string(data), "General/phone.jpg") {
		t.Errorf("metadata %v:\n%.300s", err, data)
	}

	// Running again with --skip-existing reuses every result.
	r = run(t, nil, "web", "batch", list, "--output", out, "--metadata", meta, "--skip-existing", "--jsonl")
	if strings.Count(r.stdout, `"status":"exists"`) != 2 || strings.Count(r.stdout, `"status":"duplicate"`) != 1 {
		t.Errorf("second run:\n%s", r.stdout)
	}
	_ = binary.BigEndian
}
