package imaging

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/fabiotnt/musafoz/internal/testimages"
)

// p3ToSRGB is the reference Display P3 to sRGB conversion with D65 white
// on both sides, in linear light.
var p3ToSRGB = [3][3]float64{
	{1.2249, -0.2247, 0},
	{-0.0420, 1.0419, 0},
	{-0.0197, -0.0786, 1.0979},
}

func refP3(r, g, b uint8) [3]uint8 {
	in := [3]float64{srgbToLinear(float64(r) / 255), srgbToLinear(float64(g) / 255), srgbToLinear(float64(b) / 255)}
	var out [3]uint8
	for i := range 3 {
		v := p3ToSRGB[i][0]*in[0] + p3ToSRGB[i][1]*in[1] + p3ToSRGB[i][2]*in[2]
		out[i] = uint8(math.Round(math.Max(0, math.Min(1, linearToSRGB(math.Max(0, v)))) * 255))
	}
	return out
}

func convertPixel(t *testing.T, cs *ColorSpace, c color.NRGBA) color.NRGBA {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, c)
	return cs.ToSRGB(img).(*image.NRGBA).NRGBAAt(0, 0)
}

func near(a, b uint8, tol int) bool { return math.Abs(float64(a)-float64(b)) <= float64(tol) }

func TestICCDisplayP3ToSRGB(t *testing.T) {
	for _, name := range []string{"icc", "nclx"} {
		var cs *ColorSpace
		var err error
		if name == "icc" {
			cs, err = ParseICC(testimages.ICC("Display P3", testimages.DisplayP3Colorants))
		} else {
			cs, err = NCLXColorSpace(&NCLX{Primaries: 12, Transfer: 13, Matrix: 6, FullRange: true})
		}
		if err != nil {
			t.Fatal(err)
		}
		if cs.IsSRGB {
			t.Fatalf("%s: Display P3 taken as sRGB", name)
		}
		if name == "icc" && cs.Name != "Display P3" {
			t.Errorf("name %q", cs.Name)
		}
		for _, c := range []color.NRGBA{{200, 100, 50, 255}, {255, 0, 0, 255}, {0, 255, 0, 128}, {30, 60, 220, 255}} {
			got := convertPixel(t, cs, c)
			want := refP3(c.R, c.G, c.B)
			if !near(got.R, want[0], 2) || !near(got.G, want[1], 2) || !near(got.B, want[2], 2) || got.A != c.A {
				t.Errorf("%s: %v gave %v, want %v", name, c, got, want)
			}
		}
		// Neutral grays stay neutral and keep their level.
		for _, v := range []uint8{0, 64, 128, 200, 255} {
			got := convertPixel(t, cs, color.NRGBA{v, v, v, 255})
			if !near(got.R, v, 1) || !near(got.G, v, 1) || !near(got.B, v, 1) {
				t.Errorf("%s: gray %d gave %v", name, v, got)
			}
		}
	}
}

