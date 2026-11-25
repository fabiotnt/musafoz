// Package imaging reads, orients, resizes, and writes still images, and
// reads their EXIF metadata. It uses only Go code and does not run FFmpeg.
package imaging

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Image formats recognized by their leading bytes.
const (
	FormatJPEG = "jpeg"
	FormatPNG  = "png"
	FormatGIF  = "gif"
	FormatWebP = "webp"
	FormatBMP  = "bmp"
	FormatTIFF = "tiff"
	FormatHEIF = "heif"
	FormatAVIF = "avif"
)

// Sniff returns the image format of the leading bytes of a file, or an
// empty string when they are not a known image format. HEIF and AVIF are
// recognized but cannot be decoded (see Decodable).
func Sniff(head []byte) string {
	switch {
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return FormatJPEG
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return FormatPNG
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return FormatGIF
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return FormatWebP
	case len(head) >= 14 && string(head[0:2]) == "BM" && binary.LittleEndian.Uint32(head[2:6]) >= 14:
		return FormatBMP
	case bytes.HasPrefix(head, []byte("II*\x00")), bytes.HasPrefix(head, []byte("MM\x00*")):
		return FormatTIFF
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		switch string(head[8:12]) {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return FormatHEIF
		case "avif", "avis":
			return FormatAVIF
		}
	}
	return ""
}

// Decodable reports whether musafoz can decode the format in Go. HEIF is
// decoded with FFmpeg instead; see stills.
func Decodable(format string) bool {
	switch format {
	case FormatJPEG, FormatPNG, FormatGIF, FormatWebP, FormatBMP, FormatTIFF:
		return true
	}
	return false
}

// SniffFile reads the start of a file and returns its image format, or an
// empty string when it is not a known image format.
func SniffFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 32)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	return Sniff(head[:n]), nil
}

// UnsupportedError reports a recognized image format that cannot be
// decoded.
type UnsupportedError struct {
	Format string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s images are not supported yet; convert the file to JPEG or PNG first", formatLabel(e.Format))
}

func formatLabel(f string) string {
	switch f {
	case FormatJPEG:
		return "JPEG"
	case FormatPNG:
		return "PNG"
	case FormatGIF:
		return "GIF"
	case FormatWebP:
		return "WebP"
	case FormatBMP:
		return "BMP"
	case FormatTIFF:
		return "TIFF"
	case FormatHEIF:
		return "HEIF/HEIC"
	case FormatAVIF:
		return "AVIF"
	}
	return f
}

// FormatLabel returns the display name of a format, such as "JPEG".
func FormatLabel(f string) string { return formatLabel(f) }

// findEXIF returns the TIFF-structured EXIF block of an image file, or nil
// when the format carries none. It also reports whether the file declares
// an alpha channel. Only container structure is read here.
func findEXIF(format string, data []byte) (exif []byte, alpha bool) {
	switch format {
	case FormatJPEG:
		return jpegEXIF(data), false
	case FormatPNG:
		return pngEXIF(data)
	case FormatWebP:
		return webpEXIF(data)
	case FormatTIFF:
		return data, false
	}
	return nil, false
}

func jpegEXIF(data []byte) []byte {
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return nil
		}
		marker := data[i+1]
		switch {
		case marker == 0xFF:
			i++ // fill byte
			continue
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			i += 2
			continue
		case marker == 0xDA || marker == 0xD9:
			return nil // start of scan or end of image: no more metadata
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return nil
		}
		seg := data[i+4 : i+2+n]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return seg[6:]
		}
		i += 2 + n
	}
	return nil
}

func pngEXIF(data []byte) (exif []byte, alpha bool) {
	i := 8
	for i+12 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[i:]))
		typ := string(data[i+4 : i+8])
		if n < 0 || i+12+n > len(data) {
			break
		}
		body := data[i+8 : i+8+n]
		switch typ {
		case "IHDR":
			if len(body) >= 10 && (body[9] == 4 || body[9] == 6) {
				alpha = true
			}
		case "tRNS":
			alpha = true
		case "eXIf":
			exif = body
		case "IDAT", "IEND":
			if typ == "IEND" {
				return exif, alpha
			}
		}
		i += 12 + n
	}
	return exif, alpha
}

func webpEXIF(data []byte) (exif []byte, alpha bool) {
	i := 12
	for i+8 <= len(data) {
		typ := string(data[i : i+4])
		n := int(binary.LittleEndian.Uint32(data[i+4:]))
		if n < 0 || i+8+n > len(data) {
			break
		}
		body := data[i+8 : i+8+n]
		switch typ {
		case "VP8X":
			if len(body) > 0 && body[0]&0x10 != 0 {
				alpha = true
			}
		case "ALPH":
			alpha = true
		case "VP8L":
			// The lossless header has an alpha hint bit.
			if len(body) >= 5 && body[4]&0x10 != 0 {
				alpha = true
			}
		case "EXIF":
			exif = bytes.TrimPrefix(body, []byte("Exif\x00\x00"))
		}
		i += 8 + n + n%2
	}
	return exif, alpha
}
