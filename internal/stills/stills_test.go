package stills

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fabiotnt/musafoz/internal/fetch"
	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/testimages"
)

var (
	red  = color.NRGBA{255, 0, 0, 255}
	blue = color.NRGBA{0, 0, 255, 255}
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p string, data []byte) string {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, data, 0o644))
	return p
}

// phoneJPEG is a portrait phone photo stored as 64x32 landscape pixels
// with orientation 6 and GPS, camera, and maker note tags.
func phoneJPEG() []byte {
	return testimages.JPEG(testimages.Halves(64, 32, red, blue), testimages.PhoneEXIF(binary.BigEndian, 6))
}

func newRunner(t *testing.T, out Output) *Runner {
	t.Helper()
	f, err := fetch.New(10<<20, time.Minute, nil)
	must(t, err)
	t.Cleanup(func() { f.Close() })
	if out.Format == "" {
		out.Format = imaging.OutputJPEG
	}
	if out.Mode == "" {
		out.Mode = imaging.ModeFit
	}
	return &Runner{Output: out, Fetcher: f}
}

func decode(t *testing.T, p string) (*imaging.Image, []byte) {
	t.Helper()
	data, err := os.ReadFile(p)
	must(t, err)
	img, err := imaging.Decode(data)
	must(t, err)
	return img, data
}

// thumbPath returns the output path of a Thumb call.
func thumbPath(res *ThumbResult, err error) (string, error) {
	if res == nil {
		return "", err
	}
	return res.Path, err
}

func size(img *imaging.Image) string {
	b := img.Pixels.Bounds()
	return fmt.Sprintf("%dx%d", b.Dx(), b.Dy())
}

