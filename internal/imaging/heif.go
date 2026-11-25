package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"strings"
)

// HEIF describes the primary image of a HEIF file (ISO/IEC 23008-12), as
// read from its container boxes. Pixels are not decoded here.
type HEIF struct {
	// ItemType is the primary item type, such as "hvc1" or "grid".
	ItemType string
	// Width and Height come from the ispe property: the stored size, before
	// the rotation and mirroring below.
	Width, Height int
	// Transforms lists the irot and imir properties of the primary item in
	// the order they apply.
	Transforms []HEIFTransform
	// Alpha reports an alpha auxiliary image linked to the primary item.
	Alpha bool
	// EXIF is the TIFF-structured EXIF block of the primary item, or nil.
	EXIF []byte
}

// HEIFTransform is one irot or imir property. Rotation is in degrees
// counterclockwise, as irot stores it. Mirror is "top-bottom" for imir axis
// 0 and "left-right" for axis 1, which is how libheif and FFmpeg 9 apply
// them; the axis value is kept in MirrorAxis.
type HEIFTransform struct {
	Rotation   int    `json:"rotation_ccw,omitempty"`
	Mirror     string `json:"mirror,omitempty"`
	MirrorAxis *int   `json:"mirror_axis,omitempty"`
}

// orientation returns the EXIF orientation value (see Orient) that performs
// the transform.
func (t HEIFTransform) orientation() int {
	switch {
	case t.Mirror == "top-bottom":
		return 4
	case t.Mirror == "left-right":
		return 2
	case t.Rotation == 90:
		return 8
	case t.Rotation == 180:
		return 3
	case t.Rotation == 270:
		return 6
	}
	return 1
}

// DecoderCorrection returns the EXIF orientation value (see Orient) to
// apply to an image that FFmpeg decoded from this file, so the result
// follows the transforms in the order the file lists them. FFmpeg 9 applies
// the rotation first and the mirroring second whatever their order, so the
// result is 1 (nothing to do) unless the file mirrors before it rotates.
func (h *HEIF) DecoderCorrection() int {
	var rot, mir []HEIFTransform
	for _, t := range h.Transforms {
		if t.Mirror != "" {
			mir = append(mir, t)
		} else {
			rot = append(rot, t)
		}
	}
	return correction(append(rot, mir...), h.Transforms)
}

// correction finds the orientation value that turns the result of the
// applied transforms into the result of the wanted ones, by applying both
// to a small image with distinct pixels.
func correction(applied, wanted []HEIFTransform) int {
	probe := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := range 6 {
		probe.Pix[i*4] = byte(i + 1)
		probe.Pix[i*4+3] = 255
	}
	run := func(ts []HEIFTransform) image.Image {
		var img image.Image = probe
		for _, t := range ts {
			img = Orient(img, t.orientation())
		}
		return img
	}
	got, want := run(applied), run(wanted)
	for o := 1; o <= 8; o++ {
		if samePixels(Orient(got, o), want) {
			return o
		}
	}
	return 1
}

func samePixels(a, b image.Image) bool {
	if a.Bounds().Size() != b.Bounds().Size() {
		return false
	}
	ab, bb := a.Bounds(), b.Bounds()
	for y := range ab.Dy() {
		for x := range ab.Dx() {
			if a.At(ab.Min.X+x, ab.Min.Y+y) != b.At(bb.Min.X+x, bb.Min.Y+y) {
				return false
			}
		}
	}
	return true
}

// DisplaySize returns the size after the transforms.
func (h *HEIF) DisplaySize() (w, h2 int) {
	w, h2 = h.Width, h.Height
	for _, t := range h.Transforms {
		if t.Rotation == 90 || t.Rotation == 270 {
			w, h2 = h2, w
		}
	}
	return w, h2
}

type box struct {
	typ  string
	body []byte
}

