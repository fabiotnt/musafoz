//go:build integration

// The integration tests run the real managed FFmpeg and FFprobe. Run them
// separately from the default suite:
//
//	go test -tags integration -count=1 -v ./internal/cli/
//
// They use the regular musafoz cache, or MUSAFOZ_TEST_CACHE_DIR, so the
// builds are downloaded at most once per machine. Set MUSAFOZ_TEST_DOWNLOAD=1
// to also test downloading into an empty cache; the vendor rate-limits
// repeated downloads, so do this sparingly.
//
// Optionally set MUSAFOZ_TEST_VIDEO to a local video and
// MUSAFOZ_REFERENCE_FFPROBE to a reference ffprobe to compare durations.
package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/testimages"
	"github.com/fabiotnt/musafoz/internal/tools"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in test")
}

// TestIntegrationDownload installs both managed builds into an empty cache.
// It only runs with MUSAFOZ_TEST_DOWNLOAD=1.
func TestIntegrationDownload(t *testing.T) {
	if os.Getenv("MUSAFOZ_TEST_DOWNLOAD") != "1" {
		t.Skip("set MUSAFOZ_TEST_DOWNLOAD=1 to download into an empty cache")
	}
	cache := t.TempDir()
	probe, err := tools.LookupArtifact(tools.ToolFFprobe, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	mpeg, _ := tools.LookupArtifact(tools.ToolFFmpeg, runtime.GOOS, runtime.GOARCH)

	t.Run("first runs download each build once", func(t *testing.T) {
		r := runReal(t, cache, "inspect", "--json", testdata("video", "sample.mp4"))
		if r.err != nil {
			t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
		}
		if !strings.Contains(r.stderr, "downloading ffprobe "+probe.Version) {
			t.Errorf("stderr lacks the ffprobe download:\n%s", r.stderr)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
			t.Fatalf("stdout is not pure JSON: %v\n%s", err, r.stdout)
		}
		in := copyVideo(t, t.TempDir(), "sample.mp4", "clip.mp4")
		r = runReal(t, cache, "preview", "--frames", "5", in)
		if r.err != nil {
			t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
		}
		if !strings.Contains(r.stderr, "downloading ffmpeg "+mpeg.Version) || strings.Contains(r.stderr, "downloading ffprobe") {
			t.Errorf("want exactly one ffmpeg download:\n%s", r.stderr)
		}
	})

	t.Run("later runs reuse the cache without network", func(t *testing.T) {
		root, _ := tools.DefaultCacheRoot(cache)
		for _, a := range []tools.Artifact{probe, mpeg} {
			m := &tools.Manager{Root: root, Artifact: a, Client: &http.Client{Transport: failingTransport{}}}
			if _, err := m.Ensure(context.Background()); err != nil {
				t.Fatalf("%s: %v", a.Tool, err)
			}
		}
	})
}

func TestIntegrationManagedFFprobe(t *testing.T) {
	cache := testCache(t)
	artifact, err := tools.FFprobeArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}

	t.Run("inspect with the managed build", func(t *testing.T) {
		r := runReal(t, cache, "inspect", "--json", testdata("video", "sample.mp4"))
		if r.err != nil {
			t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
			t.Fatalf("stdout is not pure JSON: %v\n%s", err, r.stdout)
		}
		if doc["duration_seconds"] != 2.0 || doc["video"].(map[string]any)["codec"] != "h264" {
			t.Errorf("unexpected result: %s", r.stdout)
		}
	})

	t.Run("cached run downloads nothing", func(t *testing.T) {
		r := runReal(t, cache, "inspect", testdata("video", "sample.mp4"))
		if r.err != nil {
			t.Fatal(r.err)
		}
		// stderr only reports the cached managed build that was chosen.
		if !strings.HasPrefix(r.stderr, "using ffprobe ") || !strings.Contains(r.stderr, "(managed ") ||
			strings.Count(r.stderr, "\n") != 1 || strings.Contains(r.stderr, "download") {
			t.Errorf("unexpected stderr on a cached run:\n%s", r.stderr)
		}
	})

	t.Run("cache works without network", func(t *testing.T) {
		root, _ := tools.DefaultCacheRoot(cache)
		m := &tools.Manager{Root: root, Artifact: artifact, Client: &http.Client{Transport: failingTransport{}}}
		if _, err := m.Ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("real edge cases", func(t *testing.T) {
		fails := map[string]string{
			"corrupt.mp4":    "ffprobe could not read the file",
			"audio_only.m4a": "no video stream found",
			"cover_art.mp3":  "no video stream found",
		}
		for name, want := range fails {
			r := runReal(t, cache, "inspect", testdata("video", name))
			if r.err == nil || !strings.Contains(r.err.Error(), want) || !strings.Contains(r.err.Error(), name) {
				t.Errorf("%s: err = %v, want %q", name, r.err, want)
			}
		}
		// A PNG is inspected as an image now, and preview still rejects it.
		r := runReal(t, cache, "inspect", testdata("video", "still.png"))
		if r.err != nil || !strings.Contains(r.stdout, "Format:      PNG image") {
			t.Errorf("still.png: err = %v\nstdout:\n%s", r.err, r.stdout)
		}
		r = runReal(t, cache, "preview", "--frames", "1", filepath.Join(t.TempDir(), "x.png"))
		if r.err == nil {
			t.Error("preview of a missing file succeeded")
		}
		png := copyVideo(t, t.TempDir(), "still.png", "still.png")
		r = runReal(t, cache, "preview", png)
		if r.err == nil || !strings.Contains(r.err.Error(), "no video stream found") {
			t.Errorf("preview still.png: err = %v", r.err)
		}
		r = runReal(t, cache, "inspect", testdata("video", "raw.h264"))
		if r.err != nil || !strings.Contains(r.stdout, "Duration:   unknown") || !strings.Contains(r.stderr, "warning:") {
			t.Errorf("raw.h264: err = %v\nstdout:\n%s\nstderr:\n%s", r.err, r.stdout, r.stderr)
		}
		r = runReal(t, cache, "inspect", testdata("video", "rotated.mp4"))
		if r.err != nil || !strings.Contains(r.stdout, "Display:    240x320") {
			t.Errorf("rotated.mp4: err = %v\nstdout:\n%s", r.err, r.stdout)
		}
	})

	t.Run("duration matches the reference ffprobe", func(t *testing.T) {
		video, ref := os.Getenv("MUSAFOZ_TEST_VIDEO"), os.Getenv("MUSAFOZ_REFERENCE_FFPROBE")
		if video == "" || ref == "" {
			t.Skip("set MUSAFOZ_TEST_VIDEO and MUSAFOZ_REFERENCE_FFPROBE to compare")
		}
		// Same duration query as the reference preview workflow.
		out, err := exec.Command(ref, "-v", "error", "-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1", video).Output()
		if err != nil {
			t.Fatal(err)
		}
		want, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		if err != nil {
			t.Fatalf("reference duration %q: %v", out, err)
		}
		r := runReal(t, cache, "inspect", "--json", video)
		if r.err != nil {
			t.Fatal(r.err)
		}
		var doc struct {
			Duration *float64 `json:"duration_seconds"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || doc.Duration == nil {
			t.Fatalf("no duration in %s (%v)", r.stdout, err)
		}
		if math.Abs(*doc.Duration-want) > 1e-6 {
			t.Errorf("duration = %f, reference = %f", *doc.Duration, want)
		}
		t.Logf("duration %f matches reference %f", *doc.Duration, want)
	})
}

func copyVideo(t *testing.T, dir, src, name string) string {
	t.Helper()
	data, err := os.ReadFile(testdata("video", src))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func sha(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func TestIntegrationPreview(t *testing.T) {
	cache := testCache(t)
	root, _ := tools.DefaultCacheRoot(cache)
	probeArt, err := tools.LookupArtifact(tools.ToolFFprobe, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	mpegArt, _ := tools.LookupArtifact(tools.ToolFFmpeg, runtime.GOOS, runtime.GOARCH)
	ffprobe := (&tools.Manager{Root: root, Artifact: probeArt}).BinaryPath()
	ffmpeg := (&tools.Manager{Root: root, Artifact: mpegArt}).BinaryPath()
	dir := t.TempDir()

	t.Run("valid video with default frames", func(t *testing.T) {
		in := copyVideo(t, dir, "sample.mp4", "clip.mp4")
		before := sha(t, in)
		r := runReal(t, cache, "preview", in)
		if r.err != nil {
			t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
		}
		base := strings.TrimSuffix(in, ".mp4")
		cover := probeFirstStream(t, ffprobe, base+"_cover.jpg")
		video := probeFirstStream(t, ffprobe, base+"_preview.mp4")
		mosaic := probeFirstStream(t, ffprobe, base+"_preview.jpg")
		if cover.Width != 320 || cover.Height != 240 {
			t.Errorf("cover = %dx%d, want 320x240", cover.Width, cover.Height)
		}
		if video.CodecName != "h264" || video.PixFmt != "yuv420p" || video.AvgFrameRate != "4/1" ||
			video.NbFrames != "100" || video.Width != 320 || video.Height != 240 {
			t.Errorf("preview video = %+v", video)
		}
		if w, h := media.MosaicSize(4.0 / 3); mosaic.Width != w || mosaic.Height != h { // sample.mp4 is 4:3
			t.Errorf("mosaic = %dx%d, want %dx%d", mosaic.Width, mosaic.Height, w, h)
		}
		if sha(t, in) != before {
			t.Error("the original changed")
		}
	})

	t.Run("non-default frames and rotation", func(t *testing.T) {
		in := copyVideo(t, dir, "rotated.mp4", "rotated.mp4")
		r := runReal(t, cache, "preview", "--frames", "7", in)
		if r.err != nil {
			t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
		}
		base := strings.TrimSuffix(in, ".mp4")
		cover := probeFirstStream(t, ffprobe, base+"_cover.jpg")
		video := probeFirstStream(t, ffprobe, base+"_preview.mp4")
		mosaic := probeFirstStream(t, ffprobe, base+"_preview.jpg")
		if cover.Width != 240 || cover.Height != 320 {
			t.Errorf("cover = %dx%d, want 240x320", cover.Width, cover.Height)
		}
		if video.NbFrames != "7" || video.Width != 240 || video.Height != 320 {
			t.Errorf("preview video = %+v", video)
		}
		if w, h := media.MosaicSize(3.0 / 4); mosaic.Width != w || mosaic.Height != h { // rotated.mp4 displays as 3:4
			t.Errorf("mosaic = %dx%d, want %dx%d", mosaic.Width, mosaic.Height, w, h)
		}
	})

	t.Run("4K portrait frames fit the mosaic", func(t *testing.T) {
		in := filepath.Join(dir, "uhd.mp4")
		gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc=size=2160x3840:rate=10:duration=1",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", in)
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generate 4K clip: %v\n%s", err, out)
		}
		r := runReal(t, cache, "preview", in)
		if r.err != nil {
			t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
		}
		mosaic := probeFirstStream(t, ffprobe, strings.TrimSuffix(in, ".mp4")+"_preview.jpg")
		if w, h := media.MosaicSize(9.0 / 16); mosaic.Width != w || mosaic.Height != h { // 2160x3840 is 9:16
			t.Errorf("mosaic = %dx%d, want %dx%d", mosaic.Width, mosaic.Height, w, h)
		}
	})

	t.Run("unfinished mp4 fails validation", func(t *testing.T) {
		in := copyVideo(t, dir, "unfinished.mp4", "unfinished.mp4")
		before := sha(t, in)
		r := runReal(t, cache, "preview", in)
		if r.err == nil || !strings.Contains(r.err.Error(), "inspect: ffprobe could not read the file") || !strings.Contains(r.err.Error(), in) {
			t.Fatalf("err = %v", r.err)
		}
		if sha(t, in) != before {
			t.Error("the original changed")
		}
		for _, s := range []string{"_cover.jpg", "_preview.mp4", "_preview.jpg"} {
			if _, err := os.Stat(strings.TrimSuffix(in, ".mp4") + s); err == nil {
				t.Errorf("output %s was created", s)
			}
		}
	})

	t.Run("inspection passes but extraction fails", func(t *testing.T) {
		in := copyVideo(t, dir, "truncated_faststart.mp4", "truncated.mp4")
		before := sha(t, in)
		base := strings.TrimSuffix(in, ".mp4")
		for _, s := range []string{"_cover.jpg", "_preview.mp4", "_preview.jpg"} {
			if err := os.WriteFile(base+s, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		r := runReal(t, cache, "inspect", in)
		if r.err != nil {
			t.Fatalf("inspect should pass: %v", r.err)
		}
		r = runReal(t, cache, "preview", "--overwrite", "--faststart-in-place", in)
		if r.err == nil || !strings.Contains(r.err.Error(), "extract frames") || !strings.Contains(r.err.Error(), "no frame decoded") {
			t.Fatalf("err = %v", r.err)
		}
		if sha(t, in) != before {
			t.Error("the original changed")
		}
		for _, s := range []string{"_cover.jpg", "_preview.mp4", "_preview.jpg"} {
			if data, _ := os.ReadFile(base + s); string(data) != "old" {
				t.Errorf("earlier output %s was not preserved", s)
			}
		}
	})

	t.Run("faststart in place keeps streams", func(t *testing.T) {
		in := copyVideo(t, dir, "sample.mp4", "fs.mp4")
		hashBefore := streamHash(t, ffmpeg, in)
		r := runReal(t, cache, "preview", "--faststart-in-place", "--json", in)
		if r.err != nil {
			t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
		}
		if !strings.Contains(r.stdout, `"faststart": "applied"`) {
			t.Errorf("unexpected result: %s", r.stdout)
		}
		if ok, err := media.IsFastStart(in); err != nil || !ok {
			t.Errorf("original is not faststart after the run: %v, %v", ok, err)
		}
		if got := streamHash(t, ffmpeg, in); got != hashBefore {
			t.Errorf("streams changed:\nbefore %s\nafter  %s", hashBefore, got)
		}
		// Windows keeps only a read-only flag, not Unix permission bits.
		if fi, _ := os.Stat(in); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o640 {
			t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
		}
	})
}

// meanLuma returns the average luma of a region of the first frame.
func meanLuma(t *testing.T, ffmpeg, p string, x, y, w, h int) float64 {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-hide_banner", "-i", p, "-frames:v", "1",
		"-vf", fmt.Sprintf("crop=%d:%d:%d:%d,signalstats,metadata=print:key=lavfi.signalstats.YAVG", w, h, x, y),
		"-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("measure %s: %v\n%s", p, err, out)
	}
	_, after, ok := strings.Cut(string(out), "lavfi.signalstats.YAVG=")
	if !ok {
		t.Fatalf("no luma for %s", p)
	}
	v, err := strconv.ParseFloat(strings.Fields(after)[0], 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// whiteSide reports which edge of a region is white, using bands that
// avoid the timestamp overlay in the top-left corner.
func whiteSide(t *testing.T, ffmpeg, p string, x0, y0, w, h int) string {
	t.Helper()
	bw, bh := w/5, h/5
	top := meanLuma(t, ffmpeg, p, x0+w-2*bw, y0, 2*bw, bh)
	bottom := meanLuma(t, ffmpeg, p, x0+w-2*bw, y0+h-bh, 2*bw, bh)
	left := meanLuma(t, ffmpeg, p, x0, y0+h-2*bh, bw, 2*bh)
	right := meanLuma(t, ffmpeg, p, x0+w-bw, y0+h-2*bh, bw, 2*bh)
	switch {
	case top > 180 && bottom < 70:
		return "top"
	case bottom > 180 && top < 70:
		return "bottom"
	case left > 180 && right < 70:
		return "left"
	case right > 180 && left < 70:
		return "right"
	}
	return fmt.Sprintf("unclear (top %.0f bottom %.0f left %.0f right %.0f)", top, bottom, left, right)
}

func TestIntegrationPreviewAspect(t *testing.T) {
	cache := testCache(t)
	root, _ := tools.DefaultCacheRoot(cache)
	var paths [2]string
	for i, tool := range []string{tools.ToolFFmpeg, tools.ToolFFprobe} {
		a, err := tools.LookupArtifact(tool, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			t.Skip(err)
		}
		p, err := (&tools.Manager{Root: root, Artifact: a}).Ensure(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		paths[i] = p
	}
	ffmpeg, ffprobe := paths[0], paths[1]

	cases := []struct {
		name string
		w, h int
		sar  string
		// rotation is the -display_rotation value: counterclockwise degrees.
		rotation     int
		aspectW      int
		aspectH      int
		dispW, dispH int
		side         string
	}{
		{"16x9", 1280, 720, "1/1", 0, 16, 9, 1280, 720, "top"},
		{"9x16", 720, 1280, "1/1", 0, 9, 16, 720, 1280, "top"},
		{"4x3", 960, 720, "1/1", 0, 4, 3, 960, 720, "top"},
		{"3x4", 720, 960, "1/1", 0, 3, 4, 720, 960, "top"},
		{"4x3_rotated_ccw90", 960, 720, "1/1", 90, 3, 4, 720, 960, "left"},
		{"4x3_rotated_cw90", 960, 720, "1/1", -90, 3, 4, 720, 960, "right"},
		{"16x9_rotated_cw90", 1280, 720, "1/1", -90, 9, 16, 720, 1280, "right"},
		{"anamorphic_16x9", 704, 480, "40/33", 0, 16, 9, 853, 480, "top"},
		{"1x1", 720, 720, "1/1", 0, 1, 1, 720, 720, "top"},
		{"320x180_16x9", 320, 180, "1/1", 0, 16, 9, 320, 180, "top"},
		{"4K_9x16_phone", 2160, 3840, "1/1", 0, 9, 16, 2160, 3840, "top"},
	}
	// darkArea holds, per case, the dark pixels of the timestamp box in the
	// first mosaic frame; it must not depend on resolution or aspect ratio.
	darkArea := map[string]int{}
	// upscale holds, per case, how much the frame is enlarged in the mosaic;
	// values below 1 mean it is reduced.
	upscale := map[string]float64{}
	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := filepath.Join(dir, c.name+"_src.mp4")
			in := filepath.Join(dir, c.name+".mp4")
			gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=%dx%d:r=10:d=1", c.w, c.h),
				"-vf", "drawbox=x=0:y=0:w=iw:h=ih/2:color=white:t=fill,setsar="+c.sar,
				"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", src)
			if out, err := gen.CombinedOutput(); err != nil {
				t.Fatalf("generate: %v\n%s", err, out)
			}
			rot := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
				"-display_rotation", strconv.Itoa(c.rotation), "-i", src, "-c", "copy", in)
			if out, err := rot.CombinedOutput(); err != nil {
				t.Fatalf("rotate: %v\n%s", err, out)
			}

			srcInfo, err := media.Inspect(t.Context(), ffprobe, in)
			if err != nil {
				t.Fatal(err)
			}
			if (c.rotation != 0) != (srcInfo.Video.Rotation != 0) {
				t.Fatalf("test input rotation is %d, want it set: %v", srcInfo.Video.Rotation, c.rotation != 0)
			}

			for _, mode := range []string{"auto", "cpu"} {
				t.Run(mode, func(t *testing.T) {
					data, err := os.ReadFile(in)
					if err != nil {
						t.Fatal(err)
					}
					in := strings.TrimSuffix(in, ".mp4") + "_" + mode + ".mp4"
					if err := os.WriteFile(in, data, 0o644); err != nil {
						t.Fatal(err)
					}
					r := runReal(t, cache, "preview", "--frames", "10", "--json", "--accel", mode, in)
					if r.err != nil {
						t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
					}
					var res struct {
						FrameDecoder   string `json:"frame_decoder"`
						PreviewEncoder string `json:"preview_encoder"`
					}
					if err := json.Unmarshal([]byte(r.stdout), &res); err != nil {
						t.Fatalf("result: %v\n%s", err, r.stdout)
					}
					// On macOS, auto mode must really decode with VideoToolbox for
					// these H.264 inputs; the stderr log says why otherwise.
					wantDecoder := "cpu"
					if mode == "auto" && runtime.GOOS == "darwin" {
						wantDecoder = "videotoolbox"
					}
					if res.FrameDecoder != wantDecoder {
						t.Errorf("frame decoder %q, want %q\nstderr:\n%s", res.FrameDecoder, wantDecoder, r.stderr)
					}
					// The JSON must name the encoder that actually wrote the preview,
					// as the log reports it. Auto mode on macOS tries
					// h264_videotoolbox; a test encode failure falls back to libx264
					// with the reason in the log.
					encoderName := map[string]string{"cpu": "libx264", "videotoolbox": "h264_videotoolbox"}[res.PreviewEncoder]
					if encoderName == "" || !strings.Contains(r.stderr, "encoded preview video with "+encoderName+" in ") {
						t.Errorf("preview encoder %q does not match the log:\n%s", res.PreviewEncoder, r.stderr)
					}
					if mode == "cpu" && res.PreviewEncoder != "cpu" {
						t.Errorf("preview encoder %q with --accel cpu", res.PreviewEncoder)
					}
					if mode == "auto" && runtime.GOOS == "darwin" && res.PreviewEncoder != "videotoolbox" {
						t.Logf("auto mode encoded with libx264:\n%s", r.stderr)
					}
					aspect := float64(c.aspectW) / float64(c.aspectH)
					base := strings.TrimSuffix(in, ".mp4")
					cover := probeFirstStream(t, ffprobe, base+"_cover.jpg")
					video := probeFirstStream(t, ffprobe, base+"_preview.mp4")
					mosaic := probeFirstStream(t, ffprobe, base+"_preview.jpg")

					if cover.Width != c.dispW || cover.Height != c.dispH {
						t.Errorf("cover %dx%d, want %dx%d", cover.Width, cover.Height, c.dispW, c.dispH)
					}
					// Rounding each dimension to an even value moves it by less than
					// 2 pixels, so the aspect ratio may differ by up to 2/w + 2/h.
					tolerance := 2/float64(video.Width) + 2/float64(video.Height)
					dar, ok := parseRatio(video.DAR)
					if video.CodecName != "h264" {
						t.Errorf("preview codec %q, want h264", video.CodecName)
					}
					if video.SAR != "1:1" || !ok || math.Abs(dar/aspect-1) > tolerance ||
						math.Abs(float64(video.Width)/float64(video.Height)/aspect-1) > tolerance {
						t.Errorf("preview %dx%d SAR %s DAR %s, want aspect %d:%d within %.4f",
							video.Width, video.Height, video.SAR, video.DAR, c.aspectW, c.aspectH, tolerance)
					}
					// The pixels are already rotated, so no rotation metadata may remain
					// for a player to apply again.
					outInfo, err := media.Inspect(t.Context(), ffprobe, base+"_preview.mp4")
					if err != nil {
						t.Fatal(err)
					}
					if outInfo.Video.Rotation != 0 {
						t.Errorf("preview keeps a rotation of %d degrees; source had %d", outInfo.Video.Rotation, srcInfo.Video.Rotation)
					}
					wantW, wantH := media.MosaicSize(aspect)
					gridAspect := float64(mosaic.Width) / float64(mosaic.Height-40)
					if mosaic.Width != wantW || mosaic.Height != wantH || math.Abs(gridAspect/aspect-1) > 0.01 {
						t.Errorf("mosaic %dx%d (grid aspect %.4f), want %dx%d (aspect %.4f)", mosaic.Width, mosaic.Height, gridAspect, wantW, wantH, aspect)
					}

					// Ten frames give a 4 by 3 grid whose cells are wider or taller
					// than the frame, so this also checks the centering in the cell.
					tx, ty, tw, th := firstThumb(aspect, 10)
					sides := map[string]string{
						"cover":       whiteSide(t, ffmpeg, base+"_cover.jpg", 0, 0, cover.Width, cover.Height),
						"preview":     whiteSide(t, ffmpeg, base+"_preview.mp4", 0, 0, video.Width, video.Height),
						"mosaic cell": whiteSide(t, ffmpeg, base+"_preview.jpg", tx, ty, tw, th),
					}
					for what, side := range sides {
						if side != c.side {
							t.Errorf("%s: white side is %s, want %s", what, side, c.side)
						}
					}
					// The measured region is all white only when the white half is on top.
					if c.side == "top" {
						cw, _ := media.MosaicCell(aspect)
						scale := float64(tw) / float64(cw)
						darkArea[c.name+"/"+mode] = overlayDarkArea(t, ffmpeg, base+"_preview.jpg", tx, ty, scale)
						upscale[c.name+"/"+mode] = float64(tw) / float64(c.dispW)
					}
					t.Logf("%s, decoder %s, encoder %s: source %dx%d SAR %s rotation %d (clockwise %d) -> display %dx%d (%d:%d); cover %dx%d; preview %dx%d SAR %s DAR %s (%.4f, want %.4f) rotation %d; mosaic %dx%d, grid %dx%d; white side %s",
						mode, res.FrameDecoder, res.PreviewEncoder, c.w, c.h, strings.Replace(c.sar, "/", ":", 1), c.rotation, srcInfo.Video.Rotation, c.dispW, c.dispH, c.aspectW, c.aspectH,
						cover.Width, cover.Height, video.Width, video.Height, video.SAR, video.DAR, dar, aspect, outInfo.Video.Rotation,
						mosaic.Width, mosaic.Height, mosaic.Width, mosaic.Height-40, sides["cover"])
				})
			}
		})
	}
	ref, ok := darkArea["16x9/cpu"]
	if !ok || ref == 0 {
		t.Fatalf("no reference overlay measurement: %v", darkArea)
	}
	for name, area := range darkArea {
		t.Logf("overlay dark area in the first mosaic cell: %s %d px (16x9/cpu: %d px)", name, area, ref)
		// The overlay is drawn in whole frame pixels. A frame narrower than
		// its cell is enlarged into the mosaic, so rounding, mostly of the
		// box border, is enlarged too; the measured area then depends more
		// on the font's rendering at small sizes.
		// With 10 frames the 320x180 case is enlarged 3 times: its 11-pixel
		// font and 1-pixel box border (1.33 would be exact) are enlarged with
		// it, and it measures about 34 percent below the reference, against
		// 23 percent at the 1.2 times of the 10 by 10 grid.
		limit := 0.2
		switch {
		case upscale[name] > 2:
			limit = 0.4
		case upscale[name] > 1:
			limit = 0.3
		}
		if math.Abs(float64(area)/float64(ref)-1) > limit {
			t.Errorf("%s: overlay dark area %d px differs from 16x9/cpu (%d px) by more than %.0f%%", name, area, ref, limit*100)
		}
	}
}

// TestIntegrationMosaicGrid checks the grid for 1, 25, 98, and 100 frames
// on a landscape and a portrait video: the mosaic keeps its size, empty
// cells are black, the MP4 keeps the requested frame count, and its
// timestamps keep the same size, while the mosaic timestamps grow with the
// cells.
func TestIntegrationMosaicGrid(t *testing.T) {
	ffmpeg, ffprobe := managedTools(t)
	cache := testCache(t)
	dir := t.TempDir()
	for _, c := range []struct {
		name   string
		w, h   int
		aspect float64
	}{{"landscape", 1280, 720, 16.0 / 9}, {"portrait", 720, 1280, 9.0 / 16}} {
		t.Run(c.name, func(t *testing.T) {
			// A bright moving pattern, so filled cells are never black.
			src := filepath.Join(dir, c.name+".mp4")
			gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=12", c.w, c.h),
				"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", src)
			if out, err := gen.CombinedOutput(); err != nil {
				t.Fatalf("generate: %v\n%s", err, out)
			}
			wantW, wantH := media.MosaicSize(c.aspect)
			cw, _ := media.MosaicCell(c.aspect)
			mosaicArea := map[int]float64{}
			videoArea := map[int]int{}
			for _, n := range []int{1, 25, 98, 100} {
				in := filepath.Join(dir, fmt.Sprintf("%s_%d.mp4", c.name, n))
				data, err := os.ReadFile(src)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(in, data, 0o644); err != nil {
					t.Fatal(err)
				}
				r := runReal(t, cache, "preview", "--accel", "cpu", "--frames", strconv.Itoa(n), in)
				if r.err != nil {
					t.Fatalf("%d frames: %v\nstderr:\n%s", n, r.err, r.stderr)
				}
				base := strings.TrimSuffix(in, ".mp4")
				l := media.MosaicLayoutFor(c.aspect, n)
				if !strings.Contains(r.stderr, fmt.Sprintf("building mosaic: %dx%d grid", l.Cols, l.Rows)) {
					t.Errorf("%d frames: stderr does not report the %dx%d grid:\n%s", n, l.Cols, l.Rows, r.stderr)
				}
				mosaic := probeFirstStream(t, ffprobe, base+"_preview.jpg")
				if mosaic.Width != wantW || mosaic.Height != wantH {
					t.Errorf("%d frames: mosaic %dx%d, want %dx%d", n, mosaic.Width, mosaic.Height, wantW, wantH)
				}
				video := probeFirstStream(t, ffprobe, base+"_preview.mp4")
				if video.NbFrames != strconv.Itoa(n) || video.Width != c.w || video.Height != c.h {
					t.Errorf("%d frames: preview has %s frames at %dx%d, want %d at %dx%d", n, video.NbFrames, video.Width, video.Height, n, c.w, c.h)
				}
				// Every frame is in its cell, and the cells after the last frame
				// are black.
				// Check the first and last filled cells and every empty one.
				for i := range l.Cols * l.Rows {
					if i >= 2 && i < n-2 {
						continue
					}
					col, row := i%l.Cols, i/l.Cols
					x := (l.GridW-l.Cols*l.CellW)/2 + col*l.CellW + (l.CellW-l.ThumbW)/2
					y := media.MosaicHeader + (l.GridH-l.Rows*l.CellH)/2 + row*l.CellH + (l.CellH-l.ThumbH)/2
					// Measure the lower right part, away from the timestamp.
					luma := meanLuma(t, ffmpeg, base+"_preview.jpg", x+l.ThumbW/2, y+l.ThumbH/2, l.ThumbW/3, l.ThumbH/3)
					if i < n && luma < 40 || i >= n && luma > 4 {
						t.Errorf("%d frames: cell %d (column %d, row %d) has mean luma %.1f", n, i+1, col+1, row+1, luma)
					}
				}
				x, y, tw, _ := firstThumb(c.aspect, n)
				scale := float64(tw) / float64(cw)
				mosaicArea[n] = float64(overlayDarkArea(t, ffmpeg, base+"_preview.jpg", x, y, scale)) / (scale * scale)
				ov := media.OverlayFor(c.w, c.aspect)
				videoArea[n] = overlayDarkArea(t, ffmpeg, base+"_preview.mp4", 0, 0, float64(ov.FontSize)/12.8)
				t.Logf("%d frames: grid %dx%d, cells %dx%d, frames %dx%d in the mosaic; mosaic font about %.1f px; MP4 overlay dark area %d px",
					n, l.Cols, l.Rows, l.CellW, l.CellH, l.ThumbW, l.ThumbH, 12.8*scale, videoArea[n])
			}
			// The MP4 timestamp does not depend on the frame count.
			for n, a := range videoArea {
				if math.Abs(float64(a)/float64(videoArea[100])-1) > 0.02 {
					t.Errorf("%d frames: MP4 overlay dark area %d px, %d px with 100 frames", n, a, videoArea[100])
				}
			}
			// In the mosaic the timestamp scales with the frame, so its area
			// divided by the squared scale stays about the same.
			for n, a := range mosaicArea {
				if math.Abs(a/mosaicArea[100]-1) > 0.25 {
					t.Errorf("%d frames: normalized mosaic overlay area %.0f, %.0f with 100 frames", n, a, mosaicArea[100])
				}
			}
		})
	}
}

// TestIntegrationCoverAt checks --cover-at on a video whose brightness
// rises with time, so the cover shows which frame was taken.
func TestIntegrationCoverAt(t *testing.T) {
	ffmpeg, ffprobe := managedTools(t)
	cache := testCache(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "ramp.mp4")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "nullsrc=size=320x180:rate=10:duration=4,geq=lum='min(235,16+T*50)':cb=128:cr=128",
		"-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv420p", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	info, err := media.Inspect(t.Context(), ffprobe, src)
	if err != nil {
		t.Fatal(err)
	}
	last := info.LastFrameTime()
	for i, c := range []struct {
		arg  string
		want float64
	}{{"00:00:00", 0}, {"00:00:01.250", 1.25}, {media.Timestamp(math.Floor(last*1000) / 1000), math.Floor(last*1000) / 1000}} {
		in := filepath.Join(dir, fmt.Sprintf("ramp_%d.mp4", i))
		data, _ := os.ReadFile(src)
		if err := os.WriteFile(in, data, 0o644); err != nil {
			t.Fatal(err)
		}
		r := runReal(t, cache, "preview", "--frames", "4", "--json", "--cover-at", c.arg, in)
		if r.err != nil {
			t.Fatalf("--cover-at %s: %v\nstderr:\n%s", c.arg, r.err, r.stderr)
		}
		var res struct {
			CoverAt float64 `json:"cover_at_seconds"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &res); err != nil || res.CoverAt != c.want {
			t.Errorf("--cover-at %s: cover_at_seconds in %s, want %v", c.arg, r.stdout, c.want)
		}
		// The frame shown at time T has a limited-range luma of 16 + 50 T.
		// The JPEG cover is full range, so that becomes 50 T * 255 / 219,
		// checked within about one frame (0.1 s, 5.8 levels) plus JPEG error.
		cover := strings.TrimSuffix(in, ".mp4") + "_cover.jpg"
		got := meanLuma(t, ffmpeg, cover, 0, 0, 320, 180)
		want := 50 * c.want * 255 / 219
		if math.Abs(got-want) > 8 {
			t.Errorf("--cover-at %s: cover luma %.1f, want about %.1f", c.arg, got, want)
		}
		t.Logf("--cover-at %s: cover_at_seconds %v, cover luma %.1f (expected %.1f)", c.arg, res.CoverAt, got, want)
	}
	for _, arg := range []string{"00:00:04", "00:00:03.999", "01:00:00"} {
		in := filepath.Join(dir, "ramp_out.mp4")
		data, _ := os.ReadFile(src)
		if err := os.WriteFile(in, data, 0o644); err != nil {
			t.Fatal(err)
		}
		r := runReal(t, cache, "preview", "--cover-at", arg, in)
		if r.err == nil || !strings.Contains(r.err.Error(), "is outside the video") {
			t.Errorf("--cover-at %s: err = %v", arg, r.err)
		}
		if _, err := os.Stat(strings.TrimSuffix(in, ".mp4") + "_cover.jpg"); err == nil {
			t.Errorf("--cover-at %s: a cover was written", arg)
		}
	}
}

// TestIntegrationStills runs image, image batch, and cover with the real
// tools on a video whose brightness rises with time, so each image shows
// which frame was taken.
func TestIntegrationStills(t *testing.T) {
	ffmpeg, ffprobe := managedTools(t)
	cache := testCache(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "ramp clip.mp4")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "nullsrc=size=320x180:rate=10:duration=4,geq=lum='min(235,16+T*50)':cb=128:cr=128",
		"-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv420p", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	before := sha(t, src)
	lumaAt := func(at float64) float64 { return 50 * at * 255 / 219 }
	check := func(p string, w, h int, at float64) {
		t.Helper()
		s := probeFirstStream(t, ffprobe, p)
		if s.Width != w || s.Height != h || s.CodecName != map[bool]string{true: "png", false: "mjpeg"}[strings.HasSuffix(p, ".png")] {
			t.Errorf("%s: %s %dx%d, want %dx%d", filepath.Base(p), s.CodecName, s.Width, s.Height, w, h)
		}
		if at >= 0 {
			if got := meanLuma(t, ffmpeg, p, 0, 0, w, h); math.Abs(got-lumaAt(at)) > 8 {
				t.Errorf("%s: luma %.1f, want about %.1f", filepath.Base(p), got, lumaAt(at))
			}
		}
	}

	out := filepath.Join(dir, "frames")
	r := runReal(t, cache, "image", src, "00:00:00", "00:00:01.250", "--output", out)
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	check(filepath.Join(out, "ramp clip_00-00-00.jpg"), 320, 180, 0)
	check(filepath.Join(out, "ramp clip_00-00-01-250.jpg"), 320, 180, 1.3)

	r = runReal(t, cache, "image", src, "00:00:02", "--output", out, "--size", "100x100", "--mode", "fill", "--format", "png", "--suffix", "_sq")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	check(filepath.Join(out, "ramp clip_00-00-02_sq.png"), 100, 100, 2)
	r = runReal(t, cache, "image", src, "00:00:02", "--output", out, "--size", "100x100", "--prefix", "fit-")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	check(filepath.Join(out, "fit-ramp clip_00-00-02.jpg"), 100, 56, 2)

	r = runReal(t, cache, "image", src, "--random", "5", "--output", filepath.Join(dir, "random"))
	if r.err != nil || len(strings.Fields(strings.ReplaceAll(r.stdout, " ", "_"))) != 5 {
		t.Fatalf("random: %v\n%s\n%s", r.err, r.stdout, r.stderr)
	}

	// The rotated fixture is stored 320x240 and displayed 240x320.
	rot := copyVideo(t, dir, "rotated.mp4", "rotated.mp4")
	r = runReal(t, cache, "image", rot, "00:00:00", "--alongside")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	check(filepath.Join(dir, "rotated_00-00-00.jpg"), 240, 320, -1)

	// A list mixing the local file and the same video by URL.
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(data)
	}))
	defer srv.Close()
	list := filepath.Join(dir, "frames.tsv")
	if err := os.WriteFile(list, []byte("ramp clip.mp4\t00:00:03\n"+
		srv.URL+"/v/remote.mp4?k=SECRET\t00:00:01\n"+
		srv.URL+"/v/remote.mp4?k=SECRET\t\n"+
		"ramp clip.mp4\t00:00:09\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bout := filepath.Join(dir, "batch")
	r = runReal(t, cache, "image", "batch", list, "--output", bout)
	if r.err == nil || r.err.Error() != "1 of 4 list lines failed" || !strings.Contains(r.stderr, "line 4: ramp clip.mp4: time 00:00:09 is outside the video") {
		t.Errorf("batch: %v\n%s", r.err, r.stderr)
	}
	if strings.Contains(r.stdout+r.stderr, "SECRET") || hits != 1 {
		t.Errorf("batch: query shown or %d downloads\n%s", hits, r.stderr)
	}
	check(filepath.Join(bout, "ramp clip_00-00-03.jpg"), 320, 180, 3)
	check(filepath.Join(bout, "remote_00-00-01.jpg"), 320, 180, 1)

	// cover writes only the cover, with the shared extraction.
	r = runReal(t, cache, "cover", src, "00:00:01.500")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	check(filepath.Join(dir, "ramp clip_cover.jpg"), 320, 180, 1.5)
	if _, err := os.Stat(filepath.Join(dir, "ramp clip_preview.mp4")); err == nil {
		t.Error("cover wrote a preview")
	}
	// The cover is FFmpeg's own JPEG at -q:v 2, byte for byte, as is the
	// preview cover at the same time.
	direct := filepath.Join(t.TempDir(), "direct.jpg")
	ref := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-ss", "1.500", "-i", "file:"+src,
		"-vf", "scale=w=round(iw*sar):h=ih,setsar=1", "-frames:v", "1", "-update", "1", "-q:v", "2", direct)
	if out, err := ref.CombinedOutput(); err != nil {
		t.Fatalf("direct cover: %v\n%s", err, out)
	}
	if sha(t, filepath.Join(dir, "ramp clip_cover.jpg")) != sha(t, direct) {
		t.Error("cover differs from the direct FFmpeg JPEG")
	}
	pdir := t.TempDir()
	pin := filepath.Join(pdir, "ramp.mp4")
	if err := os.WriteFile(pin, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r = runReal(t, cache, "preview", "--frames", "4", "--cover-at", "00:00:01.500", pin)
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	if sha(t, filepath.Join(pdir, "ramp_cover.jpg")) != sha(t, direct) {
		t.Error("preview cover differs from the direct FFmpeg JPEG")
	}
	// A random cover follows the preview rule: anywhere up to the last
	// frame for this 4-second clip.
	r = runReal(t, cache, "cover", "--json", "--overwrite", src)
	var cres struct {
		At float64 `json:"cover_at_seconds"`
	}
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &cres) != nil || cres.At < 0 || cres.At > 3.9 {
		t.Errorf("random cover: %v %s", r.err, r.stdout)
	}
	if sha(t, src) != before {
		t.Error("the source video changed")
	}
}

// quadrants names the colors at the centers of a cols x rows grid of an
// image file: R, G, B, W, or ? for other colors.
func quadrants(t *testing.T, p string, cols, rows int) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	img, err := imaging.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Pixels.Bounds()
	out := ""
	for r := range rows {
		for c := range cols {
			R, G, B, _ := img.Pixels.At(b.Min.X+(2*c+1)*b.Dx()/(2*cols), b.Min.Y+(2*r+1)*b.Dy()/(2*rows)).RGBA()
			switch {
			case R > 0xC000 && G > 0xC000 && B > 0xC000:
				out += "W"
			case R > 0xC000:
				out += "R"
			case G > 0xC000:
				out += "G"
			case B > 0xC000:
				out += "B"
			default:
				out += "?"
			}
		}
		out += "/"
	}
	return out
}

