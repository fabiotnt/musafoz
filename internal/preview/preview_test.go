package preview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fabiotnt/musafoz/internal/media"
)

func testdata(parts ...string) string {
	p, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...))
	if err != nil {
		panic(err)
	}
	return p
}

// fakeFFprobe prints FAKE_FFPROBE_JSON (default sample.json) and fails like
// ffprobe on files named unfinished.mp4.
const fakeFFprobeScript = `#!/bin/sh
if [ "$1" = "-version" ]; then echo "ffprobe version 9.1-tessus"; exit 0; fi
for a in "$@"; do last="$a"; done
case "$last" in *unfinished.mp4) echo "$last: moov atom not found" >&2; exit 1;; esac
case "$last" in *.musafoz-*) if [ -n "$FAKE_FFPROBE_REMUX_JSON" ]; then cat "$FAKE_FFPROBE_REMUX_JSON"; exit 0; fi;; esac
cat "${FAKE_FFPROBE_JSON:-SAMPLE_JSON}"
`

// fakeFFmpeg answers feature queries, fails when its arguments contain
// FAKE_FFMPEG_FAIL, copies FAKE_FASTSTART_SRC for remuxes, and otherwise
// writes "from:<input>" into its output. Outputs built from frames copy the
// first frame, so every output names the source video.
//
// FAKE_HW simulates VideoToolbox decoding: empty means not listed, "ok"
// works, "probe_fail" fails the probe, and "extract_fail" passes the probe
// but fails frame extraction. FAKE_HW_ENC simulates h264_videotoolbox:
// empty means not listed, "ok" works, "probe_fail" fails the test encode,
// and "encode_fail" passes the test encode but fails the real one.
const fakeFFmpegScript = `#!/bin/sh
if [ "$1" = "-version" ]; then echo "ffmpeg version 9.1-tessus"; exit 0; fi
for a in "$@"; do
  case "$a" in
    -encoders)
      printf ' V....D libx264 H.264\n V....D mjpeg MJPEG\n'
      if [ -n "$FAKE_HW_ENC" ]; then printf ' V....D h264_videotoolbox VT\n'; fi
      exit 0;;
    -filters) printf ' T. drawtext V->V x\n .. tile V->V x\n .. scale V->V x\n .. pad V->V x\n .. setsar V->V x\n'; exit 0;;
    -hwaccels)
      printf 'Hardware acceleration methods:\n'
      if [ -n "$FAKE_HW" ]; then printf 'videotoolbox\n'; fi
      printf '\n'; exit 0;;
  esac
done
case "$*" in
  *videotoolbox_vld*-frames:v\ 2*) if [ "$FAKE_HW" = probe_fail ]; then echo "hw probe failed" >&2; exit 1; fi; exit 0;;
  *videotoolbox_vld*) if [ "$FAKE_HW" = extract_fail ]; then echo "hw decode failed" >&2; exit 1; fi;;
  *lavfi*h264_videotoolbox*) if [ "$FAKE_HW_ENC" = probe_fail ]; then echo "hw encode probe failed" >&2; exit 1; fi; exit 0;;
  *h264_videotoolbox*) if [ "$FAKE_HW_ENC" = encode_fail ]; then echo "hw encode failed" >&2; exit 1; fi;;
esac
if [ -n "$FAKE_FFMPEG_SLEEP" ]; then sleep "$FAKE_FFMPEG_SLEEP"; fi
if [ -n "$FAKE_FFMPEG_FAIL" ]; then
  case "$*" in *"$FAKE_FFMPEG_FAIL"*) echo "fake failure on $FAKE_FFMPEG_FAIL" >&2; exit 1;; esac
fi
in=""; prev=""
for a in "$@"; do
  if [ "$prev" = "-i" ]; then in="$a"; fi
  prev="$a"; out="$a"
done
in="${in#file:}"; out="${out#file:}"
case "$out" in -*) exit 0;; esac
case "$*" in *+faststart*) cp "$FAKE_FASTSTART_SRC" "$out"; exit 0;; esac
case "$in" in
  *%03d*) cat "$(printf "$in" 0)" > "$out";;
  *grid.jpg) cat "$in" > "$out";;
  *) echo "from:$in" > "$out";;
esac
`

