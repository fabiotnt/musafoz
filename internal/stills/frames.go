package stills

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"

	"github.com/fabiotnt/musafoz/internal/media"
)

// FrameRequest selects frames of a video: the given times, or Random
// frames at random. Times and Random are mutually exclusive.
type FrameRequest struct {
	Times  []float64
	Random int
}

// Frame is one written frame.
type Frame struct {
	AtSeconds float64 `json:"at_seconds"`
	Path      string  `json:"path"`
}

func (r *Runner) tool(ctx context.Context, t *toolOnce, resolve func(context.Context) (string, error)) (string, error) {
	t.once.Do(func() {
		if resolve == nil {
			t.err = fmt.Errorf("no resolver")
			return
		}
		t.path, t.err = resolve(ctx)
	})
	return t.path, t.err
}

// inspect probes a video once per run.
func (r *Runner) inspect(ctx context.Context, s *source) (*media.Info, error) {
	r.mu.Lock()
	info := r.videos[s.path]
	r.mu.Unlock()
	if info != nil {
		return info, nil
	}
	if err := media.ValidateInput(s.path); err != nil {
		return nil, unwrapInspect(err)
	}
	ffprobe, err := r.tool(ctx, &r.ffprobe, r.ResolveFFprobe)
	if err != nil {
		return nil, err
	}
	info, err = media.Inspect(ctx, ffprobe, s.path)
	if err != nil {
		return nil, unwrapInspect(err)
	}
	if _, err := media.RequireDuration(info); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.videos == nil {
		r.videos = map[string]*media.Info{}
	}
	r.videos[s.path] = info
	r.mu.Unlock()
	return info, nil
}

func unwrapInspect(err error) error {
	if ie, ok := err.(*media.InspectError); ok {
		return ie.Err
	}
	return err
}

// Frames writes clean frames of one video, a local file or a URL. Every
// time is checked and every output is claimed before the first frame is
// extracted. Random times are drawn independently between zero and the
// start of the last frame, so two of them may select the same frame; a
// random time whose name is already used in this run gets an index. owner
// names the item in collision messages.
func (r *Runner) Frames(ctx context.Context, raw string, req FrameRequest, owner string) ([]Frame, error) {
	if len(req.Times) > 0 && req.Random > 0 {
		return nil, fmt.Errorf("give times or a random count, not both")
	}
	for _, t := range req.Times {
		if t < 0 || math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, fmt.Errorf("time %v is not zero or more", t)
		}
	}
	s, err := r.resolveSource(ctx, raw)
	if err != nil {
		return nil, err
	}
	info, err := r.inspect(ctx, s)
	if err != nil {
		return nil, err
	}
	duration, _ := media.RequireDuration(info)
	last := info.LastFrameTime()
	for _, t := range req.Times {
		if err := media.CheckFrameTime(t, last, duration); err != nil {
			return nil, err
		}
	}
	times := req.Times
	random := len(times) == 0
	if random {
		times = r.randomTimes(max(1, req.Random), last)
	}

	// Given times keep their names, so a repeated time is a conflict.
	// Random times that round to a name already used in this run get an
	// index after the time instead: _00-00-01-250-2.
	name := func(i, k int) (string, error) {
		middle := "_" + NameTime(times[i])
		if k > 1 {
			middle += fmt.Sprintf("-%d", k)
		}
		return r.outputPath(s, middle)
	}
	var paths []string
	if random {
		paths, err = r.claimIndexed(len(times), name, owner)
	} else {
		paths = make([]string, len(times))
		for i := range times {
			if paths[i], err = name(i, 1); err != nil {
				return nil, err
			}
		}
		err = r.claim(paths, owner)
	}
	if err != nil {
		return nil, err
	}
	frames := make([]Frame, len(times))
	for i, t := range times {
		frames[i] = Frame{AtSeconds: t, Path: paths[i]}
	}
	ffmpeg, err := r.tool(ctx, &r.ffmpeg, r.ResolveFFmpeg)
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "musafoz-image-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	for i, f := range frames {
		r.logf("%s: extracting the frame at %s", s.label, media.Timestamp(f.AtSeconds))
		img, err := ExtractFrame(ctx, ffmpeg, s.path, f.AtSeconds, work)
		if err != nil {
			return frames[:i], err
		}
		if err := r.writeImage(img, f.Path); err != nil {
			return frames[:i], err
		}
	}
	return frames, nil
}

// NameTime formats a time for file names: HH-MM-SS, followed by -mmm when
// it has milliseconds, such as 00-02-13-500. Colons are not allowed in
// file names on Windows.
func NameTime(t float64) string {
	return strings.NewReplacer(":", "-", ".", "-").Replace(media.Timestamp(t))
}

// randomTimes draws n times uniformly between zero and last, the start of
// the last frame, rounded down to milliseconds and sorted. Draws are
// independent, so two of them can select the same frame.
func (r *Runner) randomTimes(n int, last float64) []float64 {
	random := r.Random
	if random == nil {
		random = rand.Float64
	}
	times := make([]float64, n)
	for i := range times {
		times[i] = math.Max(0, math.Floor(random()*last*1000)/1000)
	}
	slices.Sort(times)
	return times
}
