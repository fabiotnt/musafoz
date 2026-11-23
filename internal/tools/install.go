package tools

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fabiotnt/musafoz/internal/fslock"
)

// Manager installs a pinned tool build into a local cache and verifies it
// before every use.
type Manager struct {
	// Root is the base cache directory, for example ~/Library/Caches/musafoz.
	Root string
	// Artifact is the pinned build to install.
	Artifact Artifact
	// Client downloads the archive. http.DefaultClient is used when nil.
	Client *http.Client
	// Progress, when set, receives download progress in bytes.
	Progress func(done, total int64)
	// Logf, when set, receives status messages.
	Logf func(format string, args ...any)
}

// DefaultCacheRoot returns the base cache directory. A non-empty override,
// usually from MUSAFOZ_CACHE_DIR, wins over the user cache directory.
func DefaultCacheRoot(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache directory: %w; set %s", err, EnvCacheDir)
	}
	return filepath.Join(dir, "musafoz"), nil
}

func (m *Manager) toolDir() string {
	return filepath.Join(m.Root, "tools", m.Artifact.Tool)
}

// InstallDir is the directory that holds the installed build.
func (m *Manager) InstallDir() string {
	a := m.Artifact
	return filepath.Join(m.toolDir(), a.Version+"-"+a.GOOS+"-"+a.GOARCH)
}

// BinaryPath is the path of the installed executable.
func (m *Manager) BinaryPath() string {
	return filepath.Join(m.InstallDir(), m.Artifact.BinaryName)
}

// Ensure returns the path of a verified executable, downloading and
// installing it first when the cache is empty or fails verification.
// Concurrent callers share one installation: the install runs under a file
// lock and the finished directory appears through an atomic rename, so a
// partial install is never visible at BinaryPath.
func (m *Manager) Ensure(ctx context.Context) (string, error) {
	bin := m.BinaryPath()
	if err := verifyFile(bin, m.Artifact.BinarySize, m.Artifact.BinarySHA256); err == nil {
		return bin, nil
	}

	if err := os.MkdirAll(m.toolDir(), 0o755); err != nil {
		return "", fmt.Errorf("create tool cache: %w", err)
	}
	unlock, err := fslock.Lock(ctx, filepath.Join(m.toolDir(), ".lock"), func() {
		m.logf("waiting for another musafoz process to finish installing %s", m.Artifact.Tool)
	})
	if err != nil {
		return "", fmt.Errorf("lock tool cache: %w", err)
	}
	defer unlock()

	// Another process may have finished the install while we waited.
	err = verifyFile(bin, m.Artifact.BinarySize, m.Artifact.BinarySHA256)
	if err == nil {
		return bin, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		m.logf("cached %s failed verification (%v); reinstalling", m.Artifact.Tool, err)
		if err := os.RemoveAll(m.InstallDir()); err != nil {
			return "", fmt.Errorf("remove invalid cached %s: %w", m.Artifact.Tool, err)
		}
	}
	m.removeStaleTemp()

	if err := m.install(ctx); err != nil {
		return "", err
	}
	if err := verifyFile(bin, m.Artifact.BinarySize, m.Artifact.BinarySHA256); err != nil {
		return "", fmt.Errorf("verify installed %s: %w", m.Artifact.Tool, err)
	}
	return bin, nil
}

// removeStaleTemp deletes leftovers from interrupted installs. It must only
// run while holding the lock, because only the lock holder creates them.
func (m *Manager) removeStaleTemp() {
	entries, err := os.ReadDir(m.toolDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			_ = os.RemoveAll(filepath.Join(m.toolDir(), e.Name()))
		}
	}
}

func (m *Manager) install(ctx context.Context) error {
	a := m.Artifact
	tmp, err := os.MkdirTemp(m.toolDir(), ".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary install directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	archive := filepath.Join(tmp, "archive.zip")
	m.logf("downloading %s %s for %s from %s", a.Tool, a.Version, a.Platform(), a.URL)
	if err := m.download(ctx, archive); err != nil {
		return err
	}

	stage := filepath.Join(tmp, "install")
	if err := os.Mkdir(stage, 0o755); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	staged := filepath.Join(stage, a.BinaryName)
	if err := extractBinary(archive, a, staged); err != nil {
		return fmt.Errorf("extract %s from %s: %w", a.BinaryName, a.URL, err)
	}
	if err := os.Remove(archive); err != nil {
		return fmt.Errorf("remove downloaded archive: %w", err)
	}

	line, err := versionLine(ctx, staged)
	if err != nil {
		return fmt.Errorf("check downloaded %s: %w", a.Tool, err)
	}
	if !strings.HasPrefix(line, a.VersionPrefix) {
		return fmt.Errorf("downloaded %s reports %q, expected %q", a.Tool, line, a.VersionPrefix)
	}

	if err := os.Rename(stage, m.InstallDir()); err != nil {
		return fmt.Errorf("install %s into cache: %w", a.Tool, err)
	}
	m.logf("installed %s %s at %s", a.Tool, a.Version, m.BinaryPath())
	return nil
}

func (m *Manager) download(ctx context.Context, dest string) error {
	a := m.Artifact
	client := m.Client
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", a.URL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w; check the network connection or set --ffprobe or %s", a.URL, err, EnvFFprobe)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected HTTP status %s", a.URL, resp.Status)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != a.ArchiveSize {
		return fmt.Errorf("download %s: size is %d bytes, expected %d", a.URL, resp.ContentLength, a.ArchiveSize)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer f.Close()

	h := sha256.New()
	w := io.MultiWriter(f, h, &progressWriter{total: a.ArchiveSize, report: m.Progress})
	n, err := io.Copy(w, io.LimitReader(resp.Body, a.ArchiveSize+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", a.URL, err)
	}
	if n != a.ArchiveSize {
		return fmt.Errorf("download %s: received %d bytes, expected %d", a.URL, n, a.ArchiveSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.ArchiveSHA256 {
		return fmt.Errorf("download %s: SHA-256 mismatch: got %s, expected %s", a.URL, got, a.ArchiveSHA256)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// extractBinary copies the single expected entry out of a verified archive
// and checks its size and hash. No other entry is written to disk.
func extractBinary(archive string, a Artifact, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()

	var entry *zip.File
	for _, f := range zr.File {
		if f.Name == a.BinaryName {
			entry = f
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("entry %q not found in archive", a.BinaryName)
	}
	if !entry.Mode().IsRegular() {
		return fmt.Errorf("entry %q is not a regular file", a.BinaryName)
	}
	if entry.UncompressedSize64 != uint64(a.BinarySize) {
		return fmt.Errorf("entry %q is %d bytes, expected %d", a.BinaryName, entry.UncompressedSize64, a.BinarySize)
	}

	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, a.BinarySize+1))
	if err != nil {
		return err
	}
	if n != a.BinarySize {
		return fmt.Errorf("entry %q produced %d bytes, expected %d", a.BinaryName, n, a.BinarySize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.BinarySHA256 {
		return fmt.Errorf("entry %q SHA-256 mismatch: got %s, expected %s", a.BinaryName, got, a.BinarySHA256)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}

// verifyFile checks that path is a regular executable file with the pinned
// size and SHA-256. A missing file returns an error wrapping fs.ErrNotExist.
func verifyFile(path string, size int64, sum string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() != size {
		return fmt.Errorf("%s is %d bytes, expected %d", path, fi.Size(), size)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("%s SHA-256 mismatch: got %s, expected %s", path, got, sum)
	}
	return nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

type progressWriter struct {
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.report != nil {
		p.report(p.done, p.total)
	}
	return len(b), nil
}
