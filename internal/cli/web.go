package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/fetch"
	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/preview"
	"github.com/fabiotnt/musafoz/internal/stills"
)

type webFlags struct {
	output, metadata, videoSize, videoBitrate   string
	imageSize, thumbSize, quality, thumbQuality int
	frames                                      int
	overwrite, skipExisting, noPreview          bool
}

func (f *webFlags) register(cmd *cobra.Command) {
	d := stills.DefaultWebOptions()
	fl := cmd.PersistentFlags()
	fl.StringVar(&f.output, "output", "", "directory for the web copies (required); created when missing")
	fl.StringVar(&f.metadata, "metadata", "", "directory for one <uuid>.json per content with all metadata, including GPS; keep it private")
	fl.IntVar(&f.imageSize, "image-size", d.ImageMax, "maximum long side of web images, in pixels; images are never enlarged")
	fl.IntVar(&f.thumbSize, "thumb-size", d.ThumbShort, "short side of thumbnails, in pixels; thumbnails keep the aspect ratio")
	fl.IntVar(&f.quality, "quality", d.ImageQuality, "JPEG quality of web images, 1 to 100")
	fl.IntVar(&f.thumbQuality, "thumb-quality", d.ThumbQuality, "JPEG quality of thumbnails, 1 to 100")
	fl.StringVar(&f.videoSize, "video-size", fmt.Sprintf("%dx%d", d.Video.MaxLong, d.Video.MaxShort),
		"maximum size of web videos as LONGxSHORT; portrait videos use it turned, so 1920x1080 is 1080p either way")
	fl.StringVar(&f.videoBitrate, "video-bitrate", "5M", "maximum video bitrate, such as 5M or 4500k")
	fl.IntVar(&f.frames, "frames", preview.DefaultFrames, fmt.Sprintf("frames of the video previews and mosaics, 1 to %d", preview.MaxFrames))
	fl.BoolVar(&f.noPreview, "no-preview", false, "do not write covers, preview videos, and mosaics for videos")
	fl.BoolVar(&f.overwrite, "overwrite", false, "replace existing outputs")
	fl.BoolVar(&f.skipExisting, "skip-existing", false, "reuse the result of content whose metadata file and outputs exist, to resume a job; needs --metadata")
	cmd.MarkFlagsMutuallyExclusive("overwrite", "skip-existing")
}

// parseBitrate reads 5M, 4500k, or 5000000.
func parseBitrate(s string) (int, error) {
	mult := 1
	switch {
	case strings.HasSuffix(s, "M") || strings.HasSuffix(s, "m"):
		mult, s = 1_000_000, s[:len(s)-1]
	case strings.HasSuffix(s, "k") || strings.HasSuffix(s, "K"):
		mult, s = 1000, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%q is not a bitrate such as 5M or 4500k", s)
	}
	b := int(v * float64(mult))
	if b < 100_000 || b > 100_000_000 {
		return 0, fmt.Errorf("bitrate %d is outside 100k to 100M", b)
	}
	return b, nil
}

func (f *webFlags) options(env Env, root *rootOptions) (stills.Output, stills.WebOptions, error) {
	o := stills.DefaultWebOptions()
	if f.output == "" {
		return stills.Output{}, o, errors.New("--output is required")
	}
	if f.frames < 1 || f.frames > preview.MaxFrames {
		return stills.Output{}, o, fmt.Errorf("--frames must be 1 to %d", preview.MaxFrames)
	}
	for name, q := range map[string]int{"--quality": f.quality, "--thumb-quality": f.thumbQuality} {
		if _, err := imaging.ParseQuality(q); err != nil {
			return stills.Output{}, o, fmt.Errorf("%s: %w", name, err)
		}
	}
	size, err := imaging.ParseSize(f.videoSize)
	if err != nil {
		return stills.Output{}, o, fmt.Errorf("--video-size: %w", err)
	}
	if size.Width < size.Height {
		return stills.Output{}, o, errors.New("--video-size is LONGxSHORT, such as 1920x1080")
	}
	bitrate, err := parseBitrate(f.videoBitrate)
	if err != nil {
		return stills.Output{}, o, fmt.Errorf("--video-bitrate: %w", err)
	}
	o.ImageMax, o.ThumbShort, o.ImageQuality, o.ThumbQuality = f.imageSize, f.thumbSize, f.quality, f.thumbQuality
	o.Video.MaxLong, o.Video.MaxShort, o.Video.Bitrate = size.Width, size.Height, bitrate
	o.MetadataDir, o.SkipExisting = f.metadata, f.skipExisting
	if f.skipExisting && f.metadata == "" {
		return stills.Output{}, o, errors.New("--skip-existing needs --metadata")
	}
	if !f.noPreview {
		frames := f.frames
		o.Preview = func(ctx context.Context, video string, overwrite bool) (string, string, string, error) {
			res, err := preview.Run(ctx, preview.Options{
				Input: video, Frames: frames, Overwrite: overwrite, Accel: media.AccelAuto,
				ResolveFFprobe: func(ctx context.Context) (string, error) {
					r, err := resolveFFprobe(ctx, env, root)
					return r.Path, err
				},
				ResolveFFmpeg: func(ctx context.Context) (string, error) {
					r, err := resolveFFmpeg(ctx, env, root)
					return r.Path, err
				},
				Warnf: func(format string, a ...any) { fmt.Fprintf(env.Stderr, "warning: "+format+"\n", a...) },
			})
			if err != nil {
				return "", "", "", err
			}
			return res.Outputs.Cover, res.Outputs.Video, res.Outputs.Mosaic, nil
		}
	}
	return stills.Output{Dir: f.output, Overwrite: f.overwrite}, o, nil
}

