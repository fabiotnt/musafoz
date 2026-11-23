package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

const fakeVersionLine = "ffprobe version 9.1-tessus  https://evermeet.cx/ffmpeg/"

func fakeScript(versionLine string) []byte {
	return []byte("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo '" + versionLine + "'; exit 0; fi\necho '{}'\n")
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(content)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type testServer struct {
	*httptest.Server
	hits  atomic.Int32
	body  []byte
	delay time.Duration
}

func newTestServer(t *testing.T, body []byte) *testServer {
	ts := &testServer{body: body}
	ts.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.hits.Add(1)
		time.Sleep(ts.delay)
		w.Header().Set("Content-Length", strconv.Itoa(len(ts.body)))
		w.Write(ts.body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// setup returns a manager whose artifact points at a local TLS server that
// serves a fake ffprobe build.
func setup(t *testing.T, script []byte) (*Manager, *testServer) {
	skipWithoutShell(t)
	t.Helper()
	archive := zipOf(t, "ffprobe", script)
	ts := newTestServer(t, archive)
	m := &Manager{
		Root:   t.TempDir(),
		Client: ts.Client(),
		Artifact: Artifact{
			Tool: "ffprobe", Version: "9.1", GOOS: "darwin", GOARCH: "amd64",
			URL:           ts.URL + "/ffprobe-9.1.zip",
			ArchiveSize:   int64(len(archive)),
			ArchiveSHA256: sha(archive),
			BinaryName:    "ffprobe",
			BinarySize:    int64(len(script)),
			BinarySHA256:  sha(script),
			VersionPrefix: "ffprobe version 9.1-tessus",
		},
	}
	return m, ts
}

func assertNoLeftovers(t *testing.T, m *Manager) {
	t.Helper()
	for _, dir := range []string{m.toolDir(), filepath.Join(m.Root, "tools")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				t.Errorf("temporary directory left behind: %s", e.Name())
			}
		}
	}
}

func TestEnsureDownloadsOnceAndReuses(t *testing.T) {
	m, ts := setup(t, fakeScript(fakeVersionLine))
	var logs []string
	var progressCalls int
	m.Logf = func(f string, a ...any) { logs = append(logs, f) }
	m.Progress = func(done, total int64) { progressCalls++ }

	path, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != m.BinaryPath() {
		t.Errorf("path = %s, want %s", path, m.BinaryPath())
	}
	if progressCalls == 0 || len(logs) == 0 {
		t.Errorf("expected progress and log output, got %d progress calls and %v", progressCalls, logs)
	}

	// The second call must not touch the network.
	m.Client = &http.Client{Transport: failingTransport{}}
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ts.hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1", got)
	}
	assertNoLeftovers(t, m)
}

func TestEnsureRejectsArchiveHashMismatch(t *testing.T) {
	m, _ := setup(t, fakeScript(fakeVersionLine))
	m.Artifact.ArchiveSHA256 = strings.Repeat("0", 64)
	_, err := m.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("err = %v, want SHA-256 mismatch", err)
	}
	if _, err := os.Stat(m.InstallDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install directory exists after a failed install: %v", err)
	}
	assertNoLeftovers(t, m)
}

func TestEnsureRejectsBinaryHashMismatch(t *testing.T) {
	m, _ := setup(t, fakeScript(fakeVersionLine))
	m.Artifact.BinarySHA256 = strings.Repeat("0", 64)
	_, err := m.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("err = %v, want SHA-256 mismatch", err)
	}
	if _, err := os.Stat(m.InstallDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install directory exists after a failed install: %v", err)
	}
}

func TestEnsureRejectsUnexpectedVersion(t *testing.T) {
	m, _ := setup(t, fakeScript("ffprobe version 8.0.1-tessus"))
	_, err := m.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "expected \"ffprobe version 9.1-tessus\"") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(m.InstallDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install directory exists after a failed install: %v", err)
	}
}

