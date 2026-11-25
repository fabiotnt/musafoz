package imaging

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"io"
	"regexp"
	"sort"
	"strings"
)

// MaxMetaBytes bounds an XMP packet or an ICC profile read from a file.
const MaxMetaBytes = 4 << 20

// findXMPICC returns the XMP packet and the ICC profile of a JPEG, PNG, or
// WebP file, or nil for each one the file does not have. Only container
// structure is read here.
func findXMPICC(format string, data []byte) (xmp, icc []byte) {
	switch format {
	case FormatJPEG:
		return jpegXMPICC(data)
	case FormatPNG:
		return pngXMPICC(data)
	case FormatWebP:
		return webpXMPICC(data)
	}
	return nil, nil
}

var (
	xmpJPEGPrefix = []byte("http://ns.adobe.com/xap/1.0/\x00")
	iccJPEGPrefix = []byte("ICC_PROFILE\x00")
)

func jpegXMPICC(data []byte) (xmp, icc []byte) {
	type chunk struct {
		seq  int
		body []byte
	}
	var chunks []chunk
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			break
		}
		marker := data[i+1]
		if marker == 0xFF {
			i++
			continue
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			break
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			break
		}
		seg := data[i+4 : i+2+n]
		switch {
		case marker == 0xE1 && bytes.HasPrefix(seg, xmpJPEGPrefix) && xmp == nil:
			xmp = seg[len(xmpJPEGPrefix):]
		case marker == 0xE2 && bytes.HasPrefix(seg, iccJPEGPrefix) && len(seg) > len(iccJPEGPrefix)+2:
			chunks = append(chunks, chunk{int(seg[len(iccJPEGPrefix)]), seg[len(iccJPEGPrefix)+2:]})
		}
		i += 2 + n
	}
	sort.SliceStable(chunks, func(a, b int) bool { return chunks[a].seq < chunks[b].seq })
	for _, c := range chunks {
		icc = append(icc, c.body...)
	}
	return xmp, icc
}

func pngXMPICC(data []byte) (xmp, icc []byte) {
	i := 8
	for i+12 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[i:]))
		typ := string(data[i+4 : i+8])
		if n < 0 || i+12+n > len(data) || typ == "IEND" {
			break
		}
		body := data[i+8 : i+8+n]
		switch typ {
		case "iTXt":
			// keyword, NUL, compression flag, method, language, NUL,
			// translated keyword, NUL, text
			if k := bytes.IndexByte(body, 0); k >= 0 && string(body[:k]) == "XML:com.adobe.xmp" && len(body) > k+3 {
				compressed := body[k+1] == 1
				rest := body[k+3:]
				for range 2 {
					if j := bytes.IndexByte(rest, 0); j >= 0 {
						rest = rest[j+1:]
					}
				}
				if compressed {
					rest = inflate(rest)
				}
				if rest != nil && xmp == nil {
					xmp = rest
				}
			}
		case "iCCP":
			if k := bytes.IndexByte(body, 0); k >= 0 && len(body) > k+2 {
				icc = inflate(body[k+2:])
			}
		}
		i += 12 + n
	}
	return xmp, icc
}

func webpXMPICC(data []byte) (xmp, icc []byte) {
	i := 12
	for i+8 <= len(data) {
		typ := string(data[i : i+4])
		n := int(binary.LittleEndian.Uint32(data[i+4:]))
		if n < 0 || i+8+n > len(data) {
			break
		}
		body := data[i+8 : i+8+n]
		switch typ {
		case "XMP ":
			xmp = body
		case "ICCP":
			icc = body
		}
		i += 8 + n + n%2
	}
	return xmp, icc
}

// inflate decompresses zlib data up to MaxMetaBytes, or returns nil.
func inflate(b []byte) []byte {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, MaxMetaBytes+1))
	if err != nil || len(out) > MaxMetaBytes {
		return nil
	}
	return out
}

// xmpNamespaces gives the usual prefix of the XMP namespaces musafoz
// reports. Properties of other namespaces are left out.
var xmpNamespaces = map[string]string{
	"http://ns.adobe.com/exif/1.0/":               "exif",
	"http://cipa.jp/exif/1.0/":                    "exifEX",
	"http://ns.adobe.com/tiff/1.0/":               "tiff",
	"http://ns.adobe.com/xap/1.0/":                "xmp",
	"http://ns.adobe.com/photoshop/1.0/":          "photoshop",
	"http://purl.org/dc/elements/1.1/":            "dc",
	"http://ns.adobe.com/exif/1.0/aux/":           "aux",
	"http://ns.adobe.com/xap/1.0/mm/":             "xmpMM",
	"http://ns.apple.com/HDRGainMap/1.0/":         "HDRGainMap",
	"http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/": "Iptc4xmpCore",
}

// ParseXMP returns the simple properties of an XMP packet as
// "prefix:Name" to text, for the namespaces in xmpNamespaces: attributes of
// rdf:Description and child elements that hold only text. For a language
// alternative or a list, the first item is kept. A packet that is not
// well-formed XML gives the properties read before the error.
func ParseXMP(packet []byte) map[string]string {
	out := map[string]string{}
	if len(packet) == 0 || len(packet) > MaxMetaBytes {
		return out
	}
	const rdf = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	dec := xml.NewDecoder(bytes.NewReader(packet))
	dec.Strict = false
	var stack []string // property keys of open elements, "" for others
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == rdf && t.Name.Local == "Description" {
				for _, a := range t.Attr {
					if p, ok := xmpNamespaces[a.Name.Space]; ok {
						out[p+":"+a.Name.Local] = strings.TrimSpace(a.Value)
					}
				}
			}
			key := ""
			if p, ok := xmpNamespaces[t.Name.Space]; ok {
				key = p + ":" + t.Name.Local
			}
			stack = append(stack, key)
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			key := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			v := strings.TrimSpace(text.String())
			text.Reset()
			if v == "" {
				continue
			}
			if key != "" {
				if _, seen := out[key]; !seen {
					out[key] = v
				}
				continue
			}
			// rdf:li inside rdf:Alt, rdf:Seq, or rdf:Bag: the value belongs to
			// the nearest property above.
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] != "" {
					if _, seen := out[stack[i]]; !seen {
						out[stack[i]] = v
					}
					break
				}
			}
		}
	}
}

var xmpDateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(\.\d{1,9})?)?(Z|[+-]\d{2}:\d{2})?$`)

// xmpDate checks an XMP date with a time and returns it as musafoz writes
// dates: seconds always present, the fraction and the offset when given.
func xmpDate(s string) (string, bool) {
	m := xmpDateRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", false
	}
	sec := m[6]
	if sec == "" {
		sec = "00"
	}
	v, ok := isoDate(m[1] + ":" + m[2] + ":" + m[3] + " " + m[4] + ":" + m[5] + ":" + sec)
	if !ok {
		return "", false
	}
	v += m[7]
	switch {
	case m[8] == "Z":
		v += "Z"
	case m[8] != "" && validOffset(m[8]):
		v += m[8]
	}
	return v, true
}