func TestThumbOrientsAndStripsMetadata(t *testing.T) {
	dir := t.TempDir()
	src := write(t, filepath.Join(dir, "in", "phone photo.jpg"), phoneJPEG())
	before, _ := os.ReadFile(src)
	cases := []struct {
		mode, format, want string
	}{
		{imaging.ModeFit, imaging.OutputJPEG, "16x32"},
		{imaging.ModeFill, imaging.OutputPNG, "32x32"},
	}
	for _, c := range cases {
		out := filepath.Join(dir, "out-"+c.mode)
		r := newRunner(t, Output{Dir: out, Suffix: DefaultThumbSuffix, Format: c.format, Mode: c.mode, Size: &imaging.Size{Width: 32, Height: 32}})
		p, err := thumbPath(r.Thumb(context.Background(), src, "test"))
		must(t, err)
		if want := filepath.Join(out, "phone photo_thumb"+imaging.Extension(c.format)); p != want {
			t.Errorf("output %s, want %s", p, want)
		}
		img, data := decode(t, p)
		if size(img) != c.want || img.Format != map[string]string{"jpeg": "jpeg", "png": "png"}[c.format] {
			t.Errorf("%s: %s %s, want %s", c.mode, img.Format, size(img), c.want)
		}
		if img.EXIF != nil || bytes.Contains(data, []byte("Exif")) || bytes.Contains(data, []byte("iPhone")) {
			t.Errorf("%s: output keeps metadata", c.mode)
		}
		// The top of the portrait picture is red.
		if r, _, b, _ := img.Pixels.At(8, 2).RGBA(); r>>8 < 200 || b>>8 > 60 {
			t.Errorf("%s: top pixel %v, want red: orientation not applied", c.mode, img.Pixels.At(8, 2))
		}
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(before, after) {
		t.Error("the source changed")
	}
}

func TestThumbTransparencyToJPEGIsWhite(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := range 40 {
		for x := range 20 {
			img.Set(x, y, red)
		}
	}
	src := write(t, filepath.Join(dir, "logo.png"), testimages.PNG(img, nil))
	for _, format := range []string{imaging.OutputJPEG, imaging.OutputPNG} {
		r := newRunner(t, Output{Dir: dir, Suffix: "_" + format, Format: format, Size: &imaging.Size{Width: 20, Height: 20}})
		p, err := thumbPath(r.Thumb(context.Background(), src, "test"))
		must(t, err)
		out, _ := decode(t, p)
		_, g, _, a := out.Pixels.At(15, 10).RGBA()
		switch format {
		case imaging.OutputJPEG:
			if g>>8 < 240 || a>>8 != 255 {
				t.Errorf("JPEG transparent area = %v, want white", out.Pixels.At(15, 10))
			}
		case imaging.OutputPNG:
			if a != 0 {
				t.Errorf("PNG transparent area = %v, want transparent", out.Pixels.At(15, 10))
			}
		}
	}
}

func TestThumbNamesAndConflicts(t *testing.T) {
	dir := t.TempDir()
	src := write(t, filepath.Join(dir, "src", "a.jpg"), phoneJPEG())
	sz := &imaging.Size{Width: 8, Height: 8}
	cases := []struct {
		out  Output
		want string
	}{
		{Output{Dir: filepath.Join(dir, "o"), Suffix: DefaultThumbSuffix}, filepath.Join(dir, "o", "a_thumb.jpg")},
		{Output{Dir: filepath.Join(dir, "o"), Suffix: "-small"}, filepath.Join(dir, "o", "a-small.jpg")},
		{Output{Dir: filepath.Join(dir, "o"), Prefix: "t_"}, filepath.Join(dir, "o", "t_a.jpg")},
		{Output{Alongside: true, Suffix: DefaultThumbSuffix, Format: imaging.OutputPNG}, filepath.Join(dir, "src", "a_thumb.png")},
	}
	for _, c := range cases {
		c.out.Size = sz
		p, err := thumbPath(newRunner(t, c.out).Thumb(context.Background(), src, "test"))
		if err != nil || p != c.want {
			t.Errorf("%+v: %s, %v; want %s", c.out, p, err, c.want)
		}
	}
	// An existing output is kept without --overwrite and replaced with it.
	existing := filepath.Join(dir, "o", "a_thumb.jpg")
	must(t, os.WriteFile(existing, []byte("keep"), 0o644))
	_, err := newRunner(t, Output{Dir: filepath.Join(dir, "o"), Suffix: DefaultThumbSuffix, Size: sz}).Thumb(context.Background(), src, "test")
	var ce *ConflictError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "--overwrite") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Error("existing output changed")
	}
	if _, err := newRunner(t, Output{Dir: filepath.Join(dir, "o"), Suffix: DefaultThumbSuffix, Size: sz, Overwrite: true}).Thumb(context.Background(), src, "test"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(existing); string(data) == "keep" {
		t.Error("--overwrite did not replace the output")
	}
	// An output that would replace the source is refused even with
	// --overwrite.
	jpg := write(t, filepath.Join(dir, "same", "b.jpg"), phoneJPEG())
	_, err = newRunner(t, Output{Alongside: true, Size: sz, Overwrite: true}).Thumb(context.Background(), jpg, "test")
	if err == nil || !strings.Contains(err.Error(), "would replace the source") {
		t.Errorf("err = %v", err)
	}
	// No temporary files are left.
	for _, d := range []string{filepath.Join(dir, "o"), filepath.Join(dir, "src")} {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				t.Errorf("temporary file left: %s", e.Name())
			}
		}
	}
}