type fixture struct {
	dir     string
	ffprobe string
	ffmpeg  string
	font    string
	probes  atomic.Int32
	mpegs   atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	skipWithoutShell(t)
	t.Helper()
	tools := t.TempDir()
	f := &fixture{dir: t.TempDir()}
	f.ffprobe = filepath.Join(tools, "ffprobe")
	f.ffmpeg = filepath.Join(tools, "ffmpeg")
	f.font = filepath.Join(tools, "font.ttf")
	script := strings.Replace(fakeFFprobeScript, "SAMPLE_JSON", testdata("ffprobe", "sample.json"), 1)
	must(t, os.WriteFile(f.ffprobe, []byte(script), 0o755))
	must(t, os.WriteFile(f.ffmpeg, []byte(fakeFFmpegScript), 0o755))
	must(t, os.WriteFile(f.font, []byte("\x00\x01\x00\x00fake TrueType"), 0o644))
	t.Setenv("FAKE_FASTSTART_SRC", testdata("video", "faststart.mp4"))
	t.Setenv("FAKE_FFMPEG_FAIL", "")
	t.Setenv("FAKE_FFMPEG_SLEEP", "")
	t.Setenv("FAKE_FFPROBE_JSON", "")
	t.Setenv("FAKE_FFPROBE_REMUX_JSON", "")
	t.Setenv("FAKE_HW", "")
	t.Setenv("FAKE_HW_ENC", "")
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// copyInput copies a test video into the fixture directory under name.
func (f *fixture) copyInput(t *testing.T, src, name string) string {
	t.Helper()
	data, err := os.ReadFile(testdata("video", src))
	must(t, err)
	p := filepath.Join(f.dir, name)
	must(t, os.WriteFile(p, data, 0o640))
	return p
}

func (f *fixture) options(input string) Options {
	return Options{
		Input:  input,
		Frames: 5,
		Font:   f.font,
		ResolveFFprobe: func(context.Context) (string, error) {
			f.probes.Add(1)
			return f.ffprobe, nil
		},
		ResolveFFmpeg: func(context.Context) (string, error) {
			f.mpegs.Add(1)
			return f.ffmpeg, nil
		},
		Random: func() float64 { return 0.5 },
	}
}

func hash(t *testing.T, p string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(p)
	must(t, err)
	return sha256.Sum256(data)
}

func readString(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	must(t, err)
	return string(data)
}

// assertClean checks that no staging directory is left beside the input.
func assertClean(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && strings.Contains(e.Name(), ".musafoz-") {
			t.Errorf("staging directory left behind: %s", e.Name())
		}
	}
}

func assertNoOutputs(t *testing.T, input string) {
	t.Helper()
	for _, p := range OutputsFor(input).List() {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("unexpected output %s", p)
		}
	}
}

func stepOf(t *testing.T, err error) string {
	t.Helper()
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StepError", err)
	}
	return se.Step
}

// writeOld creates earlier outputs with known content.
func writeOld(t *testing.T, input string) {
	t.Helper()
	for _, p := range OutputsFor(input).List() {
		must(t, os.WriteFile(p, []byte("old:"+filepath.Base(p)), 0o644))
	}
}

func assertOld(t *testing.T, input string) {
	t.Helper()
	for _, p := range OutputsFor(input).List() {
		if got := readString(t, p); got != "old:"+filepath.Base(p) {
			t.Errorf("%s = %q, want the earlier content", filepath.Base(p), got)
		}
	}
}

func TestOutputsFor(t *testing.T) {
	o := OutputsFor("/v/a.b.mov")
	if o.Cover != "/v/a.b_cover.jpg" || o.Video != "/v/a.b_preview.mp4" || o.Mosaic != "/v/a.b_preview.jpg" {
		t.Errorf("outputs = %+v", o)
	}
	if OutputsFor("/v/clip.mp4") != OutputsFor("/v/clip.mov") {
		t.Error("inputs with the same base name must share outputs")
	}
}