func TestEnsureReinstallsTamperedCache(t *testing.T) {
	m, ts := setup(t, fakeScript(fakeVersionLine))
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Same size, one byte changed, so only the hash check can catch it.
	tampered, err := os.ReadFile(m.BinaryPath())
	if err != nil {
		t.Fatal(err)
	}
	tampered[len(tampered)-2] ^= 0x01
	if err := os.WriteFile(m.BinaryPath(), tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(m.BinaryPath(), m.Artifact.BinarySize, m.Artifact.BinarySHA256); err == nil {
		t.Fatal("tampered binary passed verification")
	}
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ts.hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2", got)
	}
	if err := verifyFile(m.BinaryPath(), m.Artifact.BinarySize, m.Artifact.BinarySHA256); err != nil {
		t.Errorf("reinstalled binary fails verification: %v", err)
	}
}

func TestEnsureConcurrentCallersShareOneInstall(t *testing.T) {
	m, ts := setup(t, fakeScript(fakeVersionLine))
	ts.delay = 300 * time.Millisecond

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each caller has its own manager, as separate processes would.
			mi := *m
			_, errs[i] = mi.Ensure(context.Background())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if got := ts.hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1", got)
	}
	assertNoLeftovers(t, m)
}

func TestEnsureCleansStaleTemporaryDirectories(t *testing.T) {
	m, _ := setup(t, fakeScript(fakeVersionLine))
	for _, stale := range []string{
		filepath.Join(m.toolDir(), ".tmp-12345"),
		filepath.Join(m.Root, "tools", m.tempPrefix()+"12345"),
	} {
		if err := os.MkdirAll(stale, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNoLeftovers(t, m)
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in test")
}

func TestEnsureWithoutNetworkExplainsOverride(t *testing.T) {
	m, _ := setup(t, fakeScript(fakeVersionLine))
	m.Client = &http.Client{Transport: failingTransport{}}
	_, err := m.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), EnvFFprobe) {
		t.Fatalf("err = %v, want a hint about %s", err, EnvFFprobe)
	}
}

func writeExecutable(t *testing.T, content []byte) string {
	skipWithoutShell(t)
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveOrder(t *testing.T) {
	flagBin := writeExecutable(t, fakeScript("ffprobe version 8.0.1-tessus"))
	envBin := writeExecutable(t, fakeScript("ffprobe version 7.1"))
	offline, _ := setup(t, fakeScript(fakeVersionLine))
	offline.Client = &http.Client{Transport: failingTransport{}}
	ctx := context.Background()

	r, err := ResolveFFprobe(ctx, ResolveOptions{FlagPath: flagBin, EnvPath: envBin, Manager: offline})
	if err != nil || r.Path != flagBin || r.Source != SourceFlag {
		t.Errorf("flag: got %+v, %v", r, err)
	}
	r, err = ResolveFFprobe(ctx, ResolveOptions{EnvPath: envBin, Manager: offline})
	if err != nil || r.Path != envBin || r.Source != SourceEnv {
		t.Errorf("env: got %+v, %v", r, err)
	}

	managed, _ := setup(t, fakeScript(fakeVersionLine))
	r, err = ResolveFFprobe(ctx, ResolveOptions{Manager: managed})
	if err != nil || r.Path != managed.BinaryPath() || r.Source != SourceManaged {
		t.Errorf("managed: got %+v, %v", r, err)
	}
}

func TestResolveExplicitRejectsBadPaths(t *testing.T) {
	ctx := context.Background()
	notExec := filepath.Join(t.TempDir(), "ffprobe")
	os.WriteFile(notExec, fakeScript(fakeVersionLine), 0o644)
	cases := map[string]string{
		"missing":        filepath.Join(t.TempDir(), "nope"),
		"directory":      t.TempDir(),
		"not executable": notExec,
		"too old":        writeExecutable(t, fakeScript("ffprobe version 5.1.2")),
		"not ffprobe":    writeExecutable(t, fakeScript("ffmpeg version 9.1")),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveFFprobe(ctx, ResolveOptions{FlagPath: path}); err == nil {
				t.Fatal("expected an error")
			} else if !strings.Contains(err.Error(), "--ffprobe") {
				t.Errorf("error does not name the source: %v", err)
			}
		})
	}
}

