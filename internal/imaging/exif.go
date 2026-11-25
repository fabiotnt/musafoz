package imaging

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf16"
)

// EXIF holds the tags of an EXIF block, grouped by directory. Tags keep the
// order in which they appear in the file.
type EXIF struct {
	ByteOrder string `json:"byte_order"`
	IFD0      []Tag  `json:"ifd0,omitempty"`
	Exif      []Tag  `json:"exif,omitempty"`
	GPS       []Tag  `json:"gps,omitempty"`
	Interop   []Tag  `json:"interop,omitempty"`
	// IFD1 describes the embedded thumbnail, when there is one.
	IFD1 []Tag `json:"ifd1,omitempty"`
	// Warnings lists parts of the block that could not be read.
	Warnings []string `json:"warnings,omitempty"`
}

// Tag is one EXIF entry. ID is the numeric tag as "0x010F"; Name is empty
// for tags musafoz does not know, which are still listed with their value.
type Tag struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Type  string `json:"type"`
	Count uint32 `json:"count"`
	// Value is a number, a string, a Rational, or an array of these. It is
	// absent when the value is given as Hex or could not be read.
	Value any `json:"value,omitempty"`
	// Hex holds raw bytes of UNDEFINED and BYTE values that are not text.
	Hex string `json:"hex,omitempty"`
	// Note explains a value that is not decoded, such as a maker note.
	Note string `json:"note,omitempty"`
	// Error tells why the value could not be read.
	Error string `json:"error,omitempty"`

	id  uint16
	raw any
}

// Rational is an EXIF RATIONAL or SRATIONAL value.
type Rational struct {
	Num int64 `json:"numerator"`
	Den int64 `json:"denominator"`
}

// Float returns the value, or NaN when the denominator is zero.
func (r Rational) Float() float64 {
	if r.Den == 0 {
		return math.NaN()
	}
	return float64(r.Num) / float64(r.Den)
}

// Limits for reading EXIF.
const (
	maxIFDEntries = 1000
	// maxRawBytes caps the raw bytes shown as hex for one tag.
	maxRawBytes = 64 << 10
)

var typeNames = map[uint16]string{
	1: "BYTE", 2: "ASCII", 3: "SHORT", 4: "LONG", 5: "RATIONAL", 6: "SBYTE",
	7: "UNDEFINED", 8: "SSHORT", 9: "SLONG", 10: "SRATIONAL", 11: "FLOAT", 12: "DOUBLE",
}

var typeSizes = map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8}

// Pointer tags that link directories.
const (
	tagExifIFD    = 0x8769
	tagGPSIFD     = 0x8825
	tagInteropIFD = 0xA005
	tagOrient     = 0x0112
)

// ParseEXIF reads a TIFF-structured EXIF block: IFD0, the Exif, GPS, and
// Interoperability directories it points to, and IFD1. Damaged parts are
// skipped and reported in Warnings; an error is returned only when the
// header itself is invalid.
func ParseEXIF(data []byte) (*EXIF, error) {
	if len(data) < 8 {
		return nil, errors.New("EXIF block is too short")
	}
	var bo binary.ByteOrder
	e := &EXIF{}
	switch string(data[0:2]) {
	case "II":
		bo, e.ByteOrder = binary.LittleEndian, "little-endian"
	case "MM":
		bo, e.ByteOrder = binary.BigEndian, "big-endian"
	default:
		return nil, errors.New("EXIF block has no TIFF byte order mark")
	}
	if bo.Uint16(data[2:]) != 42 {
		return nil, errors.New("EXIF block has no TIFF header")
	}
	p := &exifParser{data: data, bo: bo, e: e, seen: map[uint32]bool{}}
	next := p.readIFD(bo.Uint32(data[4:]), "ifd0", &e.IFD0)
	if next != 0 {
		p.readIFD(next, "ifd1", &e.IFD1)
	}
	for _, t := range e.IFD0 {
		switch t.id {
		case tagExifIFD:
			if off, ok := t.pointer(); ok {
				p.readIFD(off, "exif", &e.Exif)
			}
		case tagGPSIFD:
			if off, ok := t.pointer(); ok {
				p.readIFD(off, "gps", &e.GPS)
			}
		}
	}
	for _, t := range e.Exif {
		if t.id == tagInteropIFD {
			if off, ok := t.pointer(); ok {
				p.readIFD(off, "interop", &e.Interop)
			}
		}
	}
	return e, nil
}

func (t Tag) pointer() (uint32, bool) {
	switch v := t.raw.(type) {
	case uint64:
		return uint32(v), true
	case []uint64:
		if len(v) > 0 {
			return uint32(v[0]), true
		}
	}
	return 0, false
}