// readBoxes splits data into ISO BMFF boxes.
func readBoxes(data []byte) ([]box, error) {
	var out []box
	for len(data) > 0 {
		if len(data) < 8 {
			return out, errors.New("truncated box header")
		}
		size := uint64(binary.BigEndian.Uint32(data))
		typ := string(data[4:8])
		hdr := uint64(8)
		switch size {
		case 1:
			if len(data) < 16 {
				return out, errors.New("truncated box header")
			}
			size, hdr = binary.BigEndian.Uint64(data[8:]), 16
		case 0:
			size = uint64(len(data))
		}
		if size < hdr || size > uint64(len(data)) {
			return out, fmt.Errorf("box %q has an invalid size", typ)
		}
		out = append(out, box{typ, data[hdr:size]})
		data = data[size:]
	}
	return out, nil
}

func findBox(boxes []box, typ string) (box, bool) {
	for _, b := range boxes {
		if b.typ == typ {
			return b, true
		}
	}
	return box{}, false
}

// reader reads big-endian fields and records the first short read.
type reader struct {
	b   []byte
	err bool
}

func (r *reader) n(size int) uint64 {
	if size == 0 {
		return 0
	}
	if len(r.b) < size {
		r.err = true
		r.b = nil
		return 0
	}
	var v uint64
	for _, c := range r.b[:size] {
		v = v<<8 | uint64(c)
	}
	r.b = r.b[size:]
	return v
}

func (r *reader) str() string {
	i := bytes.IndexByte(r.b, 0)
	if i < 0 {
		s := string(r.b)
		r.b = nil
		return s
	}
	s := string(r.b[:i])
	r.b = r.b[i+1:]
	return s
}

type heifItem struct {
	typ string
}

type extent struct {
	offset, length uint64
}

type location struct {
	method   int
	base     uint64
	extents  []extent
	dataRefs uint64
}

// ParseHEIF reads the container of a HEIF file and returns its primary
// image. data is the whole file.
func ParseHEIF(data []byte) (*HEIF, error) {
	top, err := readBoxes(data)
	if err != nil && len(top) == 0 {
		return nil, fmt.Errorf("HEIF container: %w", err)
	}
	meta, ok := findBox(top, "meta")
	if !ok || len(meta.body) < 4 {
		return nil, errors.New("HEIF container: no meta box")
	}
	children, err := readBoxes(meta.body[4:])
	if err != nil {
		return nil, fmt.Errorf("HEIF container: %w", err)
	}

	pitm, ok := findBox(children, "pitm")
	if !ok {
		return nil, errors.New("HEIF container: no primary item (pitm)")
	}
	r := &reader{b: pitm.body}
	version := r.n(1)
	r.n(3)
	primary := r.n(2)
	if version > 0 {
		primary = primary<<16 | r.n(2)
	}
	if r.err {
		return nil, errors.New("HEIF container: truncated pitm box")
	}

	items, err := parseIINF(children)
	if err != nil {
		return nil, err
	}
	locs, err := parseILOC(children)
	if err != nil {
		return nil, err
	}
	props, assoc, err := parseIPRP(children)
	if err != nil {
		return nil, err
	}
	refs := parseIREF(children)

	it, ok := items[primary]
	if !ok {
		return nil, fmt.Errorf("HEIF container: primary item %d is not described", primary)
	}
	h := &HEIF{ItemType: it.typ}
	gotSize := false
	for _, idx := range assoc[primary] {
		if idx == 0 || int(idx) > len(props) {
			continue
		}
		p := props[idx-1]
		pr := &reader{b: p.body}
		switch p.typ {
		case "ispe":
			pr.n(4)
			w, hh := pr.n(4), pr.n(4)
			if !pr.err {
				h.Width, h.Height, gotSize = int(w), int(hh), true
			}
		case "irot":
			if len(p.body) > 0 {
				h.Transforms = append(h.Transforms, HEIFTransform{Rotation: int(p.body[0]&3) * 90})
			}
		case "imir":
			if len(p.body) > 0 {
				axis := int(p.body[0] & 1)
				mirror := "top-bottom"
				if axis == 1 {
					mirror = "left-right"
				}
				h.Transforms = append(h.Transforms, HEIFTransform{Mirror: mirror, MirrorAxis: &axis})
			}
		}
	}
	if !gotSize {
		return nil, errors.New("HEIF container: the primary item has no size (ispe)")
	}
	// Rotations of zero degrees change nothing.
	kept := h.Transforms[:0]
	for _, t := range h.Transforms {
		if t.Rotation != 0 || t.Mirror != "" {
			kept = append(kept, t)
		}
	}
	h.Transforms = kept

	// Items that describe the primary item: EXIF metadata (cdsc) and an
	// alpha plane (auxl with an alpha auxC type).
	for from, links := range refs {
		for _, l := range links {
			if l.to != primary {
				continue
			}
			switch {
			case l.typ == "cdsc" && items[from].typ == "Exif" && h.EXIF == nil:
				if block, err := itemData(data, children, locs[from]); err == nil {
					h.EXIF = exifFromItem(block)
				}
			case l.typ == "auxl":
				for _, idx := range assoc[from] {
					if idx > 0 && int(idx) <= len(props) && props[idx-1].typ == "auxC" {
						ar := &reader{b: props[idx-1].body}
						ar.n(4)
						if strings.Contains(ar.str(), "alpha") {
							h.Alpha = true
						}
					}
				}
			}
		}
	}
	return h, nil
}