func TestResolveExplicitWarnsOnSnapshotVersion(t *testing.T) {
	bin := writeExecutable(t, fakeScript("ffprobe version N-121000-gabcdef-tessus"))
	var warned bool
	r, err := ResolveFFprobe(context.Background(), ResolveOptions{
		EnvPath: bin,
		Warnf:   func(string, ...any) { warned = true },
	})
	if err != nil || r.Source != SourceEnv || !warned {
		t.Fatalf("got %+v, %v, warned=%v", r, err, warned)
	}
}

func TestCheckExplicitVersion(t *testing.T) {
	ok := []string{"ffprobe version 9.1-tessus", "ffprobe version 6.0", "ffprobe version n7.1.1"}
	for _, line := range ok {
		if err := checkExplicitVersion("ffprobe", line); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
	bad := []string{"ffprobe version 5.1.2", "ffmpeg version 9.1", ""}
	for _, line := range bad {
		if err := checkExplicitVersion("ffprobe", line); err == nil {
			t.Errorf("%q: expected an error", line)
		}
	}
}

func TestFFprobeArtifact(t *testing.T) {
	a, err := FFprobeArtifact("darwin", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.URL, "https://") || len(a.ArchiveSHA256) != 64 || len(a.BinarySHA256) != 64 {
		t.Errorf("incomplete artifact: %+v", a)
	}
	if !strings.Contains(a.URL, a.Version) {
		t.Errorf("URL %s is not versioned", a.URL)
	}
	if _, err := FFprobeArtifact("freebsd", "amd64"); err == nil || !strings.Contains(err.Error(), "darwin/amd64") {
		t.Errorf("unsupported platform: err = %v", err)
	}
}

func TestFFmpegArtifactAndResolve(t *testing.T) {
	a, err := LookupArtifact(ToolFFmpeg, "darwin", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if a.BinaryName != "ffmpeg" || !strings.HasPrefix(a.VersionPrefix, "ffmpeg version ") || len(a.BinarySHA256) != 64 {
		t.Errorf("incomplete ffmpeg artifact: %+v", a)
	}
	if _, err := LookupArtifact(ToolFFmpeg, "freebsd", "amd64"); err == nil || !strings.Contains(err.Error(), EnvFFmpeg) {
		t.Errorf("unsupported platform: err = %v", err)
	}

	ctx := context.Background()
	ffmpeg := writeExecutable(t, fakeScript("ffmpeg version 9.1-tessus"))
	r, err := Resolve(ctx, ResolveOptions{Tool: ToolFFmpeg, EnvPath: ffmpeg})
	if err != nil || r.Source != SourceEnv {
		t.Errorf("env ffmpeg: got %+v, %v", r, err)
	}
	// An ffprobe binary given as ffmpeg is rejected and the error names the env var.
	probe := writeExecutable(t, fakeScript(fakeVersionLine))
	if _, err := Resolve(ctx, ResolveOptions{Tool: ToolFFmpeg, EnvPath: probe}); err == nil || !strings.Contains(err.Error(), EnvFFmpeg) {
		t.Errorf("ffprobe as ffmpeg: err = %v", err)
	}
	// A manager for the wrong tool is refused.
	m, _ := setup(t, fakeScript(fakeVersionLine))
	if _, err := Resolve(ctx, ResolveOptions{Tool: ToolFFmpeg, Manager: m}); err == nil {
		t.Error("expected an error for a mismatched manager")
	}
}

func tarXZOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(xw)
	names := []string{"pkg/", "pkg/bin/"}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasSuffix(name, "/") {
			tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755})
			continue
		}
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(files[name]))})
		tw.Write(files[name])
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A .tar.xz archive that holds both tools is downloaded once and installs
// both, whichever tool asks first.
func TestEnsureSharedTarXZArchive(t *testing.T) {
	skipWithoutShell(t)
	probe := fakeScript("ffprobe version n9.0.2-test")
	mpeg := fakeScript("ffmpeg version n9.0.2-test")
	archive := tarXZOf(t, map[string][]byte{
		"pkg/bin/ffprobe": probe,
		"pkg/bin/ffmpeg":  mpeg,
		"pkg/bin/ffplay":  []byte("not extracted"),
	})
	ts := newTestServer(t, archive)
	base := Artifact{
		Version: "n9.0.2", GOOS: "linux", GOARCH: "amd64",
		URL: ts.URL + "/ffmpeg-n9.0.2-linux64.tar.xz", ArchiveSize: int64(len(archive)), ArchiveSHA256: sha(archive),
	}
	pa, ma := base, base
	pa.Tool, pa.ArchiveEntry, pa.BinaryName = "ffprobe", "pkg/bin/ffprobe", "ffprobe"
	pa.BinarySize, pa.BinarySHA256, pa.VersionPrefix = int64(len(probe)), sha(probe), "ffprobe version n9.0.2"
	ma.Tool, ma.ArchiveEntry, ma.BinaryName = "ffmpeg", "pkg/bin/ffmpeg", "ffmpeg"
	ma.BinarySize, ma.BinarySHA256, ma.VersionPrefix = int64(len(mpeg)), sha(mpeg), "ffmpeg version n9.0.2"
	root := t.TempDir()
	newManager := func(a Artifact) *Manager {
		return &Manager{Root: root, Client: ts.Client(), Artifact: a, Siblings: []Artifact{pa, ma}}
	}
	ctx := context.Background()
	if _, err := newManager(pa).Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	mm := newManager(ma)
	path, err := mm.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if path != mm.BinaryPath() {
		t.Errorf("ffmpeg path %s, want %s", path, mm.BinaryPath())
	}
	if got := ts.hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(mm.InstallDir(), "ffplay")); err == nil {
		t.Error("an unrequested entry was extracted")
	}
	assertNoLeftovers(t, mm)

	// A tampered sibling is reinstalled from a new download.
	if err := os.WriteFile(mm.BinaryPath(), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newManager(ma).Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ts.hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2", got)
	}
}

