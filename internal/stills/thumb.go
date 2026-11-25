package stills

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"

	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
)

// DefaultThumbSuffix is added to thumbnail names unless a prefix or
// another suffix is given.
const DefaultThumbSuffix = "_thumb"

// ThumbResult describes a written thumbnail.
type ThumbResult struct {
	// Path is the output file.
	Path string
	// SourceWidth and SourceHeight are the displayed size of the source,
	// after its EXIF orientation or HEIF transforms.
	SourceWidth, SourceHeight int
	// Width and Height are the size of the output.
	Width, Height int
	// Date is the date of the picture from its EXIF block or XMP packet,
	// or nil.
	Date *imaging.Date
	// Color names the color profile converted to sRGB, or is empty when
	// the image was already sRGB or had no profile.
	Color string
}

// loaded is a decoded source image with what musafoz reads beside the
// pixels.
type loaded struct {
	format string
	// pixels are in the displayed orientation, in the source color space.
	pixels image.Image
	exif   *imaging.EXIF
	xmp    map[string]string
	// color converts the pixels to sRGB; nil when they are sRGB already,
	// the file has no profile, or its profile is not supported (logged).
	color *imaging.ColorSpace
	// info is the inspection of the source: sizes, orientation, and every
	// EXIF tag, for metadata files. heif is set for HEIF sources.
	info *imaging.Info
}

// loadImage decodes a local image file of a supported format.
func (r *Runner) loadImage(ctx context.Context, s *source, format string) (*loaded, error) {
	var (
		l       = &loaded{format: format}
		icc     []byte
		nclx    *imaging.NCLX
		xmpData []byte
	)
	if format == imaging.FormatHEIF {
		h, pixels, exif, err := r.loadHEIF(ctx, s)
		if err != nil {
			return nil, err
		}
		l.pixels, l.exif = pixels, exif
		icc, nclx, xmpData = h.ICC, h.NCLX, h.XMP
		l.xmp = imaging.ParseXMP(xmpData)
		l.info = imaging.HEIFInfo(h, exif, s.label, s.size())
	} else {
		data, err := os.ReadFile(s.path)
		if err != nil {
			return nil, err
		}
		img, err := imaging.Decode(data)
		if err != nil {
			return nil, err
		}
		if img.Orientation > 1 {
			r.logf("%s: applied EXIF orientation %d (%s)", s.label, img.Orientation, imaging.OrientationLabel(img.Orientation))
		}
		l.pixels, l.exif, l.xmp, icc = img.Pixels, img.EXIF, img.XMP, img.ICC
		l.info = imaging.ImageInfo(img, s.label, int64(len(data)))
	}
	var err error
	switch {
	case icc != nil:
		l.color, err = imaging.ParseICC(icc)
	case nclx != nil:
		l.color, err = imaging.NCLXColorSpace(nclx)
	}
	if err != nil {
		r.logf("%s: colors kept as stored: %v", s.label, err)
		l.color = nil
	}
	if l.color != nil && l.color.IsSRGB {
		l.color = nil
	}
	if l.color != nil {
		l.info.ColorProfile = l.color.Name
	} else if icc != nil {
		l.info.ColorProfile = "sRGB"
	}
	return l, nil
}

// Thumb writes a thumbnail of one image, a local file or a URL. owner
// names the item in collision messages, such as "line 3". The source is
// read once: the size and date come from the same read as the pixels.
func (r *Runner) Thumb(ctx context.Context, raw, owner string) (*ThumbResult, error) {
	if r.Output.Size == nil {
		return nil, fmt.Errorf("a size is required for thumbnails")
	}
	s, err := r.resolveSource(ctx, raw)
	if err != nil {
		return nil, err
	}
	format, err := imaging.SniffFile(s.path)
	if err != nil {
		return nil, err
	}
	if format == "" {
		return nil, fmt.Errorf("not a supported image (JPEG, PNG, GIF, WebP, BMP, TIFF, or HEIC)")
	}
	if !imaging.Decodable(format) && format != imaging.FormatHEIF {
		return nil, &imaging.UnsupportedError{Format: format}
	}
	out, err := r.outputPath(s, "")
	if err != nil {
		return nil, err
	}
	if !s.isURL && sameFile(out, s.path) {
		return nil, fmt.Errorf("the output %s would replace the source; use another suffix, prefix, or output directory", out)
	}
	if err := r.claim([]string{out}, owner); err != nil {
		return nil, err
	}
	l, err := r.loadImage(ctx, s, format)
	if err != nil {
		return nil, err
	}
	if err := r.writeImage(l.pixels, out, l.color); err != nil {
		return nil, err
	}
	b := l.pixels.Bounds()
	res := &ThumbResult{Path: out, SourceWidth: b.Dx(), SourceHeight: b.Dy(), Date: imaging.SelectDate(l.exif, l.xmp)}
	if l.color != nil {
		res.Color = l.color.Name
	}
	res.Width, res.Height = imaging.RenderedSize(b.Dx(), b.Dy(), r.Output.Size, r.Output.Mode)
	return res, nil
}

