package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/fetch"
	"github.com/fabiotnt/musafoz/internal/imaging"
	"github.com/fabiotnt/musafoz/internal/media"
	"github.com/fabiotnt/musafoz/internal/stills"
)

// stillFlags are the output flags shared by thumb and image.
type stillFlags struct {
	size, mode, format, output, prefix, suffix string
	alongside, overwrite                       bool
	// quality is registered by thumb only (registerQuality).
	quality int
}

// registerQuality adds --quality, for the commands that encode JPEG in Go.
func (f *stillFlags) registerQuality(cmd *cobra.Command) {
	cmd.PersistentFlags().IntVar(&f.quality, "quality", imaging.JPEGQuality,
		fmt.Sprintf("JPEG quality, %d to %d; lower values give smaller files", imaging.MinQuality, imaging.MaxQuality))
}

func (f *stillFlags) register(cmd *cobra.Command, sizeHelp, suffixHelp string) {
	fl := cmd.PersistentFlags()
	fl.StringVar(&f.size, "size", "", sizeHelp)
	fl.StringVar(&f.mode, "mode", imaging.ModeFit, "with --size: fit keeps the aspect ratio inside the size without cropping or borders; fill covers the exact size and crops the excess from the center")
	fl.StringVar(&f.format, "format", "jpeg", "output format: jpeg or png (transparency becomes white in JPEG)")
	fl.StringVar(&f.output, "output", "", "directory for the images (default: the current directory); created when missing")
	fl.BoolVar(&f.alongside, "alongside", false, "write each image beside its local source instead of in --output")
	fl.StringVar(&f.prefix, "prefix", "", "text added before the base name, instead of a suffix")
	fl.StringVar(&f.suffix, "suffix", "", suffixHelp)
	fl.BoolVar(&f.overwrite, "overwrite", false, "replace existing images")
	cmd.MarkFlagsMutuallyExclusive("output", "alongside")
	cmd.MarkFlagsMutuallyExclusive("prefix", "suffix")
}

// toOutput checks the flags. defaultSuffix applies when neither --prefix
// nor --suffix is given.
func (f *stillFlags) toOutput(cmd *cobra.Command, sizeRequired bool, defaultSuffix string) (stills.Output, error) {
	var o stills.Output
	fl := cmd.Flags()
	if f.size == "" {
		if sizeRequired {
			return o, errors.New("--size is required, such as --size 320x240")
		}
		if fl.Changed("mode") {
			return o, errors.New("--mode needs --size")
		}
	} else {
		s, err := imaging.ParseSize(f.size)
		if err != nil {
			return o, fmt.Errorf("--size: %w", err)
		}
		o.Size = &s
	}
	mode, err := imaging.ParseMode(f.mode)
	if err != nil {
		return o, fmt.Errorf("--mode: %w", err)
	}
	format, err := imaging.ParseOutputFormat(f.format)
	if err != nil {
		return o, fmt.Errorf("--format: %w", err)
	}
	if fl.Lookup("quality") != nil {
		q, err := imaging.ParseQuality(f.quality)
		if err != nil {
			return o, fmt.Errorf("--quality: %w", err)
		}
		if fl.Changed("quality") && format != imaging.OutputJPEG {
			return o, errors.New("--quality applies to JPEG output only")
		}
		o.Quality = q
	}
	if fl.Changed("output") && f.output == "" {
		return o, errors.New("--output needs a directory")
	}
	o.Mode, o.Format = mode, format
	o.Dir, o.Alongside, o.Overwrite = f.output, f.alongside, f.overwrite
	o.Prefix, o.Suffix = f.prefix, f.suffix
	if !fl.Changed("prefix") && !fl.Changed("suffix") {
		o.Suffix = defaultSuffix
	}
	return o, nil
}

func newRunner(env Env, root *rootOptions, out stills.Output, maxBytes int64, timeout time.Duration) (*stills.Runner, func(), error) {
	logf := func(format string, args ...any) { fmt.Fprintf(env.Stderr, format+"\n", args...) }
	f, err := fetch.New(maxBytes, timeout, logf)
	if err != nil {
		return nil, nil, err
	}
	r := &stills.Runner{
		Output:  out,
		Fetcher: f,
		Logf:    logf,
		ResolveFFprobe: func(ctx context.Context) (string, error) {
			res, err := resolveFFprobe(ctx, env, root)
			return res.Path, err
		},
		ResolveFFmpeg: func(ctx context.Context) (string, error) {
			res, err := resolveFFmpeg(ctx, env, root)
			return res.Path, err
		},
	}
	return r, func() { f.Close() }, nil
}

func batchHooks(env Env) stills.BatchHooks {
	return stills.BatchHooks{
		OnOutput: func(line int, path string) { fmt.Fprintln(env.Stdout, path) },
		OnError:  func(e *stills.LineError) { fmt.Fprintf(env.Stderr, "error: %v\n", e) },
	}
}

