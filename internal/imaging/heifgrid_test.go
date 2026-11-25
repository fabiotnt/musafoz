package imaging

import (
	"image"
	"image/color"
	"testing"

	"github.com/fabiotnt/musafoz/internal/testimages"
)

func solid(w, h int, c color.Color) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		r, g, b, a := c.RGBA()
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(r>>8), byte(g>>8), byte(b>>8), byte(a>>8)
	}
	return img
}

func TestComposeGrid(t *testing.T) {
	red, green, blue, white := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 255, 0, 255}, color.NRGBA{0, 0, 255, 255}, color.NRGBA{255, 255, 255, 255}
	tiles := []image.Image{solid(4, 3, red), solid(4, 3, green), solid(4, 3, blue), solid(4, 3, white)}
	// 2x2 tiles of 4x3 cropped to an odd 7x5, as phones write.
	g := &HEIFGrid{Rows: 2, Cols: 2, Width: 7, Height: 5}
	img, err := ComposeGrid(g, tiles)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 7 || img.Bounds().Dy() != 5 {
		t.Fatalf("size %v", img.Bounds())
	}
	for _, c := range []struct {
		x, y int
		want color.NRGBA
	}{{0, 0, red}, {3, 2, red}, {4, 0, green}, {6, 2, green}, {0, 3, blue}, {3, 4, blue}, {4, 3, white}, {6, 4, white}} {
		if got := img.NRGBAAt(c.x, c.y); got != c.want {
			t.Errorf("pixel %d,%d = %v, want %v", c.x, c.y, got, c.want)
		}
	}
	bad := []struct {
		g     *HEIFGrid
		tiles []image.Image
	}{
		{g, tiles[:3]},
		{g, []image.Image{tiles[0], tiles[1], tiles[2], solid(3, 3, red)}},
		{&HEIFGrid{Rows: 2, Cols: 2, Width: 9, Height: 5}, tiles},
	}
	for i, b := range bad {
		if _, err := ComposeGrid(b.g, b.tiles); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestMergeAlpha(t *testing.T) {
	img := solid(2, 1, color.NRGBA{10, 20, 30, 255}).(*image.NRGBA)
	alpha := image.NewGray(image.Rect(0, 0, 2, 1))
	alpha.Pix[0], alpha.Pix[1] = 0, 200
	if err := MergeAlpha(img, alpha); err != nil {
		t.Fatal(err)
	}
	if img.NRGBAAt(0, 0) != (color.NRGBA{10, 20, 30, 0}) || img.NRGBAAt(1, 0) != (color.NRGBA{10, 20, 30, 200}) {
		t.Errorf("pixels %v %v", img.NRGBAAt(0, 0), img.NRGBAAt(1, 0))
	}
	if err := MergeAlpha(img, image.NewGray(image.Rect(0, 0, 1, 1))); err == nil {
		t.Error("a smaller alpha plane was accepted")
	}
}

func TestAlphaTypes(t *testing.T) {
	for s, want := range map[string]bool{
		"urn:mpeg:mpegB:cicp:systems:auxiliary:alpha": true,
		"urn:mpeg:hevc:2015:auxid:1":                  true,
		"urn:mpeg:hevc:2015:auxid:2":                  false,
		"urn:com:apple:photo:2020:aux:hdrgainmap":     false,
		"urn:mpeg:mpegB:cicp:systems:auxiliary:depth": false,
	} {
		if isAlphaType(s) != want {
			t.Errorf("%s: %v", s, !want)
		}
	}
}

func TestParseHEIFGridAndAlpha(t *testing.T) {
	h, err := ParseHEIF(heicFixture(t, testimages.HEIFOptions{Grid: true, GridCrop: 1, Alpha: true, Mirror: -1}))
	if err != nil {
		t.Fatal(err)
	}
	g := h.Grid
	if g == nil || g.Rows != 1 || g.Cols != 2 || g.Width != 127 || g.Height != 48 || len(g.Tiles) != 2 || g.Tiles[0] != 2 || g.Tiles[1] != 3 {
		t.Errorf("grid %+v", g)
	}
	if h.Width != 127 || !h.Alpha || h.AlphaItem != 4 || h.AlphaGrid == nil || h.AlphaGrid.Tiles[0] != 5 || h.AlphaGrid.Width != 127 {
		t.Errorf("size %d, alpha %v item %d grid %+v", h.Width, h.Alpha, h.AlphaItem, h.AlphaGrid)
	}
	h, err = ParseHEIF(heicFixture(t, testimages.HEIFOptions{Alpha: true, Mirror: -1}))
	if err != nil {
		t.Fatal(err)
	}
	if h.Grid != nil || !h.Alpha || h.AlphaItem != 2 || h.AlphaGrid != nil {
		t.Errorf("single item: grid %+v alpha %v item %d", h.Grid, h.Alpha, h.AlphaItem)
	}
}

// Transform applies the transforms in the listed order.
func TestHEIFTransformOrder(t *testing.T) {
	probe := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := range 6 {
		probe.Pix[i*4], probe.Pix[i*4+3] = byte(i+1), 255
	}
	axis := 0
	rot := HEIFTransform{Rotation: 90}
	mir := HEIFTransform{Mirror: "top-bottom", MirrorAxis: &axis}
	a := (&HEIF{Transforms: []HEIFTransform{rot, mir}}).Transform(probe)
	b := (&HEIF{Transforms: []HEIFTransform{mir, rot}}).Transform(probe)
	if a.Bounds().Dx() != 2 || a.Bounds().Dy() != 3 {
		t.Fatalf("size %v", a.Bounds())
	}
	same := true
	for y := range 3 {
		for x := range 2 {
			if a.At(x, y) != b.At(x, y) {
				same = false
			}
		}
	}
	if same {
		t.Error("the order of the transforms made no difference")
	}
	// Rotating 90 degrees counterclockwise puts the top right pixel (3) at
	// the top left.
	if r, _, _, _ := (&HEIF{Transforms: []HEIFTransform{rot}}).Transform(probe).At(0, 0).RGBA(); r>>8 != 3 {
		t.Errorf("top left after irot 90 is %d, want 3", r>>8)
	}
}
