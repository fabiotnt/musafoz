package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/preview"
)

func newCoverCommand(env Env, root *rootOptions) *cobra.Command {
	var (
		opts   preview.CoverOptions
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "cover <video> [time]",
		Short: "Write only the cover image beside a video",
		Long: "Write <name>_cover.jpg beside the video, as musafoz preview does, without the\n" +
			"MP4 preview or the mosaic. The time is HH:MM:SS or HH:MM:SS.fff, from\n" +
			"00:00:00 to the start of the last frame. Without a time, a random one is\n" +
			"picked with the preview rules. An existing cover is kept unless --overwrite\n" +
			"is given.",
		Example: "  musafoz cover clip.mp4 00:03:55\n  musafoz cover clip.mp4 --overwrite",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Input = args[0]
			if len(args) == 2 {
				t, err := media.ParseTimestamp(args[1])
				if err != nil {
					return err
				}
				opts.At = &t
			}
			opts.ResolveFFprobe = func(ctx context.Context) (string, error) {
				r, err := resolveFFprobe(ctx, env, root)
				return r.Path, err
			}
			opts.ResolveFFmpeg = func(ctx context.Context) (string, error) {
				r, err := resolveFFmpeg(ctx, env, root)
				return r.Path, err
			}
			opts.Logf = func(format string, a ...any) { fmt.Fprintf(env.Stderr, format+"\n", a...) }
			res, err := preview.Cover(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(env.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			fmt.Fprintf(env.Stdout, "Cover:      %s (at %s)\n", res.Cover, media.Timestamp(res.CoverAtSeconds))
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.Overwrite, "overwrite", false, "replace an existing cover")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON on stdout")
	return cmd
}
