//go:build unix

// Package fslock provides an exclusive advisory file lock shared by
// musafoz processes.
package fslock

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// Lock takes an exclusive advisory lock on path, creating the file if
// needed. It calls onWait once if another holder has the lock, then retries
// until the lock is acquired or ctx is done. The lock is released by the
// returned function, or by the kernel when the process exits, so a crashed
// run never blocks later runs. The lock file itself is left in place.
func Lock(ctx context.Context, path string, onWait func()) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	waited := false
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if !waited {
			waited = true
			if onWait != nil {
				onWait()
			}
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