type exifParser struct {
	data []byte
	bo   binary.ByteOrder
	e    *EXIF
	seen map[uint32]bool
}

func (p *exifParser) warnf(format string, args ...any) {
	p.e.Warnings = append(p.e.Warnings, fmt.Sprintf(format, args...))
}

// readIFD appends the entries of the directory at off and returns the
// offset of the next directory in the chain, or zero.
func (p *exifParser) readIFD(off uint32, group string, out *[]Tag) uint32 {
	if off == 0 {
		return 0
	}
	if p.seen[off] {
		p.warnf("%s: directory at offset %d is referenced twice; skipped", group, off)
		return 0
	}
	p.seen[off] = true
	if int64(off)+2 > int64(len(p.data)) {
		p.warnf("%s: directory offset %d is outside the EXIF block", group, off)
		return 0
	}
	n := int(p.bo.Uint16(p.data[off:]))
	if n > maxIFDEntries {
		p.warnf("%s: %d entries is too many; skipped", group, n)
		return 0
	}
	base := int(off) + 2
	for i := range n {
		at := base + i*12
		if at+12 > len(p.data) {
			p.warnf("%s: directory is cut short after %d of %d entries", group, i, n)
			return 0
		}
		*out = append(*out, p.readEntry(group, p.data[at:at+12]))
	}
	end := base + n*12
	if end+4 > len(p.data) {
		return 0
	}
	return p.bo.Uint32(p.data[end:])
}

func (p *exifParser) readEntry(group string, ent []byte) Tag {
	id := p.bo.Uint16(ent[0:])
	typ := p.bo.Uint16(ent[2:])
	count := p.bo.Uint32(ent[4:])
	t := Tag{ID: fmt.Sprintf("0x%04X", id), Name: tagName(group, id), Count: count, id: id}
	t.Type = typeNames[typ]
	size, ok := typeSizes[typ]
	if !ok {
		t.Type = fmt.Sprintf("unknown (%d)", typ)
		t.Error = "unknown value type"
		return t
	}
	total := int64(size) * int64(count)
	var val []byte
	if total <= 4 {
		val = ent[8 : 8+total]
	} else {
		off := int64(p.bo.Uint32(ent[8:]))
		if off+total > int64(len(p.data)) {
			t.Error = fmt.Sprintf("value of %d bytes at offset %d is outside the EXIF block", total, off)
			return t
		}
		val = p.data[off : off+total]
	}
	p.decode(&t, group, typ, int(count), val)
	return t
}

func (p *exifParser) decode(t *Tag, group string, typ uint16, n int, b []byte) {
	bo := p.bo
	switch typ {
	case 2: // ASCII
		t.Value = cleanText(b)
		return
	case 1, 7: // BYTE, UNDEFINED
		if s, ok := textValue(group, t.id, b, bo); ok {
			t.Value = s
			return
		}
		if group == "exif" && t.id == 0x927C {
			t.Note = "maker note in the camera maker's proprietary format; shown as raw bytes and not decoded"
		}
		if n == 1 || (typ == 1 && n <= 8) || (group == "exif" && (t.id == 0x9101 || t.id == 0xA300 || t.id == 0xA301)) {
			vals := make([]uint64, n)
			for i := range n {
				vals[i] = uint64(b[i])
			}
			t.raw = unsignedValue(vals)
			t.Value = t.raw
			return
		}
		if len(b) > maxRawBytes {
			if t.Note == "" {
				t.Note = fmt.Sprintf("%d bytes, larger than the %d bytes shown", len(b), maxRawBytes)
			} else {
				t.Note += fmt.Sprintf("; %d bytes, larger than the %d bytes shown", len(b), maxRawBytes)
			}
			return
		}
		t.Hex = hex.EncodeToString(b)
	case 3, 4: // SHORT, LONG
		vals := make([]uint64, n)
		for i := range n {
			if typ == 3 {
				vals[i] = uint64(bo.Uint16(b[i*2:]))
			} else {
				vals[i] = uint64(bo.Uint32(b[i*4:]))
			}
		}
		t.raw = unsignedValue(vals)
		t.Value = t.raw
	case 6, 8, 9: // SBYTE, SSHORT, SLONG
		vals := make([]int64, n)
		for i := range n {
			switch typ {
			case 6:
				vals[i] = int64(int8(b[i]))
			case 8:
				vals[i] = int64(int16(bo.Uint16(b[i*2:])))
			default:
				vals[i] = int64(int32(bo.Uint32(b[i*4:])))
			}
		}
		if n == 1 {
			t.Value = vals[0]
		} else {
			t.Value = vals
		}
	case 5, 10: // RATIONAL, SRATIONAL
		vals := make([]Rational, n)
		for i := range n {
			if typ == 5 {
				vals[i] = Rational{int64(bo.Uint32(b[i*8:])), int64(bo.Uint32(b[i*8+4:]))}
			} else {
				vals[i] = Rational{int64(int32(bo.Uint32(b[i*8:]))), int64(int32(bo.Uint32(b[i*8+4:])))}
			}
		}
		if n == 1 {
			t.Value = vals[0]
		} else {
			t.Value = vals
		}
		t.raw = vals
	case 11, 12: // FLOAT, DOUBLE
		vals := make([]float64, n)
		for i := range n {
			if typ == 11 {
				vals[i] = float64(math.Float32frombits(bo.Uint32(b[i*4:])))
			} else {
				vals[i] = math.Float64frombits(bo.Uint64(b[i*8:]))
			}
		}
		if n == 1 {
			t.Value = vals[0]
		} else {
			t.Value = vals
		}
	}
	// JSON cannot carry NaN or infinities.
	switch v := t.Value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Value = fmt.Sprint(v)
		}
	case []float64:
		for _, f := range v {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				t.Value = fmt.Sprint(v)
				break
			}
		}
	}
}

