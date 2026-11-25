package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	// Decoders registered with image.Decode.
	_ "image/gif"

	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// MaxPixels limits the stored size of images that are decoded, to protect
// against images that expand to huge pixel buffers.
const MaxPixels = 200_000_000

// Image is a decoded image with its orientation already applied.
type Image struct {
	Format string
	// Pixels are in the displayed orientation.
	Pixels image.Image
	// Width and Height are the stored size, before orientation.
	Width, Height int
	// Orientation is the EXIF orientation that was applied, 1 to 8, or 0
	// when the file has none.
	Orientation int
	// AlphaChannel reports whether the file declares transparency.
	AlphaChannel bool
	// EXIF is nil when the file has no readable EXIF block.
	EXIF *EXIF
	// EXIFError tells why an EXIF block present in the file could not be
	// read.
	EXIFError string
	// XMP holds the simple properties of the XMP packet (see ParseXMP),
	// empty when the file has none.
	XMP map[string]string
	// ICC is the embedded color profile, or nil. Pixels are as stored, in
	// that profile; see ToSRGB.
	ICC []byte
}

// Load reads and decodes an image file and applies its EXIF orientation to
// the pixels. The file itself is not changed.
func Load(path string) (*Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Decode decodes image data and applies its EXIF orientation.
func Decode(data []byte) (*Image, error) {
	format := Sniff(data)
	if format == "" {
		return nil, errors.New("not a supported image (JPEG, PNG, GIF, WebP, BMP, or TIFF)")
	}
	if !Decodable(format) {
		return nil, &UnsupportedError{Format: format}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cannot read the %s header: %w", formatLabel(format), err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, fmt.Errorf("image is %dx%d, larger than the %d megapixel limit", cfg.Width, cfg.Height, MaxPixels/1_000_000)
	}
	pix, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cannot decode the %s data: %w", formatLabel(format), err)
	}
	img := &Image{Format: format, Width: pix.Bounds().Dx(), Height: pix.Bounds().Dy()}
	block, alpha := findEXIF(format, data)
	img.AlphaChannel = alpha || format == FormatGIF && hasTransparentIndex(pix)
	if block != nil {
		e, err := ParseEXIF(block)
		if err != nil {
			img.EXIFError = err.Error()
		} else {
			img.EXIF = e
			img.Orientation = e.Orientation()
		}
	}
	xmp, icc := findXMPICC(format, data)
	img.XMP, img.ICC = ParseXMP(xmp), icc
	img.Pixels = Orient(pix, img.Orientation)
	return img, nil
}

func hasTransparentIndex(m image.Image) bool {
	p, ok := m.(*image.Paletted)
	if !ok {
		return false
	}
	for _, c := range p.Palette {
		if _, _, _, a := c.RGBA(); a < 0xFFFF {
			return true
		}
	}
	return false
}

// DisplaySize returns the size after orientation.
func (m *Image) DisplaySize() (w, h int) {
	if m.Orientation >= 5 {
		return m.Height, m.Width
	}
	return m.Width, m.Height
}

// HasTransparentPixels reports whether any pixel is not fully opaque.
func (m *Image) HasTransparentPixels() bool {
	if o, ok := m.Pixels.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	b := m.Pixels.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := m.Pixels.At(x, y).RGBA(); a < 0xFFFF {
				return true
			}
		}
	}
	return false
}

// OrientationLabel describes an EXIF orientation value.
func OrientationLabel(o int) string {
	switch o {
	case 1:
		return "normal"
	case 2:
		return "mirrored horizontally"
	case 3:
		return "rotated 180 degrees"
	case 4:
		return "mirrored vertically"
	case 5:
		return "mirrored horizontally and rotated 270 degrees clockwise"
	case 6:
		return "rotated 90 degrees clockwise"
	case 7:
		return "mirrored horizontally and rotated 90 degrees clockwise"
	case 8:
		return "rotated 270 degrees clockwise"
	}
	return "not set"
}

// Orient applies an EXIF orientation, 1 to 8, so the result shows the
// picture as intended. Other values return src unchanged.
func Orient(src image.Image, o int) image.Image {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	in := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	out := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for dy := range dh {
		for dx := range dw {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-dx, dy
			case 3:
				sx, sy = w-1-dx, h-1-dy
			case 4:
				sx, sy = dx, h-1-dy
			case 5:
				sx, sy = dy, dx
			case 6:
				sx, sy = dy, h-1-dx
			case 7:
				sx, sy = w-1-dy, h-1-dx
			case 8:
				sx, sy = w-1-dy, dx
			}
			si := in.PixOffset(sx, sy)
			di := out.PixOffset(dx, dy)
			copy(out.Pix[di:di+4], in.Pix[si:si+4])
		}
	}
	return out
}

// Size is a target size in pixels.
type Size struct {
	Width, Height int
}

func (s Size) String() string { return fmt.Sprintf("%dx%d", s.Width, s.Height) }

// MaxSide limits each side of a requested size.
const MaxSide = 16384