// webRecord is one line of the web batch report.
type webRecord struct {
	Line   int    `json:"line"`
	Source string `json:"source"`
	Status string `json:"status"`
	*stills.WebResult
	Error string `json:"error,omitempty"`
}

func newWebCommand(env Env, root *rootOptions) *cobra.Command {
	var flags webFlags
	cmd := &cobra.Command{
		Use:   "web <image or video>",
		Short: "Make the web copy of a photo or video, named by its content",
		Long: "Convert a local file or a direct HTTP or HTTPS URL into files every browser\n" +
			"shows, named by a UUID derived from the SHA-256 of the content, so the same\n" +
			"content always gets the same name.\n\n" +
			"Images (JPEG, PNG, GIF, WebP, BMP, TIFF, HEIC) become <uuid>.jpg, at most\n" +
			"--image-size on the long side, and <uuid>_thumb.jpg, --thumb-size on the short\n" +
			"side. The orientation is applied and colors are converted to sRGB from the\n" +
			"embedded ICC profile or HEIF color description. Outputs carry no metadata.\n\n" +
			"Videos become <uuid>.mp4: H.264 and AAC, at most 1080p and 5 Mbps by default,\n" +
			"HDR tone mapped to SDR, faststart, and no metadata. Unless --no-preview is\n" +
			"given, the cover, preview video, and mosaic of the preview command are made\n" +
			"from it: <uuid>_cover.jpg, <uuid>_preview.mp4, and <uuid>_preview.jpg.\n\n" +
			"With --metadata, <uuid>.json keeps every metadata field read from the source,\n" +
			"including EXIF, XMP, GPS, and the video container tags. Keep it private.\n" +
			"The result is printed as JSON.",
		Example: "  musafoz web IMG_0001.HEIC --output ./web --metadata ./meta\n" +
			"  musafoz web clip.mov --output ./web --video-bitrate 4M\n" +
			"  musafoz web batch sources.txt --output ./web --metadata ./meta --jsonl > report.jsonl",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, o, err := flags.options(env, root)
			if err != nil {
				return err
			}
			r, done, err := newRunner(env, root, out, fetch.MaxVideoBytes, fetch.VideoTimeout)
			if err != nil {
				return err
			}
			defer done()
			res, err := r.Web(cmd.Context(), args[0], "this run", o)
			if err != nil {
				return fmt.Errorf("web %s: %w", labelOf(args[0]), err)
			}
			enc := json.NewEncoder(env.Stdout)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			return enc.Encode(webRecord{Line: 0, Source: labelOf(args[0]), Status: res.Status, WebResult: res})
		},
	}
	flags.register(cmd)
	var jsonl bool
	batch := &cobra.Command{
		Use:   "batch <list.txt>",
		Short: "Make web copies of the sources listed in a text file",
		Long: "Read one source per line: a local path or a direct HTTP or HTTPS URL. Relative\n" +
			"paths are resolved from the directory of the list. Content that appears again\n" +
			"in the list is converted once; its later lines report status duplicate with\n" +
			"the same UUID. A failed line is reported with its number and reason, the other\n" +
			"lines continue, and the command exits with an error at the end.\n\n" +
			"Without --jsonl the UUID and source of each line are printed. With --jsonl,\n" +
			"stdout has one JSON object per line: line, source (a URL without credentials,\n" +
			"query, or fragment), status (ok, duplicate, exists, or error), and the result\n" +
			"or the error. The report has no GPS or camera data; --metadata files do.",
		Example: "  musafoz web batch sources.txt --output ./web --metadata ./meta --jsonl > report.jsonl\n" +
			"  musafoz web batch sources.txt --output ./web --metadata ./meta --skip-existing --jsonl",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, o, err := flags.options(env, root)
			if err != nil {
				return err
			}
			r, done, err := newRunner(env, root, out, fetch.MaxVideoBytes, fetch.VideoTimeout)
			if err != nil {
				return err
			}
			defer done()
			enc := json.NewEncoder(env.Stdout)
			enc.SetEscapeHTML(false)
			hooks := stills.WebHooks{
				OnItem: func(line int, source string, res *stills.WebResult) {
					if jsonl {
						enc.Encode(webRecord{Line: line, Source: source, Status: res.Status, WebResult: res})
						return
					}
					fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", res.UUID, res.Status, source)
				},
				OnError: func(e *stills.LineError) {
					fmt.Fprintf(env.Stderr, "error: %v\n", e)
					if jsonl {
						enc.Encode(webRecord{Line: e.Line, Source: e.Source, Status: "error", Error: e.Err.Error()})
					}
				},
			}
			return r.WebBatch(cmd.Context(), args[0], o, hooks)
		},
	}
	batch.Flags().BoolVar(&jsonl, "jsonl", false, "print one JSON object per list line on stdout")
	cmd.AddCommand(batch)
	return cmd
}