func TestThumbURL(t *testing.T) {
	var hits atomic.Int32
	data := phoneJPEG()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/missing.jpg" {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	r := newRunner(t, Output{Dir: dir, Suffix: DefaultThumbSuffix, Size: &imaging.Size{Width: 16, Height: 16}})
	p, err := thumbPath(r.Thumb(context.Background(), srv.URL+"/photos/beach%20day.jpg?sig=SECRET", "test"))
	must(t, err)
	if p != filepath.Join(dir, "beach day_thumb.jpg") {
		t.Errorf("output %s", p)
	}
	_, err = r.Thumb(context.Background(), srv.URL+"/missing.jpg?sig=SECRET", "test")
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v", err)
	}
	ra := newRunner(t, Output{Alongside: true, Size: &imaging.Size{Width: 16, Height: 16}})
	if _, err := ra.Thumb(context.Background(), srv.URL+"/x.jpg", "test"); err == nil || !strings.Contains(err.Error(), "--alongside works only with local files") {
		t.Errorf("alongside URL: %v", err)
	}
}

func TestThumbBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(phoneJPEG())
	}))
	defer srv.Close()
	dir := t.TempDir()
	lists := filepath.Join(dir, "lists")
	write(t, filepath.Join(lists, "my photos", "beach day.jpg"), phoneJPEG())
	write(t, filepath.Join(lists, "a", "dup.jpg"), phoneJPEG())
	write(t, filepath.Join(lists, "b", "dup.jpg"), phoneJPEG())
	write(t, filepath.Join(lists, "notes.txt"), []byte("not an image"))
	abs := write(t, filepath.Join(dir, "elsewhere", "abs.png"), testimages.PNG(testimages.Halves(10, 10, red, blue), nil))
	list := write(t, filepath.Join(lists, "sources.txt"), []byte("\xEF\xBB\xBF"+
		"my photos/beach day.jpg\r\n"+
		"\n"+
		"   \n"+
		"  "+abs+"  \n"+
		srv.URL+"/remote/pic.jpg?token=SECRET\n"+
		"missing.jpg\n"+
		"notes.txt\n"+
		"a/dup.jpg\n"+
		"b/dup.jpg\n"+
		"x.jpg\textra\n"))
	out := filepath.Join(dir, "thumbs")
	r := newRunner(t, Output{Dir: out, Suffix: DefaultThumbSuffix, Size: &imaging.Size{Width: 32, Height: 24}})
	var written []string
	var failed []*LineError
	err := r.ThumbBatch(context.Background(), list, BatchHooks{
		OnOutput: func(line int, p string) { written = append(written, fmt.Sprintf("%d %s", line, filepath.Base(p))) },
		OnError:  func(e *LineError) { failed = append(failed, e) },
	})
	var be *BatchError
	if !errors.As(err, &be) || be.Failed != 4 || be.Total != 8 {
		t.Fatalf("err = %v", err)
	}
	wantWritten := []string{"1 beach day_thumb.jpg", "4 abs_thumb.jpg", "5 pic_thumb.jpg", "8 dup_thumb.jpg"}
	if !slices.Equal(written, wantWritten) {
		t.Errorf("written %v, want %v", written, wantWritten)
	}
	wantErr := map[int]string{6: "file does not exist", 7: "not a supported image", 9: "also written by line 8", 10: "has a TAB"}
	for _, e := range failed {
		if w, ok := wantErr[e.Line]; !ok || !strings.Contains(e.Error(), w) {
			t.Errorf("unexpected line error: %v", e)
		}
		if strings.Contains(e.Error(), "SECRET") {
			t.Errorf("error shows a URL query: %v", e)
		}
	}
	if len(failed) != 4 {
		t.Errorf("%d line errors: %v", len(failed), failed)
	}
	img, _ := decode(t, filepath.Join(out, "beach day_thumb.jpg"))
	if size(img) != "12x24" {
		t.Errorf("thumbnail %s, want 12x24", size(img))
	}
}

func TestReadListMissing(t *testing.T) {
	r := newRunner(t, Output{Size: &imaging.Size{Width: 1, Height: 1}})
	if err := r.ThumbBatch(context.Background(), filepath.Join(t.TempDir(), "none.txt"), BatchHooks{}); err == nil {
		t.Error("missing list accepted")
	}
	empty := write(t, filepath.Join(t.TempDir(), "empty.txt"), []byte("\n \n"))
	if err := r.ThumbBatch(context.Background(), empty, BatchHooks{}); err == nil || !strings.Contains(err.Error(), "lists no sources") {
		t.Errorf("empty list: %v", err)
	}
}