func parseIINF(children []box) (map[uint64]heifItem, error) {
	iinf, ok := findBox(children, "iinf")
	if !ok {
		return nil, errors.New("HEIF container: no item list (iinf)")
	}
	r := &reader{b: iinf.body}
	version := r.n(1)
	r.n(3)
	count := r.n(2)
	if version > 0 {
		count = count<<16 | r.n(2)
	}
	if r.err {
		return nil, errors.New("HEIF container: truncated iinf box")
	}
	entries, err := readBoxes(r.b)
	if err != nil {
		return nil, fmt.Errorf("HEIF container: %w", err)
	}
	items := map[uint64]heifItem{}
	for _, e := range entries {
		if e.typ != "infe" {
			continue
		}
		er := &reader{b: e.body}
		v := er.n(1)
		er.n(3)
		if v < 2 {
			continue // versions 0 and 1 predate item types
		}
		id := er.n(2)
		if v >= 3 {
			id = id<<16 | er.n(2)
		}
		er.n(2) // protection index
		typ := string(er.b[:min(4, len(er.b))])
		er.n(4)
		if er.err {
			return nil, errors.New("HEIF container: truncated infe box")
		}
		items[id] = heifItem{typ: typ}
	}
	_ = count
	return items, nil
}

func parseILOC(children []box) (map[uint64]location, error) {
	iloc, ok := findBox(children, "iloc")
	if !ok {
		return nil, errors.New("HEIF container: no item locations (iloc)")
	}
	r := &reader{b: iloc.body}
	version := r.n(1)
	r.n(3)
	sizes := r.n(1)
	offsetSize, lengthSize := int(sizes>>4), int(sizes&15)
	sizes = r.n(1)
	baseSize, indexSize := int(sizes>>4), int(sizes&15)
	if version == 0 {
		indexSize = 0
	}
	var count uint64
	if version < 2 {
		count = r.n(2)
	} else {
		count = r.n(4)
	}
	locs := map[uint64]location{}
	for range count {
		var id uint64
		if version < 2 {
			id = r.n(2)
		} else {
			id = r.n(4)
		}
		var loc location
		if version == 1 || version == 2 {
			loc.method = int(r.n(2) & 15)
		}
		loc.dataRefs = r.n(2)
		loc.base = r.n(baseSize)
		n := r.n(2)
		for range n {
			r.n(indexSize)
			off := r.n(offsetSize)
			length := r.n(lengthSize)
			loc.extents = append(loc.extents, extent{off, length})
		}
		if r.err {
			return nil, errors.New("HEIF container: truncated iloc box")
		}
		locs[id] = loc
	}
	return locs, nil
}