func TestRunCreatesOutputs(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	before := hash(t, input)

	res, err := Run(context.Background(), f.options(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Outputs.List() {
		if !strings.Contains(readString(t, p), "from:"+input) {
			t.Errorf("%s was not built from the input", p)
		}
	}
	if res.Faststart != FaststartNotRequested || res.Frames != 5 || res.DurationSeconds != 2 {
		t.Errorf("result = %+v", res)
	}
	// Two-second clip at 10 fps: the random value 0.5 picks the middle of
	// 0 to 1.9, the start of the last frame.
	if res.CoverAtSeconds != 0.95 {
		t.Errorf("cover at %v, want 0.95", res.CoverAtSeconds)
	}
	if hash(t, input) != before {
		t.Error("the original changed")
	}
	assertClean(t, f.dir)
}

func coverAt(v float64) *float64 { return &v }

func TestRunCoverAt(t *testing.T) {
	cases := []struct {
		name string
		at   float64
		ss   string
	}{
		{"zero", 0, "-ss 0.000 -i"},
		{"fraction", 1.25, "-ss 1.250 -i"},
		{"last frame", 1.9, "-ss 1.900 -i"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			input := f.copyInput(t, "sample.mp4", "clip.mp4")
			opts := f.options(input)
			opts.CoverAt = coverAt(c.at)
			opts.Random = func() float64 { t.Error("random cover used with CoverAt"); return 0 }
			var log bytes.Buffer
			opts.Logf = func(format string, a ...any) { fmt.Fprintf(&log, format+"\n", a...) }
			res, err := Run(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.CoverAtSeconds != c.at {
				t.Errorf("cover_at_seconds = %v, want %v", res.CoverAtSeconds, c.at)
			}
			if want := "generating cover at " + media.Timestamp(c.at) + " (--cover-at)"; !strings.Contains(log.String(), want) {
				t.Errorf("log lacks %q:\n%s", want, log.String())
			}

			// The cover command seeks to exactly that time: make it fail on the
			// expected -ss value.
			input2 := f.copyInput(t, "sample.mp4", "clip2.mp4")
			opts.Input = input2
			// Frame extraction also seeks with -ss; only the cover puts -vf
			// right after the input.
			t.Setenv("FAKE_FFMPEG_FAIL", c.ss+" file:"+input2+" -vf")
			if _, err := Run(context.Background(), opts); err == nil || stepOf(t, err) != StepCover {
				t.Errorf("cover command did not use %q: %v", c.ss, err)
			}
		})
	}
}

func TestRunRejectsCoverOutsideVideo(t *testing.T) {
	cases := map[string]float64{"past the last frame": 1.95, "past the end": 2, "far past": 3600, "negative": -1}
	for name, at := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			input := f.copyInput(t, "sample.mp4", "clip.mp4")
			opts := f.options(input)
			opts.CoverAt = coverAt(at)
			_, err := Run(context.Background(), opts)
			if err == nil || stepOf(t, err) != StepValidateOptions {
				t.Fatalf("err = %v", err)
			}
			if at >= 0 && !strings.Contains(err.Error(), "last frame starts at 00:00:01.900") {
				t.Errorf("error does not give the valid range: %v", err)
			}
			if f.mpegs.Load() != 0 {
				t.Error("ffmpeg was resolved before the cover time was checked")
			}
			assertNoOutputs(t, input)
			assertClean(t, f.dir)
		})
	}
}

func TestRunMosaicGridFollowsFrames(t *testing.T) {
	cases := map[int]string{1: "tile=1x1:", 5: "tile=3x2:", 25: "tile=5x5:", 98: "tile=10x10:", 100: "tile=10x10:"}
	for frames, marker := range cases {
		t.Run(fmt.Sprint(frames), func(t *testing.T) {
			f := newFixture(t)
			input := f.copyInput(t, "sample.mp4", "clip.mp4")
			opts := f.options(input)
			opts.Frames = frames
			res, err := Run(context.Background(), opts)
			if err != nil || res.Frames != frames {
				t.Fatalf("res = %+v, err = %v", res, err)
			}
			input2 := f.copyInput(t, "sample.mp4", "clip2.mp4")
			opts.Input = input2
			t.Setenv("FAKE_FFMPEG_FAIL", marker)
			if _, err := Run(context.Background(), opts); err == nil || stepOf(t, err) != StepMosaic {
				t.Errorf("mosaic did not use %q: %v", marker, err)
			}
		})
	}
}

func TestRunRejectsBadFrameCounts(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	for _, n := range []int{-1, 0, 101} {
		opts := f.options(input)
		opts.Frames = n
		if _, err := Run(context.Background(), opts); err == nil || stepOf(t, err) != StepValidateOptions {
			t.Errorf("frames %d: err = %v", n, err)
		}
	}
}

func TestRunConflictsAbortBeforeAnyTool(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	o := OutputsFor(input)
	must(t, os.WriteFile(o.Cover, []byte("x"), 0o644))
	must(t, os.WriteFile(o.Mosaic, []byte("x"), 0o644))

	_, err := Run(context.Background(), f.options(input))
	var ce *ConflictError
	if !errors.As(err, &ce) || stepOf(t, err) != StepCheckOutputs {
		t.Fatalf("err = %v, want a conflict at %s", err, StepCheckOutputs)
	}
	if len(ce.Paths) != 2 || ce.Paths[0] != o.Cover || ce.Paths[1] != o.Mosaic {
		t.Errorf("conflicts = %v", ce.Paths)
	}
	if f.probes.Load() != 0 || f.mpegs.Load() != 0 {
		t.Errorf("tools were resolved: ffprobe %d, ffmpeg %d", f.probes.Load(), f.mpegs.Load())
	}
	if _, err := os.Stat(o.Video); err == nil {
		t.Error("preview video was created")
	}
}

