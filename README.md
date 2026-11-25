# MusaFoz

MusaFoz is a command-line toolkit for video previews, still images, and
media inspection. Generate a cover, an MP4 preview, and a timestamped
mosaic from one video. Extract individual frames, resize photos from
files or URLs, and inspect image metadata including EXIF.

## Install

Download the ZIP for your platform from [GitHub Releases](https://github.com/fabiotnt/musafoz/releases)
and extract it. On macOS and Linux, run `./musafoz` from the extracted
directory. In Windows PowerShell, use `.\musafoz.exe`. The examples below
use `musafoz`, which assumes the executable is on your PATH.

To build from source, use Go 1.27.3 or newer:

```sh
go build -o musafoz ./cmd/musafoz
```

## Quick start

| Task | Command |
| --- | --- |
| Inspect a video or photo | `musafoz inspect photo.jpg --json` |
| Generate a video preview | `musafoz preview clip.mp4 --frames 25 --cover-at 00:00:01.250` |
| Generate only a cover | `musafoz cover clip.mp4 00:03:55` |
| Extract a clean video frame | `musafoz image clip.mp4 00:01:00 --output ./frames` |
| Resize a photo | `musafoz thumb photo.png --size 320x240` |

Use `musafoz <command> --help` to see the available flags. This README
describes the current source tree. Check the release notes for the
features in a downloaded build.

## Inspect media

```sh
musafoz inspect clip.mp4
musafoz inspect photo.jpg --json
musafoz inspect "https://example.com/images/photo.jpg" --json
```

For local videos, `inspect` reports duration, streams, codecs, and display
geometry. For images, it reports the format, stored and displayed
dimensions, orientation, and transparency. Image inspection supports local
files and direct HTTP(S) URLs. Supported image formats are JPEG, PNG, GIF,
WebP, BMP, TIFF, and HEIC. Formats are detected from the content, so a
HEIC file with a `.jpg` name is still read as HEIC. AVIF is not supported.

HEIC files are inspected from their container, without FFmpeg. The result
describes the primary image: its stored size, its displayed size, and the
HEIF rotation and mirroring in `heif_transforms`. Those transforms decide
the displayed orientation. The EXIF orientation of a HEIC file is listed
but not applied again. `transparent_pixels` is omitted for HEIC because
the pixels are not decoded; `alpha_channel` reports an alpha plane.

For images, `--json` includes an `exif` section with readable tags grouped
by directory, including GPS, camera, lens, and exposure data where present.
Each tag includes its numeric ID, known name, type, count, and value.
Maker notes are reported as hex when available, with raw values larger
than 64 KiB omitted with a note. Images without EXIF report `exif: null`.
JSON goes to stdout. Diagnostics and tool download progress go to stderr.

## Resize images

```sh
musafoz thumb photo.png --size 320x240
musafoz thumb photo.jpg --size 400x400 --mode fill --format png --output ./thumbs
musafoz thumb "https://example.com/images/photo.jpg" --size 320x240
```

`--size WIDTHxHEIGHT` is required. The default mode, `fit`, preserves
proportions and fits inside the requested dimensions without adding
borders. `fill` keeps proportions and crops from the center to produce
the exact requested dimensions. Both modes can upscale the source.

MusaFoz applies EXIF orientation before resizing. Thumbnails contain
pixels only, so EXIF, GPS, camera data, and color profiles are omitted.

HEIC photos, including from URLs and lists, are decoded with FFmpeg, which
is resolved as for video commands. FFmpeg builds the primary image,
including the tile grids that phones write, and applies the HEIF rotation
and mirroring. MusaFoz checks the decoded size against the container and
does not apply the EXIF orientation again, so photos are not rotated
twice. An FFmpeg older than 7.1, or one without the HEVC decoder, fails
with an error that names the limitation. The source file is not changed.
JPEG is the default and uses a white background for transparent areas.
Use `--format png` to keep transparency.

## Extract video frames and covers

```sh
musafoz image clip.mp4 00:01:00 00:02:13.500
musafoz image clip.mp4 00:01:00 --size 320x240 --mode fill
musafoz image clip.mp4 --random 10 --output ./frames
musafoz cover clip.mp4 00:03:55
```

`image` accepts a local video or direct HTTP(S) URL. It extracts clean
frames at the displayed resolution unless you pass `--size`. With a size,
the same `fit` and `fill` modes apply. Times use `HH:MM:SS` with optional
fractional seconds, and each image is the first frame at or after its
requested time. Omit the times for one random draw, or use `--random N`
for several. Random times are independent and can select the same frame.

`cover` generates only `<name>_cover.jpg` beside a local video. Omit the
time to use the same random selection as `preview`: 15% to 85% of the
duration for videos longer than ten seconds, and any valid time for
shorter clips. Pass `--overwrite` to replace an existing cover.

### Output options for thumb and image

| Option | Behavior |
| --- | --- |
| `--output DIR` | Save images in a directory. The default is the current directory. |
| `--alongside` | Save beside a local source. Use it in place of `--output`. |
| `--format png` | Write PNG. The default is JPEG. |
| `--prefix TEXT` or `--suffix TEXT` | Customize the output name. Use one per run. |
| `--overwrite` | Allow replacement of existing outputs. The original source remains protected. |

`thumb` uses `_thumb` by default, as in `photo_thumb.jpg`. `image` includes
the time, as in `clip_00-02-13-500.jpg`. Random draws that collide within a
run get an index such as `-2`. Explicit timecodes that produce the same name
are reported as a conflict. For URLs, the source name comes from the last
path segment without query parameters.

### Process a list

```sh
musafoz thumb batch sources.txt --size 320x240 --output ./thumbs
musafoz image batch frames.tsv --output ./frames
```

Put one local image path or direct URL per line in `sources.txt`:

```text
photos/beach day.jpg
https://example.com/images/logo.png
```

Put one video source per line in `frames.tsv`. Add a literal tab followed
by a timecode to select a frame, or omit the timecode for a random draw:

```tsv
videos/holiday clip.mp4	00:01:00
videos/holiday clip.mp4	00:02:13.500
videos/holiday clip.mp4
https://example.com/media/talk.mp4	00:10:00
```

Relative paths are resolved from the list directory. Blank lines are
skipped. The command continues after a failed item, reports the line
number and reason on stderr, and exits with an error if any item failed.
Successfully written paths go to stdout.

Direct HTTP(S) downloads are temporary. A repeated URL in a batch uses the
same downloaded file. Image downloads are limited to 100 MiB and two
minutes. Video downloads are limited to 8 GiB and 60 minutes.

## Generate a video preview

```sh
musafoz preview clip.mp4
musafoz preview clip.mp4 --frames 25 --cover-at 00:00:01.250 --json
```

The preview writes three files beside a local video:

- `clip_cover.jpg` is a clean frame at the video's displayed resolution.
  Use `--cover-at HH:MM:SS.fff` to select it or omit the flag for a random
  cover. Times must be between zero and the start of the last frame.
- `clip_preview.mp4` is an H.264 preview at 4 fps with timestamped frames.
- `clip_preview.jpg` is a timestamped mosaic that follows the video's
  displayed aspect ratio. A 25-frame preview fills a 5x5 grid. The default
  100 frames fill a 10x10 grid.

Use `--frames` to select 1 to 100 frames, `--overwrite` to replace
existing outputs, and `--font` to override the bundled DejaVu Sans Mono
font. The CLI reports progress for long operations in interactive
terminals.

The source stays intact by default. `--faststart-in-place` explicitly
requests a remux of a compatible MP4 or MOV. MusaFoz verifies the remux
before replacing the source and keeps the original if processing fails.

On macOS, `--accel auto` probes VideoToolbox decoding and encoding
separately and falls back to CPU for an unavailable or failing hardware
stage. Use `--accel cpu` to force software decoding and libx264. Linux and
Windows use CPU for previews.

## FFmpeg and platform support

Video commands use FFmpeg and FFprobe as needed. Image inspection runs in
Go. Thumbnails run in Go, except for HEIC sources, which FFmpeg decodes.
HEIC thumbnails have been verified with the managed macOS Intel build
(FFmpeg 9.1) and with the managed Linux build (FFmpeg n9.0.2) in a Debian
container. The Windows build compiles, but HEIC decoding has not yet been
run on Windows.

| Platform | Tool resolution |
| --- | --- |
| macOS Intel | Managed Evermeet build by default. |
| Apple Silicon | Provide local FFmpeg and FFprobe paths. |
| Linux x86-64 | Use compatible tools on PATH or download the pinned BtbN build. The managed build needs glibc 2.28 or newer. |
| Windows x86-64 | Use compatible tools on PATH or download the pinned BtbN build. |

Use `--ffmpeg` and `--ffprobe` to supply paths for one invocation, or set
`MUSAFOZ_FFMPEG` and `MUSAFOZ_FFPROBE` in the environment. Explicit paths
take precedence. Local release builds of FFmpeg and FFprobe must be version
6 or newer, and FFmpeg must supply the features needed by the command.
Managed downloads are verified and cached. Set `MUSAFOZ_CACHE_DIR` to
choose the cache location.

## Development and licenses

Run `go test ./...` and `go vet ./...` from the repository root. For native
platform tests and managed download checks, see the
[validation guide](docs/validation.md). Technical decisions and their
tradeoffs are recorded in [docs/decisions](docs/decisions).

MusaFoz is [MIT licensed](LICENSE). The bundled DejaVu font has its own
[license notice](internal/media/fonts/LICENSE), also available through
`musafoz licenses`. The managed FFmpeg and FFprobe builds are downloaded
separately and have their own licenses.
