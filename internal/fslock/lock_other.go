//go:build !unix

// Package fslock provides an exclusive advisory file lock shared by
// musafoz processes.
package fslock

import "context"

// Lock is a no-op on platforms without flock. No managed builds exist for
// them yet.
func Lock(ctx context.Context, path string, onWait func()) (func(), error) {
	return func() {}, nil
}
