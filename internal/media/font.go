package media

import (
	"fmt"
	"os"
	"strings"
)

// DefaultFonts lists the overlay fonts tried in order when no font is
// given. Andale Mono matches the reference workflow; DejaVu Sans Mono is
// its Linux fallback.
var DefaultFonts = []string{
	"/System/Library/Fonts/Supplemental/Andale Mono.ttf",
	"/Library/Fonts/Andale Mono.ttf",
	"/usr/share/fonts/truetype/andalemono/andale_mono.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
}

// FindFont returns explicit when set, after checking it, or the first
// default font that exists.
func FindFont(explicit string) (string, error) {
	if explicit != "" {
		fi, err := os.Stat(explicit)
		if err != nil {
			return "", fmt.Errorf("font: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("font: %s is not a regular file", explicit)
		}
		return explicit, nil
	}
	for _, p := range DefaultFonts {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no overlay font found; tried %s; pass --font", strings.Join(DefaultFonts, ", "))
}