func TestBtbNArtifactsShareOneArchive(t *testing.T) {
	for _, c := range []struct{ goos, ext, exe string }{{"linux", ".tar.xz", ""}, {"windows", ".zip", ".exe"}} {
		p, err := LookupArtifact(ToolFFprobe, c.goos, "amd64")
		if err != nil {
			t.Fatal(err)
		}
		m, err := LookupArtifact(ToolFFmpeg, c.goos, "amd64")
		if err != nil {
			t.Fatal(err)
		}
		if p.URL != m.URL || p.ArchiveSHA256 != m.ArchiveSHA256 || !strings.HasSuffix(p.URL, c.ext) {
			t.Errorf("%s tools should share one %s archive: %s, %s", c.goos, c.ext, p.URL, m.URL)
		}
		if got := siblings(m); len(got) != 2 {
			t.Errorf("%s siblings = %d, want 2", c.goos, len(got))
		}
		if !strings.Contains(p.URL, p.Version) || !strings.HasSuffix(p.ArchiveEntry, "/bin/ffprobe"+c.exe) || p.BinaryName != "ffprobe"+c.exe {
			t.Errorf("incomplete artifact: %+v", p)
		}
	}
}

func TestResolveSearchesPath(t *testing.T) {
	ctx := context.Background()
	good := writeExecutable(t, fakeScript("ffprobe version 7.1.1-1+b1"))
	old := writeExecutable(t, fakeScript("ffprobe version 4.4.2"))
	lookup := func(p string) func(string) (string, error) {
		return func(string) (string, error) {
			if p == "" {
				return "", errors.New("not found")
			}
			return p, nil
		}
	}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	managed, _ := setup(t, fakeScript(fakeVersionLine))
	r, err := ResolveFFprobe(ctx, ResolveOptions{SearchPath: true, LookPath: lookup(good), Manager: managed, Logf: logf})
	if err != nil || r.Source != SourcePath || r.Path != good {
		t.Fatalf("compatible PATH tool: got %+v, %v", r, err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "found on the PATH: ffprobe version 7.1.1") {
		t.Errorf("log = %q", logs)
	}

	// The flag still wins over the PATH.
	r, err = ResolveFFprobe(ctx, ResolveOptions{FlagPath: old, SearchPath: true, LookPath: lookup(good)})
	if err == nil || !strings.Contains(err.Error(), "--ffprobe") {
		t.Errorf("an invalid flag path must fail and name the flag: %+v, %v", r, err)
	}

	cases := []struct {
		name, path string
		check      func(context.Context, string) error
		reason     string
	}{
		{"missing", "", nil, "no ffprobe on the PATH"},
		{"too old", old, nil, "is not compatible: ffprobe 4.4 is too old"},
		{"failed check", good, func(context.Context, string) error { return errors.New("missing encoder libx264") }, "is not compatible: missing encoder libx264"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs = nil
			m, _ := setup(t, fakeScript(fakeVersionLine))
			r, err := ResolveFFprobe(ctx, ResolveOptions{SearchPath: true, LookPath: lookup(c.path), Check: c.check, Manager: m, Logf: logf})
			if err != nil || r.Source != SourceManaged {
				t.Fatalf("got %+v, %v", r, err)
			}
			if len(logs) != 1 || !strings.Contains(logs[0], c.reason) || !strings.Contains(logs[0], "managed 9.1 build") {
				t.Errorf("log = %q, want %q", logs, c.reason)
			}
			_, err = ResolveFFprobe(ctx, ResolveOptions{SearchPath: true, LookPath: lookup(c.path), Check: c.check,
				ManagerErr: errors.New("no managed ffprobe build for linux/arm64")})
			if err == nil || !strings.Contains(err.Error(), c.reason) || !strings.Contains(err.Error(), "linux/arm64") {
				t.Errorf("without a managed build: err = %v", err)
			}
		})
	}
}

