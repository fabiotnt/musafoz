package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSeekFFmpeg writes a frame when the seek time is at most FAKE_LAST_GOOD and
// fails otherwise, as FFmpeg does when a seek lands past the last frame it can
// decode. Every seek is logged.
const fakeSeekFFmpeg = `#!/bin/sh
prev=""; at=""; out=""
for a in "$@"; do
  if [ "$prev" = "-ss" ]; then at="$a"; fi
  prev="$a"; out="$a"
done
echo "$at" >> "$FAKE_LOG"
if awk -v t="$at" -v m="$FAKE_LAST_GOOD" 'BEGIN { exit !(t <= m) }'; then
  printf 'jpeg' > "$out"
  exit 0
fi
echo "Output file is empty, nothing was encoded" >&2
exit 1
`

func seekFixture(t *testing.T, lastGood string) (FrameOptions, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte(fakeSeekFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	font := filepath.Join(work, "font.ttf")
	os.WriteFile(font, []byte("font"), 0o644)
	log := filepath.Join(dir, "seeks.log")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_LAST_GOOD", lastGood)
	return FrameOptions{FFmpeg: ffmpeg, Input: filepath.Join(dir, "in.mp4"), WorkDir: work, FontFile: font, Concurrency: 1}, log
}

func TestExtractFramesRetriesNearTheEnd(t *testing.T) {
	o, log := seekFixture(t, "2.5")
	o.Times, o.Last = []float64{0, 1.5, 2.9, 2.96}, 3.0
	if err := ExtractFrames(context.Background(), o); err != nil {
		t.Fatalf("frames near the end were not retried: %v", err)
	}
	for i := range o.Times {
		if _, err := os.Stat(filepath.Join(o.WorkDir, frameName(i))); err != nil {
			t.Errorf("frame %d missing", i+1)
		}
	}
	seeks, _ := os.ReadFile(log)
	// 2.9 is retried at 2.86, 2.8, 2.65, then 2.4 decodes.
	if !strings.Contains(string(seeks), "2.400") {
		t.Errorf("seeks:\n%s", seeks)
	}
}

func TestExtractFramesFailsAwayFromTheEnd(t *testing.T) {
	o, log := seekFixture(t, "1.0")
	o.Times, o.Last = []float64{0.5, 1.5}, 3.0
	err := ExtractFrames(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "frame 2 at") || !strings.Contains(err.Error(), "no frame decoded") {
		t.Fatalf("err = %v", err)
	}
	seeks, _ := os.ReadFile(log)
	if n := strings.Count(strings.TrimSpace(string(seeks)), "\n") + 1; n != 2 {
		t.Errorf("a frame away from the end was retried:\n%s", seeks)
	}
	// Near the end, a frame that decodes at no earlier time still fails.
	o, _ = seekFixture(t, "0.1")
	o.Times, o.Last = []float64{2.9}, 3.0
	if err := ExtractFrames(context.Background(), o); err == nil || !strings.Contains(err.Error(), "frame 1 at 00:00:02") {
		t.Errorf("err = %v", err)
	}
	// Without Last there is no retry.
	o, _ = seekFixture(t, "2.5")
	o.Times = []float64{2.9}
	if err := ExtractFrames(context.Background(), o); err == nil {
		t.Error("retried without Last")
	}
}

func frameName(i int) string { return fmt.Sprintf(FramePattern, i) }
