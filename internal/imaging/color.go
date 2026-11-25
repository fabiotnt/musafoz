package imaging

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"strings"
	"unicode/utf16"
)

// ColorSpace converts pixels of one RGB color space to sRGB. It is built
// from an ICC profile with RGB colorants and tone curves (matrix/TRC, as
// cameras, phones, and editors write for Display P3, Adobe RGB, and sRGB)
// or from a coded color description (HEIF nclx).
type ColorSpace struct {
	// Name describes the space, such as the profile description "Display
	// P3", or "nclx primaries 12, transfer 13".
	Name string
	// IsSRGB reports that the space is sRGB within rounding, so pixels are
	// left as they are.
	IsSRGB bool

	toLinear [3][256]float64
	matrix   [3][3]float64 // source linear RGB to sRGB linear RGB
}

// sRGB colorants adapted to D50, as in the sRGB ICC profiles (columns are
// the red, green, and blue XYZ values).
var srgbD50 = [3][3]float64{
	{0.4360747, 0.3850649, 0.1430804},
	{0.2225045, 0.7168786, 0.0606169},
	{0.0139322, 0.0971045, 0.7141733},
}

func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func linearToSRGB(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// ParseICC reads a matrix/TRC RGB profile. Profiles of other kinds (gray,
// CMYK, or lookup tables only) are an error, and the caller leaves pixels
// unconverted.
func ParseICC(p []byte) (*ColorSpace, error) {
	if len(p) < 132 || int(binary.BigEndian.Uint32(p)) > len(p) {
		return nil, errors.New("ICC profile is truncated")
	}
	if string(p[16:20]) != "RGB " {
		return nil, fmt.Errorf("ICC profile is for %q data, not RGB", strings.TrimSpace(string(p[16:20])))
	}
	n := int(binary.BigEndian.Uint32(p[128:]))
	if n > 1000 || 132+12*n > len(p) {
		return nil, errors.New("ICC profile has a broken tag table")
	}
	tags := map[string][]byte{}
	for i := range n {
		e := p[132+12*i:]
		off, size := int(binary.BigEndian.Uint32(e[4:])), int(binary.BigEndian.Uint32(e[8:]))
		if off < 0 || size < 0 || off+size > len(p) || off+size < off {
			continue
		}
		tags[string(e[:4])] = p[off : off+size]
	}
	cs := &ColorSpace{Name: iccDescription(tags["desc"])}
	var m [3][3]float64
	for c, sig := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		t := tags[sig]
		if len(t) < 20 || string(t[:4]) != "XYZ " {
			return nil, errors.New("ICC profile has no RGB colorants (only lookup tables are not supported)")
		}
		for r := range 3 {
			m[r][c] = s15(t[8+4*r:])
		}
	}
	for c, sig := range []string{"rTRC", "gTRC", "bTRC"} {
		curve, err := iccCurve(tags[sig])
		if err != nil {
			return nil, fmt.Errorf("ICC profile %s: %w", sig, err)
		}
		for v := range 256 {
			cs.toLinear[c][v] = curve(float64(v) / 255)
		}
	}
	inv, ok := invert(srgbD50)
	if !ok {
		return nil, errors.New("internal error: sRGB matrix")
	}
	cs.matrix = mul(inv, m)
	cs.IsSRGB = cs.closeToSRGB()
	if cs.Name == "" {
		cs.Name = "ICC profile"
	}
	return cs, nil
}

func s15(b []byte) float64 { return float64(int32(binary.BigEndian.Uint32(b))) / 65536 }