func parseIPRP(children []box) (props []box, assoc map[uint64][]uint16, err error) {
	iprp, ok := findBox(children, "iprp")
	if !ok {
		return nil, nil, errors.New("HEIF container: no item properties (iprp)")
	}
	ib, err := readBoxes(iprp.body)
	if err != nil {
		return nil, nil, fmt.Errorf("HEIF container: %w", err)
	}
	ipco, ok := findBox(ib, "ipco")
	if !ok {
		return nil, nil, errors.New("HEIF container: no property list (ipco)")
	}
	if props, err = readBoxes(ipco.body); err != nil {
		return nil, nil, fmt.Errorf("HEIF container: %w", err)
	}
	assoc = map[uint64][]uint16{}
	for _, b := range ib {
		if b.typ != "ipma" {
			continue
		}
		r := &reader{b: b.body}
		version := r.n(1)
		flags := r.n(3)
		count := r.n(4)
		for range count {
			var id uint64
			if version < 1 {
				id = r.n(2)
			} else {
				id = r.n(4)
			}
			n := r.n(1)
			for range n {
				var idx uint16
				if flags&1 == 1 {
					idx = uint16(r.n(2) & 0x7FFF)
				} else {
					idx = uint16(r.n(1) & 0x7F)
				}
				assoc[id] = append(assoc[id], idx)
			}
		}
		if r.err {
			return nil, nil, errors.New("HEIF container: truncated ipma box")
		}
	}
	return props, assoc, nil
}

type itemRef struct {
	typ string
	to  uint64
}

func parseIREF(children []box) map[uint64][]itemRef {
	refs := map[uint64][]itemRef{}
	iref, ok := findBox(children, "iref")
	if !ok || len(iref.body) < 4 {
		return refs
	}
	version := iref.body[0]
	list, _ := readBoxes(iref.body[4:])
	idSize := 2
	if version > 0 {
		idSize = 4
	}
	for _, b := range list {
		r := &reader{b: b.body}
		from := r.n(idSize)
		n := r.n(2)
		for range n {
			to := r.n(idSize)
			if r.err {
				break
			}
			refs[from] = append(refs[from], itemRef{b.typ, to})
		}
	}
	return refs
}

// itemData returns the bytes of an item stored in the file (construction
// method 0) or in the idat box (method 1).
func itemData(data []byte, children []box, loc location) ([]byte, error) {
	if loc.dataRefs != 0 {
		return nil, errors.New("item data is in another file")
	}
	var src []byte
	switch loc.method {
	case 0:
		src = data
	case 1:
		idat, ok := findBox(children, "idat")
		if !ok {
			return nil, errors.New("item data is in a missing idat box")
		}
		src = idat.body
	default:
		return nil, fmt.Errorf("item construction method %d is not supported", loc.method)
	}
	var out []byte
	for _, e := range loc.extents {
		start := loc.base + e.offset
		length := e.length
		if length == 0 {
			if start > uint64(len(src)) {
				return nil, errors.New("item data is outside the file")
			}
			length = uint64(len(src)) - start
		}
		if start+length > uint64(len(src)) || start+length < start {
			return nil, errors.New("item data is outside the file")
		}
		out = append(out, src[start:start+length]...)
	}
	return out, nil
}

// exifFromItem finds the TIFF header in an EXIF item: a 4-byte offset to
// the header, then the block. Some writers also keep an "Exif\0\0" prefix.
func exifFromItem(b []byte) []byte {
	if len(b) < 4 {
		return nil
	}
	off := uint64(binary.BigEndian.Uint32(b))
	if 4+off <= uint64(len(b)) {
		rest := b[4+off:]
		rest = bytes.TrimPrefix(rest, []byte("Exif\x00\x00"))
		if bytes.HasPrefix(rest, []byte("II*\x00")) || bytes.HasPrefix(rest, []byte("MM\x00*")) {
			return rest
		}
	}
	for _, mark := range [][]byte{[]byte("II*\x00"), []byte("MM\x00*")} {
		if i := bytes.Index(b, mark); i >= 0 {
			return b[i:]
		}
	}
	return nil
}
