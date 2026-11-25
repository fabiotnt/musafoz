package stills

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
)

// DefaultThumbSuffix is added to thumbnail names unless a prefix or
// another suffix is given.
const DefaultThumbSuffix = "_thumb"

// Thumb writes a thumbnail of one image, a local file or a URL. owner
// names the item in collision messages, such as "line 3". It returns the
// output path.
func (r *Runner) Thumb(ctx context.Context, raw, owner string) (string, error) {
	if r.Output.Size == nil {
		return "", fmt.Errorf("a size is required for thumbnails")
	}
	s, err := r.resolveSource(ctx, raw)
	if err != nil {
		return "", err
	}
	format, err := imaging.SniffFile(s.path)
	if err != nil {
		return "", err
	}
	if format == "" {
		return "", fmt.Errorf("not a supported image (JPEG, PNG, GIF, WebP, BMP, TIFF, or HEIC)")
	}
	if !imaging.Decodable(format) && format != imaging.FormatHEIF {
		return "", &imaging.UnsupportedError{Format: format}
	}
	out, err := r.outputPath(s, "")
	if err != nil {
		return "", err
	}
	if !s.isURL && sameFile(out, s.path) {
		return "", fmt.Errorf("the output %s would replace the source; use another suffix, prefix, or output directory", out)
	}
	if err := r.claim([]string{out}, owner); err != nil {
		return "", err
	}
	var pixels image.Image
	if format == imaging.FormatHEIF {
		if pixels, err = r.loadHEIF(ctx, s); err != nil {
			return "", err
		}
	} else {
		img, err := imaging.Load(s.path)
		if err != nil {
			return "", err
		}
		if img.Orientation > 1 {
			r.logf("%s: applied EXIF orientation %d (%s)", s.label, img.Orientation, imaging.OrientationLabel(img.Orientation))
		}
		pixels = img.Pixels
	}
	if err := r.writeImage(pixels, out); err != nil {
		return "", err
	}
	return out, nil
}

// loadHEIF decodes the primary image of a HEIF file with FFmpeg. The size,
// rotation, and mirroring come from the container (imaging.ParseHEIF).
// FFmpeg applies the rotation and mirroring while it decodes; the EXIF
// orientation of a HEIF file only repeats them and is not applied again.
// The decoded size is checked against the container, so a build that
// decodes a single tile or ignores the rotation fails with a clear error.
func (r *Runner) loadHEIF(ctx context.Context, s *source) (image.Image, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	h, err := imaging.ParseHEIF(data)
	if err != nil {
		return nil, err
	}
	if h.ItemType != "hvc1" && h.ItemType != "grid" {
		return nil, fmt.Errorf("the primary image is a %q item; only HEVC images (hvc1) and tile grids are supported", h.ItemType)
	}
	ffmpeg, err := r.tool(ctx, &r.ffmpeg, r.ResolveFFmpeg)
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "musafoz-heic-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	png := filepath.Join(work, "image.png")
	if err := media.DecodeImage(ctx, ffmpeg, s.path, png, h.Alpha); err != nil {
		return nil, fmt.Errorf("FFmpeg could not decode the HEIC image (FFmpeg 7.1 or newer with the HEVC decoder is needed): %w", err)
	}
	img, err := imaging.Load(png)
	if err != nil {
		return nil, err
	}
	b := img.Pixels.Bounds()
	if w, hh := h.DisplaySize(); b.Dx() != w || b.Dy() != hh {
		return nil, fmt.Errorf("FFmpeg decoded a %dx%d image, but the primary HEIC image is %dx%d after its %s; "+
			"this FFmpeg build does not read this HEIF layout (FFmpeg 7.1 or newer is needed for tile grids and container rotation)",
			b.Dx(), b.Dy(), w, hh, imaging.HEIFLabel(h.Transforms))
	}
	pixels := img.Pixels
	if c := h.DecoderCorrection(); c != 1 {
		// FFmpeg rotated before mirroring; this file mirrors first.
		pixels = imaging.Orient(pixels, c)
	}
	if len(h.Transforms) > 0 {
		r.logf("%s: applied the HEIC transforms: %s", s.label, imaging.HEIFLabel(h.Transforms))
	}
	return pixels, nil
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}
