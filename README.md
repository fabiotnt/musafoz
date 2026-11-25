# MusaFoz

MusaFoz is a command-line toolkit for inspecting media, generating video
previews, and preparing photos and videos for the web. Extract covers and
individual frames, resize photos including HEIC, or process a list of
files and URLs with a report for each item.

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
| Make square thumbnails with a report | `musafoz thumb batch sources.txt --size 480x480 --mode fill --quality 80 --output ./thumbs --jsonl > thumbs.jsonl` |
| Make web copies of a library | `musafoz web batch sources.txt --output ./web --metadata ./private --skip-existing --jsonl` |

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
Colors are converted to sRGB first when the image has an ICC profile with
RGB colorants and tone curves, such as the Display P3 profile of phone
photos, or a HEIF color description. Without this, wide gamut photos would
look dull once their profile is gone. Profiles made only of lookup tables
are kept as stored, and a message says so.

HEIC photos, including from URLs and lists, are decoded with FFmpeg, which
is resolved as for video commands. FFmpeg decodes only the HEVC pictures.
MusaFoz reads the rest of the container itself. It places the tiles of the
grids that phones write, crops them to the exact size, including odd
widths, adds an alpha plane when the photo has one, and applies the HEIF
rotation and mirroring in the order the file lists them. The EXIF
orientation of a HEIC file repeats that rotation and is not applied again,
so photos are not rotated twice. An FFmpeg older than 7.1, or one without
the HEVC decoder, fails with an error that names the limitation. The
source file is not changed.
JPEG is the default and uses a white background for transparent areas.
Use `--format png` to keep transparency. JPEG thumbnails use quality 92.
Pass `--quality N`, from 1 to 100, to trade detail for smaller files, as
in `--quality 80`. The option is for `thumb` and `thumb batch` with JPEG
output only.

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
| `--quality N` | `thumb` only: JPEG quality from 1 to 100. The default is 92. |
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

### Thumbnail report

```sh
musafoz thumb batch sources.txt --size 480x480 --mode fill --quality 80 \
  --output ./thumbs --jsonl > report.jsonl
```

With `--jsonl`, `thumb batch` writes one JSON object per list line to
stdout in place of the paths, in list order. Errors are still reported on
stderr, and the command still exits with an error when any line failed.
Each record has the list line number, the source, and the status:

```json
{"line":1,"source":"General/beach day.jpg","status":"ok","output":"/data/thumbs/beach day_thumb.jpg","source_width":3024,"source_height":4032,"output_width":480,"output_height":480,"date":"2025-01-19T10:15:32-03:00","date_source":"DateTimeOriginal"}
{"line":2,"source":"https://example.com/images/logo.png","status":"error","error":"download https://example.com/images/logo.png: server answered 404 Not Found"}
```

- `source` is the line as written. URLs omit credentials, query
  parameters, and fragments.
- `source_width` and `source_height` are the displayed size of the
  source, after its EXIF orientation or HEIF rotation.
  `output_width` and `output_height` are the size of the thumbnail.
- `date` comes from the EXIF `DateTimeOriginal`. When that tag is missing
  or invalid, the XMP `exif:DateTimeOriginal`, `photoshop:DateCreated`, or
  `xmp:CreateDate` is used, as in `"date_source":"XMP
  photoshop:DateCreated"`. Phones keep the date of screenshots there.
  Last comes the EXIF `DateTime`, the time the file was last changed,
  often by an editing app. The fraction of a second and the UTC offset
  are added when the file has them. A date without an offset has an
  unknown time zone. It is not UTC. Both fields are `null` when the image
  has no usable date.
- The report reads the same file as the thumbnail, so a URL is downloaded
  once. It has no GPS, camera, or other EXIF data. Use `inspect --json`
  for the full metadata of one image.

Direct HTTP(S) downloads are temporary. A repeated URL in a batch uses the
same downloaded file. Image downloads are limited to 100 MiB and two
minutes. Video downloads are limited to 8 GiB and 60 minutes.

## Make web copies

```sh
musafoz web IMG_0001.HEIC --output ./single --metadata ./single
musafoz web batch sources.txt --output ./web --metadata ./private \
  --skip-existing --jsonl > web-report.jsonl
```

`web` creates browser-ready copies of photos and videos, with optional
metadata records for import. Set `--output` to the destination directory.
For a batch, put one local path or direct HTTP(S) URL per line. Relative
paths are resolved from the list directory. A UUID derived from the
SHA-256 of the original names each set of outputs. If the same content
appears again in the batch, it is reported as `duplicate` with the same
UUID.

