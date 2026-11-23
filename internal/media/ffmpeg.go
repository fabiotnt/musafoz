package media

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// progressInt calls report with the integer value of key, ignoring other
// keys and values ffmpeg reports as N/A.
func progressInt(key string, report func(int64)) func(string, string) {
	if report == nil {
		return nil
	}
	return func(k, v string) {
		if k != key {
			return
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			report(n)
		}
	}
}

// runFFmpeg runs ffmpeg quietly. On failure the error carries the last
// meaningful line ffmpeg printed.
func runFFmpeg(ctx context.Context, ffmpeg string, args ...string) error {
	return runFFmpegIn(ctx, "", ffmpeg, args...)
}

// runFFmpegIn runs ffmpeg with dir as its current directory, so that
// filters can name files in dir without their full path.
func runFFmpegIn(ctx context.Context, dir, ffmpeg string, args ...string) error {
	return runFFmpegProgress(ctx, dir, ffmpeg, nil, args...)
}

// runFFmpegProgress runs ffmpeg like runFFmpegIn. When onProgress is set,
// ffmpeg writes its machine-readable progress report ("-progress pipe:1")
// to standard output, and onProgress receives each key and value, such as
// frame=42 or out_time_us=1500000.
func runFFmpegProgress(ctx context.Context, dir, ffmpeg string, onProgress func(key, value string), args ...string) error {
	full := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
	if onProgress != nil {
		full = append(full, "-progress", "pipe:1", "-nostats")
	}
	full = append(full, args...)
	var errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, full...)
	cmd.Dir = dir
	cmd.Stderr = &errOut
	var stdout io.ReadCloser
	if onProgress != nil {
		var err error
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return err
		}
	}
	err := cmd.Start()
	if err == nil {
		if stdout != nil {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
					onProgress(k, v)
				}
			}
			// Drain anything left so ffmpeg never blocks on a full pipe.
			io.Copy(io.Discard, stdout)
		}
		err = cmd.Wait()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if msg := lastLine(errOut.String()); msg != "" {
			return fmt.Errorf("ffmpeg failed: %s", msg)
		}
		return fmt.Errorf("ffmpeg failed: %w", err)
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// requiredFilters and requiredEncoders are the FFmpeg features the preview
// workflow uses.
var (
	requiredFilters  = []string{"drawtext", "tile", "scale", "pad", "setsar"}
	requiredEncoders = []string{"libx264", "mjpeg"}
)

// CheckFFmpegFeatures confirms that ffmpeg provides the encoders and
// filters the preview workflow needs.
func CheckFFmpegFeatures(ctx context.Context, ffmpeg string) error {
	encoders, err := listFFmpeg(ctx, ffmpeg, "-encoders")
	if err != nil {
		return err
	}
	filters, err := listFFmpeg(ctx, ffmpeg, "-filters")
	if err != nil {
		return err
	}
	var missing []string
	for _, e := range requiredEncoders {
		if !encoders[e] {
			missing = append(missing, "encoder "+e)
		}
	}
	for _, f := range requiredFilters {
		if !filters[f] {
			missing = append(missing, "filter "+f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s lacks %s", ffmpeg, strings.Join(missing, ", "))
	}
	return nil
}

// listFFmpeg returns the names in the second column of "ffmpeg -encoders"
// or "ffmpeg -filters".
func listFFmpeg(ctx context.Context, ffmpeg, flag string) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", flag).Output()
	if err != nil {
		return nil, fmt.Errorf("run %s %s: %w", ffmpeg, flag, err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			names[fields[1]] = true
		}
	}
	return names, nil
}

// commandOutput runs a command and returns its standard output.
func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// ffmpegPath prefixes an absolute path with the file protocol so that
// names containing a colon are never read as another protocol.
func ffmpegPath(p string) string {
	return "file:" + p
}
