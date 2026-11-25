package imaging

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// Info describes a still image for musafoz inspect.
type Info struct {
	// Path is the local path, or the URL without its query for downloads.
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
	// Width and Height are the stored size.
	Width  int `json:"width"`
	Height int `json:"height"`
	// DisplayWidth and DisplayHeight apply the EXIF orientation.
	DisplayWidth  int `json:"display_width"`
	DisplayHeight int `json:"display_height"`
	// Orientation is the EXIF orientation, 1 to 8, or absent.
	Orientation      *int   `json:"orientation,omitempty"`
	OrientationLabel string `json:"orientation_label"`
	// AlphaChannel reports whether the file declares transparency, and
	// TransparentPixels whether any decoded pixel is not fully opaque. HEIF
	// files are inspected from their container without decoding pixels, so
	// TransparentPixels is absent for them.
	AlphaChannel      bool  `json:"alpha_channel"`
	TransparentPixels *bool `json:"transparent_pixels,omitempty"`
	// HEIFTransforms lists the rotation (irot) and mirroring (imir) of a
	// HEIF primary image. They define the displayed orientation; the EXIF
	// orientation of a HEIF file is reported but not applied.
	HEIFTransforms []HEIFTransform `json:"heif_transforms,omitempty"`
	// ColorProfile names the embedded color profile, or the HEIF nclx
	// description, when the file has one.
	ColorProfile string `json:"color_profile,omitempty"`
	// XMP holds the simple properties of the XMP packet (see ParseXMP).
	XMP map[string]string `json:"xmp,omitempty"`
	// EXIF holds every readable tag, or null when the file has none.
	EXIF     *EXIF    `json:"exif"`
	Warnings []string `json:"warnings,omitempty"`
}

// Inspect reads a local image file. name is the path reported in the
// result.
func Inspect(path, name string) (*Info, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if Sniff(data) == FormatHEIF {
		h, err := ParseHEIF(data)
		if err != nil {
			return nil, err
		}
		info := HEIFInfo(h, nil, name, fi.Size())
		if h.EXIF != nil {
			if e, err := ParseEXIF(h.EXIF); err == nil {
				info = HEIFInfo(h, e, name, fi.Size())
			} else {
				info.Warnings = append(info.Warnings, "EXIF block could not be read: "+err.Error())
			}
		}
		return info, nil
	}
	img, err := Decode(data)
	if err != nil {
		return nil, err
	}
	return ImageInfo(img, name, fi.Size()), nil
}

// ImageInfo describes a decoded image. name is the path reported.
func ImageInfo(img *Image, name string, size int64) *Info {
	dw, dh := img.DisplaySize()
	transparent := img.HasTransparentPixels()
	info := &Info{
		Path: name, Kind: "image", Format: img.Format, SizeBytes: size,
		Width: img.Width, Height: img.Height, DisplayWidth: dw, DisplayHeight: dh,
		OrientationLabel:  OrientationLabel(img.Orientation),
		AlphaChannel:      img.AlphaChannel,
		TransparentPixels: &transparent,
		ColorProfile:      profileName(img.ICC, nil),
		XMP:               nonEmpty(img.XMP),
		EXIF:              img.EXIF,
	}
	if img.Orientation != 0 {
		o := img.Orientation
		info.Orientation = &o
	}
	if img.EXIFError != "" {
		info.Warnings = append(info.Warnings, "EXIF block could not be read: "+img.EXIFError)
	}
	return info
}

// HEIFInfo describes a HEIF file from its container, with e its parsed
// EXIF block or nil. The pixels are not decoded, so TransparentPixels is
// left out.
func HEIFInfo(h *HEIF, e *EXIF, name string, size int64) *Info {
	dw, dh := h.DisplaySize()
	info := &Info{
		Path: name, Kind: "image", Format: FormatHEIF, SizeBytes: size,
		Width: h.Width, Height: h.Height, DisplayWidth: dw, DisplayHeight: dh,
		AlphaChannel: h.Alpha, HEIFTransforms: h.Transforms,
		OrientationLabel: OrientationLabel(0),
		ColorProfile:     profileName(h.ICC, h.NCLX),
		XMP:              nonEmpty(ParseXMP(h.XMP)),
		EXIF:             e,
	}
	if o := e.Orientation(); e != nil && o != 0 {
		info.Orientation = &o
		info.OrientationLabel = OrientationLabel(o)
	}
	return info
}

func nonEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

// profileName names an ICC profile or an nclx description.
func profileName(icc []byte, n *NCLX) string {
	switch {
	case icc != nil:
		if cs, err := ParseICC(icc); err == nil {
			return cs.Name
		}
		return "unsupported ICC profile"
	case n != nil:
		return fmt.Sprintf("nclx primaries %d, transfer %d", n.Primaries, n.Transfer)
	}
	return ""
}