// TestIntegrationHEIC decodes synthetic HEIC files with the managed FFmpeg.
// The picture has red, green, blue, and white quadrants, so the colors show
// how it was rotated or mirrored.
func TestIntegrationHEIC(t *testing.T) {
	managedTools(t)
	cache := testCache(t)
	hevc, err := os.ReadFile(testdata("image", "quadrants-64x48.hevc"))
	if err != nil {
		t.Fatal(err)
	}
	exif := testimages.PhoneEXIF(binary.BigEndian, 6)
	build := func(o testimages.HEIFOptions) []byte {
		o.HEVC, o.Width, o.Height = hevc, 64, 48
		return testimages.HEIF(o)
	}
	dir := t.TempDir()
	cases := []struct {
		name string
		opts testimages.HEIFOptions
		size string
		want string
	}{
		// Stored: RG over BW.
		{"plain", testimages.HEIFOptions{Mirror: -1}, "64x48", "RG/BW/"},
		{"exif_only", testimages.HEIFOptions{Mirror: -1, EXIF: exif}, "64x48", "RG/BW/"},
		{"phone", testimages.HEIFOptions{Rotation: 270, Mirror: -1, EXIF: exif}, "48x64", "BR/WG/"},
		{"rot180", testimages.HEIFOptions{Rotation: 180, Mirror: -1}, "64x48", "WB/GR/"},
		{"mirror0", testimages.HEIFOptions{Mirror: 0}, "64x48", "BW/RG/"},
		{"mirror1", testimages.HEIFOptions{Mirror: 1}, "64x48", "GR/WB/"},
		{"rot90_mirror0", testimages.HEIFOptions{Rotation: 90, Mirror: 0}, "48x64", "RB/GW/"},
		{"mirror0_rot90", testimages.HEIFOptions{Rotation: 90, Mirror: 0, MirrorFirst: true}, "48x64", "WG/BR/"},
		{"thumbnail_item", testimages.HEIFOptions{Thumbnail: true, Rotation: 90, Mirror: -1}, "48x64", "GW/RB/"},
	}
	for _, c := range cases {
		in := filepath.Join(dir, c.name+".heic")
		data := build(c.opts)
		if err := os.WriteFile(in, data, 0o644); err != nil {
			t.Fatal(err)
		}
		r := runReal(t, cache, "thumb", in, "--size", "64x64", "--format", "png", "--output", filepath.Join(dir, "out"))
		if r.err != nil {
			t.Fatalf("%s: %v\n%s", c.name, r.err, r.stderr)
		}
		p := filepath.Join(dir, "out", c.name+"_thumb.png")
		s := probeFirstStream(t, ffprobeFor(t), p)
		if got := fmt.Sprintf("%dx%d", s.Width, s.Height); got != c.size {
			t.Errorf("%s: %s, want %s", c.name, got, c.size)
		}
		if got := quadrants(t, p, 2, 2); got != c.want {
			t.Errorf("%s: colors %s, want %s", c.name, got, c.want)
		}
		if after, _ := os.ReadFile(in); !bytes.Equal(after, data) {
			t.Errorf("%s: the source changed", c.name)
		}
	}

	// Grids with an odd width, as phones write: the right column of the
	// last tile is cropped, not the whole image rounded down. An alpha
	// plane, alone or as a grid, gives transparency; its picture is the
	// luma of the quadrants, so the red and blue corners are translucent.
	odd := []struct {
		name string
		opts testimages.HEIFOptions
		w, h int
	}{
		{"odd_grid", testimages.HEIFOptions{Grid: true, GridCrop: 1, Mirror: -1}, 127, 48},
		{"odd_grid_rot", testimages.HEIFOptions{Grid: true, GridCrop: 1, Rotation: 270, Mirror: -1}, 48, 127},
		{"alpha", testimages.HEIFOptions{Alpha: true, Mirror: -1}, 64, 48},
		{"alpha_grid", testimages.HEIFOptions{Grid: true, GridCrop: 1, Alpha: true, Mirror: -1}, 127, 48},
	}
	var oddList strings.Builder
	for _, c := range odd {
		if err := os.WriteFile(filepath.Join(dir, c.name+".heic"), build(c.opts), 0o644); err != nil {
			t.Fatal(err)
		}
		oddList.WriteString(c.name + ".heic\n")
	}
	oddPath := filepath.Join(dir, "odd.txt")
	if err := os.WriteFile(oddPath, []byte(oddList.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runReal(t, cache, "thumb", "batch", oddPath, "--size", "256x256", "--format", "png", "--output", filepath.Join(dir, "odd"), "--jsonl")
	if r.err != nil {
		t.Fatalf("odd sizes: %v\n%s", r.err, r.stderr)
	}
	recs := strings.Split(strings.TrimSpace(r.stdout), "\n")
	for i, c := range odd {
		var rec struct {
			SourceWidth  int    `json:"source_width"`
			SourceHeight int    `json:"source_height"`
			Output       string `json:"output"`
		}
		if i >= len(recs) || json.Unmarshal([]byte(recs[i]), &rec) != nil {
			t.Fatalf("%s: record %q", c.name, recs)
		}
		if rec.SourceWidth != c.w || rec.SourceHeight != c.h {
			t.Errorf("%s: source %dx%d, want %dx%d", c.name, rec.SourceWidth, rec.SourceHeight, c.w, c.h)
		}
		data, _ := os.ReadFile(rec.Output)
		img, err := imaging.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if got := img.HasTransparentPixels(); got != c.opts.Alpha {
			t.Errorf("%s: transparent %v", c.name, got)
		}
		// The top left (red) corner is the first tile, right side up.
		if rr, gg, bb, _ := img.Pixels.At(4, 4).RGBA(); c.name == "odd_grid" && (rr>>8 < 200 || gg>>8 > 60 || bb>>8 > 60) {
			t.Errorf("%s: top left %v", c.name, img.Pixels.At(4, 4))
		}
	}

	// A phone-style tile grid: 2x1 tiles of 64x48, rotated 90 degrees
	// clockwise, with EXIF orientation 6 that must not rotate it again.
	grid := build(testimages.HEIFOptions{Grid: true, Rotation: 270, Mirror: -1, EXIF: exif})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(grid) }))
	defer srv.Close()
	gdir := t.TempDir()
	local := filepath.Join(gdir, "IMG grid.jpg") // HEIC content, JPEG name
	if err := os.WriteFile(local, grid, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args       []string
		file, size string
	}{
		{[]string{"--size", "240x320"}, "IMG grid_thumb.jpg", "120x320"},
		{[]string{"--size", "300x300", "--mode", "fill", "--format", "png"}, "IMG grid_thumb.png", "300x300"},
	} {
		r := runReal(t, cache, append([]string{"thumb", local, "--output", gdir}, c.args...)...)
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.stderr)
		}
		p := filepath.Join(gdir, c.file)
		data, _ := os.ReadFile(p)
		img, err := imaging.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		b := img.Pixels.Bounds()
		if got := fmt.Sprintf("%dx%d", b.Dx(), b.Dy()); got != c.size || img.EXIF != nil || bytes.Contains(data, []byte("Exif")) || bytes.Contains(data, []byte("iPhone")) {
			t.Errorf("%s: %s, EXIF %v", c.file, got, img.EXIF)
		}
	}
	// The fit thumbnail shows both tiles rotated: BR/WG twice, top to bottom.
	if got := quadrants(t, filepath.Join(gdir, "IMG grid_thumb.jpg"), 2, 4); got != "BR/WG/BR/WG/" {
		t.Errorf("grid colors %s", got)
	}

	// URL and list sources, with a failing line.
	list := filepath.Join(gdir, "sources.txt")
	content := "IMG grid.jpg\n" + srv.URL + "/p/remote.HEIC?sig=SECRET\n\nmissing.heic\n"
	if err := os.WriteFile(list, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r = runReal(t, cache, "thumb", "batch", list, "--size", "100x100", "--output", filepath.Join(gdir, "batch"))
	if r.err == nil || r.err.Error() != "1 of 3 list lines failed" || !strings.Contains(r.stderr, "line 4: missing.heic: file does not exist") ||
		strings.Contains(r.stdout+r.stderr, "SECRET") {
		t.Errorf("batch: %v\n%s\n%s", r.err, r.stdout, r.stderr)
	}
	if got := quadrants(t, filepath.Join(gdir, "batch", "remote_thumb.jpg"), 2, 4); got != "BR/WG/BR/WG/" {
		t.Errorf("remote grid colors %s", got)
	}
	r = runReal(t, cache, "inspect", "--json", srv.URL+"/p/remote.HEIC?sig=SECRET")
	if r.err != nil || !strings.Contains(r.stdout, `"display_width": 48`) || !strings.Contains(r.stdout, `"name": "GPSLatitude"`) {
		t.Errorf("inspect URL: %v\n%s", r.err, r.stdout)
	}
}

