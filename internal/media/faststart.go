package media

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FaststartMuxer returns the FFmpeg muxer used to remux a file with the
// given name, or an error if faststart does not apply to its container.
func FaststartMuxer(name string) (string, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".m4v":
		return "mp4", nil
	case ".mov":
		return "mov", nil
	}
	return "", fmt.Errorf("faststart applies only to .mp4, .m4v, and .mov files")
}

// IsFastStart reports whether the top-level moov box comes before the
// first mdat box.
func IsFastStart(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var offset int64
	hdr := make([]byte, 16)
	for {
		if _, err := f.ReadAt(hdr[:8], offset); err != nil {
			if errors.Is(err, io.EOF) {
				return false, errors.New("no moov or mdat box found")
			}
			return false, err
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		switch typ {
		case "moov":
			return true, nil
		case "mdat":
			return false, nil
		}
		switch size {
		case 0:
			return false, errors.New("no moov or mdat box found")
		case 1:
			if _, err := f.ReadAt(hdr[8:16], offset+8); err != nil {
				return false, fmt.Errorf("read box size: %w", err)
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
		}
		if size < 8 {
			return false, fmt.Errorf("invalid %q box size %d at offset %d", typ, size, offset)
		}
		offset += size
	}
}

// Remux copies every stream of src into dst with the moov box moved to the
// front. Nothing is re-encoded. A positive videoTimescale keeps the
// original video track timescale, so packet timestamps stay identical;
// otherwise the muxer picks its own. dst must not exist. progress, when
// set, receives the media time copied so far, in microseconds.
func Remux(ctx context.Context, ffmpeg, src, dst string, videoTimescale int, progress func(us int64)) error {
	muxer, err := FaststartMuxer(src)
	if err != nil {
		return err
	}
	args := []string{"-i", ffmpegPath(src), "-map", "0", "-c", "copy", "-movflags", "+faststart"}
	if videoTimescale > 0 {
		args = append(args, "-video_track_timescale", strconv.Itoa(videoTimescale))
	}
	args = append(args, "-f", muxer, ffmpegPath(dst))
	return runFFmpegProgress(ctx, "", ffmpeg, progressInt("out_time_us", progress), args...)
}
