// Package cli wires the musafoz operations to the Cobra command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/tools"
)

// Env holds the process environment the commands use. Tests replace it.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	GOOS   string
	GOARCH string
}

// DefaultEnv returns the real process environment.
func DefaultEnv() Env {
	return Env{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
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
		"path to an ffprobe executable to use instead of the managed one (overrides "+tools.EnvFFprobe+")")
	root.PersistentFlags().StringVar(&opts.ffmpeg, "ffmpeg", "",
		"path to an ffmpeg executable to use instead of the managed one (overrides "+tools.EnvFFmpeg+")")

	root.AddCommand(newInspectCommand(env, opts))
	root.AddCommand(newPreviewCommand(env, opts))
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
// then its environment variable, then the managed cache. Status and
// progress go to stderr only.
func resolveTool(ctx context.Context, env Env, tool, flagPath string) (tools.Resolved, error) {
	ro := tools.ResolveOptions{
		Tool:     tool,
		FlagPath: flagPath,
		EnvPath:  env.Getenv(tools.EnvVar(tool)),
		Warnf: func(format string, args ...any) {
			fmt.Fprintf(env.Stderr, "warning: "+format+"\n", args...)
		},
	}
	if ro.FlagPath == "" && ro.EnvPath == "" {
		artifact, err := tools.LookupArtifact(tool, env.GOOS, env.GOARCH)
		if err != nil {
			return tools.Resolved{}, err
		}
		cacheRoot, err := tools.DefaultCacheRoot(env.Getenv(tools.EnvCacheDir))
		if err != nil {
			return tools.Resolved{}, err
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