// thumbRecordOK and thumbRecordFailed are the lines of the thumb batch
// --jsonl report. They carry only what an import needs: no GPS, camera, or
// other EXIF data. A written thumbnail always has date and date_source,
// null when the picture has no date; source sizes are the displayed ones.
type thumbRecordOK struct {
	Line         int     `json:"line"`
	Source       string  `json:"source"`
	Status       string  `json:"status"`
	Output       string  `json:"output"`
	SourceWidth  int     `json:"source_width"`
	SourceHeight int     `json:"source_height"`
	OutputWidth  int     `json:"output_width"`
	OutputHeight int     `json:"output_height"`
	Date         *string `json:"date"`
	DateSource   *string `json:"date_source"`
}

type thumbRecordFailed struct {
	Line   int    `json:"line"`
	Source string `json:"source"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// thumbReportHooks write the --jsonl report: one object per list line on
// stdout, and errors on stderr as without --jsonl.
func thumbReportHooks(env Env) stills.BatchHooks {
	enc := json.NewEncoder(env.Stdout)
	enc.SetEscapeHTML(false)
	return stills.BatchHooks{
		OnThumb: func(line int, source string, res *stills.ThumbResult) {
			rec := thumbRecordOK{
				Line: line, Source: source, Status: "ok", Output: res.Path,
				SourceWidth: res.SourceWidth, SourceHeight: res.SourceHeight,
				OutputWidth: res.Width, OutputHeight: res.Height,
			}
			if res.Date != nil {
				rec.Date, rec.DateSource = &res.Date.Value, &res.Date.Source
			}
			enc.Encode(rec)
		},
		OnError: func(e *stills.LineError) {
			fmt.Fprintf(env.Stderr, "error: %v\n", e)
			enc.Encode(thumbRecordFailed{Line: e.Line, Source: e.Source, Status: "error", Error: e.Err.Error()})
		},
	}
}

func labelOf(src string) string {
	if fetch.IsURL(src) {
		return fetch.Redact(src)
	}
	return src
}

func newThumbCommand(env Env, root *rootOptions) *cobra.Command {
	var flags stillFlags
	cmd := &cobra.Command{
		Use:   "thumb <image>",
		Short: "Write a resized copy of an image, without metadata",
		Long: "Write a thumbnail of a local image or of a direct HTTP or HTTPS image URL.\n\n" +
			"The EXIF orientation is applied to the pixels before resizing, and the output\n" +
			"carries no EXIF or other metadata. The source is never changed. Reads JPEG,\n" +
			"PNG, GIF (first frame), WebP, BMP, TIFF, and HEIC; writes JPEG or PNG. HEIC\n" +
			"is decoded with FFmpeg 7.1 or newer, resolved as for the video commands, and\n" +
			"keeps the HEIF rotation and mirroring without applying the EXIF orientation\n" +
			"again.\n\n" +
			"The output is <name>_thumb.jpg in the current directory, in --output, or\n" +
			"beside the source with --alongside. For a URL, <name> is the last segment of\n" +
			"its path. Existing files are kept unless --overwrite is given. JPEG uses\n" +
			"quality 92 unless --quality is given.",
		Example: "  musafoz thumb photo.png --size 320x240\n" +
			"  musafoz thumb photo.jpg --size 400x400 --mode fill --format png --output ./thumbs\n" +
			"  musafoz thumb https://example.com/images/photo.jpg --size 320x240\n" +
			"  musafoz thumb photo.jpg --size 480x480 --mode fill --quality 80\n" +
			"  musafoz thumb batch sources.txt --size 320x240 --output ./thumbs",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := flags.toOutput(cmd, true, stills.DefaultThumbSuffix)
			if err != nil {
				return err
			}
			r, done, err := newRunner(env, root, out, fetch.MaxImageBytes, fetch.ImageTimeout)
			if err != nil {
				return err
			}
			defer done()
			res, err := r.Thumb(cmd.Context(), args[0], "this run")
			if err != nil {
				return fmt.Errorf("thumb %s: %w", labelOf(args[0]), err)
			}
			fmt.Fprintln(env.Stdout, res.Path)
			return nil
		},
	}
	flags.register(cmd, "maximum size as WIDTHxHEIGHT, such as 320x240 (required)",
		"text added after the base name instead of _thumb")
	flags.registerQuality(cmd)
	var jsonl bool
	batch := &cobra.Command{
		Use:   "batch <list.txt>",
		Short: "Write thumbnails for the images listed in a text file",
		Long: "Read one image source per line: a local path or a direct HTTP or HTTPS URL.\n" +
			"Empty lines are skipped and paths may contain spaces. Relative paths are\n" +
			"resolved from the directory of the list. A failed line is reported with its\n" +
			"number and reason, the other lines continue, and the command exits with an\n" +
			"error at the end. The output paths are printed on stdout.\n\n" +
			"With --jsonl, stdout has one JSON object per line of the list instead, in\n" +
			"list order: the line number, the source (a URL without credentials, query,\n" +
			"or fragment), and the status. A written thumbnail adds its path, the\n" +
			"displayed size of the source, the size of the output, and the date of the\n" +
			"picture with the EXIF tag it comes from: DateTimeOriginal, or DateTime as a\n" +
			"fallback, with the UTC offset when the file gives one; null when the file\n" +
			"has no date. A failed line adds the error. Errors also go to stderr.",
		Example: "  musafoz thumb batch sources.txt --size 320x240 --output ./thumbs\n" +
			"  musafoz thumb batch sources.txt --size 480x480 --mode fill --quality 80 --jsonl > report.jsonl",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := flags.toOutput(cmd, true, stills.DefaultThumbSuffix)
			if err != nil {
				return err
			}
			r, done, err := newRunner(env, root, out, fetch.MaxImageBytes, fetch.ImageTimeout)
			if err != nil {
				return err
			}
			defer done()
			hooks := batchHooks(env)
			if jsonl {
				hooks = thumbReportHooks(env)
			}
			return r.ThumbBatch(cmd.Context(), args[0], hooks)
		},
	}
	batch.Flags().BoolVar(&jsonl, "jsonl", false, "print one JSON object per list line on stdout instead of the output paths")
	cmd.AddCommand(batch)
	return cmd
}

func newImageCommand(env Env, root *rootOptions) *cobra.Command {
	var (
		flags  stillFlags
		random int
	)
	cmd := &cobra.Command{
		Use:   "image <video> [time...]",
		Short: "Extract clean frames of a video as images",
		Long: "Extract a clean frame, without a timestamp overlay, for each time given as\n" +
			"HH:MM:SS or HH:MM:SS.fff. Without times one frame is extracted at a random\n" +
			"time; --random N draws N random times. Draws are independent and may select\n" +
			"the same frame; repeated names get an index, such as clip_00-00-05-120-2.jpg.\n\n" +
			"The video can be a local file or a direct HTTP or HTTPS URL. Frames keep the\n" +
			"displayed orientation and aspect ratio of the video, at its displayed\n" +
			"resolution unless --size is given. Each output is <name>_<time><suffix>.jpg,\n" +
			"with the time as HH-MM-SS or HH-MM-SS-mmm, including random times.",
		Example: "  musafoz image clip.mp4 00:01:00 00:02:13\n" +
			"  musafoz image clip.mp4 00:01:00 --size 320x240 --mode fill\n" +
			"  musafoz image clip.mp4 --random 10 --output ./frames\n" +
			"  musafoz image batch frames.tsv --output ./frames",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := flags.toOutput(cmd, false, "")
			if err != nil {
				return err
			}
			req := stills.FrameRequest{Random: 1}
			if cmd.Flags().Changed("random") {
				if random < 1 {
					return errors.New("--random must be 1 or more")
				}
				if len(args) > 1 {
					return errors.New("give times or --random, not both")
				}
				req.Random = random
			}
			if len(args) > 1 {
				req = stills.FrameRequest{}
				for _, a := range args[1:] {
					t, err := media.ParseTimestamp(a)
					if err != nil {
						return err
					}
					req.Times = append(req.Times, t)
				}
			}
			r, done, err := newRunner(env, root, out, fetch.MaxVideoBytes, fetch.VideoTimeout)
			if err != nil {
				return err
			}
			defer done()
			frames, err := r.Frames(cmd.Context(), args[0], req, "this run")
			for _, f := range frames {
				fmt.Fprintln(env.Stdout, f.Path)
			}
			if err != nil {
				return fmt.Errorf("image %s: %w", labelOf(args[0]), err)
			}
			return nil
		},
	}
	flags.register(cmd, "maximum size as WIDTHxHEIGHT, such as 320x240 (default: the displayed size of the video)",
		"text added after the time in each name")
	cmd.Flags().IntVar(&random, "random", 1, "number of random times to extract, when no times are given")
	batch := &cobra.Command{
		Use:   "batch <list.tsv>",
		Short: "Extract the frames listed in a TSV file",
		Long: "Read one video source per line: a local path or a direct HTTP or HTTPS URL,\n" +
			"optionally followed by a TAB and a time as HH:MM:SS or HH:MM:SS.fff. An empty\n" +
			"or missing time extracts a random frame; a malformed time fails that line.\n" +
			"Repeat a source on several lines to extract several frames; a URL is\n" +
			"downloaded once per run. Empty lines are skipped and paths may contain\n" +
			"spaces. Relative paths are resolved from the directory of the list. A failed\n" +
			"line is reported with its number and reason, the other lines continue, and\n" +
			"the command exits with an error at the end.",
		Example: "  musafoz image batch frames.tsv --output ./frames --size 640x360",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := flags.toOutput(cmd, false, "")
			if err != nil {
				return err
			}
			r, done, err := newRunner(env, root, out, fetch.MaxVideoBytes, fetch.VideoTimeout)
			if err != nil {
				return err
			}
			defer done()
			return r.FrameBatch(cmd.Context(), args[0], batchHooks(env))
		},
	}
	cmd.AddCommand(batch)
	return cmd
}