func TestRunRejectsInvalidMediaBeforeFFmpeg(t *testing.T) {
	cases := []struct {
		name, src, fixture string
		want               error
		wantText           string
	}{
		{"unfinished mp4", "unfinished.mp4", "", nil, "moov atom not found"},
		{"no video", "audio_only.m4a", "audio_only.json", media.ErrNoVideoStream, ""},
		{"unknown duration", "raw.h264", "raw_h264.json", media.ErrUnknownDuration, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.fixture != "" {
				t.Setenv("FAKE_FFPROBE_JSON", testdata("ffprobe", c.fixture))
			}
			input := f.copyInput(t, c.src, c.src)
			before := hash(t, input)
			_, err := Run(context.Background(), f.options(input))
			if err == nil || stepOf(t, err) != StepInspect {
				t.Fatalf("err = %v, want failure at %s", err, StepInspect)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			if !strings.Contains(err.Error(), input) || (c.wantText != "" && !strings.Contains(err.Error(), c.wantText)) {
				t.Errorf("err = %v, want the path and reason", err)
			}
			if f.mpegs.Load() != 0 {
				t.Error("ffmpeg was resolved")
			}
			if hash(t, input) != before {
				t.Error("the original changed")
			}
			assertNoOutputs(t, input)
			assertClean(t, f.dir)
		})
	}
}

func TestRunProcessingFailurePreservesEverything(t *testing.T) {
	steps := map[string]string{
		"frame_003.jpg":        StepExtractFrames,
		"setsar=1 -frames:v 1": StepCover,
		"libx264":              StepPreviewVideo,
		"tile=":                StepMosaic,
	}
	for marker, step := range steps {
		for _, overwrite := range []bool{false, true} {
			t.Run(step+map[bool]string{false: "", true: " overwrite"}[overwrite], func(t *testing.T) {
				f := newFixture(t)
				input := f.copyInput(t, "sample.mp4", "clip.mp4")
				before := hash(t, input)
				if overwrite {
					writeOld(t, input)
				}
				t.Setenv("FAKE_FFMPEG_FAIL", marker)
				opts := f.options(input)
				opts.Overwrite = overwrite
				_, err := Run(context.Background(), opts)
				if err == nil || stepOf(t, err) != step {
					t.Fatalf("err = %v, want failure at %s", err, step)
				}
				if hash(t, input) != before {
					t.Error("the original changed")
				}
				if overwrite {
					assertOld(t, input)
				} else {
					assertNoOutputs(t, input)
				}
				assertClean(t, f.dir)
			})
		}
	}
}

func TestRunOverwriteReplacesOutputs(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	writeOld(t, input)
	opts := f.options(input)
	opts.Overwrite = true
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, p := range OutputsFor(input).List() {
		if !strings.HasPrefix(readString(t, p), "from:") {
			t.Errorf("%s was not replaced", p)
		}
	}
	assertClean(t, f.dir)
}

func TestRunPublishFailureRestoresSet(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	writeOld(t, input)
	o := OutputsFor(input)
	beforePublish = func(dst string) error {
		if dst == o.Mosaic {
			return errors.New("disk full")
		}
		return nil
	}
	t.Cleanup(func() { beforePublish = func(string) error { return nil } })

	opts := f.options(input)
	opts.Overwrite = true
	_, err := Run(context.Background(), opts)
	if err == nil || stepOf(t, err) != StepPublish || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	assertOld(t, input)
	assertClean(t, f.dir)
}

func TestRunPublishFailureWithoutEarlierOutputsRemovesNewOnes(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	o := OutputsFor(input)
	beforePublish = func(dst string) error {
		if dst == o.Mosaic {
			return errors.New("disk full")
		}
		return nil
	}
	t.Cleanup(func() { beforePublish = func(string) error { return nil } })
	if _, err := Run(context.Background(), f.options(input)); err == nil {
		t.Fatal("expected an error")
	}
	assertNoOutputs(t, input)
	assertClean(t, f.dir)
}

func TestRunFailedRestoreKeepsBackups(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	writeOld(t, input)
	o := OutputsFor(input)
	beforePublish = func(dst string) error {
		if dst == o.Mosaic {
			return errors.New("disk full")
		}
		return nil
	}
	beforeRestore = func(dst string) error {
		if dst == o.Cover {
			return errors.New("permission denied")
		}
		return nil
	}
	t.Cleanup(func() {
		beforePublish = func(string) error { return nil }
		beforeRestore = func(string) error { return nil }
	})

	opts := f.options(input)
	opts.Overwrite = true
	_, err := Run(context.Background(), opts)
	var se *StepError
	if !errors.As(err, &se) || se.Kept == "" {
		t.Fatalf("err = %v, want a kept staging directory", err)
	}
	if !strings.Contains(err.Error(), se.Kept) || !strings.Contains(err.Error(), o.Cover) {
		t.Errorf("error does not name the kept directory and output: %v", err)
	}
	backup := filepath.Join(se.Kept, "backup", filepath.Base(o.Cover))
	if got := readString(t, backup); got != "old:"+filepath.Base(o.Cover) {
		t.Errorf("backup = %q", got)
	}
	// The preview video was restored; the cover could not be.
	if got := readString(t, o.Video); got != "old:"+filepath.Base(o.Video) {
		t.Errorf("preview video = %q, want the earlier content", got)
	}
}