func unsignedValue(v []uint64) any {
	if len(v) == 1 {
		return v[0]
	}
	return v
}

// textValue decodes UNDEFINED and BYTE tags that hold text.
func textValue(group string, id uint16, b []byte, bo binary.ByteOrder) (string, bool) {
	switch {
	case group == "exif" && (id == 0x9000 || id == 0xA000), group == "interop" && id == 0x0002:
		return cleanText(b), true // version strings such as "0232"
	case group == "exif" && id == 0x9286, group == "gps" && (id == 0x001B || id == 0x001C):
		return commentText(b, bo), true
	case (group == "ifd0" || group == "ifd1") && id >= 0x9C9B && id <= 0x9C9F:
		return utf16Text(b, binary.LittleEndian), true // Windows XP tags
	case (group == "ifd0" || group == "ifd1") && id == 0x02BC:
		return cleanText(b), true // XMP packet
	}
	return "", false
}

// commentText decodes a UserComment-style value: an 8-byte character code
// followed by the text.
func commentText(b []byte, bo binary.ByteOrder) string {
	if len(b) < 8 {
		return cleanText(b)
	}
	code, body := string(b[:8]), b[8:]
	switch {
	case strings.HasPrefix(code, "UNICODE"):
		return utf16Text(body, bo)
	default:
		return cleanText(body)
	}
}

func utf16Text(b []byte, bo binary.ByteOrder) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = bo.Uint16(b[i*2:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00 ")
}

// cleanText trims padding NULs and spaces and replaces invalid UTF-8.
func cleanText(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.ToValidUTF8(strings.TrimRight(s, " "), "\uFFFD")
}

// Lookup returns the first tag with the id in the group ("ifd0", "exif",
// "gps", "interop", or "ifd1").
func (e *EXIF) Lookup(group string, id uint16) (Tag, bool) {
	if e == nil {
		return Tag{}, false
	}
	var list []Tag
	switch group {
	case "ifd0":
		list = e.IFD0
	case "exif":
		list = e.Exif
	case "gps":
		list = e.GPS
	case "interop":
		list = e.Interop
	case "ifd1":
		list = e.IFD1
	}
	for _, t := range list {
		if t.id == id && t.Error == "" {
			return t, true
		}
	}
	return Tag{}, false
}

// Count returns the number of tags in every directory.
func (e *EXIF) Count() int {
	if e == nil {
		return 0
	}
	return len(e.IFD0) + len(e.Exif) + len(e.GPS) + len(e.Interop) + len(e.IFD1)
}

// Orientation returns the EXIF orientation, 1 to 8, or 0 when it is
// absent or invalid.
func (e *EXIF) Orientation() int {
	t, ok := e.Lookup("ifd0", tagOrient)
	if !ok {
		return 0
	}
	if v, ok := t.raw.(uint64); ok && v >= 1 && v <= 8 {
		return int(v)
	}
	return 0
}

// String returns a tag value as text.
func (t Tag) String() string {
	switch v := t.Value.(type) {
	case nil:
		return ""
	case string:
		return v
	case Rational:
		return fmt.Sprintf("%d/%d", v.Num, v.Den)
	default:
		return fmt.Sprint(v)
	}
}

// Rationals returns RATIONAL or SRATIONAL values.
func (t Tag) Rationals() []Rational {
	r, _ := t.raw.([]Rational)
	return r
}

// Uint returns the first SHORT, LONG, or BYTE value.
func (t Tag) Uint() (uint64, bool) {
	switch v := t.raw.(type) {
	case uint64:
		return v, true
	case []uint64:
		if len(v) > 0 {
			return v[0], true
		}
	}
	return 0, false
}
