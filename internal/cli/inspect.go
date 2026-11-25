package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/fetch"
	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
)

func newInspectCommand(env Env, root *rootOptions) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inspect <file or image URL>",
		Short: "Show the details of a video or an image",
		Long: "Inspect a local video with FFprobe, or a local image or direct HTTP or HTTPS\n" +
			"image URL without FFprobe.\n\n" +
			"For videos, FFprobe is resolved from --ffprobe, then MUSAFOZ_FFPROBE, then a\n" +
			"compatible ffprobe on the PATH on Linux and Windows, then the managed build,\n" +
			"which is downloaded and verified on first use.\n\n" +
			"For images (JPEG, PNG, GIF, WebP, BMP, TIFF, and HEIC), the result gives the\n" +
			"format, the stored and displayed size, the EXIF orientation, and transparency.\n" +
			"With --json it also lists every readable EXIF tag, including GPS, camera,\n" +
			"lens, and exposure tags, under exif, with the numeric tag ids. Maker notes are\n" +
			"shown as raw bytes and not decoded. HEIC files are read from their container\n" +
			"without FFmpeg; their HEIF rotation and mirroring, not the EXIF orientation,\n" +
			"give the displayed orientation.",
		Example: "  musafoz inspect clip.mp4\n  musafoz inspect clip.mp4 --json\n" +
			"  musafoz inspect photo.jpg --json\n  musafoz inspect https://example.com/photo.jpg",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if fetch.IsURL(path) {
				return inspectImageURL(cmd.Context(), env, path, asJSON)
			}
			if err := media.ValidateInput(path); err != nil {
				return err
			}
			if format, err := imaging.SniffFile(path); err != nil {
				return err
			} else if format != "" {
				return inspectImage(env, path, path, asJSON)
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

// inspectImageURL downloads an image URL and inspects it. Videos are
// inspected from local files only.
func inspectImageURL(ctx context.Context, env Env, raw string, asJSON bool) error {
	label := fetch.Redact(raw)
	f, err := fetch.New(fetch.MaxImageBytes, fetch.ImageTimeout, func(format string, a ...any) {
		fmt.Fprintf(env.Stderr, format+"\n", a...)
	})
	if err != nil {
		return err
	}
	defer f.Close()
	local, err := f.Fetch(ctx, raw)
	if err != nil {
		return err
	}
	format, err := imaging.SniffFile(local)
	if err != nil {
		return err
	}
	if format == "" {
		return fmt.Errorf("inspect %s: not a supported image; videos are inspected from local files only", label)
	}
	return inspectImage(env, local, label, asJSON)
}

func inspectImage(env Env, path, name string, asJSON bool) error {
	info, err := imaging.Inspect(path, name)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	for _, w := range info.Warnings {
		fmt.Fprintf(env.Stderr, "warning: %s: %s\n", name, w)
	}
	if asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	writeImageText(env.Stdout, info)
	return nil
}

func writeImageText(w io.Writer, info *imaging.Info) {
	row := func(label, value string) {
		fmt.Fprintf(w, "%-12s %s\n", label+":", value)
	}
	row("File", info.Path)
	row("Size", fmt.Sprintf("%s (%d bytes)", humanBytes(info.SizeBytes), info.SizeBytes))
	row("Format", imaging.FormatLabel(info.Format)+" image")
	row("Dimensions", fmt.Sprintf("%dx%d", info.Width, info.Height))
	if info.Format == imaging.FormatHEIF {
		row("Transforms", imaging.HEIFLabel(info.HEIFTransforms)+" (HEIF irot and imir, applied)")
		if info.Orientation != nil {
			row("Orientation", fmt.Sprintf("%d (%s) in EXIF, not applied to HEIF", *info.Orientation, info.OrientationLabel))
		}
	} else if info.Orientation != nil {
		row("Orientation", fmt.Sprintf("%d (%s)", *info.Orientation, info.OrientationLabel))
	} else {
		row("Orientation", "not set")
	}
	if info.DisplayWidth != info.Width || info.DisplayHeight != info.Height {
		row("Display", fmt.Sprintf("%dx%d", info.DisplayWidth, info.DisplayHeight))
	}
	switch {
	case info.TransparentPixels == nil && info.AlphaChannel:
		row("Alpha", "alpha plane")
	case info.TransparentPixels != nil && *info.TransparentPixels:
		row("Alpha", "transparent pixels")
	case info.AlphaChannel:
		row("Alpha", "alpha channel, every pixel opaque")
	default:
		row("Alpha", "none")
	}
	for _, r := range info.EXIF.Summary() {
		row(r[0], r[1])
	}
	if n := info.EXIF.Count(); n == 1 {
		row("EXIF", "1 tag (use --json to list it)")
	} else if n > 1 {
		row("EXIF", fmt.Sprintf("%d tags (use --json to list them all)", n))
	} else {
		row("EXIF", "none")
	}
}