// iccCurve reads a curv or para tag.
func iccCurve(t []byte) (func(float64) float64, error) {
	if len(t) < 12 {
		return nil, errors.New("missing tone curve")
	}
	switch string(t[:4]) {
	case "curv":
		n := int(binary.BigEndian.Uint32(t[8:]))
		switch {
		case n == 0:
			return func(x float64) float64 { return x }, nil
		case n == 1 && len(t) >= 14:
			g := float64(binary.BigEndian.Uint16(t[12:])) / 256
			return func(x float64) float64 { return math.Pow(x, g) }, nil
		case n >= 2 && len(t) >= 12+2*n:
			table := make([]float64, n)
			for i := range n {
				table[i] = float64(binary.BigEndian.Uint16(t[12+2*i:])) / 65535
			}
			return func(x float64) float64 {
				pos := x * float64(n-1)
				i := int(pos)
				if i >= n-1 {
					return table[n-1]
				}
				f := pos - float64(i)
				return table[i]*(1-f) + table[i+1]*f
			}, nil
		}
		return nil, errors.New("truncated curv tone curve")
	case "para":
		kind := int(binary.BigEndian.Uint16(t[8:]))
		count := map[int]int{0: 1, 1: 3, 2: 4, 3: 5, 4: 7}[kind]
		if count == 0 || len(t) < 12+4*count {
			return nil, fmt.Errorf("unsupported or truncated para tone curve type %d", kind)
		}
		var a [7]float64
		for i := range count {
			a[i] = s15(t[12+4*i:])
		}
		g := a[0]
		pow := func(x float64) float64 {
			if x <= 0 {
				return 0
			}
			return math.Pow(x, g)
		}
		switch kind {
		case 0:
			return pow, nil
		case 1:
			return func(x float64) float64 {
				if x >= -a[2]/a[1] {
					return pow(a[1]*x + a[2])
				}
				return 0
			}, nil
		case 2:
			return func(x float64) float64 {
				if x >= -a[2]/a[1] {
					return pow(a[1]*x+a[2]) + a[3]
				}
				return a[3]
			}, nil
		case 3:
			return func(x float64) float64 {
				if x >= a[4] {
					return pow(a[1]*x + a[2])
				}
				return a[3] * x
			}, nil
		default:
			return func(x float64) float64 {
				if x >= a[4] {
					return pow(a[1]*x+a[2]) + a[5]
				}
				return a[3]*x + a[6]
			}, nil
		}
	}
	return nil, fmt.Errorf("unsupported tone curve type %q", string(t[:4]))
}

// iccDescription reads a desc (ICC v2) or mluc (ICC v4) tag.
func iccDescription(t []byte) string {
	if len(t) < 12 {
		return ""
	}
	switch string(t[:4]) {
	case "desc":
		n := int(binary.BigEndian.Uint32(t[8:]))
		if n > 0 && 12+n <= len(t) {
			return strings.TrimRight(string(t[12:12+n]), "\x00 ")
		}
	case "mluc":
		if len(t) < 28 {
			return ""
		}
		size, off := int(binary.BigEndian.Uint32(t[20:])), int(binary.BigEndian.Uint32(t[24:]))
		if off+size > len(t) || size%2 != 0 {
			return ""
		}
		u := make([]uint16, size/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(t[off+2*i:])
		}
		return strings.TrimRight(string(utf16.Decode(u)), "\x00 ")
	}
	return ""
}

// NCLXColorSpace builds the conversion of an nclx description. Only SDR
// transfers are supported: sRGB (13) and the BT.709 family (1, 6, 14, 15),
// which phones use for photos, decoded with the sRGB curve as displays do.
// Unspecified primaries (2) are taken as BT.709.
func NCLXColorSpace(n *NCLX) (*ColorSpace, error) {
	switch n.Transfer {
	case 1, 2, 6, 13, 14, 15:
	default:
		return nil, fmt.Errorf("nclx transfer %d is not an SDR curve; HDR images are not tone mapped", n.Transfer)
	}
	prim, ok := nclxPrimaries[n.Primaries]
	if n.Primaries == 2 {
		prim, ok = nclxPrimaries[1], true
	}
	if !ok {
		return nil, fmt.Errorf("nclx primaries %d are not supported", n.Primaries)
	}
	src, ok := primariesToXYZ(prim)
	dst, ok2 := primariesToXYZ(nclxPrimaries[1])
	inv, ok3 := invert(dst)
	if !ok || !ok2 || !ok3 {
		return nil, errors.New("nclx primaries are degenerate")
	}
	cs := &ColorSpace{Name: fmt.Sprintf("nclx primaries %d, transfer %d", n.Primaries, n.Transfer), matrix: mul(inv, src)}
	for c := range 3 {
		for v := range 256 {
			cs.toLinear[c][v] = srgbToLinear(float64(v) / 255)
		}
	}
	cs.IsSRGB = cs.closeToSRGB()
	return cs, nil
}

// nclxPrimaries are the red, green, blue, and white chromaticities (x, y)
// of ISO/IEC 23091-2 colour primaries: 1 BT.709 (sRGB), 9 BT.2020, 11 DCI
// P3, 12 Display P3 (P3 with a D65 white).
var nclxPrimaries = map[int][8]float64{
	1:  {0.640, 0.330, 0.300, 0.600, 0.150, 0.060, 0.3127, 0.3290},
	9:  {0.708, 0.292, 0.170, 0.797, 0.131, 0.046, 0.3127, 0.3290},
	11: {0.680, 0.320, 0.265, 0.690, 0.150, 0.060, 0.3140, 0.3510},
	12: {0.680, 0.320, 0.265, 0.690, 0.150, 0.060, 0.3127, 0.3290},
}

