package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeFFmpegForCLI = `#!/bin/sh
if [ "$1" = "-version" ]; then echo "ffmpeg version 9.1-tessus"; exit 0; fi
for a in "$@"; do
  case "$a" in
    -encoders) printf ' V....D libx264 H.264\n V....D mjpeg MJPEG\n'; exit 0;;
    -filters) printf ' T. drawtext V->V x\n .. tile V->V x\n .. scale V->V x\n .. pad V->V x\n .. setsar V->V x\n'; exit 0;;
    -hwaccels) printf 'Hardware acceleration methods:\n\n'; exit 0;;
  esac
  out="$a"
done
case "$out" in -*) exit 0;; esac
echo data > "${out#file:}"
`

type previewFixture struct {
	input string
	args  []string
}

func newPreviewFixture(t *testing.T) previewFixture {
	t.Helper()
	skipWithoutShell(t)
	dir := t.TempDir()
	data, err := os.ReadFile(testdata("video", "sample.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(input, data, 0o644); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	ffmpeg := filepath.Join(tools, "ffmpeg")
	font := filepath.Join(tools, "font.ttf")
	if err := os.WriteFile(ffmpeg, []byte(fakeFFmpegForCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(font, []byte("\x00\x01\x00\x00fake TrueType"), 0o644); err != nil {
		t.Fatal(err)
	}
	return previewFixture{
		input: input,
		args:  []string{"--ffprobe", fakeFFprobe(t, "sample.json"), "--ffmpeg", ffmpeg, "preview", "--font", font, "--frames", "3"},
	}
}

func TestPreviewText(t *testing.T) {
	f := newPreviewFixture(t)
	r := run(t, nil, append(f.args, "--cover-at", "00:00:01.250", f.input)...)
	if r.err != nil {
		t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
	}
	for _, want := range []string{"clip_cover.jpg (at 00:00:01.250)", "clip_preview.mp4 (3 frames at 4 fps)", "clip_preview.jpg", "not requested, original unchanged"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "extracting 3 frames on CPU (") {
		t.Errorf("stderr lacks progress:\n%s", r.stderr)
	}
}

func TestPreviewJSONIsClean(t *testing.T) {
	f := newPreviewFixture(t)
	r := run(t, nil, append(f.args, "--json", f.input)...)
	if r.err != nil {
		t.Fatal(r.err)
	}
	var doc struct {
		Outputs struct {
			Cover string `json:"cover"`
		} `json:"outputs"`
		Frames    int    `json:"frames"`
		Faststart string `json:"faststart"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, r.stdout)
	}
	if doc.Frames != 3 || doc.Faststart != "not_requested" || !strings.HasSuffix(doc.Outputs.Cover, "clip_cover.jpg") {
		t.Errorf("unexpected JSON: %s", r.stdout)
	}
}

func TestPreviewConflictsListPaths(t *testing.T) {
	f := newPreviewFixture(t)
	if r := run(t, nil, append(f.args, f.input)...); r.err != nil {
		t.Fatal(r.err)
	}
	r := run(t, nil, append(f.args, f.input)...)
	if r.err == nil {
		t.Fatal("expected a conflict")
	}
	msg := r.err.Error()
	for _, want := range []string{"check outputs", "--overwrite", "clip_cover.jpg", "clip_preview.mp4", "clip_preview.jpg"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q: %s", want, msg)
		}
	}
	if r := run(t, nil, append(f.args, "--overwrite", f.input)...); r.err != nil {
		t.Errorf("--overwrite: %v", r.err)
	}
}

func TestPreviewRejectsFrameCounts(t *testing.T) {
	f := newPreviewFixture(t)
	for _, n := range []string{"0", "101"} {
		args := append(append([]string{}, f.args...), "--frames", n, f.input)
		if r := run(t, nil, args...); r.err == nil || !strings.Contains(r.err.Error(), "frames must be between 1 and 100") {
			t.Errorf("--frames %s: err = %v", n, r.err)
		}
	}
}

func TestPreviewCoverAt(t *testing.T) {
	f := newPreviewFixture(t)
	for _, c := range []struct {
		arg  string
		want float64
	}{{"00:00:00", 0}, {"00:00:01.5", 1.5}} {
		r := run(t, nil, append(f.args, "--overwrite", "--json", "--cover-at", c.arg, f.input)...)
		if r.err != nil {
			t.Fatalf("--cover-at %s: %v\nstderr:\n%s", c.arg, r.err, r.stderr)
		}
		var doc struct {
			CoverAt *float64 `json:"cover_at_seconds"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || doc.CoverAt == nil || *doc.CoverAt != c.want {
			t.Errorf("--cover-at %s: cover_at_seconds in %s, want %v", c.arg, r.stdout, c.want)
		}
	}
}

func TestPreviewRejectsCoverAt(t *testing.T) {
	f := newPreviewFixture(t)
	cases := map[string]string{
		"":             "--cover-at: timestamp \"\" is not in HH:MM:SS",
		"75":           "--cover-at: timestamp \"75\" is not in HH:MM:SS",
		"00:01":        "is not in HH:MM:SS",
		"00:00:61":     "60 or more",
		"-00:00:01":    "is negative",
		"00:00:02":     "cover time 00:00:02 is outside the video: the last frame starts at 00:00:01.900",
		"10:00:00.500": "cover time 10:00:00.500 is outside the video",
	}
	for arg, want := range cases {
		r := run(t, nil, append(f.args, "--cover-at="+arg, f.input)...)
		if r.err == nil || !strings.Contains(r.err.Error(), want) {
			t.Errorf("--cover-at %q: err = %v, want %q", arg, r.err, want)
		}
		for _, s := range []string{"_cover.jpg", "_preview.mp4", "_preview.jpg"} {
			if _, err := os.Stat(strings.TrimSuffix(f.input, ".mp4") + s); err == nil {
				t.Fatalf("--cover-at %q: output %s was created", arg, s)
			}
		}
	}
}