func TestRunReportsInterruptedRuns(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	stale := filepath.Join(f.dir, ".clip.musafoz-123456")
	must(t, os.MkdirAll(filepath.Join(stale, "backup"), 0o755))
	must(t, os.WriteFile(filepath.Join(stale, "backup", "clip_cover.jpg"), []byte("old"), 0o644))
	other := filepath.Join(f.dir, ".clip.v2.musafoz-1")
	must(t, os.MkdirAll(other, 0o755))

	var warnings []string
	opts := f.options(input)
	opts.Warnf = func(format string, a ...any) {
		warnings = append(warnings, strings.TrimSpace(strings.ReplaceAll(format, "%s", a[0].(string))))
	}
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], stale) {
		t.Errorf("warnings = %v, want one naming %s", warnings, stale)
	}
	if _, err := os.Stat(filepath.Join(stale, "backup", "clip_cover.jpg")); err != nil {
		t.Errorf("stale backup was removed: %v", err)
	}
}

func TestRunFaststartInPlace(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	opts := f.options(input)
	opts.FaststartInPlace = true
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Faststart != FaststartApplied {
		t.Errorf("faststart = %q", res.Faststart)
	}
	if hash(t, input) != hash(t, testdata("video", "faststart.mp4")) {
		t.Error("the original was not replaced by the remux")
	}
	fi, err := os.Stat(input)
	must(t, err)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 kept from the original", fi.Mode().Perm())
	}
	// Outputs come from the remuxed copy, which then replaced the original.
	if !strings.Contains(readString(t, OutputsFor(input).Cover), ".musafoz-") {
		t.Error("cover was not built from the remuxed file")
	}
	assertClean(t, f.dir)
}

func TestRunFaststartAlreadyApplied(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "faststart.mp4", "clip.mp4")
	before := hash(t, input)
	opts := f.options(input)
	opts.FaststartInPlace = true
	t.Setenv("FAKE_FFMPEG_FAIL", "+faststart")
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Faststart != FaststartAlready || hash(t, input) != before {
		t.Errorf("faststart = %q, original changed = %v", res.Faststart, hash(t, input) != before)
	}
}

func TestRunFaststartFailures(t *testing.T) {
	cases := map[string]string{
		"+faststart": StepFaststart,
		"tile=":      StepMosaic,
		"libx264":    StepPreviewVideo,
	}
	for marker, step := range cases {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			input := f.copyInput(t, "sample.mp4", "clip.mp4")
			before := hash(t, input)
			t.Setenv("FAKE_FFMPEG_FAIL", marker)
			opts := f.options(input)
			opts.FaststartInPlace = true
			_, err := Run(context.Background(), opts)
			if err == nil || stepOf(t, err) != step {
				t.Fatalf("err = %v, want failure at %s", err, step)
			}
			if hash(t, input) != before {
				t.Error("the original changed")
			}
			assertNoOutputs(t, input)
			assertClean(t, f.dir)
		})
	}
}