// HEIFLabel describes HEIF transforms, such as "rotated 90 degrees
// clockwise". It is "none" when there are none.
func HEIFLabel(ts []HEIFTransform) string {
	if len(ts) == 0 {
		return "none"
	}
	var parts []string
	for _, t := range ts {
		switch t.Mirror {
		case "left-right":
			parts = append(parts, "mirrored left to right")
		case "top-bottom":
			parts = append(parts, "mirrored top to bottom")
		default:
			parts = append(parts, fmt.Sprintf("rotated %d degrees clockwise", (360-t.Rotation)%360))
		}
	}
	return strings.Join(parts, ", then ")
}

// Summary returns labeled lines with the most useful EXIF fields, for text
// output. Fields that are absent are left out.
func (e *EXIF) Summary() [][2]string {
	if e == nil {
		return nil
	}
	var rows [][2]string
	add := func(label, value string) {
		if value = strings.TrimSpace(value); value != "" {
			rows = append(rows, [2]string{label, value})
		}
	}
	str := func(group string, id uint16) string {
		t, ok := e.Lookup(group, id)
		if !ok {
			return ""
		}
		return t.String()
	}
	camera := strings.TrimSpace(str("ifd0", 0x010F) + " " + str("ifd0", 0x0110))
	add("Camera", camera)
	lens := str("exif", 0xA434)
	if mk := str("exif", 0xA433); mk != "" && !strings.HasPrefix(lens, mk) {
		lens = strings.TrimSpace(mk + " " + lens)
	}
	add("Lens", lens)

	var exp []string
	if t, ok := e.Lookup("exif", 0x829A); ok {
		if r := t.Rationals(); len(r) > 0 && r[0].Den != 0 && r[0].Num != 0 {
			v := r[0].Float()
			if v < 1 {
				exp = append(exp, fmt.Sprintf("1/%s s", trimNum(1/v)))
			} else {
				exp = append(exp, trimNum(v)+" s")
			}
		}
	}
	if t, ok := e.Lookup("exif", 0x829D); ok {
		if r := t.Rationals(); len(r) > 0 && r[0].Den != 0 {
			exp = append(exp, "f/"+trimNum(r[0].Float()))
		}
	}
	if t, ok := e.Lookup("exif", 0x8827); ok {
		if v, ok := t.Uint(); ok {
			exp = append(exp, fmt.Sprintf("ISO %d", v))
		}
	}
	if t, ok := e.Lookup("exif", 0x920A); ok {
		if r := t.Rationals(); len(r) > 0 && r[0].Den != 0 {
			fl := trimNum(r[0].Float()) + " mm"
			if t35, ok := e.Lookup("exif", 0xA405); ok {
				if v, ok := t35.Uint(); ok && v > 0 {
					fl += fmt.Sprintf(" (%d mm equivalent)", v)
				}
			}
			exp = append(exp, fl)
		}
	}
	add("Exposure", strings.Join(exp, ", "))

	taken := str("exif", 0x9003)
	if taken == "" {
		taken = str("ifd0", 0x0132)
	}
	if off := str("exif", 0x9011); taken != "" && off != "" {
		taken += " " + off
	}
	add("Taken", taken)
	if lat, lon, ok := e.GPSPosition(); ok {
		pos := fmt.Sprintf("%.6f, %.6f", lat, lon)
		if t, ok := e.Lookup("gps", 0x0006); ok {
			if r := t.Rationals(); len(r) > 0 && r[0].Den != 0 {
				alt := r[0].Float()
				if ref, ok := e.Lookup("gps", 0x0005); ok {
					if v, _ := ref.Uint(); v == 1 {
						alt = -alt
					}
				}
				pos += fmt.Sprintf(", altitude %s m", trimNum(alt))
			}
		}
		add("GPS", pos)
	}
	add("Software", str("ifd0", 0x0131))
	return rows
}

// GPSPosition returns the latitude and longitude in decimal degrees.
func (e *EXIF) GPSPosition() (lat, lon float64, ok bool) {
	coord := func(valueID, refID uint16, neg string) (float64, bool) {
		t, ok := e.Lookup("gps", valueID)
		if !ok {
			return 0, false
		}
		r := t.Rationals()
		if len(r) != 3 || r[0].Den == 0 || r[1].Den == 0 {
			return 0, false
		}
		v := r[0].Float() + r[1].Float()/60
		if r[2].Den != 0 {
			v += r[2].Float() / 3600
		}
		if ref, ok := e.Lookup("gps", refID); ok && strings.EqualFold(ref.String(), neg) {
			v = -v
		}
		return v, !math.IsNaN(v)
	}
	lat, ok1 := coord(0x0002, 0x0001, "S")
	lon, ok2 := coord(0x0004, 0x0003, "W")
	return lat, lon, ok1 && ok2
}

func trimNum(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
