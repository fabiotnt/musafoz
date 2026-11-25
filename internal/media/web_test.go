package media

import (
	"strings"
	"testing"
)

func TestWebSize(t *testing.T) {
	o := DefaultWebVideo
	cases := []struct{ w, h, ww, wh int }{
		{3840, 2160, 1920, 1080},
		{2160, 3840, 1080, 1920},
		{1920, 1080, 1920, 1080},
		{1280, 720, 1280, 720},
		{1440, 1080, 1440, 1080},
		{2560, 1080, 1920, 810},
		{1080, 1080, 1080, 1080},
		{4096, 4096, 1080, 1080},
		{241, 321, 240, 320},
	}
	for _, c := range cases {
		if w, h := o.WebSize(c.w, c.h); w != c.ww || h != c.wh {
			t.Errorf("%dx%d gave %dx%d, want %dx%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}

func TestWebFilter(t *testing.T) {
	sdr := WebFilter(VideoStream{ColorTransfer: "bt709"}, 1920, 1080)
	if strings.Contains(sdr, "tonemap") || !strings.Contains(sdr, "scale=1920:1080") || !strings.HasSuffix(sdr, "color_trc=bt709:colorspace=bt709:range=tv") {
		t.Errorf("SDR filter %s", sdr)
	}
	for _, trc := range []string{"arib-std-b67", "smpte2084"} {
		v := VideoStream{ColorTransfer: trc, ColorPrimaries: "bt2020", ColorSpace: "bt2020nc"}
		if !v.HDR() {
			t.Errorf("%s is not HDR", trc)
		}
		f := WebFilter(v, 1080, 1920)
		if !strings.Contains(f, "tin="+trc+":pin=bt2020:min=bt2020nc") || !strings.Contains(f, "tonemap=tonemap=hable") || !strings.Contains(f, "scale=1080:1920") {
			t.Errorf("HDR filter %s", f)
		}
	}
	// Missing primaries and matrix default to BT.2020.
	if f := WebFilter(VideoStream{ColorTransfer: "arib-std-b67"}, 2, 2); !strings.Contains(f, "pin=bt2020:min=bt2020nc") {
		t.Errorf("HDR defaults %s", f)
	}
}

func TestVideoDate(t *testing.T) {
	cases := []struct {
		tags        map[string]string
		value, from string
	}{
		{map[string]string{"com.apple.quicktime.creationdate": "2025-03-26T15:07:12-0300", "creation_time": "2025-03-26T18:07:12.000000Z"}, "2025-03-26T15:07:12-03:00", "QuickTime creationdate"},
		{map[string]string{"com.apple.quicktime.creationdate": "2025-03-26T15:07:12+05:30"}, "2025-03-26T15:07:12+05:30", "QuickTime creationdate"},
		{map[string]string{"creation_time": "2025-03-26T18:07:12.000000Z"}, "2025-03-26T18:07:12Z", "creation_time"},
		{map[string]string{"creation_time": "1970-01-01T00:00:00.000000Z"}, "", ""},
		{map[string]string{"creation_time": "1904-01-01T00:00:00.000000Z"}, "", ""},
		{map[string]string{"com.apple.quicktime.creationdate": "garbage"}, "", ""},
		{nil, "", ""},
	}
	for _, c := range cases {
		v, from, ok := VideoDate(c.tags)
		if v != c.value || from != c.from || ok != (c.value != "") {
			t.Errorf("%v gave %q %q %v", c.tags, v, from, ok)
		}
	}
}
