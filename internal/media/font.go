package media

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// embeddedFont is DejaVu Sans Mono 2.37, the overlay font unless --font is
// given. EmbeddedFontLicense is its license, as shipped with the official
// release; it travels inside the binary with the font.
var (
	//go:embed fonts/DejaVuSansMono.ttf
	embeddedFont []byte
	//go:embed fonts/LICENSE
	EmbeddedFontLicense string
)

// EmbeddedFontName describes the embedded font in logs.
const EmbeddedFontName = "DejaVu Sans Mono 2.37 (embedded)"

// Font is the overlay font: a file on disk, or the embedded font when Path
// is empty.
type Font struct {
	Path string
}

// String names the font for logs.
func (f Font) String() string {
	if f.Path == "" {
		return EmbeddedFontName
	}
	return f.Path
}

// WriteCopy writes the font to dir under a plain name that FFmpeg filters
// can take without escaping, and returns that path. A copy is used instead
// of a symbolic link, which would need extra privileges on Windows.
func (f Font) WriteCopy(dir string) (string, error) {
	dst := filepath.Join(dir, "font"+plainExt(f.Path))
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if f.Path == "" {
		_, err = out.Write(embeddedFont)
	} else {
		var in *os.File
		in, err = os.Open(f.Path)
		if err == nil {
			_, err = io.Copy(out, in)
			in.Close()
		}
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("copy font %s: %w", f, err)
	}
	return dst, nil
}

// plainExt returns the lower-case extension of a font path when it has only
// letters and digits, and .ttf for the embedded font.
func plainExt(path string) string {
	if path == "" {
		return ".ttf"
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, r := range ext[min(1, len(ext)):] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

// fontMagic lists the first bytes of the font files FFmpeg's drawtext
// filter reads through FreeType: TrueType, OpenType, and collections.
var fontMagic = [][]byte{{0, 1, 0, 0}, []byte("OTTO"), []byte("true"), []byte("ttcf")}

// FindFont returns the overlay font. Without an explicit path it is always
// the embedded DejaVu Sans Mono, so the outputs look the same on every
// platform. An explicit path must be a readable TrueType or OpenType file.
func FindFont(explicit string) (Font, error) {
	if explicit == "" {
		return Font{}, nil
	}
	fi, err := os.Stat(explicit)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Font{}, fmt.Errorf("font file %s does not exist", explicit)
		}
		return Font{}, fmt.Errorf("font file %s: %w", explicit, err)
	}
	if !fi.Mode().IsRegular() {
		return Font{}, fmt.Errorf("font file %s is not a regular file", explicit)
	}
	f, err := os.Open(explicit)
	if err != nil {
		return Font{}, fmt.Errorf("font file %s cannot be read: %w", explicit, err)
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err == nil {
		for _, m := range fontMagic {
			if bytes.Equal(head, m) {
				return Font{Path: explicit}, nil
			}
		}
	}
	return Font{}, fmt.Errorf("font file %s is not a TrueType or OpenType font (.ttf, .otf, or .ttc)", explicit)
}