func TestRunFaststartRejectsOtherContainers(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mkv")
	opts := f.options(input)
	opts.FaststartInPlace = true
	_, err := Run(context.Background(), opts)
	if err == nil || stepOf(t, err) != StepValidateOptions || f.probes.Load() != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestRunReplaceOriginalFailureRestoresOutputs(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	before := hash(t, input)
	writeOld(t, input)
	o := OutputsFor(input)
	// Touch the original after the last output is published, so the
	// replacement detects the change and refuses.
	beforePublish = func(dst string) error {
		if dst == o.Mosaic {
			return os.WriteFile(input, bytes.Repeat([]byte("x"), 10), 0o640)
		}
		return nil
	}
	t.Cleanup(func() { beforePublish = func(string) error { return nil } })
	opts := f.options(input)
	opts.Overwrite = true
	opts.FaststartInPlace = true
	_, err := Run(context.Background(), opts)
	if err == nil || stepOf(t, err) != StepReplaceOriginal {
		t.Fatalf("err = %v", err)
	}
	assertOld(t, input)
	if hash(t, input) == before {
		t.Fatal("test setup did not modify the original")
	}
	if got := readString(t, input); got != "xxxxxxxxxx" {
		t.Errorf("the modified original was overwritten: %q", got)
	}
	assertClean(t, f.dir)
}

// Two runs whose inputs share a base name compete for one output set.
func TestConcurrentRunsShareOutputSet(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "no overwrite", true: "overwrite"}[overwrite], func(t *testing.T) {
			f := newFixture(t)
			t.Setenv("FAKE_FFMPEG_SLEEP", "0.05")
			inputs := []string{
				f.copyInput(t, "sample.mp4", "clip.mp4"),
				f.copyInput(t, "sample.mp4", "clip.mov"),
			}
			errs := make([]error, len(inputs))
			var wg sync.WaitGroup
			for i, in := range inputs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					opts := f.options(in)
					opts.Overwrite = overwrite
					_, errs[i] = Run(context.Background(), opts)
				}()
			}
			wg.Wait()

			failed := 0
			for _, err := range errs {
				if err != nil {
					failed++
					var ce *ConflictError
					if overwrite || !errors.As(err, &ce) || len(ce.Paths) != 3 {
						t.Errorf("unexpected error: %v", err)
					}
				}
			}
			if want := map[bool]int{false: 1, true: 0}[overwrite]; failed != want {
				t.Errorf("%d runs failed, want %d", failed, want)
			}
			// All three outputs must come from the same input.
			var source string
			for _, p := range OutputsFor(inputs[0]).List() {
				content := readString(t, p)
				if source == "" {
					source = content
				} else if content != source {
					t.Errorf("mixed output set: %q vs %q", content, source)
				}
			}
			assertClean(t, f.dir)
		})
	}
}

// A remux that rewrites a track, as FFmpeg does with Apple timed metadata,
// must be refused and the original kept.
func TestRunFaststartRefusesChangedTracks(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mov")
	before := hash(t, input)
	data, err := os.ReadFile(testdata("ffprobe", "sample.json"))
	must(t, err)
	changed := strings.Replace(string(data), `"codec_tag_string": "mp4a"`, `"codec_tag_string": "stts"`, 1)
	if changed == string(data) {
		t.Fatal("fixture has no mp4a codec tag")
	}
	remuxJSON := filepath.Join(t.TempDir(), "remux.json")
	must(t, os.WriteFile(remuxJSON, []byte(changed), 0o644))
	t.Setenv("FAKE_FFPROBE_REMUX_JSON", remuxJSON)

	opts := f.options(input)
	opts.FaststartInPlace = true
	_, err = Run(context.Background(), opts)
	if err == nil || stepOf(t, err) != StepFaststart || !strings.Contains(err.Error(), "would not preserve every track") {
		t.Fatalf("err = %v", err)
	}
	if hash(t, input) != before {
		t.Error("the original changed")
	}
	assertNoOutputs(t, input)
	assertClean(t, f.dir)
}

// mebxFixture writes sample.json with an extra Apple timed metadata track.
func mebxFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(testdata("ffprobe", "sample.json"))
	must(t, err)
	var doc map[string]any
	must(t, json.Unmarshal(data, &doc))
	streams := doc["streams"].([]any)
	doc["streams"] = append(streams, map[string]any{
		"index": len(streams), "codec_type": "data", "codec_tag_string": "mebx",
		"tags": map[string]any{"handler_name": "Core Media Metadata"},
	})
	out, err := json.Marshal(doc)
	must(t, err)
	p := filepath.Join(t.TempDir(), "mebx.json")
	must(t, os.WriteFile(p, out, 0o644))
	return p
}

func TestRunFaststartRefusesMebxBeforeRemux(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_FFPROBE_JSON", mebxFixture(t))
	input := f.copyInput(t, "sample.mp4", "IMG_0001.mov")
	before := hash(t, input)
	opts := f.options(input)
	opts.FaststartInPlace = true
	_, err := Run(context.Background(), opts)
	var ue *UnsupportedTracksError
	if !errors.As(err, &ue) || stepOf(t, err) != StepFaststart {
		t.Fatalf("err = %v, want UnsupportedTracksError at %s", err, StepFaststart)
	}
	for _, want := range []string{"mebx", "original was preserved", "without --faststart-in-place", input} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if f.mpegs.Load() != 0 {
		t.Error("ffmpeg was resolved before the refusal")
	}
	if hash(t, input) != before {
		t.Error("the original changed")
	}
	assertNoOutputs(t, input)
	assertClean(t, f.dir)
}

