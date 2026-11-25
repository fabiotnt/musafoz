package imaging

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
)

// ComposeGrid places decoded tiles, row by row, and keeps the top left
// Width x Height part, as the HEIF grid item specifies. Every tile must
// have the size of the first one, and the tiles must cover the output.
func ComposeGrid(g *HEIFGrid, tiles []image.Image) (*image.NRGBA, error) {
	if len(tiles) != g.Rows*g.Cols || len(tiles) == 0 {
		return nil, fmt.Errorf("the %dx%d grid needs %d tiles, got %d", g.Cols, g.Rows, g.Rows*g.Cols, len(tiles))
	}
	ts := tiles[0].Bounds().Size()
	for i, t := range tiles {
		if t.Bounds().Size() != ts {
			return nil, fmt.Errorf("tile %d is %dx%d, the first tile %dx%d", i+1, t.Bounds().Dx(), t.Bounds().Dy(), ts.X, ts.Y)
		}
	}
	if ts.X*g.Cols < g.Width || ts.Y*g.Rows < g.Height {
		return nil, fmt.Errorf("%dx%d tiles of %dx%d do not cover the %dx%d image", g.Cols, g.Rows, ts.X, ts.Y, g.Width, g.Height)
	}
	if int64(g.Width)*int64(g.Height) > MaxPixels {
		return nil, fmt.Errorf("image is %dx%d, larger than the %d megapixel limit", g.Width, g.Height, MaxPixels/1_000_000)
	}
	out := image.NewNRGBA(image.Rect(0, 0, g.Width, g.Height))
	for i, t := range tiles {
		at := image.Pt(i%g.Cols*ts.X, i/g.Cols*ts.Y)
		r := image.Rectangle{Min: at, Max: at.Add(ts)}.Intersect(out.Bounds())
		if !r.Empty() {
			draw.Draw(out, r, t, t.Bounds().Min, draw.Src)
		}
	}
	return out, nil
}

// MergeAlpha sets the alpha of img from the gray levels of alpha, which
// must have the same size.
func MergeAlpha(img *image.NRGBA, alpha image.Image) error {
	if alpha.Bounds().Size() != img.Bounds().Size() {
		return fmt.Errorf("the alpha plane is %dx%d, the image %dx%d",
			alpha.Bounds().Dx(), alpha.Bounds().Dy(), img.Bounds().Dx(), img.Bounds().Dy())
	}
	ab := alpha.Bounds()
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			g := color.GrayModel.Convert(alpha.At(ab.Min.X+x, ab.Min.Y+y)).(color.Gray)
			img.Pix[img.PixOffset(x, y)+3] = g.Y
		}
	}
	return nil
}