func TestResolveLogsManagedDefault(t *testing.T) {
	m, _ := setup(t, fakeScript(fakeVersionLine))
	var logs []string
	_, err := ResolveFFprobe(context.Background(), ResolveOptions{Manager: m,
		LookPath: func(string) (string, error) { t.Error("the PATH was searched"); return "", errors.New("no") },
		Logf:     func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "the managed build is the default on this platform") {
		t.Errorf("log = %q", logs)
	}
}

func TestExecutablePathOnWindows(t *testing.T) {
	saved := hostOS
	hostOS = "windows"
	t.Cleanup(func() { hostOS = saved })
	dir := t.TempDir()
	exe := filepath.Join(dir, "ffprobe.exe")
	txt := filepath.Join(dir, "ffprobe.txt")
	os.WriteFile(exe, []byte("MZ"), 0o644)
	os.WriteFile(txt, []byte("MZ"), 0o644)
	if p, err := executablePath(exe); err != nil || p != exe {
		t.Errorf(".exe without execute bits: %s, %v", p, err)
	}
	if p, err := executablePath(filepath.Join(dir, "ffprobe")); err != nil || p != exe {
		t.Errorf("path without extension: %s, %v", p, err)
	}
	if _, err := executablePath(txt); err == nil || !strings.Contains(err.Error(), ".exe") {
		t.Errorf(".txt accepted: %v", err)
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
