// Package stills writes still images: thumbnails of images and clean
// frames of videos, one at a time or from a list. It does not depend on any
// user interface.
package stills

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fabiotnt/musafoz/internal/fetch"
	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
)

// Output says where and how images are written.
type Output struct {
	// Dir receives the images. It is created when missing. Ignored with
	// Alongside.
	Dir string
	// Alongside writes each image beside its local source.
	Alongside bool
	// Prefix and Suffix are added around the source base name.
	Prefix, Suffix string
	// Format is imaging.OutputJPEG or imaging.OutputPNG.
	Format string
	// Quality is the JPEG quality, 1 to 100; 0 means imaging.JPEGQuality.
	Quality int
	// Size, when set, resizes with Mode (imaging.ModeFit or ModeFill).
	Size *imaging.Size
	Mode string
	// Overwrite replaces existing files; otherwise they are conflicts.
	Overwrite bool
}

// Runner holds what a set of operations shares: downloads, resolved tools,
// inspected videos, and the outputs already claimed, so that two items of
// one run never write the same file.
type Runner struct {
	Output Output
	// Fetcher downloads URL sources. Nil rejects URLs.
	Fetcher *fetch.Fetcher
	// ResolveFFprobe and ResolveFFmpeg are called once, when a video is
	// first needed.
	ResolveFFprobe func(context.Context) (string, error)
	ResolveFFmpeg  func(context.Context) (string, error)
	// Logf receives progress messages. It may be nil.
	Logf func(format string, args ...any)
	// Random returns numbers in [0, 1). Nil uses math/rand/v2.
	Random func() float64

	mu       sync.Mutex
	claimed  map[string]string
	ffprobe  toolOnce
	ffmpeg   toolOnce
	videos   map[string]*media.Info
	dirReady map[string]bool
	// web holds the web results of this run by SHA-256.
	web map[string]*WebResult
}

type toolOnce struct {
	once sync.Once
	path string
	err  error
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// ConflictError lists outputs that already exist.
type ConflictError struct {
	Paths []string
}

func (e *ConflictError) Error() string {
	return "outputs already exist (use --overwrite to replace them): " + strings.Join(e.Paths, ", ")
}

// source is a resolved input.
type source struct {
	// raw is the item as given; label is safe to print (no URL query).
	raw, label string
	// path is the local file to read, downloaded for URLs.
	path string
	// base is the output base name, without extension.
	base string
	// dir is the directory of a local source.
	dir   string
	isURL bool
}

// resolveSource checks a local source or downloads a URL. kind is "image"
// or "video" for messages.
func (r *Runner) resolveSource(ctx context.Context, raw string) (*source, error) {
	if fetch.IsURL(raw) {
		s := &source{raw: raw, label: fetch.Redact(raw), isURL: true}
		if r.Output.Alongside {
			return s, errors.New("--alongside works only with local files, not URLs")
		}
		if r.Fetcher == nil {
			return s, errors.New("URL sources are not supported here")
		}
		p, err := r.Fetcher.Fetch(ctx, raw)
		if err != nil {
			return s, err
		}
		name := fetch.Name(raw)
		s.path, s.base = p, strings.TrimSuffix(name, filepath.Ext(name))
		if s.base == "" {
			s.base = name
		}
		return s, nil
	}
	abs, err := filepath.Abs(raw)
	s := &source{raw: raw, label: raw}
	if err != nil {
		return s, err
	}
	fi, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, errors.New("file does not exist")
	case err != nil:
		return s, err
	case fi.IsDir():
		return s, errors.New("is a directory, expected a file")
	case !fi.Mode().IsRegular():
		return s, errors.New("is not a regular file")
	}
	name := filepath.Base(abs)
	s.path, s.dir, s.base = abs, filepath.Dir(abs), strings.TrimSuffix(name, filepath.Ext(name))
	if s.base == "" {
		s.base = name
	}
	return s, nil
}

