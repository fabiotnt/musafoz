package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	entries, _ := os.ReadDir(m.toolDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary directory left behind: %s", e.Name())
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
	stale := filepath.Join(m.toolDir(), ".tmp-12345")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
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
	if _, err := FFprobeArtifact("linux", "amd64"); err == nil || !strings.Contains(err.Error(), "darwin/amd64") {
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
	if _, err := LookupArtifact(ToolFFmpeg, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), EnvFFmpeg) {
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
