package testimages

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"math"
)

// Colorants adapted to D50, as columns red, green, blue of XYZ values.
var (
	SRGBColorants = [3][3]float64{
		{0.4360747, 0.3850649, 0.1430804},
		{0.2225045, 0.7168786, 0.0606169},
		{0.0139322, 0.0971045, 0.7141733},
	}
	DisplayP3Colorants = [3][3]float64{
		{0.5151, 0.2920, 0.1571},
		{0.2412, 0.6922, 0.0666},
		{-0.0011, 0.0419, 0.7841},
	}
)

// ICC builds a matrix/TRC RGB display profile, version 4, with the
// colorants and the sRGB tone curve (para type 3) on all three channels,
// and a desc tag with the description.
func ICC(desc string, colorants [3][3]float64) []byte {
	s15 := func(v float64) []byte {
		return binary.BigEndian.AppendUint32(nil, uint32(int32(math.Round(v*65536))))
	}
	type tag struct {
		sig  string
		data []byte
	}
	var tags []tag
	d := append([]byte("desc\x00\x00\x00\x00"), binary.BigEndian.AppendUint32(nil, uint32(len(desc)+1))...)
	d = append(append(d, desc...), 0)
	tags = append(tags, tag{"desc", d})
	for c, sig := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		x := []byte("XYZ \x00\x00\x00\x00")
		for r := range 3 {
			x = append(x, s15(colorants[r][c])...)
		}
		tags = append(tags, tag{sig, x})
	}
	curve := []byte("para\x00\x00\x00\x00\x00\x03\x00\x00")
	for _, v := range []float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045} {
		curve = append(curve, s15(v)...)
	}
	for _, sig := range []string{"rTRC", "gTRC", "bTRC"} {
		tags = append(tags, tag{sig, curve})
	}
	off := 128 + 4 + 12*len(tags)
	var table, body bytes.Buffer
	table.Write(binary.BigEndian.AppendUint32(nil, uint32(len(tags))))
	for _, t := range tags {
		for len(t.data)%4 != 0 {
			t.data = append(t.data, 0)
		}
		table.WriteString(t.sig)
		table.Write(binary.BigEndian.AppendUint32(nil, uint32(off+body.Len())))
		table.Write(binary.BigEndian.AppendUint32(nil, uint32(len(t.data))))
		body.Write(t.data)
	}
	head := make([]byte, 128)
	binary.BigEndian.PutUint32(head, uint32(128+table.Len()+body.Len()))
	copy(head[8:], []byte{4, 0x40, 0, 0})
	copy(head[12:], "mntr")
	copy(head[16:], "RGB ")
	copy(head[20:], "XYZ ")
	copy(head[36:], "acsp")
	copy(head[68:], s15(0.9642))
	copy(head[72:], s15(1))
	copy(head[76:], s15(0.8249))
	return append(append(head, table.Bytes()...), body.Bytes()...)
}

// WithJPEGSegments inserts APP segments after the SOI marker of a JPEG.
// Each segment is marker (such as 0xE2) and payload.
func WithJPEGSegments(jpeg []byte, segs ...struct {
	Marker  byte
	Payload []byte
}) []byte {
	out := append([]byte{}, jpeg[:2]...)
	for _, s := range segs {
		out = append(out, 0xFF, s.Marker)
		out = binary.BigEndian.AppendUint16(out, uint16(len(s.Payload)+2))
		out = append(out, s.Payload...)
	}
	return append(out, jpeg[2:]...)
}

// JPEGICC returns the APP2 payloads of a profile split in chunks of at most
// size bytes, as writers do for profiles over 64 KiB.
func JPEGICC(icc []byte, size int) [][]byte {
	var out [][]byte
	n := (len(icc) + size - 1) / size
	for i := range n {
		part := icc[i*size : min(len(icc), (i+1)*size)]
		out = append(out, append(append([]byte("ICC_PROFILE\x00"), byte(i+1), byte(n)), part...))
	}
	return out
}

// WithPNGChunks inserts chunks after the IHDR chunk of a PNG. Each chunk
// is a type and its data.
func WithPNGChunks(png []byte, chunks ...[2][]byte) []byte {
	ihdrEnd := 8 + 12 + int(binary.BigEndian.Uint32(png[8:]))
	out := append([]byte{}, png[:ihdrEnd]...)
	for _, c := range chunks {
		out = binary.BigEndian.AppendUint32(out, uint32(len(c[1])))
		start := len(out)
		out = append(append(out, c[0]...), c[1]...)
		out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[start:]))
	}
	return append(out, png[ihdrEnd:]...)
}

// PNGICCP returns the data of an iCCP chunk.
func PNGICCP(icc []byte) []byte {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write(icc)
	w.Close()
	return append([]byte("profile\x00\x00"), z.Bytes()...)
}

// PNGXMP returns the data of an uncompressed iTXt chunk with an XMP packet.
func PNGXMP(packet string) []byte {
	return []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00" + packet)
}

// ScreenshotXMP is an XMP packet as phones write it in screenshots.
func ScreenshotXMP(date string) string {
	return `<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="XMP Core 6.0.0"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:exif="http://ns.adobe.com/exif/1.0/" xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/">` +
		`<exif:UserComment>Screenshot</exif:UserComment><photoshop:DateCreated>` + date + `</photoshop:DateCreated>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`
}