// outputPath returns the output file for a source and a name part placed
// between the base name and the suffix.
func (r *Runner) outputPath(s *source, middle string) (string, error) {
	dir := r.Output.Dir
	if r.Output.Alongside {
		dir = s.dir
	}
	if dir == "" {
		dir = "."
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	name := r.Output.Prefix + s.base + middle + r.Output.Suffix + imaging.Extension(r.Output.Format)
	if name != fetch.SafeName(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("output name %q contains characters that are not allowed in file names", name)
	}
	return filepath.Join(dir, name), nil
}

// claim reserves output paths for one item. It fails when another item of
// this run claimed one of them, or, without Overwrite, when one exists.
// owner describes the item, such as "line 4".
func (r *Runner) claim(paths []string, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed == nil {
		r.claimed = map[string]string{}
	}
	key := func(p string) string { return strings.ToLower(p) }
	seen := map[string]bool{}
	for _, p := range paths {
		if prev, ok := r.claimed[key(p)]; ok {
			return fmt.Errorf("output %s is also written by %s", p, prev)
		}
		if seen[key(p)] {
			return fmt.Errorf("output %s would be written twice", p)
		}
		seen[key(p)] = true
	}
	if !r.Output.Overwrite {
		var conflicts []string
		for _, p := range paths {
			if _, err := os.Lstat(p); err == nil {
				conflicts = append(conflicts, p)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if len(conflicts) > 0 {
			return &ConflictError{Paths: conflicts}
		}
	}
	for _, p := range paths {
		r.claimed[key(p)] = owner
	}
	return nil
}

// claimIndexed claims n output paths for random frames. name(i, k)
// builds the path of item i: k is 1 for the plain name and 2, 3, and so on
// for indexed names, used when the plain name is already claimed in this
// run or by an earlier item. Existing files are still conflicts unless
// Overwrite is set.
func (r *Runner) claimIndexed(n int, name func(i, k int) (string, error), owner string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed == nil {
		r.claimed = map[string]string{}
	}
	key := func(p string) string { return strings.ToLower(p) }
	taken := map[string]bool{}
	paths := make([]string, n)
	for i := range n {
		for k := 1; ; k++ {
			if k > maxIndex {
				return nil, fmt.Errorf("more than %d outputs share the name of %s", maxIndex, paths[i])
			}
			p, err := name(i, k)
			if err != nil {
				return nil, err
			}
			if _, ok := r.claimed[key(p)]; !ok && !taken[key(p)] {
				paths[i] = p
				taken[key(p)] = true
				break
			}
			paths[i] = p
		}
	}
	if !r.Output.Overwrite {
		var conflicts []string
		for _, p := range paths {
			if _, err := os.Lstat(p); err == nil {
				conflicts = append(conflicts, p)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
		if len(conflicts) > 0 {
			return nil, &ConflictError{Paths: conflicts}
		}
	}
	for _, p := range paths {
		r.claimed[key(p)] = owner
	}
	return paths, nil
}

// maxIndex bounds the indexes added to names of random frames.
const maxIndex = 10000

// writeImage resizes img with the output size and mode, converts it to
// sRGB with cs when cs is not nil, encodes it, and places it at dst. The
// color conversion runs after the resize, on fewer pixels; resizing in the
// source space and converting after gives the same result within rounding.
func (r *Runner) writeImage(img image.Image, dst string, cs *imaging.ColorSpace) error {
	if err := r.ensureDir(filepath.Dir(dst)); err != nil {
		return err
	}
	if r.Output.Size != nil {
		img = imaging.Resize(img, *r.Output.Size, r.Output.Mode)
	}
	img = cs.ToSRGB(img)
	return WriteFile(dst, r.Output.Overwrite, func(w io.Writer) error {
		return imaging.Encode(w, img, r.Output.Format, r.Output.Quality)
	})
}

func (r *Runner) ensureDir(dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dirReady[dir] {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if r.dirReady == nil {
		r.dirReady = map[string]bool{}
	}
	r.dirReady[dir] = true
	return nil
}

// WriteFile writes a file through a temporary file in the same directory
// and then puts it in place. Without overwrite an existing file is never
// replaced, even if it appears while writing: the file is placed with a
// hard link, which fails when the name exists. With overwrite it replaces
// the file with an atomic rename. On failure nothing is left behind.
func WriteFile(dst string, overwrite bool, write func(io.Writer) error) error {
	dir, name := filepath.Split(dst)
	tmp, err := os.CreateTemp(dir, "."+name+".musafoz-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return PlaceFile(tmpName, dst, overwrite)
}

// PlaceFile moves a finished temporary file in the directory of dst to dst,
// with the same rules as WriteFile: without overwrite an existing file is
// a conflict, even one that appears meanwhile.
func PlaceFile(tmpName, dst string, overwrite bool) error {
	defer os.Remove(tmpName)
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if overwrite {
		return os.Rename(tmpName, dst)
	}
	err := os.Link(tmpName, dst)
	if errors.Is(err, fs.ErrExist) {
		return &ConflictError{Paths: []string{dst}}
	}
	if err == nil {
		return nil
	}
	// No hard links on this file system: check, then rename.
	if _, serr := os.Lstat(dst); serr == nil {
		return &ConflictError{Paths: []string{dst}}
	}
	return os.Rename(tmpName, dst)
}

// ExtractFrame decodes the frame shown at the given time, in seconds, as a
// clean image: displayed orientation, displayed aspect ratio with square
// pixels, source resolution, and no overlay. It uses the seek and
// normalization of the covers (media.ExtractCover) but writes a lossless
// PNG, held in workDir, so musafoz image can resize and choose the format.
func ExtractFrame(ctx context.Context, ffmpeg, input string, at float64, workDir string) (image.Image, error) {
	f, err := os.CreateTemp(workDir, "frame-*.png")
	if err != nil {
		return nil, err
	}
	p := f.Name()
	f.Close()
	defer os.Remove(p)
	if err := media.ExtractStill(ctx, ffmpeg, input, at, p); err != nil {
		return nil, err
	}
	in, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	img, err := png.Decode(in)
	if err != nil {
		return nil, fmt.Errorf("frame at %s: %w", media.Timestamp(at), err)
	}
	return img, nil
}

// size returns the size of the local file of a source, or 0.
func (s *source) size() int64 {
	fi, err := os.Stat(s.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
