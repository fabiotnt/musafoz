package cli

import (
	"fmt"
	"io"
	"os"
)

// progress renders download progress on stderr. On a terminal it redraws
// one line; otherwise it prints a line every 25 percent.
type progress struct {
	w        io.Writer
	terminal bool
	last     int
	active   bool
}

func newProgress(w io.Writer) *progress {
	p := &progress{w: w, last: -1}
	if f, ok := w.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			p.terminal = true
		}
	}
	return p
}

func (p *progress) update(done, total int64) {
	if total <= 0 {
		return
	}
	pct := int(done * 100 / total)
	step := 1
	if !p.terminal {
		step = 25
	}
	if p.last >= 0 && pct/step == p.last/step {
		return
	}
	p.last = pct
	msg := fmt.Sprintf("downloaded %3d%% (%.1f of %.1f MiB)", pct, mib(done), mib(total))
	if p.terminal {
		fmt.Fprintf(p.w, "\r%s", msg)
		p.active = true
	} else {
		fmt.Fprintln(p.w, msg)
	}
}

// finish ends a redrawn progress line so the next message starts cleanly.
func (p *progress) finish() {
	if p.active {
		fmt.Fprintln(p.w)
		p.active = false
	}
}

func mib(n int64) float64 { return float64(n) / (1 << 20) }