// ParseSize reads WIDTHxHEIGHT, such as 320x240.
func ParseSize(s string) (Size, error) {
	ws, hs, ok := strings.Cut(strings.ToLower(s), "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if !ok || err1 != nil || err2 != nil || strings.HasPrefix(ws, "+") || strings.HasPrefix(hs, "+") {
		return Size{}, fmt.Errorf("size %q is not in WIDTHxHEIGHT form, such as 320x240", s)
	}
	if w < 1 || h < 1 || w > MaxSide || h > MaxSide {
		return Size{}, fmt.Errorf("size %q must have sides from 1 to %d pixels", s, MaxSide)
	}
	return Size{w, h}, nil
}

// Resize modes.
const (
	// ModeFit scales the image to fit inside the size, keeping its aspect
	// ratio, without cropping or borders. One side matches the size.
	ModeFit = "fit"
	// ModeFill scales the image to cover the size, keeping its aspect
	// ratio, and crops the excess equally from both sides.
	ModeFill = "fill"
)

// ParseMode checks a resize mode.
func ParseMode(s string) (string, error) {
	switch s {
	case ModeFit, ModeFill:
		return s, nil
	}
	return "", fmt.Errorf("mode %q is not fit or fill", s)
}

// FitSize returns the size of a src image scaled to fit inside size.
func FitSize(srcW, srcH int, size Size) (w, h int) {
	scale := math.Min(float64(size.Width)/float64(srcW), float64(size.Height)/float64(srcH))
	w = max(1, min(size.Width, int(math.Round(float64(srcW)*scale))))
	h = max(1, min(size.Height, int(math.Round(float64(srcH)*scale))))
	return w, h
}

// FillCrop returns the centered part of a srcW x srcH image that has the
// aspect ratio of size.
func FillCrop(srcW, srcH int, size Size) image.Rectangle {
	target := float64(size.Width) / float64(size.Height)
	cw, ch := srcW, srcH
	if float64(srcW)/float64(srcH) > target {
		cw = max(1, min(srcW, int(math.Round(float64(srcH)*target))))
	} else {
		ch = max(1, min(srcH, int(math.Round(float64(srcW)/target))))
	}
	x, y := (srcW-cw)/2, (srcH-ch)/2
	return image.Rect(x, y, x+cw, y+ch)
}

// Resize scales src with the mode: ModeFit or ModeFill. Images are scaled
// up as well as down. The result uses Catmull-Rom resampling with
// premultiplied alpha, so transparent edges do not darken.
func Resize(src image.Image, size Size, mode string) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	srcRect := b
	w, h := size.Width, size.Height
	if mode == ModeFill {
		r := FillCrop(sw, sh, size)
		srcRect = r.Add(b.Min)
	} else {
		w, h = FitSize(sw, sh, size)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, srcRect, xdraw.Src, nil)
	return dst
}

// Output formats.
const (
	OutputJPEG = "jpeg"
	OutputPNG  = "png"
)

// ParseOutputFormat checks an output format name: jpeg (also jpg) or png.
func ParseOutputFormat(s string) (string, error) {
	switch strings.ToLower(s) {
	case "jpeg", "jpg":
		return OutputJPEG, nil
	case "png":
		return OutputPNG, nil
	}
	return "", fmt.Errorf("format %q is not jpeg or png", s)
}

// Extension returns the file extension for an output format.
func Extension(format string) string {
	if format == OutputPNG {
		return ".png"
	}
	return ".jpg"
}

// JPEGQuality is the default quality of JPEG outputs. MinQuality and
// MaxQuality bound the quality that can be chosen.
const (
	JPEGQuality = 92
	MinQuality  = 1
	MaxQuality  = 100
)

// ParseQuality checks a JPEG quality from 1 to 100.
func ParseQuality(q int) (int, error) {
	if q < MinQuality || q > MaxQuality {
		return 0, fmt.Errorf("quality %d is outside %d to %d", q, MinQuality, MaxQuality)
	}
	return q, nil
}

// Encode writes img in the output format. JPEG has no transparency, so
// transparent pixels are composited over white. quality applies to JPEG
// only; 0 means JPEGQuality. The encoders write pixel data only: no EXIF,
// GPS, or other metadata reaches the output.
func Encode(w io.Writer, img image.Image, format string, quality int) error {
	if format == OutputPNG {
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		return enc.Encode(w, img)
	}
	if quality == 0 {
		quality = JPEGQuality
	}
	return jpeg.Encode(w, flatten(img), &jpeg.Options{Quality: quality})
}

// flatten composites img over white when it has transparent pixels.
func flatten(img image.Image) image.Image {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return img
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Over)
	return out
}

// Render resizes img when size is set and encodes it with quality (see
// Encode). It is the shared
// step that writes every still image: thumbnails, extracted frames, and
// covers.
func Render(w io.Writer, img image.Image, size *Size, mode, format string, quality int) error {
	if size != nil {
		img = Resize(img, *size, mode)
	}
	return Encode(w, img, format, quality)
}

// RenderedSize returns the size Render writes for an image of srcW x srcH.
func RenderedSize(srcW, srcH int, size *Size, mode string) (w, h int) {
	switch {
	case size == nil:
		return srcW, srcH
	case mode == ModeFill:
		return size.Width, size.Height
	default:
		return FitSize(srcW, srcH, *size)
	}
}