func ffprobeFor(t *testing.T) string {
	_, ffprobe := managedTools(t)
	return ffprobe
}

// managedTools returns the managed ffmpeg and ffprobe from the test cache.
func managedTools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	root, _ := tools.DefaultCacheRoot(testCache(t))
	var paths [2]string
	for i, tool := range []string{tools.ToolFFmpeg, tools.ToolFFprobe} {
		a, err := tools.LookupArtifact(tool, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			t.Skip(err)
		}
		p, err := (&tools.Manager{Root: root, Artifact: a}).Ensure(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		paths[i] = p
	}
	return paths[0], paths[1]
}

// parseRatio reads an FFprobe ratio such as "16:9".
func parseRatio(s string) (float64, bool) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return 0, false
	}
	num, err1 := strconv.ParseFloat(a, 64)
	den, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil || num <= 0 || den <= 0 {
		return 0, false
	}
	return num / den, true
}

// overlayDarkArea counts dark pixels in the 120x30 region at the top-left of
// a mosaic cell. On a white source that region holds the timestamp box, so
// the count follows the overlay size in the mosaic.
// firstThumb returns the position and size of the first frame in a mosaic,
// below the header.
func firstThumb(aspect float64, frames int) (x, y, w, h int) {
	l := media.MosaicLayoutFor(aspect, frames)
	x = (l.GridW-l.Cols*l.CellW)/2 + (l.CellW-l.ThumbW)/2
	y = media.MosaicHeader + (l.GridH-l.Rows*l.CellH)/2 + (l.CellH-l.ThumbH)/2
	return x, y, l.ThumbW, l.ThumbH
}

// overlayDarkArea counts the dark pixels of the timestamp box in the
// region of 120x30 pixels times scale at x, y. Scale is the size of the
// frame in the mosaic relative to a cell of the 10 by 10 grid.
func overlayDarkArea(t *testing.T, ffmpeg, p string, x, y int, scale float64) int {
	t.Helper()
	w, h := int(math.Round(120*scale)), int(math.Round(30*scale))
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", p,
		"-vf", fmt.Sprintf("crop=%d:%d:%d:%d,format=gray", w, h, x, y),
		"-frames:v", "1", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatalf("read overlay region of %s: %v", p, err)
	}
	dark := 0
	for _, v := range out {
		if v < 128 {
			dark++
		}
	}
	return dark
}
