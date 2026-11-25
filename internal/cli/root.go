// Package cli wires the musafoz operations to the Cobra command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/tools"
)

// Env holds the process environment the commands use. Tests replace it.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	GOOS   string
	GOARCH string
	// LookPath finds a tool on the PATH where the platform searches it.
	LookPath func(string) (string, error)
}

// DefaultEnv returns the real process environment.
func DefaultEnv() Env {
	return Env{
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Getenv:   os.Getenv,
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		LookPath: exec.LookPath,
	}
}

type rootOptions struct {
	ffprobe string
	ffmpeg  string
}

// NewRootCommand builds the musafoz command tree.
func NewRootCommand(env Env) *cobra.Command {
	opts := &rootOptions{}
	root := &cobra.Command{
		Use:           "musafoz",
		Short:         "Local media toolkit",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	root.PersistentFlags().StringVar(&opts.ffprobe, "ffprobe", "",
		"path to the ffprobe executable to use (overrides "+tools.EnvFFprobe+", the PATH, and the managed build)")
	root.PersistentFlags().StringVar(&opts.ffmpeg, "ffmpeg", "",
		"path to the ffmpeg executable to use (overrides "+tools.EnvFFmpeg+", the PATH, and the managed build)")

	root.AddCommand(newInspectCommand(env, opts))
	root.AddCommand(newPreviewCommand(env, opts))
	root.AddCommand(newCoverCommand(env, opts))
	root.AddCommand(newImageCommand(env, opts))
	root.AddCommand(newThumbCommand(env, opts))
	root.AddCommand(newLicensesCommand(env))
	return root
}

// Execute runs musafoz and returns the process exit code.
func Execute(ctx context.Context) int {
	env := DefaultEnv()
	if err := NewRootCommand(env).ExecuteContext(ctx); err != nil {
		fmt.Fprintf(env.Stderr, "musafoz: %v\n", err)
		return 1
	}
	return 0
}

// resolveTool applies the resolution order for a managed tool: the flag,
// then its environment variable, then a compatible tool on the PATH on
// Linux and Windows, then the managed build. The choice, status, and
// progress go to stderr only.
func resolveTool(ctx context.Context, env Env, tool, flagPath string) (tools.Resolved, error) {
	ro := tools.ResolveOptions{
		Tool:       tool,
		FlagPath:   flagPath,
		EnvPath:    env.Getenv(tools.EnvVar(tool)),
		SearchPath: tools.SearchesPath(env.GOOS),
		LookPath:   env.LookPath,
		Warnf: func(format string, args ...any) {
			fmt.Fprintf(env.Stderr, "warning: "+format+"\n", args...)
		},
		Logf: func(format string, args ...any) {
			fmt.Fprintf(env.Stderr, format+"\n", args...)
		},
	}
	if tool == tools.ToolFFmpeg {
		ro.Check = media.CheckFFmpegFeatures
	}
	if ro.FlagPath == "" && ro.EnvPath == "" {
		artifact, err := tools.LookupArtifact(tool, env.GOOS, env.GOARCH)
		if err != nil {
			ro.ManagerErr = err
			return tools.Resolve(ctx, ro)
		}
		cacheRoot, err := tools.DefaultCacheRoot(env.Getenv(tools.EnvCacheDir))
		if err != nil {
			ro.ManagerErr = err
			return tools.Resolve(ctx, ro)
		}
		progress := newProgress(env.Stderr)
		ro.Manager = &tools.Manager{
			Root:     cacheRoot,
			Artifact: artifact,
			Progress: progress.update,
			Logf: func(format string, args ...any) {
				progress.finish()
				fmt.Fprintf(env.Stderr, format+"\n", args...)
			},
		}
		defer progress.finish()
	}
	return tools.Resolve(ctx, ro)
}

func resolveFFprobe(ctx context.Context, env Env, opts *rootOptions) (tools.Resolved, error) {
	return resolveTool(ctx, env, tools.ToolFFprobe, opts.ffprobe)
}

func resolveFFmpeg(ctx context.Context, env Env, opts *rootOptions) (tools.Resolved, error) {
	return resolveTool(ctx, env, tools.ToolFFmpeg, opts.ffmpeg)
}