func TestICCSRGBIsLeftAlone(t *testing.T) {
	cs, err := ParseICC(testimages.ICC("sRGB IEC61966-2.1", testimages.SRGBColorants))
	if err != nil {
		t.Fatal(err)
	}
	if !cs.IsSRGB {
		t.Error("an sRGB profile was not recognized")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	if cs.ToSRGB(img) != image.Image(img) {
		t.Error("sRGB pixels were copied")
	}
	n, err := NCLXColorSpace(&NCLX{Primaries: 1, Transfer: 13})
	if err != nil || !n.IsSRGB {
		t.Errorf("nclx BT.709: %v %v", n, err)
	}
}

func TestICCRefused(t *testing.T) {
	p := testimages.ICC("x", testimages.SRGBColorants)
	gray := append([]byte{}, p...)
	copy(gray[16:], "GRAY")
	for name, b := range map[string][]byte{"short": p[:100], "gray": gray, "truncated table": p[:140]} {
		if _, err := ParseICC(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, n := range []NCLX{{Primaries: 9, Transfer: 16}, {Primaries: 9, Transfer: 18}, {Primaries: 5, Transfer: 13}} {
		if _, err := NCLXColorSpace(&n); err == nil {
			t.Errorf("nclx %+v accepted", n)
		}
	}
	// Every truncation parses without panicking.
	for i := range p {
		ParseICC(p[:i])
	}
}

func TestReadXMPAndICC(t *testing.T) {
	icc := testimages.ICC("Display P3", testimages.DisplayP3Colorants)
	png := testimages.WithPNGChunks(testimages.PNG(testimages.Halves(4, 4, color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}), nil),
		[2][]byte{[]byte("iCCP"), testimages.PNGICCP(icc)},
		[2][]byte{[]byte("iTXt"), testimages.PNGXMP(testimages.ScreenshotXMP("2025-01-31T09:35:03"))})
	img, err := Decode(png)
	if err != nil {
		t.Fatal(err)
	}
	if string(img.ICC) != string(icc) || img.XMP["photoshop:DateCreated"] != "2025-01-31T09:35:03" || img.XMP["exif:UserComment"] != "Screenshot" {
		t.Errorf("png: icc %d bytes, xmp %v", len(img.ICC), img.XMP)
	}
	if d := SelectDate(img.EXIF, img.XMP); d == nil || d.Value != "2025-01-31T09:35:03" || d.Source != "XMP photoshop:DateCreated" {
		t.Errorf("png date %+v", d)
	}

	type seg = struct {
		Marker  byte
		Payload []byte
	}
	chunks := testimages.JPEGICC(icc, 200)
	segs := []seg{{0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00" + `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:CreateDate="2024-03-02T01:02:03.5-03:00"/></rdf:RDF></x:xmpmeta>`)}}
	// Chunks out of order are put back in sequence.
	for i := len(chunks) - 1; i >= 0; i-- {
		segs = append(segs, seg{0xE2, chunks[i]})
	}
	jpg := testimages.WithJPEGSegments(testimages.JPEG(testimages.Halves(8, 8, color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}), nil), segs...)
	img, err = Decode(jpg)
	if err != nil {
		t.Fatal(err)
	}
	if string(img.ICC) != string(icc) || len(chunks) < 2 {
		t.Errorf("jpeg icc %d bytes from %d chunks", len(img.ICC), len(chunks))
	}
	if d := SelectDate(nil, img.XMP); d == nil || d.Value != "2024-03-02T01:02:03.5-03:00" || d.Source != "XMP xmp:CreateDate" {
		t.Errorf("jpeg date %+v from %v", d, img.XMP)
	}
}

func TestXMPDates(t *testing.T) {
	cases := map[string]string{
		"2025-01-31T09:35:03":          "2025-01-31T09:35:03",
		"2025-01-31T09:35":             "2025-01-31T09:35:00",
		"2025-01-31T09:35:03Z":         "2025-01-31T09:35:03Z",
		"2025-01-31T09:35:03.25+01:00": "2025-01-31T09:35:03.25+01:00",
		"2025-02-30T09:35:03":          "",
		"2025-01-31":                   "",
		"yesterday":                    "",
	}
	for in, want := range cases {
		got, ok := xmpDate(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q gave %q %v, want %q", in, got, ok, want)
		}
	}
	// EXIF DateTimeOriginal wins over XMP; XMP wins over EXIF DateTime.
	xmp := map[string]string{"photoshop:DateCreated": "2020-01-01T00:00:00"}
	if d := SelectDate(nil, xmp); d.Source != "XMP photoshop:DateCreated" {
		t.Errorf("source %s", d.Source)
	}
	// A packet with a list and garbage after it still gives its properties.
	got := ParseXMP([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Make="Apple"><dc:title><rdf:Alt><rdf:li xml:lang="x-default">Beach</rdf:li></rdf:Alt></dc:title></rdf:Description></rdf:RDF></x:xmpmeta><broken`))
	if got["tiff:Make"] != "Apple" || got["dc:title"] != "Beach" {
		t.Errorf("parsed %v", got)
	}
}