| Source | Outputs |
| --- | --- |
| JPEG, PNG, GIF, WebP, BMP, TIFF, HEIC | `<uuid>.jpg` at most 2560 pixels on the long side, and `<uuid>_thumb.jpg` 480 pixels on the short side |
| Video | `<uuid>.mp4`, plus `<uuid>_cover.jpg`, `<uuid>_preview.mp4`, and `<uuid>_preview.jpg` as made by `preview` |

- Images keep their proportions without being enlarged. MusaFoz applies
  orientation and converts supported color profiles to sRGB before writing
  JPEGs without source metadata. Use `--image-size` and `--thumb-size`
  to set dimensions, and `--quality` and `--thumb-quality` to set JPEG
  quality. The default quality is 85 for images and 80 for thumbnails.
- Videos use H.264 High in MP4 with faststart and stereo AAC when audio is
  present. The defaults are up to 1080p in either orientation and a 5 Mbps
  video bitrate cap (`--video-size` and `--video-bitrate`). HDR video
  (HLG or PQ) is tone mapped to SDR using the FFmpeg `zscale` and
  `tonemap` filters. Source container tags are removed. Use `--frames`
  to set the preview frame count or `--no-preview` to skip all three
  preview outputs.
- `--metadata DIR` writes a private `<uuid>.json` record with source
  metadata, generation settings, and the name and SHA-256 of each output.
  It can contain EXIF with GPS, XMP, color information, or raw FFprobe
  metadata. The directory can be the same as `--output`. If you upload
  both to one location, keep objects private by default and grant public
  read access only to an explicit list of media objects.

For square 480x480 thumbnails with a report, without the import outputs,
use `musafoz thumb batch sources.txt --size 480x480 --mode fill --quality
80 --jsonl`.

### Interrupted runs

MusaFoz generates each item's files in `.musafoz-staging/` inside the
output directory. A pending file, `.<uuid>.musafoz-pending.json`, records
the name and SHA-256 of every output before they are placed. When
`--metadata` is set, MusaFoz writes the metadata record last to mark
completion.

Use `--skip-existing` from the first run to reuse completed work and
recover from interruptions. It requires `--metadata`. Repeat the
command with the same output and metadata directories and settings.
Write each attempt's JSONL to its own file if you need to keep the
history.

The result depends on what is already on disk:

| State found | Result |
| --- | --- |
| Record with the same settings, every output in `--output` with its recorded SHA-256 | Reused, status `exists`, media unchanged |
| Record with the same settings, but an expected output is missing in `--output` | Made again, status `resumed` |
| Pending file, no record (stopped while placing outputs) | Made again, status `resumed` |
| Record made with other settings | Error naming the settings, such as `image_quality 85 -> 70`; `--overwrite` makes it again |
| An existing output with an unrecognized SHA-256 | Conflict; the file is kept |

During recovery, MusaFoz only replaces files verified against a recorded
checksum or the newly generated output. MusaFoz compares settings by
media type. Reusing an item still requires reading the entire original
to compute its SHA-256 and UUID. The `timings` field reports hashing,
conversion, and output verification separately.

Without `--skip-existing` or `--overwrite`, an existing record, an
unfinished content, or an existing output is an error, and nothing is
replaced. Processes that share an output directory never work on the same
UUID at once. This coordination applies to local runs that share an
output directory. See [the pilot guide](docs/web-pilot.md) for the Linux
container results and the pending Cloud Run upload and access checks.

### Report

With `--jsonl`, `web batch` writes one record per list line: `line`,
`source`, `status` (`ok`, `resumed`, `duplicate` for content already
converted in this run, `exists` with `--skip-existing`, or `error`), and
the result: `kind`, `uuid`, `sha256`, `bytes`, `format`, `files`, the
source and output sizes, `duration_seconds` for videos, `date` and
`date_source`, `color_converted_from`, `hdr_tone_mapped`, `screenshot` when
the XMP marks a screenshot, `metadata`, and `timings`. Video dates come
from the QuickTime creation date that phones write, with its UTC offset,
or else from `creation_time` in UTC.

The report omits GPS and camera fields and strips credentials, query
parameters, and fragments from source URLs. Keep it private because it
identifies the sources. The batch continues after a failed item and exits
with a nonzero status if any item failed.

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
