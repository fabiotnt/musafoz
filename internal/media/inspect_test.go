package media

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ffprobe", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseSample(t *testing.T) {
	info, err := ParseProbeJSON(fixture(t, "sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	v := info.Video
	if v.Codec != "h264" || v.Width != 320 || v.Height != 240 || v.PixelFormat != "yuv420p" {
		t.Errorf("unexpected video stream: %+v", v)
	}
	if v.FrameRate == nil || *v.FrameRate != 10 {
		t.Errorf("frame rate = %v, want 10", v.FrameRate)
	}
	if v.FrameCount == nil || *v.FrameCount != 20 {
		t.Errorf("frame count = %v, want 20", v.FrameCount)
	}
	if v.Rotation != 0 || v.DisplayWidth != 320 || v.DisplayHeight != 240 {
		t.Errorf("unexpected rotation or display size: %+v", v)
	}
	if info.DurationSeconds == nil || *info.DurationSeconds != 2 || info.DurationSource != DurationFromContainer {
		t.Errorf("duration = %v from %q, want 2 from container", info.DurationSeconds, info.DurationSource)
	}
	if info.Audio == nil || info.Audio.Codec != "aac" || info.Audio.SampleRate != 44100 {
		t.Errorf("unexpected audio stream: %+v", info.Audio)
	}
	if info.Streams != (StreamCounts{Video: 1, Audio: 1}) {
		t.Errorf("stream counts = %+v", info.Streams)
	}
	if !strings.HasPrefix(info.Format, "mov,mp4") {
		t.Errorf("format = %q", info.Format)
	}
}

func TestParseRotated(t *testing.T) {
	info, err := ParseProbeJSON(fixture(t, "rotated.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The display matrix says 90 degrees counterclockwise.
	v := info.Video
	if v.Rotation != 270 || v.DisplayWidth != 240 || v.DisplayHeight != 320 {
		t.Errorf("rotation = %d, display = %dx%d; want 270, 240x320", v.Rotation, v.DisplayWidth, v.DisplayHeight)
	}
}

func TestParseRejectsInputsWithoutRealVideo(t *testing.T) {
	for _, name := range []string{"audio_only.json", "cover_art.json", "still.json"} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseProbeJSON(fixture(t, name))
			if !errors.Is(err, ErrNoVideoStream) {
				t.Fatalf("err = %v, want ErrNoVideoStream", err)
			}
		})
	}
}

func TestParseDurationMissingEverywhere(t *testing.T) {
	info, err := ParseProbeJSON(fixture(t, "raw_h264.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.DurationSeconds != nil || info.DurationSource != DurationUnknown {
		t.Errorf("duration = %v from %q, want nil from unknown", info.DurationSeconds, info.DurationSource)
	}
	out, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"duration_seconds":null`) {
		t.Errorf("JSON does not report a null duration: %s", out)
	}
}

func TestParseDurationFallsBackToVideoStream(t *testing.T) {
	for _, value := range []any{nil, "N/A"} {
		var doc map[string]any
		if err := json.Unmarshal(fixture(t, "sample.json"), &doc); err != nil {
			t.Fatal(err)
		}
		format := doc["format"].(map[string]any)
		if value == nil {
			delete(format, "duration")
		} else {
			format["duration"] = value
		}
		data, _ := json.Marshal(doc)
		info, err := ParseProbeJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		if info.DurationSeconds == nil || *info.DurationSeconds != 2 || info.DurationSource != DurationFromVideo {
			t.Errorf("container duration %v: got %v from %q, want 2 from video stream", value, info.DurationSeconds, info.DurationSource)
		}
	}
}

func TestParseInvalidJSON(t *testing.T) {
	if _, err := ParseProbeJSON([]byte("{")); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := ParseProbeJSON([]byte("{}")); err == nil {
		t.Fatal("expected an error for output without a format section")
	}
}

func TestValidateInput(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateInput(filepath.Join(dir, "missing.mp4")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing file: err = %v", err)
	}
	if err := ValidateInput(dir); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("directory: err = %v", err)
	}
}

// fakeFFprobe writes a shell script that prints a fixture, or fails like
// ffprobe does on unreadable input when fixture is empty.
func fakeFFprobe(t *testing.T, fixtureName string) string {
	t.Helper()
	script := "#!/bin/sh\necho 'corrupt.mp4: Invalid data found when processing input' >&2\nexit 1\n"
	if fixtureName != "" {
		abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "ffprobe", fixtureName))
		if err != nil {
			t.Fatal(err)
		}
		script = "#!/bin/sh\ncat '" + abs + "'\n"
	}
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectRunsFFprobe(t *testing.T) {
	video := filepath.Join("..", "..", "testdata", "video", "sample.mp4")
	info, err := Inspect(context.Background(), fakeFFprobe(t, "sample.json"), video)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != video || info.SizeBytes == 0 || info.Video.Codec != "h264" {
		t.Errorf("unexpected info: %+v", info)
	}
}

func TestInspectReportsFFprobeFailure(t *testing.T) {
	video := filepath.Join("..", "..", "testdata", "video", "corrupt.mp4")
	_, err := Inspect(context.Background(), fakeFFprobe(t, ""), video)
	var ie *InspectError
	if !errors.As(err, &ie) || ie.Path != video {
		t.Fatalf("err = %v, want InspectError for %s", err, video)
	}
	if !strings.Contains(err.Error(), "ffprobe could not read the file: corrupt.mp4: Invalid data") {
		t.Errorf("err = %v", err)
	}
}

func TestInspectNoVideoNamesInput(t *testing.T) {
	video := filepath.Join("..", "..", "testdata", "video", "audio_only.m4a")
	_, err := Inspect(context.Background(), fakeFFprobe(t, "audio_only.json"), video)
	if !errors.Is(err, ErrNoVideoStream) || !strings.Contains(err.Error(), video) {
		t.Fatalf("err = %v", err)
	}
}
