package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
	SourcePath    Source = "path"
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
	// SearchPath looks for the tool on the PATH after the explicit paths and
	// before the managed build. See SearchesPath.
	SearchPath bool
	// LookPath finds the tool on the PATH. exec.LookPath is used when nil.
	LookPath func(file string) (string, error)
	// Check, when set, runs extra compatibility checks on a tool found on
	// the PATH, such as the FFmpeg encoders and filters the preview needs.
	// A failing tool is skipped in favor of the managed build.
	Check func(ctx context.Context, path string) error
	// Manager installs and verifies the managed build. It is used only when
	// no explicit or PATH tool is chosen.
	Manager *Manager
	// ManagerErr explains why Manager is nil, for example when no managed
	// build exists for the platform.
	ManagerErr error
	// Warnf receives non-fatal warnings. It may be nil.
	Warnf func(format string, args ...any)
	// Logf receives the choice of executable and the reason. It may be nil.
	Logf func(format string, args ...any)
}

// Resolved is an executable ready to run.
type Resolved struct {
	Path        string
	Source      Source
	VersionLine string
	// Reason explains the choice, for the log.
	Reason string
}

// ResolveFFprobe resolves ffprobe. See Resolve.
func ResolveFFprobe(ctx context.Context, opts ResolveOptions) (Resolved, error) {
	opts.Tool = ToolFFprobe
	return Resolve(ctx, opts)
}

// Resolve finds the executable to use for opts.Tool. The order is the flag,
// then the environment variable, then, where SearchPath is set, a compatible
// tool on the PATH, then the managed cache, which is populated on first use.
// The choice and its reason go to opts.Logf.
func Resolve(ctx context.Context, opts ResolveOptions) (Resolved, error) {
	r, err := resolve(ctx, opts)
	if err == nil && opts.Logf != nil {
		opts.Logf("using %s %s (%s)", opts.Tool, r.Path, r.Reason)
	}
	return r, err
}

func resolve(ctx context.Context, opts ResolveOptions) (Resolved, error) {
	if opts.Tool == "" {
		return Resolved{}, errors.New("resolve tool: no tool name given")
	}
	switch {
	case opts.FlagPath != "":
		return resolveExplicit(ctx, opts.Tool, opts.FlagPath, SourceFlag, opts.Warnf)
	case opts.EnvPath != "":
		return resolveExplicit(ctx, opts.Tool, opts.EnvPath, SourceEnv, opts.Warnf)
	}
	// A saved path from a local configuration file would come here. There
	// is no configuration file yet.
	why := ""
	if opts.SearchPath {
		r, err := resolveFromPath(ctx, opts)
		if err == nil {
			return r, nil
		}
		why = err.Error()
	}
	if opts.Manager == nil {
		err := opts.ManagerErr
		if err == nil {
			err = fmt.Errorf("resolve %s: no manager configured", opts.Tool)
		}
		if why != "" {
			return Resolved{}, fmt.Errorf("%s; %w", why, err)
		}
		return Resolved{}, err
	}
	if opts.Manager.Artifact.Tool != opts.Tool {
		return Resolved{}, fmt.Errorf("resolve %s: manager is configured for %s", opts.Tool, opts.Manager.Artifact.Tool)
	}
	path, err := opts.Manager.Ensure(ctx)
	if err != nil {
		if why != "" {
			return Resolved{}, fmt.Errorf("%s; %w", why, err)
		}
		return Resolved{}, err
	}
	a := opts.Manager.Artifact
	reason := fmt.Sprintf("managed %s build for %s", a.Version, a.Platform())
	if why != "" {
		reason += "; " + why
	} else if !opts.SearchPath {
		reason += "; the managed build is the default on this platform"
	}
	return Resolved{Path: path, Source: SourceManaged, VersionLine: a.VersionPrefix, Reason: reason}, nil
}

// resolveFromPath looks for a compatible tool on the PATH. Its errors
// explain why the PATH was not used.
func resolveFromPath(ctx context.Context, opts ResolveOptions) (Resolved, error) {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(opts.Tool)
	if err != nil {
		return Resolved{}, fmt.Errorf("no %s on the PATH", opts.Tool)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	r, err := checkCandidate(ctx, opts.Tool, path, opts.Warnf)
	if err == nil && opts.Check != nil {
		err = opts.Check(ctx, path)
	}
	if err != nil {
		return Resolved{}, fmt.Errorf("%s on the PATH (%s) is not compatible: %v", opts.Tool, path, err)
	}
	r.Source = SourcePath
	r.Reason = "found on the PATH: " + r.VersionLine
	return r, nil
}

func resolveExplicit(ctx context.Context, tool, path string, src Source, warnf func(string, ...any)) (Resolved, error) {
	origin := "--" + tool
	if src == SourceEnv {
		origin = EnvVar(tool)
	}
	r, err := checkCandidate(ctx, tool, path, warnf)
	if err != nil {
		return Resolved{}, fmt.Errorf("%s from %s (%s): %w", tool, origin, path, err)
	}
	r.Source = src
	r.Reason = "from " + origin + ": " + r.VersionLine
	return r, nil
}

// checkCandidate checks that path is an executable file that identifies as
// tool, and applies the version policy.
func checkCandidate(ctx context.Context, tool, path string, warnf func(string, ...any)) (Resolved, error) {
	path, err := executablePath(path)
	if err != nil {
		return Resolved{}, err
	}
	line, err := versionLine(ctx, path)
	if err != nil {
		return Resolved{}, err
	}
	if err := checkExplicitVersion(tool, line); err != nil {
		var unknown *unknownVersionError
		if !errors.As(err, &unknown) {
			return Resolved{}, err
		}
		if warnf != nil {
			warnf("cannot check the release version of %s (%q); continuing", path, line)
		}
	}
	return Resolved{Path: path, VersionLine: line}, nil
}

// hostOS is the operating system the executable checks follow. Tests
// replace it.
var hostOS = runtime.GOOS

// executablePath checks that path names a regular executable file. On
// Windows the file must have the .exe or .com extension, and a path without
// an extension is tried with .exe added; elsewhere an execute permission
// bit must be set.
func executablePath(path string) (string, error) {
	if hostOS == "windows" && filepath.Ext(path) == "" {
		if fi, err := os.Stat(path + ".exe"); err == nil && fi.Mode().IsRegular() {
			path += ".exe"
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if hostOS == "windows" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".exe", ".com":
		default:
			return "", fmt.Errorf("%s is not a Windows executable (.exe)", path)
		}
	} else if fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable", path)
	}
	return path, nil
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
