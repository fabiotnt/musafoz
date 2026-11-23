package tools

import (
	"archive/tar"
	"archive/zip"
	"bufio"
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
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"

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
	// Siblings, when set, replaces the manifest lookup of other tools stored
	// in the same archive. Tests use it.
	Siblings []Artifact
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
// lock per archive and each finished directory appears through an atomic
// rename, so a partial install is never visible at BinaryPath. When other
// pinned tools come from the same archive, they are installed from the same
// download.
func (m *Manager) Ensure(ctx context.Context) (string, error) {
	bin := m.BinaryPath()
	if err := verifyFile(bin, m.Artifact.BinarySize, m.Artifact.BinarySHA256); err == nil {
		return bin, nil
	}

	tools := filepath.Join(m.Root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		return "", fmt.Errorf("create tool cache: %w", err)
	}
	unlock, err := fslock.Lock(ctx, filepath.Join(tools, "."+m.Artifact.archiveKey()+".lock"), func() {
		m.logf("waiting for another musafoz process to finish installing %s", m.Artifact.Tool)
	})
	if err != nil {
		return "", fmt.Errorf("lock tool cache: %w", err)
	}
	defer unlock()

	// Another process may have finished the install while we waited. Find
	// every tool of this archive that still needs installing.
	var pending []*Manager
	for _, a := range m.archiveArtifacts() {
		sm := *m
		sm.Artifact = a
		err := verifyFile(sm.BinaryPath(), a.BinarySize, a.BinarySHA256)
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			m.logf("cached %s failed verification (%v); reinstalling", a.Tool, err)
			if err := os.RemoveAll(sm.InstallDir()); err != nil {
				return "", fmt.Errorf("remove invalid cached %s: %w", a.Tool, err)
			}
		}
		pending = append(pending, &sm)
	}
	if len(pending) > 0 {
		m.removeStaleTemp()
		if err := m.install(ctx, pending); err != nil {
			return "", err
		}
	}
	if err := verifyFile(bin, m.Artifact.BinarySize, m.Artifact.BinarySHA256); err != nil {
		return "", fmt.Errorf("verify installed %s: %w", m.Artifact.Tool, err)
	}
	return bin, nil
}

// archiveArtifacts returns the tools stored in the archive of m.Artifact,
// m.Artifact first.
func (m *Manager) archiveArtifacts() []Artifact {
	list := m.Siblings
	if list == nil {
		list = siblings(m.Artifact)
	}
	out := []Artifact{m.Artifact}
	for _, a := range list {
		if a.Tool != m.Artifact.Tool && a.URL == m.Artifact.URL && a.ArchiveSHA256 == m.Artifact.ArchiveSHA256 {
			out = append(out, a)
		}
	}
	return out
}

func (m *Manager) tempPrefix() string {
	return ".tmp-" + m.Artifact.archiveKey() + "-"
}