// Frame extraction tests use shell scripts as fake FFprobe and FFmpeg.

const fakeFFprobe = `#!/bin/sh
if [ "$1" = "-version" ]; then echo "ffprobe version 9.1-tessus"; exit 0; fi
cat "SAMPLE_JSON"
`

// fakeFFmpeg writes FAKE_PNG for still frames and logs each seek time.
const fakeFFmpeg = `#!/bin/sh
if [ "$1" = "-version" ]; then echo "ffmpeg version 9.1-tessus"; exit 0; fi
prev=""; at=""
for a in "$@"; do
  if [ "$prev" = "-ss" ]; then at="$a"; fi
  prev="$a"; out="$a"
done
echo "$at" >> "$FAKE_LOG"
cp "$FAKE_PNG" "$out"
`

type frameFixture struct {
	dir     string
	log     string
	probes  atomic.Int32
	ffprobe string
	ffmpeg  string
}

func newFrameFixture(t *testing.T) *frameFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tools are shell scripts")
	}
	tools := t.TempDir()
	f := &frameFixture{dir: t.TempDir(), log: filepath.Join(tools, "log")}
	sample, err := filepath.Abs(filepath.Join("..", "..", "testdata", "ffprobe", "sample.json"))
	must(t, err)
	f.ffprobe = write(t, filepath.Join(tools, "ffprobe"), []byte(strings.Replace(fakeFFprobe, "SAMPLE_JSON", sample, 1)))
	f.ffmpeg = write(t, filepath.Join(tools, "ffmpeg"), []byte(fakeFFmpeg))
	must(t, os.Chmod(f.ffprobe, 0o755))
	must(t, os.Chmod(f.ffmpeg, 0o755))
	var buf bytes.Buffer
	must(t, png.Encode(&buf, testimages.Halves(80, 60, red, blue)))
	t.Setenv("FAKE_PNG", write(t, filepath.Join(tools, "frame.png"), buf.Bytes()))
	t.Setenv("FAKE_LOG", f.log)
	return f
}

func (f *frameFixture) runner(t *testing.T, out Output) *Runner {
	r := newRunner(t, out)
	r.ResolveFFprobe = func(context.Context) (string, error) { f.probes.Add(1); return f.ffprobe, nil }
	r.ResolveFFmpeg = func(context.Context) (string, error) { return f.ffmpeg, nil }
	return r
}

func (f *frameFixture) video(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "video", "sample.mp4"))
	must(t, err)
	return write(t, filepath.Join(f.dir, name), data)
}

func (f *frameFixture) seeks(t *testing.T) []string {
	data, _ := os.ReadFile(f.log)
	return strings.Fields(string(data))
}

func TestFramesAtTimes(t *testing.T) {
	f := newFrameFixture(t)
	in := f.video(t, "my clip.mp4")
	out := filepath.Join(f.dir, "frames")
	r := f.runner(t, Output{Dir: out, Size: &imaging.Size{Width: 40, Height: 40}, Mode: imaging.ModeFill, Suffix: "_x"})
	frames, err := r.Frames(context.Background(), in, FrameRequest{Times: []float64{0, 1.25, 1.9}}, "test")
	must(t, err)
	var names []string
	for _, fr := range frames {
		names = append(names, filepath.Base(fr.Path))
		img, _ := decode(t, fr.Path)
		if size(img) != "40x40" {
			t.Errorf("%s is %s, want 40x40", fr.Path, size(img))
		}
	}
	want := []string{"my clip_00-00-00_x.jpg", "my clip_00-00-01-250_x.jpg", "my clip_00-00-01-900_x.jpg"}
	if !slices.Equal(names, want) {
		t.Errorf("names %v, want %v", names, want)
	}
	if got := f.seeks(t); !slices.Equal(got, []string{"0.000", "1.250", "1.900"}) {
		t.Errorf("seeks %v", got)
	}
	// Without a size, the frame keeps the extracted (displayed) size.
	r = f.runner(t, Output{Dir: out, Prefix: "full-"})
	frames, err = r.Frames(context.Background(), in, FrameRequest{Times: []float64{1}}, "test")
	must(t, err)
	if img, _ := decode(t, frames[0].Path); size(img) != "80x60" || filepath.Base(frames[0].Path) != "full-my clip_00-00-01.jpg" {
		t.Errorf("%s is %s", frames[0].Path, size(img))
	}
}

