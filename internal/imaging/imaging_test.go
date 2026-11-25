package imaging

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabiotnt/musafoz/internal/testimages"
)

var (
	red  = color.NRGBA{255, 0, 0, 255}
	blue = color.NRGBA{0, 0, 255, 255}
)

func TestSniff(t *testing.T) {
	cases := map[string]string{
		"\xff\xd8\xff\xe0":             FormatJPEG,
		"\x89PNG\r\n\x1a\nrest":        FormatPNG,
		"GIF89a....":                   FormatGIF,
		"RIFF\x10\x00\x00\x00WEBPVP8 ": FormatWebP,
		"BM\x46\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00": FormatBMP,
		"II*\x00\x08\x00\x00\x00":                            FormatTIFF,
		"MM\x00*\x00\x00\x00\x08":                            FormatTIFF,
		"\x00\x00\x00\x18ftypheic\x00\x00":                   FormatHEIF,
		"\x00\x00\x00\x18ftypavif\x00\x00":                   FormatAVIF,
		"\x00\x00\x00\x18ftypisom\x00\x00":                   "",
		"\x00\x00\x00\x14ftypqt  \x00\x00":                   "",
		"hello":                                              "",
	}
	for in, want := range cases {
		if got := Sniff([]byte(in)); got != want {
			t.Errorf("Sniff(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEXIFPhone(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		e, err := ParseEXIF(testimages.PhoneEXIF(bo, 6))
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Warnings) > 0 {
			t.Errorf("%v: warnings %v", bo, e.Warnings)
		}
		if e.Orientation() != 6 {
			t.Errorf("%v: orientation %d", bo, e.Orientation())
		}
		want := map[string]string{
			"ifd0 0x010F Make":                    "Apple",
			"ifd0 0x0110 Model":                   "iPhone 15 Pro",
			"ifd0 0x0112 Orientation":             "6",
			"ifd0 0x8769 ExifIFDPointer":          "",
			"ifd0 0x8825 GPSInfoIFDPointer":       "",
			"ifd0 0xC5D8 ":                        "[7 8]",
			"exif 0x829A ExposureTime":            "1/120",
			"exif 0x829D FNumber":                 "178/100",
			"exif 0x8827 PhotographicSensitivity": "64",
			"exif 0x9000 ExifVersion":             "0232",
			"exif 0x9204 ExposureBiasValue":       "-1/3",
			"exif 0x9286 UserComment":             "holiday",
			"exif 0xA434 LensModel":               "iPhone 15 Pro back triple camera 6.765mm f/1.78",
			"exif 0xA432 LensSpecification":       "",
			"gps 0x0000 GPSVersionID":             "[2 2 0 0]",
			"gps 0x0001 GPSLatitudeRef":           "S",
			"gps 0x0005 GPSAltitudeRef":           "0",
		}
		got := map[string]Tag{}
		for group, list := range map[string][]Tag{"ifd0": e.IFD0, "exif": e.Exif, "gps": e.GPS} {
			for _, tag := range list {
				got[group+" "+tag.ID+" "+tag.Name] = tag
			}
		}
		for k, v := range want {
			tag, ok := got[k]
			if !ok {
				t.Errorf("%v: missing %s", bo, k)
				continue
			}
			if v != "" && tag.String() != v {
				t.Errorf("%v: %s = %q, want %q", bo, k, tag.String(), v)
			}
		}
		mn := got["exif 0x927C MakerNote"]
		if mn.Hex == "" || !strings.Contains(mn.Note, "not decoded") {
			t.Errorf("%v: maker note %+v", bo, mn)
		}
		lat, lon, ok := e.GPSPosition()
		if !ok || math.Abs(lat+23.555) > 1e-9 || math.Abs(lon+46.63) > 1e-9 {
			t.Errorf("%v: GPS %v %v %v", bo, lat, lon, ok)
		}
		if n := len(got["exif 0xA432 LensSpecification"].Rationals()); n != 4 {
			t.Errorf("%v: lens specification has %d values", bo, n)
		}
		// The JSON keeps ids and values, including for unknown tags.
		js, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{`"id":"0xC5D8","type":"LONG","count":2,"value":[7,8]`, `"name":"GPSLatitude"`, `{"numerator":1,"denominator":120}`} {
			if !bytes.Contains(js, []byte(s)) {
				t.Errorf("%v: JSON lacks %s:\n%s", bo, s, js)
			}
		}
	}
}

func TestParseEXIFSummary(t *testing.T) {
	e, err := ParseEXIF(testimages.PhoneEXIF(binary.BigEndian, 6))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range e.Summary() {
		got[r[0]] = r[1]
	}
	want := map[string]string{
		"Camera":   "Apple iPhone 15 Pro",
		"Lens":     "Apple iPhone 15 Pro back triple camera 6.765mm f/1.78",
		"Exposure": "1/120 s, f/1.78, ISO 64, 6.76 mm (24 mm equivalent)",
		"Taken":    "2026:10:31 17:51:24 -03:00",
		"GPS":      "-23.555000, -46.630000, altitude 760 m",
		"Software": "17.1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseEXIFDamaged(t *testing.T) {
	block := testimages.PhoneEXIF(binary.LittleEndian, 6)
	// Every truncation must parse without panicking.
	for n := range len(block) {
		e, err := ParseEXIF(block[:n])
		if err == nil && e == nil {
			t.Fatalf("truncated at %d: nil result", n)
		}
	}
	if _, err := ParseEXIF([]byte("XX*\x00\x08\x00\x00\x00")); err == nil {
		t.Error("bad byte order accepted")
	}
	// An IFD pointing to itself is read once.
	b := testimages.New(binary.LittleEndian)
	b.IFD0 = []testimages.Entry{b.Long(0x8769, 8)}
	e, err := ParseEXIF(b.Build())
	if err != nil || len(e.Warnings) == 0 {
		t.Errorf("loop: %+v, %v", e, err)
	}
	// A value outside the block is reported on the tag.
	b.IFD0 = []testimages.Entry{{ID: 0x010F, Type: testimages.ASCII, Count: 1000, Data: []byte{0xFF, 0xFF, 0, 0}}}
	e, err = ParseEXIF(b.Build())
	if err != nil || len(e.IFD0) != 1 || e.IFD0[0].Error == "" {
		t.Errorf("bad offset: %+v, %v", e, err)
	}
}

func TestOrient(t *testing.T) {
	// The stored image is
	//   a b c
	//   d e f
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i, ch := range "abcdef" {
		src.Pix[i*4] = byte(ch)
		src.Pix[i*4+3] = 255
	}
	want := map[int][]string{
		1: {"abc", "def"},
		2: {"cba", "fed"},
		3: {"fed", "cba"},
		4: {"def", "abc"},
		5: {"ad", "be", "cf"},
		6: {"da", "eb", "fc"},
		7: {"fc", "eb", "da"},
		8: {"cf", "be", "ad"},
	}
	for o, rows := range want {
		got := Orient(src, o)
		b := got.Bounds()
		var lines []string
		for y := range b.Dy() {
			var row []byte
			for x := range b.Dx() {
				r, _, _, _ := got.At(x, y).RGBA()
				row = append(row, byte(r>>8))
			}
			lines = append(lines, string(row))
		}
		if strings.Join(lines, "/") != strings.Join(rows, "/") {
			t.Errorf("orientation %d: %v, want %v", o, lines, rows)
		}
	}
}

func TestDecodeAppliesOrientation(t *testing.T) {
	// A phone stores a portrait photo as landscape pixels with orientation
	// 6. The left half of the stored pixels is the top of the picture.
	stored := testimages.Halves(64, 32, red, blue)
	for name, data := range map[string][]byte{
		"jpeg": testimages.JPEG(stored, testimages.PhoneEXIF(binary.BigEndian, 6)),
		"png":  testimages.PNG(stored, testimages.PhoneEXIF(binary.LittleEndian, 6)),
	} {
		img, err := Decode(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if img.Orientation != 6 || img.Width != 64 || img.Height != 32 {
			t.Errorf("%s: %+v", name, img)
		}
		b := img.Pixels.Bounds()
		if b.Dx() != 32 || b.Dy() != 64 {
			t.Errorf("%s: pixels %v, want 32x64", name, b)
		}
		if !isColor(img.Pixels.At(16, 8), red) || !isColor(img.Pixels.At(16, 56), blue) {
			t.Errorf("%s: top %v bottom %v, want red over blue", name, img.Pixels.At(16, 8), img.Pixels.At(16, 56))
		}
		if w, h := img.DisplaySize(); w != 32 || h != 64 {
			t.Errorf("%s: display %dx%d", name, w, h)
		}
	}
}

func isColor(c color.Color, want color.NRGBA) bool {
	r, g, b, _ := c.RGBA()
	wr, wg, wb, _ := want.RGBA()
	d := func(a, b uint32) bool { return math.Abs(float64(a>>8)-float64(b>>8)) < 40 }
	return d(r, wr) && d(g, wg) && d(b, wb)
}

func TestResize(t *testing.T) {
	cases := []struct {
		w, h int
		size Size
		mode string
		want string
	}{
		{400, 300, Size{320, 240}, ModeFit, "320x240"},
		{300, 400, Size{320, 240}, ModeFit, "180x240"},
		{1000, 100, Size{320, 240}, ModeFit, "320x32"},
		{100, 50, Size{320, 240}, ModeFit, "320x160"},
		{300, 400, Size{320, 240}, ModeFill, "320x240"},
		{1000, 100, Size{320, 240}, ModeFill, "320x240"},
		{1, 1000, Size{10, 10}, ModeFit, "1x10"},
	}
	for _, c := range cases {
		got := Resize(image.NewNRGBA(image.Rect(0, 0, c.w, c.h)), c.size, c.mode).Bounds()
		if s := (Size{got.Dx(), got.Dy()}).String(); s != c.want {
			t.Errorf("%dx%d %s %v = %s, want %s", c.w, c.h, c.mode, c.size, s, c.want)
		}
	}
	// Fill crops from the center: a tall image with red at the top and
	// bottom quarters and blue in the middle becomes all blue at 2:1.
	tall := image.NewNRGBA(image.Rect(0, 0, 100, 200))
	for y := range 200 {
		for x := range 100 {
			c := blue
			if y < 50 || y >= 150 {
				c = red
			}
			tall.Set(x, y, c)
		}
	}
	out := Resize(tall, Size{100, 50}, ModeFill)
	for _, y := range []int{2, 25, 47} {
		if !isColor(out.At(50, y), blue) {
			t.Errorf("fill row %d = %v, want blue from the center", y, out.At(50, y))
		}
	}
	// Fit keeps everything.
	out = Resize(tall, Size{50, 50}, ModeFit)
	if !isColor(out.At(12, 2), red) || !isColor(out.At(12, 25), blue) {
		t.Errorf("fit lost content: %v %v", out.At(12, 2), out.At(12, 25))
	}
}

func TestEncodeTransparency(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := Encode(&buf, img, OutputJPEG); err != nil {
		t.Fatal(err)
	}
	out, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !isColor(out.At(6, 6), color.NRGBA{255, 255, 255, 255}) {
		t.Errorf("transparent pixel became %v in JPEG, want white", out.At(6, 6))
	}
	buf.Reset()
	if err := Encode(&buf, img, OutputPNG); err != nil {
		t.Fatal(err)
	}
	p, err := png.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := p.At(6, 6).RGBA(); a != 0 {
		t.Errorf("PNG alpha %d, want transparent", a)
	}
}

func TestRenderWritesNoMetadata(t *testing.T) {
	src := testimages.JPEG(testimages.Halves(64, 32, red, blue), testimages.PhoneEXIF(binary.BigEndian, 6))
	img, err := Decode(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{OutputJPEG, OutputPNG} {
		var buf bytes.Buffer
		if err := Render(&buf, img.Pixels, &Size{16, 16}, ModeFit, format); err != nil {
			t.Fatal(err)
		}
		data := buf.Bytes()
		for _, marker := range []string{"Exif", "eXIf", "Apple", "iPhone", "tEXt", "iTXt", "zTXt"} {
			if bytes.Contains(data, []byte(marker)) {
				t.Errorf("%s output contains %q", format, marker)
			}
		}
		out, err := Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if out.EXIF != nil || out.Orientation != 0 {
			t.Errorf("%s output has EXIF %+v", format, out.EXIF)
		}
		if b := out.Pixels.Bounds(); b.Dx() != 8 || b.Dy() != 16 {
			t.Errorf("%s output is %v, want the oriented 8x16", format, b)
		}
	}
}

func TestParseOptions(t *testing.T) {
	for in, want := range map[string]Size{"320x240": {320, 240}, "1X1": {1, 1}} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "320", "x240", "320x", "0x10", "-1x10", "+3x4", "320x240x1", "abcxdef", "99999x1"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) accepted", in)
		}
	}
	if _, err := ParseMode("crop"); err == nil {
		t.Error("mode crop accepted")
	}
	for in, want := range map[string]string{"jpg": OutputJPEG, "JPEG": OutputJPEG, "png": OutputPNG} {
		if got, err := ParseOutputFormat(in); err != nil || got != want {
			t.Errorf("ParseOutputFormat(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseOutputFormat("webp"); err == nil {
		t.Error("format webp accepted")
	}
}

func TestInspect(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "phone photo.jpg")
	if err := os.WriteFile(p, testimages.JPEG(testimages.Halves(64, 32, red, blue), testimages.PhoneEXIF(binary.BigEndian, 6)), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(p, p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != FormatJPEG || info.Width != 64 || info.Height != 32 || info.DisplayWidth != 32 || info.DisplayHeight != 64 ||
		info.Orientation == nil || *info.Orientation != 6 || info.AlphaChannel || info.TransparentPixels == nil || *info.TransparentPixels || info.EXIF.Count() < 20 {
		t.Errorf("info = %+v", info)
	}

	// Transparent PNG without EXIF.
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, red)
	q := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(q, testimages.PNG(img, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err = Inspect(q, q)
	if err != nil {
		t.Fatal(err)
	}
	if !info.AlphaChannel || info.TransparentPixels == nil || !*info.TransparentPixels || info.EXIF != nil || info.Orientation != nil || info.OrientationLabel != "not set" {
		t.Errorf("png info = %+v", info)
	}
	js, _ := json.Marshal(info)
	if !bytes.Contains(js, []byte(`"exif":null`)) {
		t.Errorf("JSON has no exif section: %s", js)
	}

	// A HEIC header without a container is an error; AVIF is recognized
	// but not supported.
	h := filepath.Join(dir, "photo.heic")
	if err := os.WriteFile(h, []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(h, h); err == nil || !strings.Contains(err.Error(), "no meta box") {
		t.Errorf("heic: %v", err)
	}
	a := filepath.Join(dir, "photo.avif")
	if err := os.WriteFile(a, []byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avif"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(a, a); err == nil || !strings.Contains(err.Error(), "AVIF images are not supported") {
		t.Errorf("avif: %v", err)
	}
}
