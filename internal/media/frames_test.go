package media

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFrameTimes(t *testing.T) {
	got := FrameTimes(10, 4, 9.9)
	want := []float64{0, 2.5, 5, 7.5}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FrameTimes = %v, want %v", got, want)
		}
	}
	// A 2-second clip at 10 fps: the last frame starts at 1.9 seconds.
	capped := FrameTimes(2, 100, 1.9)
	if capped[95] != 1.9 || capped[99] != 1.9 || capped[94] != 1.88 {
		t.Errorf("cap not applied: %v", capped[94:])
	}
}

func TestCoverTime(t *testing.T) {
	if got := CoverTime(100, 99, func() float64 { return 0 }); got != 15 {
		t.Errorf("low end = %v, want 15", got)
	}
	if got := CoverTime(100, 99, func() float64 { return 0.999999 }); got < 84.99 || got >= 85 {
		t.Errorf("high end = %v, want just under 85", got)
	}
	// Clips of 10 seconds or less draw from the whole range up to the last
	// frame instead of using the midpoint.
	short := []struct {
		r, want float64
	}{{0, 0}, {0.5, 4.95}, {0.9, 8.91}, {0.999999, 9.899}}
	for _, c := range short {
		if got := CoverTime(10, 9.9, func() float64 { return c.r }); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("10-second clip, r=%v: %v, want %v", c.r, got, c.want)
		}
	}
	if got := CoverTime(2, 1.9, func() float64 { return 0.999999 }); got > 1.9 {
		t.Errorf("short clip = %v, past the last frame 1.9", got)
	}
	if got := CoverTime(0.1, 0, func() float64 { return 0.7 }); got != 0 {
		t.Errorf("one-frame clip = %v, want 0", got)
	}
	// Every draw stays inside the range.
	for i := range 1000 {
		r := float64(i) / 1000
		if got := CoverTime(3.3, 3.266, func() float64 { return r }); got < 0 || got > 3.266 {
			t.Fatalf("r=%v: %v outside [0, 3.266]", r, got)
		}
		if got := CoverTime(60, 59.9, func() float64 { return r }); got < 9 || got > 51 {
			t.Fatalf("r=%v: %v outside [9, 51]", r, got)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	good := map[string]float64{
		"00:00:00":      0,
		"00:01:15.500":  75.5,
		"00:01:15.5":    75.5,
		"01:02:03":      3723,
		"00:00:00.001":  0.001,
		"00:00:01.0004": 1,
		"00:00:01.9996": 2,
		"100:00:00":     360000,
		"0:00:05":       5,
	}
	for in, want := range good {
		got, err := ParseTimestamp(in)
		if err != nil || got != want {
			t.Errorf("ParseTimestamp(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":              "not in HH:MM:SS",
		"75":            "not in HH:MM:SS",
		"75.5":          "not in HH:MM:SS",
		"01:15":         "not in HH:MM:SS",
		"00:1:15":       "not in HH:MM:SS",
		"00:01:15.":     "not in HH:MM:SS",
		"00:01:15,5":    "not in HH:MM:SS",
		" 00:01:15":     "not in HH:MM:SS",
		"00:01:15 ":     "not in HH:MM:SS",
		"aa:bb:cc":      "not in HH:MM:SS",
		"00:60:00":      "60 or more",
		"00:00:60":      "60 or more",
		"00:00:59.9999": "", // rounds to 60.000 seconds, still valid as 59.9999 < 60
		"-00:00:01":     "negative",
		" -1":           "negative",
	}
	for in, want := range bad {
		_, err := ParseTimestamp(in)
		if want == "" {
			if err != nil {
				t.Errorf("ParseTimestamp(%q) = %v, want success", in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseTimestamp(%q) error = %v, want %q", in, err, want)
		}
	}
}

func TestTimestamp(t *testing.T) {
	cases := map[float64]string{0: "00:00:00", 75.5: "00:01:15.500", 0.001: "00:00:00.001", 3723: "01:02:03", 59.9996: "00:01:00", 1.9: "00:00:01.900"}
	for in, want := range cases {
		if got := Timestamp(in); got != want {
			t.Errorf("Timestamp(%v) = %q, want %q", in, got, want)
		}
	}
	// A parsed timestamp formats back to the same text.
	for _, s := range []string{"00:01:15.500", "00:00:00", "12:34:56.789"} {
		v, err := ParseTimestamp(s)
		if err != nil || Timestamp(v) != s {
			t.Errorf("round trip of %q gave %q (%v)", s, Timestamp(v), err)
		}
	}
}

func TestClock(t *testing.T) {
	cases := map[float64]string{0: "00:00:00", 59.99: "00:00:59", 61.5: "00:01:01", 3725: "01:02:05"}
	for in, want := range cases {
		if got := Clock(in); got != want {
			t.Errorf("Clock(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestLastFrameTime(t *testing.T) {
	info, err := ParseProbeJSON(fixture(t, "sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.LastFrameTime(); got < 1.899 || got > 1.901 {
		t.Errorf("LastFrameTime = %v, want 1.9", got)
	}
}

func TestIsFastStart(t *testing.T) {
	video := func(name string) string { return filepath.Join("..", "..", "testdata", "video", name) }
	cases := map[string]bool{"sample.mp4": false, "faststart.mp4": true, "truncated_faststart.mp4": true}
	for name, want := range cases {
		got, err := IsFastStart(video(name))
		if err != nil || got != want {
			t.Errorf("%s: got %v, %v; want %v", name, got, err, want)
		}
	}
	if _, err := IsFastStart(video("corrupt.mp4")); err == nil {
		t.Error("corrupt.mp4: expected an error")
	}
}

func TestFaststartMuxer(t *testing.T) {
	cases := map[string]string{"a.mp4": "mp4", "a.M4V": "mp4", "a.mov": "mov", "a.mkv": ""}
	for name, want := range cases {
		got, err := FaststartMuxer(name)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestFindFont(t *testing.T) {
	// Without --font the embedded font is used, whatever is installed.
	if got, err := FindFont(""); err != nil || got.Path != "" || got.String() != EmbeddedFontName {
		t.Errorf("default font: got %q, %v", got, err)
	}
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ttf := write("f.TTF", []byte("\x00\x01\x00\x00rest"))
	got, err := FindFont(ttf)
	if err != nil || got.Path != ttf {
		t.Errorf("explicit font: got %q, %v", got, err)
	}
	copied, err := got.WriteCopy(t.TempDir())
	if err != nil || filepath.Base(copied) != "font.ttf" {
		t.Errorf("copy of explicit font: %q, %v", copied, err)
	}
	if b, _ := os.ReadFile(copied); string(b) != "\x00\x01\x00\x00rest" {
		t.Errorf("copied font content %q", b)
	}
	for _, ok := range [][]byte{[]byte("OTTOrest"), []byte("ttcfrest"), []byte("truerest")} {
		if _, err := FindFont(write("ok"+string(ok[:4]), ok)); err != nil {
			t.Errorf("%q: %v", ok[:4], err)
		}
	}
	cases := map[string]struct {
		path, want string
	}{
		"missing":   {filepath.Join(dir, "missing.ttf"), "does not exist"},
		"directory": {dir, "not a regular file"},
		"not font":  {write("notes.ttf", []byte("hello world")), "not a TrueType or OpenType font"},
		"empty":     {write("empty.ttf", nil), "not a TrueType or OpenType font"},
	}
	for name, c := range cases {
		_, err := FindFont(c.path)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), c.path) {
			t.Errorf("%s: err = %v, want %q with the path", name, err, c.want)
		}
	}
}

func TestEmbeddedFontLicense(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("fonts", "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != EmbeddedFontLicense || !strings.Contains(EmbeddedFontLicense, "Bitstream Vera Fonts Copyright") {
		t.Error("the embedded license must be the release LICENSE file")
	}
}

// The embedded font is a TrueType file and is written under a plain name.
func TestEmbeddedFont(t *testing.T) {
	if len(embeddedFont) < 100000 || !bytes.HasPrefix(embeddedFont, []byte{0, 1, 0, 0}) {
		t.Fatalf("embedded font is not a TrueType file (%d bytes)", len(embeddedFont))
	}
	f := Font{}
	if f.String() != EmbeddedFontName {
		t.Errorf("name %q", f)
	}
	copied, err := f.WriteCopy(t.TempDir())
	if err != nil || filepath.Base(copied) != "font.ttf" {
		t.Fatalf("copy: %q, %v", copied, err)
	}
	if b, _ := os.ReadFile(copied); !bytes.Equal(b, embeddedFont) {
		t.Error("copied embedded font differs")
	}
}

func TestWorkFile(t *testing.T) {
	// Filters only see names relative to the work directory, so the work
	// directory itself may hold spaces, colons, or a drive letter.
	work := filepath.Join(t.TempDir(), "C: dir with spaces", "musafoz-preview-1")
	got, err := workFile(work, filepath.Join(work, "font.ttf"))
	if err != nil || got != "font.ttf" {
		t.Errorf("font: got %q, %v", got, err)
	}
	if _, err := workFile(work, filepath.Join(filepath.Dir(work), "font.ttf")); err == nil {
		t.Error("a file outside the work directory was accepted")
	}
	for _, bad := range []string{"a b.ttf", "a:b.ttf", "a'b.ttf", "a,b.ttf"} {
		if _, err := workFile(work, filepath.Join(work, bad)); err == nil || !strings.Contains(err.Error(), "filters") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestCodecTags(t *testing.T) {
	info, err := ParseProbeJSON(fixture(t, "sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.CodecTags) != 2 || info.CodecTags[0] != "avc1" || info.CodecTags[1] != "mp4a" {
		t.Errorf("CodecTags = %v", info.CodecTags)
	}
}

func TestDisplayAspect(t *testing.T) {
	cases := []struct {
		name         string
		w, h, rot    int
		sar          string
		want         float64
		dispW, dispH int
	}{
		{"16:9", 1920, 1080, 0, "1:1", 16.0 / 9, 1920, 1080},
		{"9:16", 1080, 1920, 0, "1:1", 9.0 / 16, 1080, 1920},
		{"4:3", 640, 480, 0, "", 4.0 / 3, 640, 480},
		{"3:4", 480, 640, 0, "0:1", 3.0 / 4, 480, 640},
		{"4:3 rotated 90", 640, 480, 90, "1:1", 3.0 / 4, 480, 640},
		{"4:3 rotated 270", 640, 480, 270, "1:1", 3.0 / 4, 480, 640},
		{"16:9 phone rotated", 3840, 2160, 90, "1:1", 9.0 / 16, 2160, 3840},
		{"anamorphic 16:9", 352, 240, 0, "40:33", 16.0 / 9, 427, 240},
		{"anamorphic rotated", 352, 240, 90, "40:33", 9.0 / 16, 240, 427},
	}
	for _, c := range cases {
		doc := map[string]any{
			"format": map[string]any{"format_name": "mov,mp4", "duration": "1.0"},
			"streams": []any{map[string]any{
				"index": 0, "codec_type": "video", "codec_name": "h264",
				"width": c.w, "height": c.h, "sample_aspect_ratio": c.sar,
				"tags": map[string]any{"rotate": strconv.Itoa(c.rot)},
			}},
		}
		data, _ := json.Marshal(doc)
		info, err := ParseProbeJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Video.DisplayAspect(); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: DisplayAspect = %v, want %v", c.name, got, c.want)
		}
		if info.Video.DisplayWidth != c.dispW || info.Video.DisplayHeight != c.dispH {
			t.Errorf("%s: display = %dx%d, want %dx%d", c.name, info.Video.DisplayWidth, info.Video.DisplayHeight, c.dispW, c.dispH)
		}
	}
}

func TestMosaicCellAndSize(t *testing.T) {
	cases := []struct {
		name         string
		aspect       float64
		cw, ch, w, h int
	}{
		{"16:9", 16.0 / 9, 384, 216, 3840, 2200},
		{"9:16", 9.0 / 16, 216, 384, 2160, 3880},
		{"4:3", 4.0 / 3, 288, 216, 2880, 2200},
		{"3:4", 3.0 / 4, 216, 288, 2160, 2920},
		{"1:1", 1, 216, 216, 2160, 2200},
		{"2.39:1", 2.39, 384, 160, 3840, 1640},
		{"2:5", 0.4, 154, 384, 1540, 3880},
		{"unknown", 0, 384, 216, 3840, 2200},
	}
	for _, c := range cases {
		cw, ch := MosaicCell(c.aspect)
		w, h := MosaicSize(c.aspect)
		if cw != c.cw || ch != c.ch || w != c.w || h != c.h {
			t.Errorf("%s: cell %dx%d, mosaic %dx%d; want %dx%d, %dx%d", c.name, cw, ch, w, h, c.cw, c.ch, c.w, c.h)
		}
		// The grid always fits in 4K in the source orientation.
		gw, gh := 10*cw, 10*ch
		if c.aspect >= 1 && (gw > 3840 || gh > 2160) || c.aspect < 1 && c.aspect > 0 && (gw > 2160 || gh > 3840) {
			t.Errorf("%s: grid %dx%d does not fit in 4K", c.name, gw, gh)
		}
	}
}

func TestMosaicGrid(t *testing.T) {
	cases := map[int][2]int{
		1: {1, 1}, 2: {2, 1}, 3: {2, 2}, 4: {2, 2}, 5: {3, 2}, 7: {3, 3}, 10: {4, 3},
		24: {5, 5}, 25: {5, 5}, 26: {6, 5}, 50: {8, 7}, 81: {9, 9}, 82: {10, 9},
		90: {10, 9}, 91: {10, 10}, 98: {10, 10}, 100: {10, 10},
	}
	for n, want := range cases {
		cols, rows := MosaicGrid(n)
		if cols != want[0] || rows != want[1] {
			t.Errorf("MosaicGrid(%d) = %dx%d, want %dx%d", n, cols, rows, want[0], want[1])
		}
	}
	for n := 1; n <= 100; n++ {
		cols, rows := MosaicGrid(n)
		if cols != int(math.Ceil(math.Sqrt(float64(n)))) || rows != (n+cols-1)/cols || cols*rows < n || cols*(rows-1) >= n {
			t.Errorf("MosaicGrid(%d) = %dx%d breaks the rule", n, cols, rows)
		}
	}
}

func TestMosaicLayoutFor(t *testing.T) {
	cases := []struct {
		name   string
		aspect float64
		frames int
		want   MosaicLayout
	}{
		{"16:9 100", 16.0 / 9, 100, MosaicLayout{10, 10, 3840, 2160, 384, 216, 384, 216}},
		{"16:9 98", 16.0 / 9, 98, MosaicLayout{10, 10, 3840, 2160, 384, 216, 384, 216}},
		{"16:9 25", 16.0 / 9, 25, MosaicLayout{5, 5, 3840, 2160, 768, 432, 768, 432}},
		{"16:9 1", 16.0 / 9, 1, MosaicLayout{1, 1, 3840, 2160, 3840, 2160, 3840, 2160}},
		{"16:9 5", 16.0 / 9, 5, MosaicLayout{3, 2, 3840, 2160, 1280, 1080, 1280, 720}},
		{"16:9 2", 16.0 / 9, 2, MosaicLayout{2, 1, 3840, 2160, 1920, 2160, 1920, 1080}},
		{"9:16 25", 9.0 / 16, 25, MosaicLayout{5, 5, 2160, 3840, 432, 768, 432, 768}},
		{"9:16 98", 9.0 / 16, 98, MosaicLayout{10, 10, 2160, 3840, 216, 384, 216, 384}},
		{"9:16 1", 9.0 / 16, 1, MosaicLayout{1, 1, 2160, 3840, 2160, 3840, 2160, 3840}},
		{"9:16 2", 9.0 / 16, 2, MosaicLayout{2, 1, 2160, 3840, 1080, 3840, 1080, 1920}},
		{"4:3 7", 4.0 / 3, 7, MosaicLayout{3, 3, 2880, 2160, 960, 720, 960, 720}},
		{"2.39:1 100 fills the even-rounded cell", 2.39, 100, MosaicLayout{10, 10, 3840, 1600, 384, 160, 384, 160}},
		{"16:9 40 leaves a border", 16.0 / 9, 40, MosaicLayout{7, 6, 3840, 2160, 548, 360, 548, 308}},
	}
	for _, c := range cases {
		got := MosaicLayoutFor(c.aspect, c.frames)
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// For every count and common aspect ratio: the grid area does not depend
	// on the count, cells fit in it, thumbnails fit in their cells with the
	// source aspect ratio, and never get smaller than the 10 by 10 cell.
	for _, aspect := range []float64{16.0 / 9, 9.0 / 16, 4.0 / 3, 3.0 / 4, 1, 2.39, 0.4} {
		cw, ch := MosaicCell(aspect)
		for n := 1; n <= 100; n++ {
			l := MosaicLayoutFor(aspect, n)
			if l.GridW != 10*cw || l.GridH != 10*ch {
				t.Fatalf("aspect %.3f, %d frames: grid %dx%d, want %dx%d", aspect, n, l.GridW, l.GridH, 10*cw, 10*ch)
			}
			if l.Cols*l.CellW > l.GridW || l.Rows*l.CellH > l.GridH || l.ThumbW > l.CellW || l.ThumbH > l.CellH {
				t.Fatalf("aspect %.3f, %d frames: layout does not fit: %+v", aspect, n, l)
			}
			if l.ThumbW < cw || l.ThumbH < ch {
				t.Fatalf("aspect %.3f, %d frames: thumbnail %dx%d smaller than the 10x10 cell %dx%d", aspect, n, l.ThumbW, l.ThumbH, cw, ch)
			}
			if got := float64(l.ThumbW) / float64(l.ThumbH); math.Abs(got/aspect-1) > 0.02 {
				t.Fatalf("aspect %.3f, %d frames: thumbnail %dx%d has aspect %.4f", aspect, n, l.ThumbW, l.ThumbH, got)
			}
		}
	}
}

func TestOverlayFor(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		aspect float64
		want   Overlay
	}{
		{"1080p 16:9 keeps the reference sizes", 1920, 16.0 / 9, Overlay{64, 25, 8}},
		{"4K 16:9", 3840, 16.0 / 9, Overlay{128, 50, 16}},
		{"1080p 9:16", 1080, 9.0 / 16, Overlay{64, 25, 8}},
		{"4K 9:16 phone", 2160, 9.0 / 16, Overlay{128, 50, 16}},
		{"320x180", 320, 16.0 / 9, Overlay{11, 4, 1}},
		{"8K 16:9", 7680, 16.0 / 9, Overlay{256, 100, 32}},
		{"4:3 960 wide", 960, 4.0 / 3, Overlay{43, 17, 5}},
	}
	for _, c := range cases {
		got := OverlayFor(c.width, c.aspect)
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
		// In the mosaic the font is about 12.8 pixels for every source.
		cw, _ := MosaicCell(c.aspect)
		if inCell := float64(got.FontSize) * float64(cw) / float64(c.width); math.Abs(inCell-12.8) > 0.5 {
			t.Errorf("%s: font in the mosaic is %.2f px, want about 12.8", c.name, inCell)
		}
	}
}