func TestFramesRejectsBadTimesFirst(t *testing.T) {
	f := newFrameFixture(t)
	in := f.video(t, "clip.mp4")
	existing := write(t, filepath.Join(f.dir, "out", "clip_00-00-01.jpg"), []byte("keep"))
	r := f.runner(t, Output{Dir: filepath.Join(f.dir, "out")})
	_, err := r.Frames(context.Background(), in, FrameRequest{Times: []float64{0.5, 1.95}}, "test")
	if err == nil || !strings.Contains(err.Error(), "time 00:00:01.950 is outside the video") {
		t.Errorf("err = %v", err)
	}
	_, err = r.Frames(context.Background(), in, FrameRequest{Times: []float64{0.5, 1}}, "test")
	var ce *ConflictError
	if !errors.As(err, &ce) || len(ce.Paths) != 1 || ce.Paths[0] != existing {
		t.Errorf("err = %v", err)
	}
	if len(f.seeks(t)) != 0 {
		t.Error("frames were extracted before the checks")
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Error("existing output changed")
	}
}

func TestFramesRandom(t *testing.T) {
	f := newFrameFixture(t)
	in := f.video(t, "clip.mp4")
	// sample.json: 2 seconds at 10 fps, so random times are in [0, 1.9].
	r := f.runner(t, Output{Dir: filepath.Join(f.dir, "a")})
	frames, err := r.Frames(context.Background(), in, FrameRequest{Random: 50}, "test")
	must(t, err)
	// 50 draws are more than the 20 frames of the clip: every draw is
	// written anyway, under a distinct name that holds its time.
	if len(frames) != 50 {
		t.Fatalf("%d frames, want 50", len(frames))
	}
	names := map[string]bool{}
	for _, fr := range frames {
		if fr.AtSeconds < 0 || fr.AtSeconds > 1.9 {
			t.Errorf("time %v outside [0, 1.9]", fr.AtSeconds)
		}
		base := filepath.Base(fr.Path)
		if names[base] || !strings.HasPrefix(base, "clip_"+NameTime(fr.AtSeconds)) {
			t.Errorf("bad or repeated name %s for %v", base, fr.AtSeconds)
		}
		names[base] = true
		if _, err := os.Stat(fr.Path); err != nil {
			t.Error(err)
		}
	}
	if len(f.seeks(t)) != 50 {
		t.Errorf("%d extractions", len(f.seeks(t)))
	}
}

