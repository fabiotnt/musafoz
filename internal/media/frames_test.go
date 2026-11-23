package media

import (
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
	if got := CoverTime(10, 9.9, func() float64 { return 0.9 }); got != 5 {
		t.Errorf("10-second clip = %v, want the midpoint 5", got)
	}
	if got := CoverTime(0.1, 0.05, nil); got != 0.05 {
		t.Errorf("tiny clip = %v, want the cap 0.05", got)
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
	if _, err := FindFont(filepath.Join(t.TempDir(), "missing.ttf")); err == nil {
		t.Error("missing explicit font: expected an error")
	}
	f := filepath.Join(t.TempDir(), "f.ttf")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := FindFont(f); err != nil || got != f {
		t.Errorf("explicit font: got %q, %v", got, err)
	}
}

func TestCheckFilterPath(t *testing.T) {
	if err := checkFilterPath("/var/folders/ab/c_d-1/T/musafoz-preview-123/font.ttf"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"/tmp/a b", "/tmp/a:b", "/tmp/a'b", "/tmp/a,b"} {
		if err := checkFilterPath(bad); err == nil || !strings.Contains(err.Error(), "TMPDIR") {
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
