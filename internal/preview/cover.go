package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"

	"github.com/fabiotnt/musafoz/internal/fslock"
	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/stills"
)

// CoverOptions configures a cover-only run.
type CoverOptions struct {
	Input string
	// At is the cover time in seconds. Nil picks one with media.CoverTime,
	// as the preview does.
	At        *float64
	Overwrite bool

	ResolveFFprobe func(context.Context) (string, error)
	ResolveFFmpeg  func(context.Context) (string, error)
	Logf           func(format string, args ...any)
	// Random returns a number in [0, 1). Nil uses math/rand/v2.
	Random func() float64
}

// CoverResult describes a written cover.
type CoverResult struct {
	Input          string  `json:"input"`
	Cover          string  `json:"cover"`
	CoverAtSeconds float64 `json:"cover_at_seconds"`
}

// Cover writes only <name>_cover.jpg beside the video, with the same
// name, time rules, FFmpeg JPEG extraction, output lock, and conflict
// policy as the preview cover. Other preview outputs are not touched.
func Cover(ctx context.Context, o CoverOptions) (*CoverResult, error) {
	fail := func(step string, err error) error { return &StepError{Path: o.Input, Step: step, Err: err} }
	logf := func(format string, args ...any) {
		if o.Logf != nil {
			o.Logf(format, args...)
		}
	}
	if c := o.At; c != nil && (*c < 0 || math.IsNaN(*c) || math.IsInf(*c, 0)) {
		return nil, fail(StepValidateOptions, fmt.Errorf("cover time must be zero or more, got %v", *c))
	}
	abs, err := filepath.Abs(o.Input)
	if err != nil {
		return nil, fail(StepValidateInput, err)
	}
	if err := media.ValidateInput(abs); err != nil {
		return nil, fail(StepValidateInput, unwrapInspect(err))
	}
	dir, base := splitInput(abs)
	out := OutputsFor(abs).Cover
	unlock, err := fslock.Lock(ctx, lockPath(dir, base), func() {
		logf("waiting for another musafoz run that writes %s outputs", base)
	})
	if err != nil {
		return nil, fail(StepLockOutputs, err)
	}
	defer unlock()
	if !o.Overwrite {
		if _, err := os.Lstat(out); err == nil {
			return nil, fail(StepCheckOutputs, &ConflictError{Paths: []string{out}})
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fail(StepCheckOutputs, err)
		}
	}

	ffprobe, err := o.ResolveFFprobe(ctx)
	if err != nil {
		return nil, fail(StepResolveFFprobe, err)
	}
	logf("inspecting %s", abs)
	info, err := media.Inspect(ctx, ffprobe, abs)
	if err != nil {
		return nil, fail(StepInspect, unwrapInspect(err))
	}
	duration, err := media.RequireDuration(info)
	if err != nil {
		return nil, fail(StepInspect, err)
	}
	last := info.LastFrameTime()
	res := &CoverResult{Input: abs, Cover: out}
	if o.At != nil {
		if err := media.CheckFrameTime(*o.At, last, duration); err != nil {
			return nil, fail(StepValidateOptions, fmt.Errorf("cover %w", err))
		}
		res.CoverAtSeconds = *o.At
	} else {
		random := o.Random
		if random == nil {
			random = rand.Float64
		}
		res.CoverAtSeconds = media.CoverTime(duration, last, random)
	}

	ffmpeg, err := o.ResolveFFmpeg(ctx)
	if err != nil {
		return nil, fail(StepResolveFFmpeg, err)
	}
	work, err := os.MkdirTemp("", "musafoz-cover-")
	if err != nil {
		return nil, fail(StepPrepare, err)
	}
	defer os.RemoveAll(work)
	how := "random"
	if o.At != nil {
		how = "given"
	}
	logf("generating cover at %s (%s)", media.Timestamp(res.CoverAtSeconds), how)
	tmp := filepath.Join(work, "cover.jpg")
	if err := media.ExtractCover(ctx, ffmpeg, abs, res.CoverAtSeconds, tmp); err != nil {
		return nil, fail(StepCover, err)
	}
	err = stills.WriteFile(out, o.Overwrite, func(w io.Writer) error {
		f, err := os.Open(tmp)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		var ce *stills.ConflictError
		if errors.As(err, &ce) {
			err = &ConflictError{Paths: ce.Paths}
		}
		return nil, fail(StepPublish, err)
	}
	return res, nil
}

// lockPath returns the lock file of an output set.
func lockPath(dir, base string) string {
	return filepath.Join(dir, "."+base+".musafoz.lock")
}
