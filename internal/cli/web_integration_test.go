//go:build integration

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabiotnt/musafoz/internal/media"
)

// TestIntegrationWebVideo converts videos with the managed FFmpeg: a
// rotated clip, a sample with audio, and, when the build has libx265, a
// 4K HLG clip with QuickTime date and location tags.
func TestIntegrationWebVideo(t *testing.T) {
	ffmpeg, ffprobe := managedTools(t)
	cache := testCache(t)
	dir := t.TempDir()
	sources := []string{testdata("video", "rotated.mp4"), testdata("video", "sample.mp4")}
	hlg := filepath.Join(dir, "hlg.mov")
	gen := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=3840x2160:rate=30:duration=1",
		"-vf", "format=yuv420p10le,setparams=color_trc=arib-std-b67:color_primaries=bt2020:colorspace=bt2020nc",
		"-c:v", "libx265", "-x265-params", "log-level=error",
		"-metadata", "com.apple.quicktime.creationdate=2025-03-26T15:07:12-0300",
		"-metadata", "com.apple.quicktime.location.ISO6709=+23.5505-046.6333/",
		"-movflags", "use_metadata_tags", hlg)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Logf("no HLG clip (libx265 missing?): %v %s", err, out)
	} else {
		sources = append(sources, hlg)
	}
	var list strings.Builder
	for _, s := range sources {
		abs, _ := filepath.Abs(s)
		list.WriteString(abs + "\n")
	}
	listPath := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, meta := filepath.Join(dir, "web"), filepath.Join(dir, "meta")
	r := runReal(t, cache, "web", "batch", listPath, "--output", out, "--metadata", meta, "--frames", "9", "--jsonl")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
	if strings.Contains(r.stdout, "ISO6709") || strings.Contains(r.stdout, "23.5505") {
		t.Error("the report has the location")
	}
	for i, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		var rec struct {
			Status, Kind, UUID string
			Files              map[string]string
			Width, Height      int
			SourceWidth        int     `json:"source_width"`
			SourceHeight       int     `json:"source_height"`
			Date               *string `json:"date"`
			HDR                bool    `json:"hdr_tone_mapped"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(sources[i])
		if rec.Status != "ok" || rec.Kind != "video" || len(rec.Files) != 4 {
			t.Errorf("%s: %s", name, line)
			continue
		}
		for _, p := range rec.Files {
			if fi, err := os.Stat(p); err != nil || fi.Size() == 0 || !strings.HasPrefix(filepath.Base(p), rec.UUID) {
				t.Errorf("%s: output %s: %v", name, p, err)
			}
		}
		raw, err := exec.Command(ffprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", rec.Files["video"]).Output()
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Streams []struct {
				CodecType      string `json:"codec_type"`
				CodecName      string `json:"codec_name"`
				Profile        string `json:"profile"`
				PixFmt         string `json:"pix_fmt"`
				Width, Height  int
				ColorTransfer  string `json:"color_transfer"`
				ColorPrimaries string `json:"color_primaries"`
			}
			Format struct {
				BitRate string            `json:"bit_rate"`
				Tags    map[string]string `json:"tags"`
			}
		}
		json.Unmarshal(raw, &doc)
		v := doc.Streams[0]
		if v.CodecName != "h264" || v.Profile != "High" || v.PixFmt != "yuv420p" || v.Width != rec.Width || v.Height != rec.Height ||
			v.ColorTransfer != "bt709" || v.ColorPrimaries != "bt709" {
			t.Errorf("%s: video stream %+v", name, v)
		}
		for k := range doc.Format.Tags {
			if strings.Contains(k, "quicktime") || k == "creation_time" || k == "location" {
				t.Errorf("%s: tag %s kept", name, k)
			}
		}
		if ok, err := media.IsFastStart(rec.Files["video"]); err != nil || !ok {
			t.Errorf("%s: faststart %v %v", name, ok, err)
		}
		if max(rec.Width, rec.Height) > 1920 || min(rec.Width, rec.Height) > 1080 {
			t.Errorf("%s: %dx%d is over 1080p", name, rec.Width, rec.Height)
		}
		if name == "hlg.mov" {
			if !rec.HDR || rec.Width != 1920 || rec.Height != 1080 || rec.Date == nil || *rec.Date != "2025-03-26T15:07:12-03:00" {
				t.Errorf("hlg: %s", line)
			}
			data, _ := os.ReadFile(filepath.Join(meta, rec.UUID+".json"))
			if !strings.Contains(string(data), "ISO6709") {
				t.Error("hlg: the metadata file lost the location")
			}
		}
		if name == "rotated.mp4" && (rec.Width != rec.SourceWidth || rec.Height != rec.SourceHeight || rec.Width > rec.Height) {
			t.Errorf("rotated: %s", line)
		}
	}
}