// loadHEIF decodes the primary image of a HEIF file. The container is read
// in Go (imaging.ParseHEIF): the size, the grid layout, the alpha plane,
// and the rotation and mirroring. FFmpeg only decodes HEVC items, each
// selected by its id and without its automatic rotation; musafoz then
// places the tiles of a grid, crops it to its exact size, adds the alpha
// plane, and applies the transforms in the order the file lists them. The
// EXIF orientation of a HEIF file only repeats the transforms and is not
// applied. It also returns the EXIF block of the primary image, or nil.
func (r *Runner) loadHEIF(ctx context.Context, s *source) (*imaging.HEIF, image.Image, *imaging.EXIF, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil, nil, err
	}
	h, err := imaging.ParseHEIF(data)
	if err != nil {
		return nil, nil, nil, err
	}
	if h.ItemType != "hvc1" && h.ItemType != "grid" {
		return nil, nil, nil, fmt.Errorf("the primary image is a %q item; only HEVC images (hvc1) and tile grids are supported", h.ItemType)
	}
	ffmpeg, err := r.tool(ctx, &r.ffmpeg, r.ResolveFFmpeg)
	if err != nil {
		return nil, nil, nil, err
	}
	work, err := os.MkdirTemp("", "musafoz-heic-")
	if err != nil {
		return nil, nil, nil, err
	}
	defer os.RemoveAll(work)

	primary := &imaging.HEIFGrid{Rows: 1, Cols: 1, Width: h.Width, Height: h.Height, Tiles: []uint64{h.Primary}}
	if h.Grid != nil {
		primary = h.Grid
	}
	pixels, err := decodeLayout(ctx, ffmpeg, s.path, primary, h.Grid != nil, work, false)
	if err != nil {
		return nil, nil, nil, err
	}
	if b := pixels.Bounds(); b.Dx() != h.Width || b.Dy() != h.Height {
		return nil, nil, nil, fmt.Errorf("the primary HEIC image is %dx%d, but its container says %dx%d", b.Dx(), b.Dy(), h.Width, h.Height)
	}
	if h.AlphaItem != 0 {
		layout := h.AlphaGrid
		if layout == nil {
			layout = &imaging.HEIFGrid{Rows: 1, Cols: 1, Width: h.Width, Height: h.Height, Tiles: []uint64{h.AlphaItem}}
		}
		// The alpha plane only adds transparency: when it cannot be read,
		// the image is kept opaque.
		alpha, err := decodeLayout(ctx, ffmpeg, s.path, layout, h.AlphaGrid != nil, work, true)
		if err == nil {
			err = imaging.MergeAlpha(pixels, alpha)
		}
		if err != nil {
			r.logf("%s: the alpha plane was not applied: %v", s.label, err)
		}
	}
	out := h.Transform(pixels)
	if len(h.Transforms) > 0 {
		r.logf("%s: applied the HEIC transforms: %s", s.label, imaging.HEIFLabel(h.Transforms))
	}
	// A damaged EXIF block only loses the date; the image is still usable.
	var exif *imaging.EXIF
	if h.EXIF != nil {
		if e, err := imaging.ParseEXIF(h.EXIF); err == nil {
			exif = e
		} else {
			r.logf("%s: EXIF block could not be read: %v", s.label, err)
		}
	}
	return h, out, exif, nil
}

// decodeLayout decodes the items of a grid with FFmpeg and places them. A
// single item is passed as a 1x1 grid with isGrid false: it must decode to
// exactly the size of its container, so a decoder that rotates, crops, or
// scales it is reported instead of hidden.
func decodeLayout(ctx context.Context, ffmpeg, src string, g *imaging.HEIFGrid, isGrid bool, work string, gray bool) (*image.NRGBA, error) {
	files, err := media.DecodeItems(ctx, ffmpeg, src, g.Tiles, work, gray)
	if err != nil {
		return nil, fmt.Errorf("FFmpeg could not decode the HEIC image (FFmpeg 7.1 or newer with the HEVC decoder is needed): %w", err)
	}
	tiles := make([]image.Image, len(files))
	for i, f := range files {
		in, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		tiles[i], err = png.Decode(in)
		in.Close()
		if err != nil {
			return nil, fmt.Errorf("decoded HEIC item: %w", err)
		}
		os.Remove(f)
	}
	if b := tiles[0].Bounds(); !isGrid && (b.Dx() != g.Width || b.Dy() != g.Height) {
		return nil, fmt.Errorf("FFmpeg decoded a %dx%d image, but the HEIC item is %dx%d", b.Dx(), b.Dy(), g.Width, g.Height)
	}
	return imaging.ComposeGrid(g, tiles)
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}
