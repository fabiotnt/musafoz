package preview

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ConflictError lists outputs that already exist.
type ConflictError struct {
	Paths []string
}

func (e *ConflictError) Error() string {
	return "outputs already exist (use --overwrite to replace them): " + strings.Join(e.Paths, ", ")
}

// published records one output moved into place, so it can be undone.
type published struct {
	dst string
	// backup holds the previous version, or is empty when dst was new.
	backup string
}

// publisher moves staged outputs into place one file at a time. Each move
// is atomic when the file system supports hard links; rollback restores the
// whole set.
type publisher struct {
	backupDir string
	overwrite bool
	done      []published
}

// test hooks
var (
	beforePublish = func(dst string) error { return nil }
	beforeRestore = func(dst string) error { return nil }
)

func (p *publisher) publish(src, dst string) error {
	if err := beforePublish(dst); err != nil {
		return err
	}
	_, err := os.Lstat(dst)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if exists && !p.overwrite {
		return &ConflictError{Paths: []string{dst}}
	}
	if !exists {
		if err := placeNew(src, dst); err != nil {
			return err
		}
		p.done = append(p.done, published{dst: dst})
		return nil
	}

	if err := os.MkdirAll(p.backupDir, 0o755); err != nil {
		return err
	}
	backup := filepath.Join(p.backupDir, filepath.Base(dst))
	if err := os.Link(dst, backup); err == nil {
		// The old file stays reachable through the backup link while the
		// new file atomically replaces it.
		if err := os.Rename(src, dst); err != nil {
			os.Remove(backup)
			return err
		}
	} else {
		// No hard links (for example exFAT): move the old file aside first.
		if err := os.Rename(dst, backup); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			if rerr := os.Rename(backup, dst); rerr != nil {
				return fmt.Errorf("%w; restoring %s also failed: %v", err, dst, rerr)
			}
			return err
		}
	}
	p.done = append(p.done, published{dst: dst, backup: backup})
	return nil
}

// placeNew moves src to dst without ever replacing an existing dst.
func placeNew(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil {
		return os.Remove(src)
	}
	if errors.Is(err, fs.ErrExist) {
		return &ConflictError{Paths: []string{dst}}
	}
	// No hard links: the output-set lock keeps other musafoz runs away, so
	// checking then renaming is safe from them.
	if _, err := os.Lstat(dst); err == nil {
		return &ConflictError{Paths: []string{dst}}
	}
	return os.Rename(src, dst)
}

// rollback undoes published outputs in reverse order. It returns an error
// listing every output it could not restore.
func (p *publisher) rollback() error {
	var failed []string
	for i := len(p.done) - 1; i >= 0; i-- {
		d := p.done[i]
		err := beforeRestore(d.dst)
		if err == nil {
			if d.backup == "" {
				err = os.Remove(d.dst)
			} else {
				err = os.Rename(d.backup, d.dst)
			}
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = append(failed, fmt.Sprintf("%s (%v)", d.dst, err))
		}
	}
	p.done = nil
	if len(failed) > 0 {
		return fmt.Errorf("could not restore %s", strings.Join(failed, "; "))
	}
	return nil
}
