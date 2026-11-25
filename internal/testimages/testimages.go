// Package testimages builds images with EXIF metadata for tests.
package testimages

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
)

// EXIF value types.
const (
	Byte      = 1
	ASCII     = 2
	Short     = 3
	Long      = 4
	Rational  = 5
	Undefined = 7
	SLong     = 9
	SRational = 10
	Double    = 12
)

// Entry is one EXIF tag with its value already encoded in the builder's
// byte order. Use the builder methods to create entries.
type Entry struct {
	ID    uint16
	Type  uint16
	Count uint32
	Data  []byte
}

// EXIF builds a TIFF-structured EXIF block with IFD0 and optional Exif and
// GPS directories.
type EXIF struct {
	BO              binary.ByteOrder
	IFD0, Exif, GPS []Entry
}

// New returns a builder in the byte order.
func New(bo binary.ByteOrder) *EXIF { return &EXIF{BO: bo} }

// ASCII returns an ASCII entry.
func (b *EXIF) ASCII(id uint16, s string) Entry {
	d := append([]byte(s), 0)
	return Entry{id, ASCII, uint32(len(d)), d}
}

// Short returns a SHORT entry.
func (b *EXIF) Short(id uint16, v ...uint16) Entry {
	d := make([]byte, 2*len(v))
	for i, x := range v {
		b.BO.PutUint16(d[i*2:], x)
	}
	return Entry{id, Short, uint32(len(v)), d}
}

// Long returns a LONG entry.
func (b *EXIF) Long(id uint16, v ...uint32) Entry {
	d := make([]byte, 4*len(v))
	for i, x := range v {
		b.BO.PutUint32(d[i*4:], x)
	}
	return Entry{id, Long, uint32(len(v)), d}
}

// Rational returns a RATIONAL entry from numerator and denominator pairs.
func (b *EXIF) Rational(id uint16, nd ...uint32) Entry {
	d := make([]byte, 4*len(nd))
	for i, x := range nd {
		b.BO.PutUint32(d[i*4:], x)
	}
	return Entry{id, Rational, uint32(len(nd) / 2), d}
}

// SRational returns an SRATIONAL entry from numerator and denominator
// pairs.
func (b *EXIF) SRational(id uint16, nd ...int32) Entry {
	d := make([]byte, 4*len(nd))
	for i, x := range nd {
		b.BO.PutUint32(d[i*4:], uint32(x))
	}
	return Entry{id, SRational, uint32(len(nd) / 2), d}
}

// Double returns a DOUBLE entry.
func (b *EXIF) Double(id uint16, v float64) Entry {
	d := make([]byte, 8)
	b.BO.PutUint64(d, math.Float64bits(v))
	return Entry{id, Double, 1, d}
}

// Bytes returns an entry of raw bytes with the type, BYTE or UNDEFINED.
func (b *EXIF) Bytes(id, typ uint16, d []byte) Entry {
	return Entry{id, typ, uint32(len(d)), d}
}

// Build returns the EXIF block. Pointer tags to the Exif and GPS
// directories are added to IFD0 when those directories have entries.
func (b *EXIF) Build() []byte {
	ifd0 := append([]Entry(nil), b.IFD0...)
	exifAt, gpsAt := -1, -1
	if len(b.Exif) > 0 {
		exifAt = len(ifd0)
		ifd0 = append(ifd0, Entry{0x8769, Long, 1, make([]byte, 4)})
	}
	if len(b.GPS) > 0 {
		gpsAt = len(ifd0)
		ifd0 = append(ifd0, Entry{0x8825, Long, 1, make([]byte, 4)})
	}
	lists := [][]Entry{ifd0, b.Exif, b.GPS}
	starts := make([]int, len(lists))
	off := 8
	for i, l := range lists {
		if len(l) == 0 {
			continue
		}
		starts[i] = off
		off += 2 + 12*len(l) + 4
		for _, e := range l {
			if len(e.Data) > 4 {
				off += len(e.Data) + len(e.Data)%2
			}
		}
	}
	if exifAt >= 0 {
		b.BO.PutUint32(ifd0[exifAt].Data, uint32(starts[1]))
	}
	if gpsAt >= 0 {
		b.BO.PutUint32(ifd0[gpsAt].Data, uint32(starts[2]))
	}
	buf := make([]byte, off)
	if b.BO == binary.ByteOrder(binary.LittleEndian) {
		copy(buf, "II")
	} else {
		copy(buf, "MM")
	}
	b.BO.PutUint16(buf[2:], 42)
	b.BO.PutUint32(buf[4:], 8)
	for i, l := range lists {
		if len(l) == 0 {
			continue
		}
		pos := starts[i]
		b.BO.PutUint16(buf[pos:], uint16(len(l)))
		data := pos + 2 + 12*len(l) + 4
		for j, e := range l {
			at := pos + 2 + 12*j
			b.BO.PutUint16(buf[at:], e.ID)
			b.BO.PutUint16(buf[at+2:], e.Type)
			b.BO.PutUint32(buf[at+4:], e.Count)
			if len(e.Data) <= 4 {
				copy(buf[at+8:], e.Data)
			} else {
				b.BO.PutUint32(buf[at+8:], uint32(data))
				copy(buf[data:], e.Data)
				data += len(e.Data) + len(e.Data)%2
			}
		}
	}
	return buf
}

