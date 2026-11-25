package testimages

import (
	"bytes"
	"encoding/binary"
)

// HEIFOptions describe a synthetic HEIF file built around an HEVC still.
type HEIFOptions struct {
	// HEVC is an Annex B HEVC bitstream with one picture, such as
	// testdata/image/quadrants-64x48.hevc.
	HEVC []byte
	// Width and Height are the picture size, written to ispe.
	Width, Height int
	// Grid places the picture twice side by side in a 2x1 tile grid, so
	// the primary item is a grid of twice the width.
	Grid bool
	// Rotation is an irot angle in degrees counterclockwise: 0, 90, 180,
	// or 270. Zero writes no irot.
	Rotation int
	// Mirror writes an imir property: -1 for none, 0 for a flip about the
	// vertical axis, 1 for a flip about the horizontal axis.
	Mirror int
	// MirrorFirst writes imir before irot in the property associations.
	MirrorFirst bool
	// EXIF is a TIFF-structured block stored as an Exif item that
	// describes the primary item.
	EXIF []byte
	// Thumbnail adds a second, smaller-looking hvc1 item that is not the
	// primary item.
	Thumbnail bool
}

type bbuf struct{ bytes.Buffer }

func (b *bbuf) u8(v int)     { b.WriteByte(byte(v)) }
func (b *bbuf) u16(v int)    { b.Write(binary.BigEndian.AppendUint16(nil, uint16(v))) }
func (b *bbuf) u32(v int)    { b.Write(binary.BigEndian.AppendUint32(nil, uint32(v))) }
func (b *bbuf) str(s string) { b.WriteString(s) }

