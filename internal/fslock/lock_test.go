package fslock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary act as a second process that holds a lock.
func TestMain(m *testing.M) {
	if path := os.Getenv("FSLOCK_HOLD"); path != "" {
		unlock, err := Lock(context.Background(), path, nil)
		if err != nil {
			os.Stdout.WriteString("error: " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Stdout.WriteString("locked\n")
		// Hold until stdin closes.
		buf := make([]byte, 1)
		os.Stdin.Read(buf)
		unlock()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A lock held by another process blocks Lock until it is released, and
// cancellation stops the wait.
func TestLockAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".clip.musafoz.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "FSLOCK_HOLD="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Wait() })
	line := make([]byte, 64)
	n, _ := stdout.Read(line)
	if !strings.HasPrefix(string(line[:n]), "locked") {
		t.Fatalf("holder: %q", line[:n])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	waited := false
	if _, err := Lock(ctx, path, func() { waited = true }); err == nil || !waited {
		t.Fatalf("lock held elsewhere: err = %v, waited = %v", err, waited)
	}

	done := make(chan error, 1)
	go func() {
		unlock, err := Lock(context.Background(), path, nil)
		if err == nil {
			unlock()
		}
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	stdin.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock was not acquired after the holder released it")
	}
}
