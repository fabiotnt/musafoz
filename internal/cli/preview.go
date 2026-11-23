package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/preview"
)

func newPreviewCommand(env Env, root *rootOptions) *cobra.Command {
	var (
		opts   preview.Options
		asJSON bool
		accel  string
	)
	cmd := &cobra.Command{
		Use:   "preview <video>",
		Short: "Generate a cover, an MP4 preview, and a mosaic beside a video",
		Long: "Generate <name>_cover.jpg, <name>_preview.mp4, and <name>_preview.jpg beside\n" +
			"the video.\n\n" +
			"The command stops before processing if any output exists, unless --overwrite\n" +
			"is given. Outputs are generated in temporary files and published together;\n" +
			"on failure the earlier outputs are restored. The original video is left\n" +
			"unchanged unless --faststart-in-place is given, in which case it is replaced\n" +
			"by a stream-copy remux only after all three outputs are published.",
		Example: "  musafoz preview clip.mp4\n" +
			"  musafoz preview clip.mp4 --frames 50 --overwrite\n" +
			"  musafoz preview clip.mp4 --faststart-in-place --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Input = args[0]
			mode, err := media.ParseAccel(accel)
			if err != nil {
				return err
			}
			opts.Accel = mode
			opts.ResolveFFprobe = func(ctx context.Context) (string, error) {
				r, err := resolveFFprobe(ctx, env, root)
				return r.Path, err
			}
			opts.ResolveFFmpeg = func(ctx context.Context) (string, error) {
				r, err := resolveFFmpeg(ctx, env, root)
				return r.Path, err
			}
			opts.Logf = func(format string, a ...any) {
				fmt.Fprintf(env.Stderr, format+"\n", a...)
			}
			opts.Warnf = func(format string, a ...any) {
				fmt.Fprintf(env.Stderr, "warning: "+format+"\n", a...)
			}
			res, err := preview.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(env.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			w := env.Stdout
			fmt.Fprintf(w, "Cover:      %s (at %s)\n", res.Outputs.Cover, media.Clock(res.CoverAtSeconds))
			fmt.Fprintf(w, "Preview:    %s (%d frames at 4 fps)\n", res.Outputs.Video, res.Frames)
			fmt.Fprintf(w, "Mosaic:     %s\n", res.Outputs.Mosaic)
			fmt.Fprintf(w, "Faststart:  %s\n", faststartLabel(res.Faststart))
			fmt.Fprintf(w, "Decoding:   %s\n", media.BackendLabel(res.FrameDecoder))
			fmt.Fprintf(w, "Encoding:   %s\n", media.BackendLabel(res.PreviewEncoder))
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&opts.Frames, "frames", preview.DefaultFrames, fmt.Sprintf("number of frames for the preview and mosaic (1 to %d)", preview.MaxFrames))
	f.BoolVar(&opts.Overwrite, "overwrite", false, "replace existing outputs")
	f.BoolVar(&opts.FaststartInPlace, "faststart-in-place", false, "remux the original MP4 or MOV with faststart and replace it after the outputs are published")
	f.StringVar(&opts.Font, "font", "", "font file for the overlays (default: Andale Mono or DejaVu Sans Mono)")
	f.StringVar(&accel, "accel", string(media.AccelAuto),
		"hardware acceleration: auto uses VideoToolbox on macOS when a probe confirms it works for the input, cpu forces software")
	f.BoolVar(&asJSON, "json", false, "print the result as JSON on stdout")
	return cmd
}

func faststartLabel(s string) string {
	switch s {
	case preview.FaststartApplied:
		return "applied, original replaced"
	case preview.FaststartAlready:
		return "already applied, original unchanged"
	}
	return "not requested, original unchanged"
}
