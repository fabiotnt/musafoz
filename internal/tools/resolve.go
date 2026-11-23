package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Managed tool names.
const (
	ToolFFprobe = "ffprobe"
	ToolFFmpeg  = "ffmpeg"
)

const (
	// EnvFFprobe and EnvFFmpeg name the environment variables that point to
	// explicit executables. The --ffprobe and --ffmpeg flags take precedence.
	EnvFFprobe = "MUSAFOZ_FFPROBE"
	EnvFFmpeg  = "MUSAFOZ_FFMPEG"
	// EnvCacheDir names the environment variable that overrides the base
	// directory of the managed tool cache.
	EnvCacheDir = "MUSAFOZ_CACHE_DIR"

	// MinExplicitMajor is the oldest FFmpeg or FFprobe major release
	// accepted from an explicit path.
	MinExplicitMajor = 6
)

// EnvVar returns the environment variable that overrides a tool's path.
func EnvVar(tool string) string {
	return "MUSAFOZ_" + strings.ToUpper(tool)
}

// Source tells where a resolved tool came from.
type Source string

const (
	SourceFlag    Source = "flag"
	SourceEnv     Source = "env"
	SourceManaged Source = "managed"
)

// ResolveOptions configures tool resolution.
type ResolveOptions struct {
	// Tool is ToolFFprobe or ToolFFmpeg. ResolveFFprobe sets it.
	Tool string
	// FlagPath is the value of the --ffprobe or --ffmpeg flag, if any.
	FlagPath string
	// EnvPath is the value of MUSAFOZ_FFPROBE or MUSAFOZ_FFMPEG, if any.
	EnvPath string
	// Manager installs and verifies the managed build. It is used only when
	// no explicit path is given.
	Manager *Manager
	// Warnf receives non-fatal warnings. It may be nil.
	Warnf func(format string, args ...any)
}

// Resolved is an executable ready to run.
type Resolved struct {
	Path        string
	Source      Source
	VersionLine string
}

// ResolveFFprobe resolves ffprobe. See Resolve.
func ResolveFFprobe(ctx context.Context, opts ResolveOptions) (Resolved, error) {
	opts.Tool = ToolFFprobe
	return Resolve(ctx, opts)
}

// Resolve finds the executable to use for opts.Tool. The order is the flag,
// then the environment variable, then the managed cache, which is populated
// on first use. The PATH is never searched.
func Resolve(ctx context.Context, opts ResolveOptions) (Resolved, error) {
	if opts.Tool == "" {
		return Resolved{}, errors.New("resolve tool: no tool name given")
	}
	switch {
	case opts.FlagPath != "":
		return resolveExplicit(ctx, opts.Tool, opts.FlagPath, SourceFlag, opts.Warnf)
	case opts.EnvPath != "":
		return resolveExplicit(ctx, opts.Tool, opts.EnvPath, SourceEnv, opts.Warnf)
	}
	if opts.Manager == nil {
		return Resolved{}, fmt.Errorf("resolve %s: no manager configured", opts.Tool)
	}
	if opts.Manager.Artifact.Tool != opts.Tool {
		return Resolved{}, fmt.Errorf("resolve %s: manager is configured for %s", opts.Tool, opts.Manager.Artifact.Tool)
	}
	path, err := opts.Manager.Ensure(ctx)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Path: path, Source: SourceManaged, VersionLine: opts.Manager.Artifact.VersionPrefix}, nil
}

func resolveExplicit(ctx context.Context, tool, path string, src Source, warnf func(string, ...any)) (Resolved, error) {
	origin := "--" + tool
	if src == SourceEnv {
		origin = EnvVar(tool)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Resolved{}, fmt.Errorf("%s from %s: %w", tool, origin, err)
	}
	if !fi.Mode().IsRegular() {
		return Resolved{}, fmt.Errorf("%s from %s: %s is not a regular file", tool, origin, path)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return Resolved{}, fmt.Errorf("%s from %s: %s is not executable", tool, origin, path)
	}
	line, err := versionLine(ctx, path)
	if err != nil {
		return Resolved{}, fmt.Errorf("%s from %s: %w", tool, origin, err)
	}
	if err := checkExplicitVersion(tool, line); err != nil {
		var unknown *unknownVersionError
		if !errors.As(err, &unknown) {
			return Resolved{}, fmt.Errorf("%s from %s (%s): %w", tool, origin, path, err)
		}
		if warnf != nil {
			warnf("cannot check the release version of %s (%q); continuing", path, line)
		}
	}
	return Resolved{Path: path, Source: src, VersionLine: line}, nil
}

// versionLine runs "<path> -version" and returns its first output line.
func versionLine(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "-version")
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		msg := firstLine(errOut.String())
		if msg != "" {
			return "", fmt.Errorf("run %s -version: %w: %s", path, err, msg)
		}
		return "", fmt.Errorf("run %s -version: %w", path, err)
	}
	return firstLine(out.String()), nil
}

var releaseVersionRE = regexp.MustCompile(`^\S+ version n?(\d+)\.(\d+)`)

type unknownVersionError struct{ line string }

func (e *unknownVersionError) Error() string {
	return fmt.Sprintf("unrecognized version %q", e.line)
}

// checkExplicitVersion applies the compatibility policy for explicit paths:
// the program must identify itself as the requested tool, and a release
// build must be at least MinExplicitMajor. Snapshot builds whose release
// version cannot be read are reported through unknownVersionError.
func checkExplicitVersion(tool, line string) error {
	if !strings.HasPrefix(line, tool+" version ") {
		return fmt.Errorf("does not identify as %s (got %q)", tool, line)
	}
	m := releaseVersionRE.FindStringSubmatch(line)
	if m == nil {
		return &unknownVersionError{line: line}
	}
	major, _ := strconv.Atoi(m[1])
	if major < MinExplicitMajor {
		return fmt.Errorf("%s %s.%s is too old; version %d or newer is required", tool, m[1], m[2], MinExplicitMajor)
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
