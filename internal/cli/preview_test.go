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
	if err := os.WriteFile(font, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return previewFixture{
		input: input,
		args:  []string{"--ffprobe", fakeFFprobe(t, "sample.json"), "--ffmpeg", ffmpeg, "preview", "--font", font, "--frames", "3"},
	}
}

func TestPreviewText(t *testing.T) {
	f := newPreviewFixture(t)
	r := run(t, nil, append(f.args, f.input)...)
	if r.err != nil {
		t.Fatalf("%v\nstderr:\n%s", r.err, r.stderr)
	}
	for _, want := range []string{"clip_cover.jpg (at 00:00:01)", "clip_preview.mp4 (3 frames at 4 fps)", "clip_preview.jpg", "not requested, original unchanged"} {
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
