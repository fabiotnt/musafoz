package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/media"
)

func newInspectCommand(env Env, root *rootOptions) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inspect <file>",
		Short: "Show duration, dimensions, codecs, and stream details of a video",
		Long: "Inspect a local video with FFprobe.\n\n" +
			"FFprobe is resolved from --ffprobe, then MUSAFOZ_FFPROBE, then the managed\n" +
			"cache. The managed build is downloaded and verified on first use. The PATH\n" +
			"is not searched.",
		Example: "  musafoz inspect clip.mp4\n  musafoz inspect clip.mp4 --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			// Fail fast on bad input before any download.
			if err := media.ValidateInput(path); err != nil {
				return err
			}
			ffprobe, err := resolveFFprobe(cmd.Context(), env, root)
			if err != nil {
				return err
			}
			info, err := media.Inspect(cmd.Context(), ffprobe.Path, path)
			if err != nil {
				return err
			}
			if info.DurationSeconds == nil {
				fmt.Fprintf(env.Stderr, "warning: %s: duration not reported by the container or the video stream\n", path)
			}
			if asJSON {
				enc := json.NewEncoder(env.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			writeText(env.Stdout, info)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON on stdout")
	return cmd
}

func writeText(w io.Writer, info *media.Info) {
	row := func(label, value string) {
		fmt.Fprintf(w, "%-11s %s\n", label+":", value)
	}
	row("File", info.Path)
	row("Size", fmt.Sprintf("%s (%d bytes)", humanBytes(info.SizeBytes), info.SizeBytes))
	container := info.Format
	if info.FormatLongName != "" {
		container += " (" + info.FormatLongName + ")"
	}
	row("Container", container)
	if info.DurationSeconds != nil {
		row("Duration", fmt.Sprintf("%.3f s (%s, from %s)", *info.DurationSeconds,
			clock(*info.DurationSeconds), strings.ReplaceAll(info.DurationSource, "_", " ")))
	} else {
		row("Duration", "unknown")
	}
	if info.BitRate != nil {
		row("Bit rate", fmt.Sprintf("%d kb/s", *info.BitRate/1000))
	}

	v := info.Video
	parts := []string{codecLabel(v.Codec, v.Profile), fmt.Sprintf("%dx%d", v.Width, v.Height)}
	if v.PixelFormat != "" {
		parts = append(parts, v.PixelFormat)
	}
	if v.FrameRate != nil {
		parts = append(parts, fmt.Sprintf("%s fps", trimFloat(*v.FrameRate)))
	}
	if v.FrameCount != nil {
		parts = append(parts, fmt.Sprintf("%d frames", *v.FrameCount))
	}
	row("Video", strings.Join(parts, ", "))
	if v.SampleAspectRatio != "1:1" {
		row("Pixels", fmt.Sprintf("sample aspect ratio %s", v.SampleAspectRatio))
	}
	if v.Rotation != 0 {
		row("Rotation", fmt.Sprintf("%d degrees clockwise", v.Rotation))
	}
	if v.Rotation != 0 || v.SampleAspectRatio != "1:1" {
		row("Display", fmt.Sprintf("%dx%d", v.DisplayWidth, v.DisplayHeight))
	}

	if a := info.Audio; a != nil {
		parts := []string{a.Codec}
		ch := fmt.Sprintf("%d channels", a.Channels)
		if a.Channels == 1 {
			ch = "1 channel"
		}
		if a.ChannelLayout != "" {
			ch += " (" + a.ChannelLayout + ")"
		}
		parts = append(parts, ch)
		if a.SampleRate > 0 {
			parts = append(parts, fmt.Sprintf("%d Hz", a.SampleRate))
		}
		row("Audio", strings.Join(parts, ", "))
	} else {
		row("Audio", "none")
	}

	s := info.Streams
	counts := fmt.Sprintf("%d video, %d audio, %d subtitle", s.Video, s.Audio, s.Subtitle)
	if s.AttachedPicture > 0 {
		counts += fmt.Sprintf(", %d attached picture", s.AttachedPicture)
	}
	if s.Other > 0 {
		counts += fmt.Sprintf(", %d other", s.Other)
	}
	row("Streams", counts)
}

func codecLabel(codec, profile string) string {
	if profile == "" {
		return codec
	}
	return codec + " (" + profile + ")"
}

func clock(sec float64) string {
	ms := int64(sec*1000 + 0.5)
	h := ms / 3600000
	m := ms / 60000 % 60
	s := ms / 1000 % 60
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms%1000)
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