// primariesToXYZ returns the RGB to XYZ matrix of chromaticities, without
// white point adaptation (sources here share the D65 white, except DCI P3,
// whose white is kept as is).
func primariesToXYZ(p [8]float64) ([3][3]float64, bool) {
	xyz := func(x, y float64) [3]float64 { return [3]float64{x / y, 1, (1 - x - y) / y} }
	r, g, b, w := xyz(p[0], p[1]), xyz(p[2], p[3]), xyz(p[4], p[5]), xyz(p[6], p[7])
	m := [3][3]float64{{r[0], g[0], b[0]}, {r[1], g[1], b[1]}, {r[2], g[2], b[2]}}
	inv, ok := invert(m)
	if !ok {
		return m, false
	}
	var s [3]float64
	for i := range 3 {
		s[i] = inv[i][0]*w[0] + inv[i][1]*w[1] + inv[i][2]*w[2]
	}
	for i := range 3 {
		for j := range 3 {
			m[i][j] *= s[j]
		}
	}
	return m, true
}

func mul(a, b [3][3]float64) (c [3][3]float64) {
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				c[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return c
}

func invert(m [3][3]float64) ([3][3]float64, bool) {
	det := m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if math.Abs(det) < 1e-12 {
		return m, false
	}
	var r [3][3]float64
	r[0][0] = (m[1][1]*m[2][2] - m[1][2]*m[2][1]) / det
	r[0][1] = (m[0][2]*m[2][1] - m[0][1]*m[2][2]) / det
	r[0][2] = (m[0][1]*m[1][2] - m[0][2]*m[1][1]) / det
	r[1][0] = (m[1][2]*m[2][0] - m[1][0]*m[2][2]) / det
	r[1][1] = (m[0][0]*m[2][2] - m[0][2]*m[2][0]) / det
	r[1][2] = (m[0][2]*m[1][0] - m[0][0]*m[1][2]) / det
	r[2][0] = (m[1][0]*m[2][1] - m[1][1]*m[2][0]) / det
	r[2][1] = (m[0][1]*m[2][0] - m[0][0]*m[2][1]) / det
	r[2][2] = (m[0][0]*m[1][1] - m[0][1]*m[1][0]) / det
	return r, true
}

// closeToSRGB reports a matrix within 0.002 of identity and tone curves
// within half a level of sRGB.
func (c *ColorSpace) closeToSRGB() bool {
	for i := range 3 {
		for j := range 3 {
			want := 0.0
			if i == j {
				want = 1
			}
			if math.Abs(c.matrix[i][j]-want) > 0.002 {
				return false
			}
		}
		for v := range 256 {
			if math.Abs(linearToSRGB(c.toLinear[i][v])*255-float64(v)) > 0.5 {
				return false
			}
		}
	}
	return true
}

// encodeLUT maps linear light in 1/4095 steps to 8-bit sRGB.
var encodeLUT = func() (t [4096]uint8) {
	for i := range t {
		t[i] = uint8(math.Round(linearToSRGB(float64(i)/4095) * 255))
	}
	return t
}()

// ToSRGB returns img converted to sRGB. Colors outside sRGB are clipped
// per channel. Alpha is kept. When the space is already sRGB, img is
// returned as it is.
func (c *ColorSpace) ToSRGB(img image.Image) image.Image {
	if c == nil || c.IsSRGB {
		return img
	}
	b := img.Bounds()
	out, ok := img.(*image.NRGBA)
	if !ok {
		out = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	} else {
		cp := *out
		cp.Pix = append([]uint8(nil), out.Pix...)
		out = &cp
	}
	m := c.matrix
	enc := func(v float64) uint8 {
		switch {
		case v <= 0:
			return 0
		case v >= 1:
			return 255
		}
		return encodeLUT[int(v*4095+0.5)]
	}
	ob := out.Bounds()
	for y := ob.Min.Y; y < ob.Max.Y; y++ {
		row := out.Pix[out.PixOffset(ob.Min.X, y) : out.PixOffset(ob.Min.X, y)+4*ob.Dx()]
		for i := 0; i < len(row); i += 4 {
			r, g, bl := c.toLinear[0][row[i]], c.toLinear[1][row[i+1]], c.toLinear[2][row[i+2]]
			row[i] = enc(m[0][0]*r + m[0][1]*g + m[0][2]*bl)
			row[i+1] = enc(m[1][0]*r + m[1][1]*g + m[1][2]*bl)
			row[i+2] = enc(m[2][0]*r + m[2][1]*g + m[2][2]*bl)
		}
	}
	return out
}