func TestRunMebxWithoutFaststartWorks(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_FFPROBE_JSON", mebxFixture(t))
	input := f.copyInput(t, "sample.mp4", "IMG_0001.mov")
	before := hash(t, input)
	res, err := Run(context.Background(), f.options(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Outputs.List() {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing output %s", p)
		}
	}
	if hash(t, input) != before {
		t.Error("the original changed")
	}
}

// runLogged runs a preview and returns its result, error, and log lines.
func runLogged(t *testing.T, ctx context.Context, opts Options) (*Result, error, string) {
	t.Helper()
	var lines []string
	opts.Logf = func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }
	res, err := Run(ctx, opts)
	return res, err, strings.Join(lines, "\n")
}

func TestAccelDecodeChoice(t *testing.T) {
	cases := []struct {
		name, hw, platform string
		accel              media.Accel
		backend, reason    string
	}{
		{"forced cpu", "ok", "darwin", media.AccelCPU, "cpu", "frame decoding: CPU (forced by --accel cpu)"},
		{"auto with videotoolbox", "ok", "darwin", media.AccelAuto, "videotoolbox", "frame decoding: VideoToolbox (VideoToolbox decoded a h264 test sample)"},
		{"probe fails", "probe_fail", "darwin", media.AccelAuto, "cpu", "VideoToolbox could not decode this h264 input"},
		{"not listed", "", "darwin", media.AccelAuto, "cpu", "FFmpeg does not list VideoToolbox"},
		{"not macOS", "ok", "linux", media.AccelAuto, "cpu", "hardware decoding is only implemented on macOS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			t.Setenv("FAKE_HW", c.hw)
			opts := f.options(f.copyInput(t, "sample.mp4", "clip.mp4"))
			opts.Accel, opts.Platform = c.accel, c.platform
			res, err, log := runLogged(t, context.Background(), opts)
			if err != nil {
				t.Fatalf("%v\n%s", err, log)
			}
			if res.FrameDecoder != c.backend || !strings.Contains(log, c.reason) {
				t.Errorf("decoder %q, want %q with %q in log:\n%s", res.FrameDecoder, c.backend, c.reason, log)
			}
			if !strings.Contains(log, "extracting 5 frames on "+media.BackendLabel(c.backend)) {
				t.Errorf("log lacks the extraction backend:\n%s", log)
			}
		})
	}
}

func TestAccelExtractionFallsBackToCPU(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HW", "extract_fail")
	opts := f.options(f.copyInput(t, "sample.mp4", "clip.mp4"))
	opts.Platform = "darwin"
	res, err, log := runLogged(t, context.Background(), opts)
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if res.FrameDecoder != "cpu" || !strings.Contains(log, "frame extraction on VideoToolbox failed") ||
		!strings.Contains(log, "hw decode failed") || !strings.Contains(log, "retrying once on CPU") {
		t.Errorf("decoder %q, log:\n%s", res.FrameDecoder, log)
	}
}

// A failure on both paths, such as a damaged file, keeps the media error.
func TestAccelMediaErrorSurvivesFallback(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HW", "ok")
	t.Setenv("FAKE_FFMPEG_FAIL", "frame_003.jpg")
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	opts := f.options(input)
	opts.Platform = "darwin"
	_, err, log := runLogged(t, context.Background(), opts)
	if err == nil || stepOf(t, err) != StepExtractFrames || !strings.Contains(err.Error(), "frame 4") ||
		!strings.Contains(err.Error(), "fake failure on frame_003.jpg") {
		t.Fatalf("err = %v", err)
	}
	if strings.Count(log, "extracting 5 frames") != 2 {
		t.Errorf("want one hardware attempt and one CPU retry:\n%s", log)
	}
	assertNoOutputs(t, input)
	assertClean(t, f.dir)
}

func TestAccelCancellationIsNotRetried(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HW", "ok")
	t.Setenv("FAKE_FFMPEG_SLEEP", "0.3")
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lines []string
	opts := f.options(input)
	opts.Platform = "darwin"
	opts.Logf = func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		lines = append(lines, line)
		if strings.HasPrefix(line, "extracting") {
			cancel()
		}
	}
	_, err := Run(ctx, opts)
	log := strings.Join(lines, "\n")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled\n%s", err, log)
	}
	if strings.Contains(log, "retrying") {
		t.Errorf("a cancellation was retried:\n%s", log)
	}
	assertNoOutputs(t, input)
	assertClean(t, f.dir)
}