// A repeated draw selects the same time: the outputs get indexed names,
// so the requested number of files is written.
func TestFramesRandomRepeatedDraw(t *testing.T) {
	f := newFrameFixture(t)
	in := f.video(t, "clip.mp4")
	out := filepath.Join(f.dir, "out")
	r := f.runner(t, Output{Dir: out, Suffix: "_s"})
	r.Random = func() float64 { return 0.5 }
	frames, err := r.Frames(context.Background(), in, FrameRequest{Random: 4}, "first")
	must(t, err)
	var got []string
	for _, fr := range frames {
		got = append(got, filepath.Base(fr.Path))
		if fr.AtSeconds != 0.95 {
			t.Errorf("time %v, want 0.95", fr.AtSeconds)
		}
	}
	want := []string{"clip_00-00-00-950_s.jpg", "clip_00-00-00-950-2_s.jpg", "clip_00-00-00-950-3_s.jpg", "clip_00-00-00-950-4_s.jpg"}
	if !slices.Equal(got, want) {
		t.Errorf("names %v, want %v", got, want)
	}
	// A later request in the same run continues the index.
	more, err := r.Frames(context.Background(), in, FrameRequest{Random: 1}, "second")
	if err != nil || filepath.Base(more[0].Path) != "clip_00-00-00-950-5_s.jpg" {
		t.Errorf("next: %v %v", more, err)
	}
	// A new run keeps the existing files: the plain name is a conflict.
	r2 := f.runner(t, Output{Dir: out, Suffix: "_s"})
	r2.Random = func() float64 { return 0.5 }
	before, _ := os.ReadFile(frames[0].Path)
	_, err = r2.Frames(context.Background(), in, FrameRequest{Random: 2}, "new run")
	var ce *ConflictError
	if !errors.As(err, &ce) || len(ce.Paths) != 2 {
		t.Errorf("new run: %v", err)
	}
	if after, _ := os.ReadFile(frames[0].Path); !bytes.Equal(before, after) {
		t.Error("an existing frame changed")
	}
	// Given times are not indexed: a repeated time is a conflict.
	_, err = r.Frames(context.Background(), in, FrameRequest{Times: []float64{1, 1}}, "times")
	if err == nil || !strings.Contains(err.Error(), "written twice") {
		t.Errorf("repeated time: %v", err)
	}
}

func TestFrameBatch(t *testing.T) {
	f := newFrameFixture(t)
	var hits atomic.Int32
	video, err := os.ReadFile(filepath.Join("..", "..", "testdata", "video", "sample.mp4"))
	must(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(video)
	}))
	defer srv.Close()
	f.video(t, "videos/holiday clip.mp4")
	url := srv.URL + "/media/remote.mp4?token=SECRET"
	list := write(t, filepath.Join(f.dir, "frames.tsv"), []byte(
		"videos/holiday clip.mp4\t00:00:01\n"+
			"videos/holiday clip.mp4\t\n"+
			"videos/holiday clip.mp4\n"+
			"\n"+
			url+"\t00:00:00.500\n"+
			url+"\n"+
			"videos/holiday clip.mp4\t1:2\n"+
			"videos/holiday clip.mp4\t00:00:05\n"+
			"videos/holiday clip.mp4\t00:00:01\n"+
			"missing.mp4\t00:00:01\n"+
			"a\tb\tc\n"))
	out := filepath.Join(f.dir, "out")
	r := f.runner(t, Output{Dir: out})
	r.Random = func() float64 { return 0.5 }
	var written []string
	var failed []*LineError
	err = r.FrameBatch(context.Background(), list, BatchHooks{
		OnOutput: func(line int, p string) { written = append(written, fmt.Sprintf("%d %s", line, filepath.Base(p))) },
		OnError:  func(e *LineError) { failed = append(failed, e) },
	})
	var be *BatchError
	if !errors.As(err, &be) || be.Failed != 5 || be.Total != 10 {
		t.Fatalf("err = %v", err)
	}
	// The random lines draw the same time; the second gets an index.
	want := []string{"1 holiday clip_00-00-01.jpg", "2 holiday clip_00-00-00-950.jpg", "3 holiday clip_00-00-00-950-2.jpg",
		"5 remote_00-00-00-500.jpg", "6 remote_00-00-00-950.jpg"}
	if !slices.Equal(written, want) {
		t.Errorf("written %v, want %v", written, want)
	}
	wantErr := map[int]string{7: "not in HH:MM:SS", 8: "outside the video", 9: "also written by line 1", 10: "file does not exist", 11: "3 columns"}
	for _, e := range failed {
		if w, ok := wantErr[e.Line]; !ok || !strings.Contains(e.Error(), w) {
			t.Errorf("unexpected line error: %v", e)
		}
		if strings.Contains(e.Error(), "SECRET") {
			t.Errorf("error shows a URL query: %v", e)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("remote video downloaded %d times, want once", hits.Load())
	}
	if f.probes.Load() != 1 {
		t.Errorf("ffprobe resolved %d times", f.probes.Load())
	}
}

