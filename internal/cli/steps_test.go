package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestStepProgressLine(t *testing.T) {
	var buf bytes.Buffer
	clock := time.Unix(0, 0)
	p := &stepProgress{w: &buf, terminal: true, now: func() time.Time { return clock }}

	p.Start("encoding preview video", 100, "frames")
	clock = clock.Add(12 * time.Second)
	p.Set(45)
	if got := p.line(); got != "encoding preview video [#########-----------]  45% 45/100 frames  12 s" {
		t.Errorf("line = %q", got)
	}
	// A log line clears the progress line, prints, and redraws below it.
	p.logf("hello %d", 1)
	out := buf.String()
	i := strings.LastIndex(out, "hello 1\n")
	if i < 0 || !strings.Contains(out[i:], "\rencoding preview video [") {
		t.Errorf("log line was not followed by a redraw:\n%q", out)
	}
	p.Done()
	if !strings.HasSuffix(buf.String(), "\r") || p.active {
		t.Errorf("Done did not clear the line: %q", buf.String())
	}

	p.Start("building mosaic", 0, "")
	clock = clock.Add(3 * time.Second)
	if got := p.line(); got != `building mosaic - 3 s` && !strings.HasPrefix(got, "building mosaic ") {
		t.Errorf("indeterminate line = %q", got)
	}
	p.Done()

	p.Start("remuxing", 15363, "ms")
	p.Set(15363)
	if got := p.line(); !strings.HasPrefix(got, "remuxing [####################] 100%  ") {
		t.Errorf("remux line = %q", got)
	}
	p.Done()
}

// Off a terminal, progress prints nothing and log lines are unchanged.
func TestStepProgressNotTerminal(t *testing.T) {
	var buf bytes.Buffer
	p := newStepProgress(&buf)
	p.Start("extracting frames", 10, "frames")
	p.Set(5)
	p.logf("line")
	p.Done()
	if buf.String() != "line\n" {
		t.Errorf("output = %q", buf.String())
	}
}