func TestAccelEncodeChoiceAndFallback(t *testing.T) {
	cases := []struct {
		name, accel, platform, hwEnc string
		backend, logWant             string
	}{
		{"hardware encode in auto", "auto", "darwin", "ok", "videotoolbox", "preview encoding: VideoToolbox with h264_videotoolbox (h264_videotoolbox passed a test encode)"},
		{"forced cpu", "cpu", "darwin", "ok", "cpu", "preview encoding: CPU with libx264 (forced by --accel cpu)"},
		{"other platform", "auto", "linux", "ok", "cpu", "hardware encoding is only implemented on macOS"},
		{"encoder not listed", "auto", "darwin", "", "cpu", "FFmpeg does not list h264_videotoolbox"},
		{"test encode fails", "auto", "darwin", "probe_fail", "cpu", "h264_videotoolbox failed a test encode"},
		{"hardware encode fails", "auto", "darwin", "encode_fail", "cpu", "retrying once with libx264"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			t.Setenv("FAKE_HW_ENC", c.hwEnc)
			opts := f.options(f.copyInput(t, "sample.mp4", "clip.mp4"))
			opts.Platform = c.platform
			opts.Accel = media.Accel(c.accel)
			res, err, log := runLogged(t, context.Background(), opts)
			if err != nil {
				t.Fatalf("%v\n%s", err, log)
			}
			if res.PreviewEncoder != c.backend || !strings.Contains(log, c.logWant) {
				t.Errorf("encoder %q, want %q with %q in log:\n%s", res.PreviewEncoder, c.backend, c.logWant, log)
			}
			if c.hwEnc == "encode_fail" && strings.Count(log, "extracting ") != 1 {
				t.Errorf("frames were extracted again for the libx264 retry:\n%s", log)
			}
		})
	}
}

// skipWithoutShell skips tests whose fake FFmpeg and FFprobe are shell
// scripts, which Windows cannot run. Windows is covered by the integration
// tests with real tools and by the manual procedure in the README.
func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tools are shell scripts")
	}
}

type recordedProgress struct {
	mu     sync.Mutex
	events []string
	max    map[string]int64
	label  string
}

func (p *recordedProgress) Start(label string, total int64, unit string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.label = label
	p.events = append(p.events, fmt.Sprintf("start %s %d %s", label, total, unit))
}

func (p *recordedProgress) Set(done int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.max == nil {
		p.max = map[string]int64{}
	}
	p.max[p.label] = max(p.max[p.label], done)
}

func (p *recordedProgress) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "done "+p.label)
}

// Every step without log output reports progress, and each Start is ended.
func TestProgressCoversSilentSteps(t *testing.T) {
	f := newFixture(t)
	opts := f.options(f.copyInput(t, "sample.mp4", "clip.mp4"))
	rec := &recordedProgress{}
	opts.Progress = rec
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"start choosing the frame decoder 0 ", "done choosing the frame decoder",
		"start extracting frames 5 frames", "done extracting frames",
		"start generating cover 0 ", "done generating cover",
		"start choosing the preview encoder 0 ", "done choosing the preview encoder",
		"start encoding preview video 5 frames", "done encoding preview video",
		"start building mosaic 0 ", "done building mosaic",
	}
	if strings.Join(rec.events, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(rec.events, "\n"), strings.Join(want, "\n"))
	}
	if rec.max["extracting frames"] != 5 {
		t.Errorf("frames reported = %d, want 5", rec.max["extracting frames"])
	}
}

// A failing step still ends its progress display.
func TestProgressEndsOnFailure(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_FFMPEG_FAIL", "tile=")
	opts := f.options(f.copyInput(t, "sample.mp4", "clip.mp4"))
	rec := &recordedProgress{}
	opts.Progress = rec
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("expected a mosaic failure")
	}
	if last := rec.events[len(rec.events)-1]; last != "done building mosaic" {
		t.Errorf("last event %q", last)
	}
}

// A bad --font fails before any tool is resolved and leaves nothing behind.
func TestInvalidFontFailsFirst(t *testing.T) {
	f := newFixture(t)
	input := f.copyInput(t, "sample.mp4", "clip.mp4")
	notFont := filepath.Join(t.TempDir(), "notes.ttf")
	must(t, os.WriteFile(notFont, []byte("hello"), 0o644))
	for _, font := range []string{filepath.Join(t.TempDir(), "missing.ttf"), notFont} {
		opts := f.options(input)
		opts.Font = font
		_, err := Run(context.Background(), opts)
		var se *StepError
		if !errors.As(err, &se) || se.Step != StepFindFont || !strings.Contains(err.Error(), font) {
			t.Fatalf("err = %v, want a %s error naming %s", err, StepFindFont, font)
		}
	}
	if f.probes.Load() != 0 || f.mpegs.Load() != 0 {
		t.Errorf("tools resolved: ffprobe %d, ffmpeg %d", f.probes.Load(), f.mpegs.Load())
	}
	assertNoOutputs(t, input)
	assertClean(t, f.dir)
}
