//go:build integration || parity

package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/fabiotnt/musafoz/internal/tools"
)

// testCache returns the tool cache used by tests that run the real tools:
// MUSAFOZ_TEST_CACHE_DIR when set, otherwise the regular musafoz cache. The
// managed builds are therefore downloaded at most once per machine, and
// every run still verifies them by size and SHA-256 before use. Only
// TestIntegrationDownload, enabled with MUSAFOZ_TEST_DOWNLOAD=1, starts
// from an empty cache.
func testCache(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("MUSAFOZ_TEST_CACHE_DIR"); dir != "" {
		return dir
	}
	root, err := tools.DefaultCacheRoot("")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runReal(t *testing.T, cache string, args ...string) result {
	t.Helper()
	vars := map[string]string{"MUSAFOZ_CACHE_DIR": cache}
	var stdout, stderr strings.Builder
	env := Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(k string) string { return vars[k] },
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}
	cmd := NewRootCommand(env)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return result{stdout.String(), stderr.String(), err}
}

type streamInfo struct {
	CodecName    string `json:"codec_name"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	PixFmt       string `json:"pix_fmt"`
	AvgFrameRate string `json:"avg_frame_rate"`
	NbFrames     string `json:"nb_frames"`
	SAR          string `json:"sample_aspect_ratio"`
	DAR          string `json:"display_aspect_ratio"`
}

func probeFirstStream(t *testing.T, ffprobe, p string) streamInfo {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-print_format", "json", "-show_streams", p).Output()
	if err != nil {
		t.Fatalf("probe %s: %v", p, err)
	}
	var doc struct {
		Streams []streamInfo `json:"streams"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.Streams) == 0 {
		t.Fatalf("probe %s: %v", p, err)
	}
	return doc.Streams[0]
}

func streamHash(t *testing.T, ffmpeg, p string) string {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", p,
		"-map", "0", "-c", "copy", "-f", "streamhash", "-hash", "sha256", "-").Output()
	if err != nil {
		t.Fatalf("streamhash %s: %v", p, err)
	}
	return string(out)
}
