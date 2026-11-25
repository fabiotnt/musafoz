package stills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fabiotnt/musafoz/internal/fetch"
	"github.com/fabiotnt/musafoz/internal/media"
)

// Entry is one non-empty line of a list.
type Entry struct {
	Line   int
	Fields []string
}

// MaxListBytes limits the size of a list file.
const MaxListBytes = 16 << 20

// ReadList reads a UTF-8 list file. Lines end in LF or CRLF; a leading
// byte order mark is ignored. Lines that are empty or hold only spaces and
// tabs are skipped. Fields are separated by TAB, and spaces around each
// field are trimmed, so paths may contain inner spaces.
func ReadList(path string) ([]Entry, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, expected a list file", path)
	}
	if fi.Size() > MaxListBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxListBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	var out []Entry
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		for j := range fields {
			fields[j] = strings.TrimSpace(fields[j])
		}
		out = append(out, Entry{Line: i + 1, Fields: fields})
	}
	return out, nil
}

// ResolveListSource returns a list source as a URL or as a path relative
// to the list's directory.
func ResolveListSource(listDir, s string) string {
	if fetch.IsURL(s) || filepath.IsAbs(s) {
		return s
	}
	return filepath.Join(listDir, s)
}

// LineError is the failure of one list line.
type LineError struct {
	Line   int
	Source string
	Err    error
}

func (e *LineError) Error() string {
	if e.Source == "" {
		return fmt.Sprintf("line %d: %v", e.Line, e.Err)
	}
	return fmt.Sprintf("line %d: %s: %v", e.Line, e.Source, e.Err)
}

func (e *LineError) Unwrap() error { return e.Err }

// BatchError reports that some lines failed. Each failure was already
// passed to the batch's OnError.
type BatchError struct {
	Failed, Total int
}

func (e *BatchError) Error() string {
	return fmt.Sprintf("%d of %d list lines failed", e.Failed, e.Total)
}

// Batch callbacks.
type BatchHooks struct {
	// OnOutput receives each written file.
	OnOutput func(line int, path string)
	// OnError receives each failed line.
	OnError func(*LineError)
}

func label(src string) string {
	if fetch.IsURL(src) {
		return fetch.Redact(src)
	}
	return src
}

// ThumbBatch writes a thumbnail for each line of a list: one image source
// per line. A failed line is reported and the others continue; the result
// is a *BatchError when any line failed.
func (r *Runner) ThumbBatch(ctx context.Context, listPath string, h BatchHooks) error {
	entries, err := ReadList(listPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(listPath)
	failed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		src := ResolveListSource(dir, e.Fields[0])
		fail := func(err error) {
			failed++
			if h.OnError != nil {
				h.OnError(&LineError{Line: e.Line, Source: label(e.Fields[0]), Err: err})
			}
		}
		if len(e.Fields) > 1 {
			fail(errors.New("the line has a TAB; thumbnail lists take one source per line"))
			continue
		}
		out, err := r.Thumb(ctx, src, fmt.Sprintf("line %d", e.Line))
		if err != nil {
			fail(err)
			continue
		}
		if h.OnOutput != nil {
			h.OnOutput(e.Line, out)
		}
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s lists no sources", listPath)
	}
	if failed > 0 {
		return &BatchError{Failed: failed, Total: len(entries)}
	}
	return nil
}

// FrameBatch extracts frames for each line of a TSV list: a video source,
// then optionally a TAB and a time. An empty or missing time picks a
// random frame; a malformed time fails that line. Lines that repeat a
// source extract more frames of it, reusing its download and inspection.
// Random times are drawn independently; a random time whose name is
// already used in the run gets an index (see Frames).
func (r *Runner) FrameBatch(ctx context.Context, listPath string, h BatchHooks) error {
	entries, err := ReadList(listPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(listPath)
	failed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		src := ResolveListSource(dir, e.Fields[0])
		fail := func(err error) {
			failed++
			if h.OnError != nil {
				h.OnError(&LineError{Line: e.Line, Source: label(e.Fields[0]), Err: err})
			}
		}
		if e.Fields[0] == "" {
			fail(errors.New("the source column is empty"))
			continue
		}
		if len(e.Fields) > 2 {
			fail(fmt.Errorf("the line has %d columns; expected a source and an optional time", len(e.Fields)))
			continue
		}
		req := FrameRequest{Random: 1}
		if len(e.Fields) == 2 && e.Fields[1] != "" {
			t, err := media.ParseTimestamp(e.Fields[1])
			if err != nil {
				fail(err)
				continue
			}
			req = FrameRequest{Times: []float64{t}}
		}
		frames, err := r.Frames(ctx, src, req, fmt.Sprintf("line %d", e.Line))
		if h.OnOutput != nil {
			for _, f := range frames {
				h.OnOutput(e.Line, f.Path)
			}
		}
		if err != nil {
			fail(err)
		}
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s lists no sources", listPath)
	}
	if failed > 0 {
		return &BatchError{Failed: failed, Total: len(entries)}
	}
	return nil
}
