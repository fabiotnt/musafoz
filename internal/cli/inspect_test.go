package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testdata(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...)
}

// fakeFFprobe writes an executable that reports a release version and
// prints the given ffprobe JSON fixture.
func fakeFFprobe(t *testing.T, fixture string) string {
	t.Helper()
	abs, err := filepath.Abs(testdata("ffprobe", fixture))
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffprobe version 9.1-tessus'; exit 0; fi\ncat '" + abs + "'\n"
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type result struct {
	stdout, stderr string
	err            error
}

func run(t *testing.T, vars map[string]string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if vars == nil {
		vars = map[string]string{}
	}
	if _, ok := vars["MUSAFOZ_CACHE_DIR"]; !ok {
		vars["MUSAFOZ_CACHE_DIR"] = t.TempDir()
	}
	env := Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(k string) string { return vars[k] },
		GOOS:   "darwin",
		GOARCH: "amd64",
	}
	cmd := NewRootCommand(env)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return result{stdout.String(), stderr.String(), err}
}

func TestInspectText(t *testing.T) {
	r := run(t, nil, "inspect", "--ffprobe", fakeFFprobe(t, "sample.json"), testdata("video", "sample.mp4"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{
		"Duration:   2.000 s (00:00:02.000, from container)",
		"Video:      h264 (High), 320x240, yuv420p, 10 fps, 20 frames",
		"Audio:      aac, 1 channel (mono), 44100 Hz",
		"Streams:    1 video, 1 audio, 0 subtitle",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if r.stderr != "" {
		t.Errorf("unexpected stderr: %q", r.stderr)
	}
}

func TestInspectJSONIsCleanAndComplete(t *testing.T) {
	r := run(t, nil, "inspect", "--json", "--ffprobe", fakeFFprobe(t, "rotated.json"), testdata("video", "rotated.mp4"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, r.stdout)
	}
	video := doc["video"].(map[string]any)
	if video["rotation"] != 270.0 || video["display_width"] != 240.0 || doc["duration_seconds"] != 2.0 {
		t.Errorf("unexpected JSON: %s", r.stdout)
	}
}

func TestInspectUnknownDuration(t *testing.T) {
	ffprobe := fakeFFprobe(t, "raw_h264.json")
	input := testdata("video", "raw.h264")

	text := run(t, nil, "inspect", "--ffprobe", ffprobe, input)
	if text.err != nil {
		t.Fatal(text.err)
	}
	if !strings.Contains(text.stdout, "Duration:   unknown") {
		t.Errorf("stdout lacks unknown duration:\n%s", text.stdout)
	}
	if !strings.Contains(text.stderr, "warning: "+input+": duration not reported") {
		t.Errorf("stderr lacks the warning: %q", text.stderr)
	}

	js := run(t, nil, "inspect", "--json", "--ffprobe", ffprobe, input)
	if js.err != nil {
		t.Fatal(js.err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil {
		t.Fatalf("stdout is not pure JSON: %v", err)
	}
	if v, ok := doc["duration_seconds"]; !ok || v != nil {
		t.Errorf("duration_seconds = %v, want null", v)
	}
	if !strings.Contains(js.stderr, "warning:") {
		t.Errorf("stderr lacks the warning: %q", js.stderr)
	}
}

func TestInspectFailsWithoutVideo(t *testing.T) {
	r := run(t, nil, "inspect", "--ffprobe", fakeFFprobe(t, "cover_art.json"), testdata("video", "cover_art.mp3"))
	if r.err == nil || !strings.Contains(r.err.Error(), "no video stream found") {
		t.Fatalf("err = %v", r.err)
	}
	if r.stdout != "" {
		t.Errorf("unexpected stdout: %q", r.stdout)
	}
}

func TestInspectMissingFileFailsBeforeResolvingFFprobe(t *testing.T) {
	cache := t.TempDir()
	r := run(t, map[string]string{"MUSAFOZ_CACHE_DIR": cache}, "inspect", "missing.mp4")
	if r.err == nil || !strings.Contains(r.err.Error(), "inspect missing.mp4: file does not exist") {
		t.Fatalf("err = %v", r.err)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Errorf("cache was touched: %v", entries)
	}
}

func TestInspectUsesEnvironmentFFprobe(t *testing.T) {
	vars := map[string]string{"MUSAFOZ_FFPROBE": fakeFFprobe(t, "sample.json")}
	r := run(t, vars, "inspect", testdata("video", "sample.mp4"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	// The flag wins over the environment.
	vars["MUSAFOZ_FFPROBE"] = filepath.Join(t.TempDir(), "missing-ffprobe")
	r = run(t, vars, "inspect", "--ffprobe", fakeFFprobe(t, "sample.json"), testdata("video", "sample.mp4"))
	if r.err != nil {
		t.Fatal(r.err)
	}
}

func TestInspectUnsupportedPlatform(t *testing.T) {
	var stdout, stderr bytes.Buffer
	env := Env{Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" }, GOOS: "linux", GOARCH: "amd64"}
	cmd := NewRootCommand(env)
	cmd.SetArgs([]string{"inspect", testdata("video", "sample.mp4")})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no managed ffprobe build for linux/amd64") {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectRequiresOneArgument(t *testing.T) {
	if r := run(t, nil, "inspect"); r.err == nil {
		t.Fatal("expected an error")
	}
}