func heic(t *testing.T, o testimages.HEIFOptions) []byte {
	t.Helper()
	hevc, err := os.ReadFile(filepath.Join("..", "..", "testdata", "image", "quadrants-64x48.hevc"))
	must(t, err)
	o.HEVC, o.Width, o.Height = hevc, 64, 48
	return testimages.HEIF(o)
}

// HEIC thumbnails are decoded with FFmpeg; the fake FFmpeg returns a PNG of
// a chosen size, so these tests cover the flow and the size check.
func TestThumbHEIC(t *testing.T) {
	f := newFrameFixture(t)
	data := heic(t, testimages.HEIFOptions{Rotation: 270, Mirror: -1, EXIF: testimages.PhoneEXIF(binary.BigEndian, 6)})
	// The content decides, not the extension.
	src := write(t, filepath.Join(f.dir, "photo.jpg"), data)
	// FFmpeg returns the stored 64x48 item; musafoz rotates it.
	var buf bytes.Buffer
	must(t, png.Encode(&buf, testimages.Halves(64, 48, red, blue)))
	t.Setenv("FAKE_PNG", write(t, filepath.Join(f.dir, "decoded.png"), buf.Bytes()))
	out := filepath.Join(f.dir, "out")
	r := f.runner(t, Output{Dir: out, Suffix: DefaultThumbSuffix, Size: &imaging.Size{Width: 24, Height: 24}})
	res, err := r.Thumb(context.Background(), src, "test")
	must(t, err)
	p := res.Path
	// The size and date come from the HEIF container and its EXIF item.
	if res.SourceWidth != 48 || res.SourceHeight != 64 || res.Width != 18 || res.Height != 24 ||
		res.Date == nil || res.Date.Value != "2026-10-31T17:51:24-03:00" || res.Date.Source != "DateTimeOriginal" {
		t.Errorf("result %+v, date %+v", res, res.Date)
	}
	img, raw := decode(t, p)
	if size(img) != "18x24" || img.EXIF != nil || bytes.Contains(raw, []byte("Exif")) || bytes.Contains(raw, []byte("iPhone")) {
		t.Errorf("thumbnail %s, EXIF %v", size(img), img.EXIF)
	}
	if after, _ := os.ReadFile(src); !bytes.Equal(after, data) {
		t.Error("the source changed")
	}

	// A decoder that returns another size, as one that rotates the item
	// itself would, is reported instead of written.
	buf.Reset()
	must(t, png.Encode(&buf, testimages.Halves(48, 64, red, blue)))
	t.Setenv("FAKE_PNG", write(t, filepath.Join(f.dir, "decoded.png"), buf.Bytes()))
	r = f.runner(t, Output{Dir: out, Suffix: "_b", Size: &imaging.Size{Width: 24, Height: 24}})
	_, err = r.Thumb(context.Background(), src, "test")
	if err == nil || !strings.Contains(err.Error(), "FFmpeg decoded a 48x64 image, but the HEIC item is 64x48") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "photo_b.jpg")); err == nil {
		t.Error("a thumbnail was written after the size check failed")
	}
}