func mkbox(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(n))
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func full(version, flags int) []byte {
	return []byte{byte(version), byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

// nalUnits splits an Annex B stream at its start codes.
func nalUnits(stream []byte) [][]byte {
	var units [][]byte
	start := -1
	for i := 0; i+3 <= len(stream); i++ {
		if stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 1 {
			if start >= 0 {
				units = append(units, bytes.TrimRight(stream[start:i], "\x00"))
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(stream) {
		units = append(units, stream[start:])
	}
	return units
}

// hevcItem returns the hvcC property and the length-prefixed picture data
// of an Annex B stream.
func hevcItem(stream []byte) (hvcC, data []byte) {
	var params [3][]byte // VPS, SPS, PPS
	var pic bbuf
	for _, nal := range nalUnits(stream) {
		if len(nal) < 2 {
			continue
		}
		switch t := int(nal[0]>>1) & 0x3F; t {
		case 32, 33, 34:
			params[t-32] = nal
		default:
			if t < 32 { // picture slices; SEI and others are left out
				pic.u32(len(nal))
				pic.Write(nal)
			}
		}
	}
	var c bbuf
	c.u8(1)                  // configurationVersion
	c.u8(1)                  // profile space 0, tier 0, profile Main
	c.u32(0x60000000)        // profile compatibility
	c.Write(make([]byte, 6)) // constraint flags
	c.u8(60)                 // level 2
	c.u16(0xF000)            // min spatial segmentation
	c.u8(0xFC)               // parallelism
	c.u8(0xFD)               // chroma 4:2:0
	c.u8(0xF8)               // luma bit depth 8
	c.u8(0xF8)               // chroma bit depth 8
	c.u16(0)                 // average frame rate
	c.u8(0x0F)               // one temporal layer, nested, 4-byte lengths
	c.u8(3)                  // arrays
	for i, nal := range params {
		c.u8(0x80 | (32 + i))
		c.u16(1)
		c.u16(len(nal))
		c.Write(nal)
	}
	return mkbox("hvcC", c.Bytes()), pic.Bytes()
}

// HEIF builds a HEIF file. The primary item is item 1.
func HEIF(o HEIFOptions) []byte {
	hvcC, pic := hevcItem(o.HEVC)
	ispe := func(w, h int) []byte {
		var b bbuf
		b.Write(full(0, 0))
		b.u32(w)
		b.u32(h)
		return mkbox("ispe", b.Bytes())
	}

	// Items: 1 primary (hvc1 or grid), 2 and 3 tiles for a grid, then an
	// optional thumbnail and Exif item. Data: id -> bytes in mdat, or idat
	// for the grid description.
	type item struct {
		id   int
		typ  string
		data []byte
		idat bool
	}
	items := []item{}
	primaryW, primaryH := o.Width, o.Height
	if o.Grid {
		primaryW = 2 * o.Width
		var g bbuf
		g.u8(0)         // version
		g.u8(0)         // flags: 16-bit sizes
		g.u8(0)         // rows - 1
		g.u8(1)         // columns - 1
		g.u16(primaryW) // output width
		g.u16(primaryH) // output height
		items = append(items, item{1, "grid", g.Bytes(), true}, item{2, "hvc1", pic, false}, item{3, "hvc1", pic, false})
	} else {
		items = append(items, item{1, "hvc1", pic, false})
	}
	next := len(items) + 1
	thumbID, exifID := 0, 0
	if o.Thumbnail {
		thumbID = next
		items = append(items, item{thumbID, "hvc1", pic, false})
		next++
	}
	if o.EXIF != nil {
		exifID = next
		var e bbuf
		e.u32(0) // offset to the TIFF header after this field
		e.Write(o.EXIF)
		items = append(items, item{exifID, "Exif", e.Bytes(), false})
	}

	// Properties, 1-based: hvcC, ispe of the tile, ispe of the grid, then
	// the transforms.
	props := [][]byte{hvcC, ispe(o.Width, o.Height)}
	gridIspe := 0
	if o.Grid {
		props = append(props, ispe(primaryW, primaryH))
		gridIspe = len(props)
	}
	var transforms []int
	addRot := func() {
		if o.Rotation != 0 {
			props = append(props, mkbox("irot", []byte{byte(o.Rotation / 90 & 3)}))
			transforms = append(transforms, len(props))
		}
	}
	addMir := func() {
		if o.Mirror >= 0 {
			props = append(props, mkbox("imir", []byte{byte(o.Mirror & 1)}))
			transforms = append(transforms, len(props))
		}
	}
	if o.MirrorFirst {
		addMir()
		addRot()
	} else {
		addRot()
		addMir()
	}
	var ipco []byte
	for _, p := range props {
		ipco = append(ipco, p...)
	}

	var ipma bbuf
	ipma.Write(full(0, 0))
	assoc := map[int][]int{}
	if o.Grid {
		assoc[1] = append([]int{gridIspe}, transforms...)
		assoc[2] = []int{1, 2}
		assoc[3] = []int{1, 2}
	} else {
		assoc[1] = append([]int{1, 2}, transforms...)
	}
	if thumbID != 0 {
		assoc[thumbID] = []int{1, 2}
	}
	ids := []int{}
	for _, it := range items {
		if _, ok := assoc[it.id]; ok {
			ids = append(ids, it.id)
		}
	}
	ipma.u32(len(ids))
	for _, id := range ids {
		ipma.u16(id)
		ipma.u8(len(assoc[id]))
		for _, p := range assoc[id] {
			essential := 0
			if p != 2 && p != gridIspe {
				essential = 0x80
			}
			ipma.u8(essential | p)
		}
	}
	iprp := mkbox("iprp", mkbox("ipco", ipco), mkbox("ipma", ipma.Bytes()))

	var iinf bbuf
	iinf.Write(full(0, 0))
	iinf.u16(len(items))
	for _, it := range items {
		var e bbuf
		e.Write(full(2, 0))
		e.u16(it.id)
		e.u16(0)
		e.str(it.typ)
		e.u8(0) // empty name
		iinf.Write(mkbox("infe", e.Bytes()))
	}

	var iref bbuf
	iref.Write(full(0, 0))
	if o.Grid {
		var d bbuf
		d.u16(1)
		d.u16(2)
		d.u16(2)
		d.u16(3)
		iref.Write(mkbox("dimg", d.Bytes()))
	}
	if thumbID != 0 {
		var d bbuf
		d.u16(thumbID)
		d.u16(1)
		d.u16(1)
		iref.Write(mkbox("thmb", d.Bytes()))
	}
	if exifID != 0 {
		var d bbuf
		d.u16(exifID)
		d.u16(1)
		d.u16(1)
		iref.Write(mkbox("cdsc", d.Bytes()))
	}

	var hdlr bbuf
	hdlr.Write(full(0, 0))
	hdlr.u32(0)
	hdlr.str("pict")
	hdlr.Write(make([]byte, 12))
	hdlr.u8(0)
	var pitm bbuf
	pitm.Write(full(0, 0))
	pitm.u16(1)

	var idat []byte
	for _, it := range items {
		if it.idat {
			idat = append(idat, it.data...)
		}
	}

	// iloc version 1 for construction methods; offsets are filled in after
	// the size of everything before mdat is known.
	buildILOC := func(mdatStart int) []byte {
		var l bbuf
		l.Write(full(1, 0))
		l.u8(0x44) // offset size 4, length size 4
		l.u8(0x00) // base offset size 0, index size 0
		l.u16(len(items))
		off, idatOff := mdatStart, 0
		for _, it := range items {
			l.u16(it.id)
			if it.idat {
				l.u16(1)
			} else {
				l.u16(0)
			}
			l.u16(0) // data reference
			l.u16(1) // one extent
			if it.idat {
				l.u32(idatOff)
				idatOff += len(it.data)
			} else {
				l.u32(off)
				off += len(it.data)
			}
			l.u32(len(it.data))
		}
		return mkbox("iloc", l.Bytes())
	}
	ftyp := mkbox("ftyp", []byte("heic"), []byte{0, 0, 0, 0}, []byte("mif1heic"))
	assemble := func(mdatStart int) []byte {
		parts := [][]byte{full(0, 0), mkbox("hdlr", hdlr.Bytes()), mkbox("pitm", pitm.Bytes()),
			mkbox("iinf", iinf.Bytes()), mkbox("iref", iref.Bytes()), iprp}
		if idat != nil {
			parts = append(parts, mkbox("idat", idat))
		}
		parts = append(parts, buildILOC(mdatStart))
		return append(append([]byte{}, ftyp...), mkbox("meta", parts...)...)
	}
	head := assemble(0)
	head = assemble(len(head) + 8)
	var mdat []byte
	for _, it := range items {
		if !it.idat {
			mdat = append(mdat, it.data...)
		}
	}
	return append(head, mkbox("mdat", mdat)...)
}