// removeStaleTemp deletes leftovers from interrupted installs of this
// archive. It must only run while holding the archive lock, because only
// the lock holder creates them.
func (m *Manager) removeStaleTemp() {
	tools := filepath.Join(m.Root, "tools")
	entries, err := os.ReadDir(tools)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), m.tempPrefix()) {
			_ = os.RemoveAll(filepath.Join(tools, e.Name()))
		}
	}
	// Installs before archive locks kept their temporary files per tool.
	for _, a := range m.archiveArtifacts() {
		dir := filepath.Join(m.Root, "tools", a.Tool)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// install downloads the archive once, extracts and checks every pending
// tool, and moves each into place.
func (m *Manager) install(ctx context.Context, pending []*Manager) error {
	a := m.Artifact
	tmp, err := os.MkdirTemp(filepath.Join(m.Root, "tools"), m.tempPrefix())
	if err != nil {
		return fmt.Errorf("create temporary install directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	archive := filepath.Join(tmp, "archive")
	names := make([]string, len(pending))
	for i, p := range pending {
		names[i] = p.Artifact.Tool
	}
	m.logf("downloading %s %s for %s from %s", strings.Join(names, " and "), a.Version, a.Platform(), a.URL)
	if err := m.download(ctx, archive); err != nil {
		return err
	}

	wanted := map[string]extractTarget{}
	for i, p := range pending {
		stage := filepath.Join(tmp, fmt.Sprintf("install-%d", i))
		if err := os.Mkdir(stage, 0o755); err != nil {
			return fmt.Errorf("create staging directory: %w", err)
		}
		wanted[p.Artifact.entry()] = extractTarget{artifact: p.Artifact, dest: filepath.Join(stage, p.Artifact.BinaryName)}
	}
	m.logf("extracting %s", strings.Join(names, " and "))
	if err := extractBinaries(archive, a.URL, wanted); err != nil {
		return fmt.Errorf("extract from %s: %w", a.URL, err)
	}
	if err := os.Remove(archive); err != nil {
		return fmt.Errorf("remove downloaded archive: %w", err)
	}

	for i, p := range pending {
		pa := p.Artifact
		staged := filepath.Join(tmp, fmt.Sprintf("install-%d", i), pa.BinaryName)
		line, err := versionLine(ctx, staged)
		if err != nil {
			if pa.GOOS == "linux" {
				return fmt.Errorf("check downloaded %s: %w; the managed Linux build needs glibc 2.28 or newer, so on other systems install %s on the PATH or set --%s", pa.Tool, err, pa.Tool, pa.Tool)
			}
			return fmt.Errorf("check downloaded %s: %w", pa.Tool, err)
		}
		if !strings.HasPrefix(line, pa.VersionPrefix) {
			return fmt.Errorf("downloaded %s reports %q, expected %q", pa.Tool, line, pa.VersionPrefix)
		}
	}
	for i, p := range pending {
		if err := os.MkdirAll(p.toolDir(), 0o755); err != nil {
			return fmt.Errorf("create tool cache: %w", err)
		}
		if err := os.Rename(filepath.Join(tmp, fmt.Sprintf("install-%d", i)), p.InstallDir()); err != nil {
			return fmt.Errorf("install %s into cache: %w", p.Artifact.Tool, err)
		}
		m.logf("installed %s %s at %s", p.Artifact.Tool, p.Artifact.Version, p.BinaryPath())
	}
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
		return fmt.Errorf("download %s: %w; check the network connection or set --%s or %s", a.URL, err, a.Tool, EnvVar(a.Tool))
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

type extractTarget struct {
	artifact Artifact
	dest     string
}

// extractBinaries copies the expected entries out of a verified archive and
// checks their sizes and hashes. No other entry is written to disk. ZIP and
// .tar.xz archives are supported; the format follows the URL.
func extractBinaries(archive, url string, wanted map[string]extractTarget) error {
	found := map[string]bool{}
	var err error
	switch {
	case strings.HasSuffix(url, ".zip"):
		err = extractZip(archive, wanted, found)
	case strings.HasSuffix(url, ".tar.xz"):
		err = extractTarXZ(archive, wanted, found)
	default:
		err = fmt.Errorf("unsupported archive format")
	}
	if err != nil {
		return err
	}
	for name := range wanted {
		if !found[name] {
			return fmt.Errorf("entry %q not found in archive", name)
		}
	}
	return nil
}

func extractZip(archive string, wanted map[string]extractTarget, found map[string]bool) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		t, ok := wanted[f.Name]
		if !ok || found[f.Name] {
			continue
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("entry %q is not a regular file", f.Name)
		}
		if f.UncompressedSize64 != uint64(t.artifact.BinarySize) {
			return fmt.Errorf("entry %q is %d bytes, expected %d", f.Name, f.UncompressedSize64, t.artifact.BinarySize)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeVerified(rc, f.Name, t)
		rc.Close()
		if err != nil {
			return err
		}
		found[f.Name] = true
	}
	return nil
}

func extractTarXZ(archive string, wanted map[string]extractTarget, found map[string]bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	xr, err := xz.NewReader(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return err
	}
	tr := tar.NewReader(xr)
	for len(found) < len(wanted) {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		t, ok := wanted[h.Name]
		if !ok || found[h.Name] {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("entry %q is not a regular file", h.Name)
		}
		if h.Size != t.artifact.BinarySize {
			return fmt.Errorf("entry %q is %d bytes, expected %d", h.Name, h.Size, t.artifact.BinarySize)
		}
		if err := writeVerified(tr, h.Name, t); err != nil {
			return err
		}
		found[h.Name] = true
	}
	return nil
}

// writeVerified writes one entry to its destination and checks its size
// and SHA-256.
func writeVerified(r io.Reader, name string, t extractTarget) error {
	a := t.artifact
	out, err := os.OpenFile(t.dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, a.BinarySize+1))
	if err != nil {
		return err
	}
	if n != a.BinarySize {
		return fmt.Errorf("entry %q produced %d bytes, expected %d", name, n, a.BinarySize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.BinarySHA256 {
		return fmt.Errorf("entry %q SHA-256 mismatch: got %s, expected %s", name, got, a.BinarySHA256)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(t.dest, 0o755)
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
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
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
