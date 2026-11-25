package imaging

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// EXIF tags of the dates and their companions.
const (
	tagDateTime           = 0x0132 // IFD0: when the file was last changed
	tagDateTimeOriginal   = 0x9003 // Exif: when the picture was taken
	tagOffsetTime         = 0x9010 // Exif: UTC offset of DateTime
	tagOffsetTimeOriginal = 0x9011 // Exif: UTC offset of DateTimeOriginal
	tagSubSecTime         = 0x9290 // Exif: fraction of DateTime
	tagSubSecTimeOriginal = 0x9291 // Exif: fraction of DateTimeOriginal
)

// Date is the date of an image read from its EXIF block.
type Date struct {
	// Value is an ISO 8601 local date and time, such as
	// "2025-01-19T10:15:32", with the fraction of a second when the file
	// gives one and the UTC offset when the file gives one, such as
	// "2025-01-19T10:15:32.120-03:00". Without an offset the time zone is
	// unknown; it is not UTC.
	Value string
	// Source names where the date comes from: "DateTimeOriginal" (EXIF);
	// then an XMP property, such as "XMP photoshop:DateCreated", which is
	// where phones keep the date of screenshots; then "DateTime" (EXIF),
	// when the file was last changed, often by an editor, as a fallback.
	Source string
}

var (
	exifDateRe   = regexp.MustCompile(`^(\d{4}):(\d{2}):(\d{2}) (\d{2}):(\d{2}):(\d{2})$`)
	exifOffsetRe = regexp.MustCompile(`^[+-]\d{2}:\d{2}$`)
	subSecRe     = regexp.MustCompile(`^\d{1,9}$`)
)

// xmpDateKeys are the XMP properties that hold the time a picture was
// taken or created, in order of preference.
var xmpDateKeys = []string{"exif:DateTimeOriginal", "photoshop:DateCreated", "xmp:CreateDate"}

// SelectDate returns the date of the picture: the EXIF DateTimeOriginal,
// with OffsetTimeOriginal and SubSecTimeOriginal; else the first valid XMP
// date of xmpDateKeys, as written, with its offset when it has one; else
// the EXIF DateTime, with OffsetTime and SubSecTime. Dates that are blank,
// zero, or malformed are skipped, as are malformed offsets and fractions.
// It returns nil when no date is usable. e and xmp may be nil.
func SelectDate(e *EXIF, xmp map[string]string) *Date {
	pick := []struct {
		group                string
		date, offset, subSec uint16
		name                 string
	}{
		{"exif", tagDateTimeOriginal, tagOffsetTimeOriginal, tagSubSecTimeOriginal, "DateTimeOriginal"},
		{"ifd0", tagDateTime, tagOffsetTime, tagSubSecTime, "DateTime"},
	}
	text := func(group string, id uint16) string {
		t, ok := e.Lookup(group, id)
		if !ok {
			return ""
		}
		s, _ := t.Value.(string)
		return strings.TrimSpace(strings.TrimRight(s, "\x00"))
	}
	for i, p := range pick {
		if i == 1 {
			for _, k := range xmpDateKeys {
				if v, ok := xmpDate(xmp[k]); ok {
					return &Date{Value: v, Source: "XMP " + k}
				}
			}
		}
		v, ok := isoDate(text(p.group, p.date))
		if !ok {
			continue
		}
		if sub := text("exif", p.subSec); subSecRe.MatchString(sub) {
			v += "." + sub
		}
		if off := text("exif", p.offset); exifOffsetRe.MatchString(off) && validOffset(off) {
			v += off
		}
		return &Date{Value: v, Source: p.name}
	}
	return nil
}

// isoDate converts "YYYY:MM:DD HH:MM:SS" to "YYYY-MM-DDTHH:MM:SS" after
// checking the ranges. A zero date such as "0000:00:00 00:00:00", which
// cameras write when the clock was not set, is not a date.
func isoDate(s string) (string, bool) {
	m := exifDateRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	// strconv, not fmt.Sscan, which reads "08" and "09" as bad octal.
	var n [6]int
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	y, mo, d, h, mi, se := n[0], n[1], n[2], n[3], n[4], n[5]
	if y < 1 || mo < 1 || mo > 12 || d < 1 || d > daysIn(y, mo) || h > 23 || mi > 59 || se > 60 {
		return "", false
	}
	return fmt.Sprintf("%s-%s-%sT%s:%s:%s", m[1], m[2], m[3], m[4], m[5], m[6]), true
}

func daysIn(y, m int) int {
	switch m {
	case 2:
		if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// validOffset checks an offset such as "-03:00": hours up to 14 and
// minutes up to 59.
func validOffset(s string) bool {
	h, err1 := strconv.Atoi(s[1:3])
	m, err2 := strconv.Atoi(s[4:6])
	return err1 == nil && err2 == nil && h <= 14 && m <= 59
}
