package imaging

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fabiotnt/musafoz/internal/testimages"
)

func heicFixture(t *testing.T, o testimages.HEIFOptions) []byte {
	t.Helper()
	hevc, err := os.ReadFile(filepath.Join("..", "..", "testdata", "image", "quadrants-64x48.hevc"))
	if err != nil {
		t.Fatal(err)
	}
	o.HEVC, o.Width, o.Height = hevc, 64, 48
	return testimages.HEIF(o)
}

func TestParseHEIF(t *testing.T) {
	exif := testimages.PhoneEXIF(binary.BigEndian, 6)
	cases := []struct {
		name       string
		opts       testimages.HEIFOptions
		typ        string
		w, h       int
		dw, dh     int
		transforms string
		exif       bool
	}{
		{"plain", testimages.HEIFOptions{Mirror: -1}, "hvc1", 64, 48, 64, 48, "none", false},
		{"EXIF orientation only", testimages.HEIFOptions{Mirror: -1, EXIF: exif}, "hvc1", 64, 48, 64, 48, "none", true},
		{"phone: irot 270 and EXIF 6", testimages.HEIFOptions{Rotation: 270, Mirror: -1, EXIF: exif}, "hvc1", 64, 48, 48, 64, "rotated 90 degrees clockwise", true},
		{"rotate 180", testimages.HEIFOptions{Rotation: 180, Mirror: -1}, "hvc1", 64, 48, 64, 48, "rotated 180 degrees clockwise", false},
		{"mirror axis 0", testimages.HEIFOptions{Mirror: 0}, "hvc1", 64, 48, 64, 48, "mirrored top to bottom", false},
		{"mirror axis 1", testimages.HEIFOptions{Mirror: 1}, "hvc1", 64, 48, 64, 48, "mirrored left to right", false},
		{"rotate then mirror", testimages.HEIFOptions{Rotation: 90, Mirror: 0}, "hvc1", 64, 48, 48, 64, "rotated 270 degrees clockwise, then mirrored top to bottom", false},
		{"mirror then rotate", testimages.HEIFOptions{Rotation: 90, Mirror: 0, MirrorFirst: true}, "hvc1", 64, 48, 48, 64, "mirrored top to bottom, then rotated 270 degrees clockwise", false},
		{"grid", testimages.HEIFOptions{Grid: true, Rotation: 270, Mirror: -1, EXIF: exif}, "grid", 128, 48, 48, 128, "rotated 90 degrees clockwise", true},
		{"thumbnail item", testimages.HEIFOptions{Thumbnail: true, Mirror: -1}, "hvc1", 64, 48, 64, 48, "none", false},
	}
	for _, c := range cases {
		h, err := ParseHEIF(heicFixture(t, c.opts))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		dw, dh := h.DisplaySize()
		if h.ItemType != c.typ || h.Width != c.w || h.Height != c.h || dw != c.dw || dh != c.dh ||
			HEIFLabel(h.Transforms) != c.transforms || (h.EXIF != nil) != c.exif || (h.Grid != nil) != (c.typ == "grid") || h.Primary != 1 {
			t.Errorf("%s: %s %dx%d -> %dx%d %q exif %v grid %v", c.name, h.ItemType, h.Width, h.Height, dw, dh,
				HEIFLabel(h.Transforms), h.EXIF != nil, h.Grid)
		}
		if c.exif {
			e, err := ParseEXIF(h.EXIF)
			if err != nil || e.Orientation() != 6 || e.Count() < 20 {
				t.Errorf("%s: EXIF %v %v", c.name, e, err)
			}
		}
	}
}

func TestParseHEIFDamaged(t *testing.T) {
	data := heicFixture(t, testimages.HEIFOptions{Grid: true, Rotation: 270, Mirror: 0, EXIF: testimages.PhoneEXIF(binary.LittleEndian, 6)})
	for n := range len(data) {
		ParseHEIF(data[:n]) // must not panic
	}
	if _, err := ParseHEIF([]byte("\x00\x00\x00\x10ftypheic\x00\x00\x00\x00")); err == nil {
		t.Error("file without meta box accepted")
	}
}

func TestInspectHEIC(t *testing.T) {
	dir := t.TempDir()
	// HEIC content with a .jpg name is still read as HEIC.
	p := filepath.Join(dir, "photo.jpg")
	data := heicFixture(t, testimages.HEIFOptions{Rotation: 270, Mirror: -1, EXIF: testimages.PhoneEXIF(binary.BigEndian, 6)})
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(p, p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != FormatHEIF || info.Width != 64 || info.Height != 48 || info.DisplayWidth != 48 || info.DisplayHeight != 64 ||
		info.Orientation == nil || *info.Orientation != 6 || info.TransparentPixels != nil || len(info.HEIFTransforms) != 1 {
		t.Errorf("info = %+v", info)
	}
	lat, lon, ok := info.EXIF.GPSPosition()
	if !ok || lat > -23 || lon > -46 {
		t.Errorf("GPS %v %v %v", lat, lon, ok)
	}
	js, _ := json.Marshal(info)
	for _, want := range []string{`"format":"heif"`, `"heif_transforms":[{"rotation_ccw":270}]`, `"name":"MakerNote"`} {
		if !bytes.Contains(js, []byte(want)) {
			t.Errorf("JSON lacks %s", want)
		}
	}
	if bytes.Contains(js, []byte("transparent_pixels")) {
		t.Error("HEIC JSON reports transparent_pixels without decoding pixels")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(after, data) {
		t.Error("inspect changed the file")
	}
}

func TestParseHEIFColorAndXMP(t *testing.T) {
	icc := testimages.ICC("Display P3", testimages.DisplayP3Colorants)
	h, err := ParseHEIF(heicFixture(t, testimages.HEIFOptions{Grid: true, Mirror: -1, ICC: icc, NCLX: []int{12, 13, 6},
		XMP: testimages.ScreenshotXMP("2024-09-27T08:37:55")}))
	if err != nil {
		t.Fatal(err)
	}
	if string(h.ICC) != string(icc) || h.NCLX == nil || h.NCLX.Primaries != 12 || h.NCLX.Transfer != 13 || !h.NCLX.FullRange {
		t.Errorf("icc %d bytes, nclx %+v", len(h.ICC), h.NCLX)
	}
	if x := ParseXMP(h.XMP); x["photoshop:DateCreated"] != "2024-09-27T08:37:55" {
		t.Errorf("xmp %v", x)
	}
}