// Thumb reports the displayed size of the source, the size of the output,
// and the date, from the same read that gives the pixels.
func TestThumbResult(t *testing.T) {
	dir := t.TempDir()
	b := testimages.New(binary.BigEndian)
	b.IFD0 = []testimages.Entry{b.ASCII(0x0132, "2025:01:19 10:15:32")}
	edited := testimages.JPEG(testimages.Halves(40, 20, red, blue), b.Build())
	cases := []struct {
		name string
		data []byte
		mode string
		want string
		date *imaging.Date
	}{
		{"phone.jpg", phoneJPEG(), imaging.ModeFit, "32x64 16x32", &imaging.Date{Value: "2026-10-31T17:51:24-03:00", Source: "DateTimeOriginal"}},
		{"edited.jpg", edited, imaging.ModeFill, "40x20 32x32", &imaging.Date{Value: "2025-01-19T10:15:32", Source: "DateTime"}},
		{"screen.png", testimages.PNG(testimages.Halves(30, 60, red, blue), nil), imaging.ModeFit, "30x60 16x32", nil},
	}
	for _, c := range cases {
		src := write(t, filepath.Join(dir, "in", c.name), c.data)
		r := newRunner(t, Output{Dir: filepath.Join(dir, "out"), Suffix: DefaultThumbSuffix, Mode: c.mode, Size: &imaging.Size{Width: 32, Height: 32}})
		res, err := r.Thumb(context.Background(), src, "test")
		must(t, err)
		got := fmt.Sprintf("%dx%d %dx%d", res.SourceWidth, res.SourceHeight, res.Width, res.Height)
		if got != c.want {
			t.Errorf("%s: sizes %s, want %s", c.name, got, c.want)
		}
		img, _ := decode(t, res.Path)
		if size(img) != fmt.Sprintf("%dx%d", res.Width, res.Height) {
			t.Errorf("%s: reported %dx%d, wrote %s", c.name, res.Width, res.Height, size(img))
		}
		switch {
		case c.date == nil && res.Date != nil:
			t.Errorf("%s: date %+v, want none", c.name, res.Date)
		case c.date != nil && (res.Date == nil || *res.Date != *c.date):
			t.Errorf("%s: date %+v, want %+v", c.name, res.Date, c.date)
		}
	}
}

// --quality changes the JPEG encoding only: orientation, stripped
// metadata, and overwrite protection stay the same.
func TestThumbQuality(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 128, 64))
	for y := range 64 {
		for x := range 128 {
			img.Set(x, y, color.NRGBA{uint8(x * 2), uint8(y * 4), uint8(x ^ y), 255})
		}
	}
	src := write(t, filepath.Join(dir, "noisy.jpg"), testimages.JPEG(img, testimages.PhoneEXIF(binary.BigEndian, 6)))
	sizes := map[int]int64{}
	for _, q := range []int{20, 95} {
		out := filepath.Join(dir, fmt.Sprint(q))
		r := newRunner(t, Output{Dir: out, Suffix: DefaultThumbSuffix, Quality: q, Size: &imaging.Size{Width: 64, Height: 64}})
		p, err := thumbPath(r.Thumb(context.Background(), src, "test"))
		must(t, err)
		fi, err := os.Stat(p)
		must(t, err)
		sizes[q] = fi.Size()
		dec, raw := decode(t, p)
		if size(dec) != "32x64" || dec.EXIF != nil || bytes.Contains(raw, []byte("Exif")) || bytes.Contains(raw, []byte("iPhone")) {
			t.Errorf("quality %d: %s, EXIF %v", q, size(dec), dec.EXIF)
		}
		// Protected without Overwrite, whatever the quality.
		r = newRunner(t, Output{Dir: out, Suffix: DefaultThumbSuffix, Quality: 50, Size: &imaging.Size{Width: 64, Height: 64}})
		var ce *ConflictError
		if _, err := r.Thumb(context.Background(), src, "test"); !errors.As(err, &ce) {
			t.Errorf("quality %d: second run err = %v", q, err)
		}
	}
	if sizes[20] >= sizes[95] {
		t.Errorf("quality 20 wrote %d bytes, quality 95 %d", sizes[20], sizes[95])
	}
}
