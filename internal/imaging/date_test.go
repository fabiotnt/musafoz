package imaging

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"github.com/fabiotnt/musafoz/internal/testimages"
)

func TestSelectDate(t *testing.T) {
	b := testimages.New(binary.LittleEndian)
	cases := []struct {
		name      string
		ifd0, sub []testimages.Entry
		value     string
		source    string
	}{
		{
			name:  "original with offset and fraction",
			ifd0:  []testimages.Entry{b.ASCII(0x0132, "2025:01:20 08:00:00")},
			sub:   []testimages.Entry{b.ASCII(0x9003, "2025:01:19 10:15:32"), b.ASCII(0x9011, "-03:00"), b.ASCII(0x9291, "120"), b.ASCII(0x9010, "+01:00")},
			value: "2025-01-19T10:15:32.120-03:00", source: "DateTimeOriginal",
		},
		{
			name:  "original without offset is not UTC",
			sub:   []testimages.Entry{b.ASCII(0x9003, "2024:05:25 10:19:25")},
			value: "2024-05-25T10:19:25", source: "DateTimeOriginal",
		},
		{
			name:  "fallback to DateTime with its own offset",
			ifd0:  []testimages.Entry{b.ASCII(0x0132, "2025:01:19 10:15:32")},
			sub:   []testimages.Entry{b.ASCII(0x9010, "-03:00"), b.ASCII(0x9011, "+09:00")},
			value: "2025-01-19T10:15:32-03:00", source: "DateTime",
		},
		{
			name:  "zero original falls back",
			ifd0:  []testimages.Entry{b.ASCII(0x0132, "2023:12:31 23:59:59")},
			sub:   []testimages.Entry{b.ASCII(0x9003, "0000:00:00 00:00:00")},
			value: "2023-12-31T23:59:59", source: "DateTime",
		},
		{
			name:  "invalid day and blank value fall back",
			ifd0:  []testimages.Entry{b.ASCII(0x0132, "2024:02:29 12:00:00")},
			sub:   []testimages.Entry{b.ASCII(0x9003, "2023:02:29 12:00:00")},
			value: "2024-02-29T12:00:00", source: "DateTime",
		},
		{
			name:  "malformed offset and fraction are left out",
			sub:   []testimages.Entry{b.ASCII(0x9003, "2022:07:01 06:05:04"), b.ASCII(0x9011, "-3"), b.ASCII(0x9291, "12a")},
			value: "2022-07-01T06:05:04", source: "DateTimeOriginal",
		},
		{
			name:  "months and hours 08 and 09 with offset +08:00",
			sub:   []testimages.Entry{b.ASCII(0x9003, "2024:09:08 09:08:09"), b.ASCII(0x9011, "+08:00")},
			value: "2024-09-08T09:08:09+08:00", source: "DateTimeOriginal",
		},
		{
			name: "no usable date",
			ifd0: []testimages.Entry{b.ASCII(0x0132, "    :  :     :  :  ")},
			sub:  []testimages.Entry{b.ASCII(0x9011, "-03:00")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blk := testimages.New(binary.LittleEndian)
			blk.IFD0 = append([]testimages.Entry{blk.Short(0x0112, 1)}, c.ifd0...)
			blk.Exif = c.sub
			e, err := ParseEXIF(blk.Build())
			if err != nil {
				t.Fatal(err)
			}
			d := SelectDate(e, nil)
			switch {
			case c.value == "" && d != nil:
				t.Errorf("date %+v, want none", d)
			case c.value != "" && (d == nil || d.Value != c.value || d.Source != c.source):
				t.Errorf("date %+v, want %s from %s", d, c.value, c.source)
			}
		})
	}
	if d := SelectDate(nil, nil); d != nil {
		t.Errorf("nil EXIF gave %+v", d)
	}
}

func TestEncodeQuality(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.Set(x, y, color.NRGBA{uint8(x * 4), uint8(y * 4), uint8((x * y) % 256), 255})
		}
	}
	sizes := map[int]int{}
	for _, q := range []int{0, 10, 92, 100} {
		var buf bytes.Buffer
		if err := Encode(&buf, img, OutputJPEG, q); err != nil {
			t.Fatal(err)
		}
		sizes[q] = buf.Len()
	}
	if sizes[0] != sizes[JPEGQuality] {
		t.Errorf("quality 0 gave %d bytes, the default %d", sizes[0], sizes[JPEGQuality])
	}
	if !(sizes[10] < sizes[92] && sizes[92] < sizes[100]) {
		t.Errorf("sizes do not grow with quality: %v", sizes)
	}
	for _, q := range []int{0, -1, 101} {
		if _, err := ParseQuality(q); err == nil {
			t.Errorf("quality %d accepted", q)
		}
	}
	for _, q := range []int{1, 100} {
		if _, err := ParseQuality(q); err != nil {
			t.Errorf("quality %d: %v", q, err)
		}
	}
}
