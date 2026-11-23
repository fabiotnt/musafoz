//go:build !unix && !windows

// Package fslock provides an exclusive advisory file lock shared by
// musafoz processes.
package fslock

import (
	"context"
	"errors"
)

// Lock is not implemented on this platform. musafoz refuses to run without
// a working lock rather than risk concurrent writes to the same outputs.
func Lock(ctx context.Context, path string, onWait func()) (func(), error) {
	return nil, errors.New("file locking is not supported on this platform")
}