// JPEG encodes img as a JPEG with the EXIF block in an APP1 segment. A nil
// block gives a plain JPEG.
func JPEG(img image.Image, exif []byte) []byte {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 95}); err != nil {
		panic(err)
	}
	data := out.Bytes()
	if exif == nil {
		return data
	}
	seg := make([]byte, 4, 10+len(exif))
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:], uint16(2+6+len(exif)))
	seg = append(seg, "Exif\x00\x00"...)
	seg = append(seg, exif...)
	res := append([]byte{}, data[:2]...)
	res = append(res, seg...)
	return append(res, data[2:]...)
}

// PNG encodes img as a PNG with the EXIF block in an eXIf chunk after IHDR.
// A nil block gives a plain PNG.
func PNG(img image.Image, exif []byte) []byte {
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		panic(err)
	}
	data := out.Bytes()
	if exif == nil {
		return data
	}
	const afterIHDR = 8 + 4 + 4 + 13 + 4
	chunk := make([]byte, 8, 12+len(exif))
	binary.BigEndian.PutUint32(chunk, uint32(len(exif)))
	copy(chunk[4:], "eXIf")
	chunk = append(chunk, exif...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	res := append([]byte{}, data[:afterIHDR]...)
	res = append(res, chunk...)
	return append(res, data[afterIHDR:]...)
}

// Halves returns a w x h image whose left half is a and right half is b.
func Halves(w, h int, a, b color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if x < w/2 {
				img.Set(x, y, a)
			} else {
				img.Set(x, y, b)
			}
		}
	}
	return img
}

// PhoneEXIF returns the EXIF block of a phone photo taken in portrait: the
// orientation (6, rotate 90 degrees clockwise), camera, lens, exposure, and
// GPS tags, a maker note, and an unknown tag 0xC5D8.
func PhoneEXIF(bo binary.ByteOrder, orientation uint16) []byte {
	b := New(bo)
	b.IFD0 = []Entry{
		b.ASCII(0x010F, "Apple"),
		b.ASCII(0x0110, "iPhone 15 Pro"),
		b.Short(0x0112, orientation),
		b.Rational(0x011A, 72, 1),
		b.ASCII(0x0131, "17.1"),
		b.ASCII(0x0132, "2026:10:31 17:51:24"),
		b.Long(0xC5D8, 7, 8),
	}
	b.Exif = []Entry{
		b.Rational(0x829A, 1, 120),
		b.Rational(0x829D, 178, 100),
		b.Short(0x8827, 64),
		b.Bytes(0x9000, Undefined, []byte("0232")),
		b.ASCII(0x9003, "2026:10:31 17:51:24"),
		b.ASCII(0x9011, "-03:00"),
		b.SRational(0x9204, -1, 3),
		b.Rational(0x920A, 6765, 1000),
		b.Bytes(0x927C, Undefined, []byte("Apple iOS\x00\x00\x01MM\x00\x01\x02\x03")),
		b.Bytes(0x9286, Undefined, append([]byte("ASCII\x00\x00\x00"), "holiday"...)),
		b.Short(0xA405, 24),
		b.Rational(0xA432, 2220, 1000, 6765, 1000, 178, 100, 178, 100),
		b.ASCII(0xA433, "Apple"),
		b.ASCII(0xA434, "iPhone 15 Pro back triple camera 6.765mm f/1.78"),
	}
	b.GPS = []Entry{
		b.Bytes(0x0000, Byte, []byte{2, 2, 0, 0}),
		b.ASCII(0x0001, "S"),
		b.Rational(0x0002, 23, 1, 33, 1, 1800, 100),
		b.ASCII(0x0003, "W"),
		b.Rational(0x0004, 46, 1, 37, 1, 4800, 100),
		b.Bytes(0x0005, Byte, []byte{0}),
		b.Rational(0x0006, 76000, 100),
	}
	return b.Build()
}
