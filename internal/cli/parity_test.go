//go:build parity

// The parity test compares musafoz preview outputs with those of the
// reference preview script on separate copies of the same videos. The
// script is not part of this repository. Run it with:
//
//	MUSAFOZ_REFERENCE_SCRIPT=/path/to/adjusted/script.sh \
//	MUSAFOZ_PARITY_VIDEOS=/path/a.mp4:/path/b.mov \
//	go test -tags parity -count=1 -v -run Parity ./internal/cli/
//
// The script runs with the managed ffmpeg and ffprobe first on its PATH,
// so both sides use the same FFmpeg build. The builds come from the cache
// described in testCache.
package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/tools"
)

func TestParityWithReferenceScript(t *testing.T) {
	script := os.Getenv("MUSAFOZ_REFERENCE_SCRIPT")
	videos := filepath.SplitList(os.Getenv("MUSAFOZ_PARITY_VIDEOS"))
	if script == "" || len(videos) == 0 {
		t.Skip("set MUSAFOZ_REFERENCE_SCRIPT and MUSAFOZ_PARITY_VIDEOS")
	}
	cache := testCache(t)
	root, _ := tools.DefaultCacheRoot(cache)
	bin := t.TempDir()
	paths := map[string]string{}
	for _, tool := range []string{tools.ToolFFmpeg, tools.ToolFFprobe} {
		a, err := tools.LookupArtifact(tool, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			t.Fatal(err)
		}
		m := &tools.Manager{Root: root, Artifact: a}
		p, err := m.Ensure(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		paths[tool] = p
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	ffprobe := paths[tools.ToolFFprobe]

	for _, src := range videos {
		t.Run(filepath.Base(src), func(t *testing.T) {
			name := filepath.Base(src)
			base := strings.TrimSuffix(name, filepath.Ext(name))
			refDir, ourDir := t.TempDir(), t.TempDir()
			ref, ours := copyFile(t, src, refDir, name), copyFile(t, src, ourDir, name)

			cmd := exec.Command("bash", script, ref)
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("reference script failed: %v\n%s", err, out)
			}
			r := runReal(t, cache, "preview", "--faststart-in-place", "--json", ours)
			if r.err != nil {
				t.Fatalf("musafoz failed: %v\n%s", r.err, r.stderr)
			}
			var res struct {
				FrameDecoder   string `json:"frame_decoder"`
				PreviewEncoder string `json:"preview_encoder"`
			}
			if err := json.Unmarshal([]byte(r.stdout), &res); err != nil {
				t.Fatalf("result: %v\n%s", err, r.stdout)
			}
			t.Logf("musafoz auto mode: frame decoder %s, preview encoder %s", res.FrameDecoder, res.PreviewEncoder)

			info, err := media.Inspect(t.Context(), ffprobe, ours)
			if err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"_cover.jpg", "_preview.mp4", "_preview.jpg"} {
				refOut, ourOut := filepath.Join(refDir, base+suffix), filepath.Join(ourDir, base+suffix)
				if _, err := os.Stat(refOut); err != nil {
					t.Errorf("reference output %s missing", suffix)
					continue
				}
				if _, err := os.Stat(ourOut); err != nil {
					t.Errorf("musafoz output %s missing", suffix)
					continue
				}
				a, b := probeFirstStream(t, ffprobe, refOut), probeFirstStream(t, ffprobe, ourOut)
				t.Logf("%s reference %+v", suffix, a)
				t.Logf("%s musafoz  %+v", suffix, b)
				// The mosaic follows the source aspect ratio; the reference
				// always uses a 16:9 grid. Other outputs must match.
				if suffix != "_preview.jpg" && (a.Width != b.Width || a.Height != b.Height) {
					t.Errorf("%s: dimensions %dx%d vs %dx%d", suffix, a.Width, a.Height, b.Width, b.Height)
				}
				switch suffix {
				case "_cover.jpg":
					if b.Width != info.Video.DisplayWidth || b.Height != info.Video.DisplayHeight {
						t.Errorf("cover is %dx%d, source displays as %dx%d", b.Width, b.Height, info.Video.DisplayWidth, info.Video.DisplayHeight)
					}
				case "_preview.mp4":
					if a.CodecName != "h264" || b.CodecName != "h264" || a.AvgFrameRate != "4/1" || b.AvgFrameRate != "4/1" {
						t.Errorf("preview codec or rate: %+v vs %+v", a, b)
					}
					if a.NbFrames != b.NbFrames {
						t.Errorf("preview frames %s vs %s", a.NbFrames, b.NbFrames)
					}
					if b.PixFmt != "yuv420p" {
						t.Errorf("musafoz preview pixel format %s", b.PixFmt)
					}
					if b.SAR != "1:1" {
						t.Errorf("musafoz preview SAR %s", b.SAR)
					}
					if outInfo, err := media.Inspect(t.Context(), ffprobe, ourOut); err != nil || outInfo.Video.Rotation != 0 {
						t.Errorf("musafoz preview rotation metadata: %v", err)
					}
				case "_preview.jpg":
					aspect := info.Video.DisplayAspect()
					if w, h := media.MosaicSize(aspect); b.Width != w || b.Height != h {
						t.Errorf("mosaic is %dx%d, want %dx%d for aspect %.4f", b.Width, b.Height, w, h, aspect)
					}
					if a.Width != b.Width || a.Height != b.Height {
						t.Logf("intentional difference: reference mosaic %dx%d (16:9 grid), musafoz %dx%d (source aspect %.4f)",
							a.Width, a.Height, b.Width, b.Height, aspect)
						break
					}
					out, err := exec.Command(paths[tools.ToolFFmpeg], "-hide_banner", "-i", refOut, "-i", ourOut,
						"-lavfi", "ssim", "-f", "null", "-").CombinedOutput()
					if err == nil {
						for _, line := range strings.Split(string(out), "\n") {
							if i := strings.Index(line, "SSIM "); i >= 0 {
								t.Logf("mosaic %s", strings.TrimSpace(line[i:]))
							}
						}
					}
				}
			}
			for _, p := range []string{ref, ours} {
				if ok, err := media.IsFastStart(p); err != nil || !ok {
					t.Errorf("%s is not faststart after the run: %v", p, err)
				}
			}
			refInfo, err := media.Inspect(t.Context(), ffprobe, ref)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after the run: reference container %q, musafoz container %q", refInfo.Format, info.Format)
			a := strings.Split(streamHash(t, paths[tools.ToolFFmpeg], ref), "\n")
			b := strings.Split(streamHash(t, paths[tools.ToolFFmpeg], ours), "\n")
			for i := range a {
				if i < len(b) && a[i] != b[i] {
					t.Logf("remuxed stream hash line differs:\n  reference %s\n  musafoz  %s", a[i], b[i])
				}
			}
		})
	}
}

func copyFile(t *testing.T, src, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
