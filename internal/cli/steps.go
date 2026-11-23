package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// stepProgress draws a progress line on a terminal for preview steps that
// run without log output, and keeps it below the log lines. It redraws the
// line with a carriage return and pads it with spaces instead of using
// terminal escape codes, so it also works in a plain Windows console. When
// standard error is not a terminal it prints nothing extra.
type stepProgress struct {
	mu       sync.Mutex
	w        io.Writer
	terminal bool
	now      func() time.Time

	active bool
	label  string
	unit   string
	total  int64
	done   int64
	start  time.Time
	width  int // length of the line on screen
	drawn  time.Time
	stop   chan struct{}
}

func newStepProgress(w io.Writer) *stepProgress {
	return &stepProgress{w: w, terminal: newProgress(w).terminal, now: time.Now}
}

// logf prints a log line, moving the progress line below it.
func (p *stepProgress) logf(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	fmt.Fprintf(p.w, format+"\n", args...)
	if p.active {
		p.draw()
	}
}

// Start implements preview.Progress.
func (p *stepProgress) Start(label string, total int64, unit string) {
	if !p.terminal {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active, p.label, p.unit, p.total, p.done = true, label, unit, total, 0
	p.start = p.now()
	p.stop = make(chan struct{})
	p.draw()
	// Redraw every half second so the elapsed time keeps moving while
	// nothing is reported.
	go func(stop chan struct{}) {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				p.mu.Lock()
				if p.active {
					p.draw()
				}
				p.mu.Unlock()
			}
		}
	}(p.stop)
}

// Set implements preview.Progress.
func (p *stepProgress) Set(done int64) {
	if !p.terminal {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active || done < p.done {
		return
	}
	p.done = done
	if p.now().Sub(p.drawn) >= 100*time.Millisecond || (p.total > 0 && done >= p.total) {
		p.draw()
	}
}

// Done implements preview.Progress.
func (p *stepProgress) Done() {
	if !p.terminal {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active {
		return
	}
	p.active = false
	close(p.stop)
	p.clear()
}

// line renders the progress line, for example
// "encoding preview video [#########-----------]  45% 45/100 frames  12 s".
func (p *stepProgress) line() string {
	elapsed := int(p.now().Sub(p.start).Seconds())
	if p.total <= 0 {
		spin := `|/-\`[elapsed%4]
		return fmt.Sprintf("%s %c %d s", p.label, spin, elapsed)
	}
	done := min(p.done, p.total)
	const width = 20
	filled := int(done * width / p.total)
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	count := ""
	if p.unit != "" && p.unit != "ms" {
		count = fmt.Sprintf(" %d/%d %s", done, p.total, p.unit)
	}
	return fmt.Sprintf("%s [%s] %3d%%%s  %d s", p.label, bar, done*100/p.total, count, elapsed)
}

func (p *stepProgress) draw() {
	s := p.line()
	pad := max(0, p.width-len(s))
	fmt.Fprintf(p.w, "\r%s%s", s, strings.Repeat(" ", pad))
	p.width = len(s)
	p.drawn = p.now()
}

func (p *stepProgress) clear() {
	if p.width > 0 {
		fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.width))
		p.width = 0
	}
}
