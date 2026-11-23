# musafoz

musafoz is a command-line toolkit for working with local media. It inspects
video files and generates a cover image, an MP4 preview, and a mosaic for a
single video.

## Build

Use Go 1.27.3 or newer. From the repository root:

```sh
go build -o musafoz ./cmd/musafoz
```

## Inspect a video

```sh
./musafoz inspect ./clip.mp4
./musafoz inspect ./clip.mp4 --json
```

With `--json`, the result goes to standard output; the tool choice,
warnings, and download progress go to standard error.

## Platforms and FFmpeg

musafoz needs FFmpeg and FFprobe. You do not have to install them: where
no suitable copy is available, musafoz downloads a pinned build on first
use, verifies it, and keeps it in a cache for later runs, which then work
offline.

| Platform | Managed build | Installed tools on the PATH |
| --- | --- | --- |
| macOS Intel (darwin/amd64) | evermeet.cx FFmpeg 9.1 | not searched |
| Linux x86-64 (linux/amd64) | BtbN FFmpeg n9.0.2, GPL, glibc 2.28 or newer | used when compatible |
| Windows x86-64 (windows/amd64) | BtbN FFmpeg n9.0.2, GPL | used when compatible |

For each tool, musafoz uses the first of:

1. `--ffprobe` or `--ffmpeg`.
2. `MUSAFOZ_FFPROBE` or `MUSAFOZ_FFMPEG`.
3. On Linux and Windows, `ffprobe` or `ffmpeg` found on the PATH, if it is
   version 6 or newer and, for FFmpeg, offers the encoders and filters the
   preview needs. Otherwise musafoz says why it was skipped.
4. The managed build, downloaded into the cache if needed.

A path given with a flag or variable must work: if it is missing, not an
executable (an `.exe` file on Windows), or too old, musafoz stops and names
the flag or variable. Standard error always reports which executable was
chosen and why, for example:

```text
using ffmpeg /usr/bin/ffmpeg (found on the PATH: ffmpeg version 7.1.5-0+deb13u1 ...)
using ffprobe /home/me/.cache/musafoz/tools/ffprobe/n9.0.2-39-g7adfbe40dc-linux-amd64/ffprobe (managed n9.0.2-39-g7adfbe40dc build for linux/amd64; no ffprobe on the PATH)
```

The cache lives in the user cache directory (`~/Library/Caches/musafoz` on
macOS, `~/.cache/musafoz` on Linux, `%LocalAppData%\musafoz` on Windows).
Set `MUSAFOZ_CACHE_DIR` to move it. The Linux and Windows builds share one
archive for both tools, about 146 MiB and 186 MiB to download and 331 MiB
and 316 MiB once installed. Every cached executable is checked against its
pinned size and SHA-256 before each use, and replaced if it does not match.
On other platforms, such as Linux on ARM, use tools on the PATH or pass
their paths explicitly.

## Generate a preview

```sh
./musafoz preview ./clip.mp4
./musafoz preview ./clip.mp4 --frames 50 --overwrite
./musafoz preview ./clip.mp4 --cover-at 00:01:15.500
./musafoz preview ./clip.mp4 --faststart-in-place --json
```

The command writes three files beside the video:

- `clip_cover.jpg`: a clean frame at source resolution, taken at the time
  given with `--cover-at`, or else at a random time: between 15 and 85
  percent of the duration for videos longer than 10 seconds, anywhere up to
  the last frame for shorter ones.
- `clip_preview.mp4`: the frames with a timestamp overlay, as H.264 at 4 fps
  in yuv420p.
- `clip_preview.jpg`: the frames in a grid with a 40-pixel header showing
  the file name. The grid has `ceil(sqrt(frames))` columns and as many rows
  as needed: 25 frames give 5x5, 98 give 10x10 with two empty black cells,
  and 100 give 10x10. The grid area has the aspect ratio of the video and is
  the largest that fits in 4K in the video's orientation, whatever the
  source resolution or frame count: 3840x2160 for 16:9, 2160x3840 for 9:16,
  2880x2160 for 4:3, 2160x2880 for 3:4, and 2160x2160 for 1:1, plus the
  header. Smaller grids get larger frames. Frames keep their aspect ratio
  and are centered in their cells, with black bars when a grid has more
  columns than rows.

`--cover-at` takes `HH:MM:SS` with an optional fraction of a second, such
as `00:01:15.500`, from `00:00:00` up to the start of the last frame. A
malformed, negative, or later time stops the command before processing.
The cover is the first frame at or after that time, and `cover_at_seconds`
in the JSON result reports the time used, given or random.

The timestamp overlay has the same size relative to the frame for every
source resolution, orientation, and frame count, so the preview video looks
the same whatever `--frames` is. It matches the reference size (64 pixels)
for 1080p 16:9 sources. In a 10 by 10 mosaic it is about 13 pixels high;
smaller grids show it larger, up to about 128 pixels with one frame.

All three keep the video's displayed orientation and aspect ratio. Rotation
metadata, such as that of phone videos recorded upright, and non-square
pixels are applied, so a 4:3 video with a 90 degree rotation gives 3:4
outputs.

`--frames` sets the number of evenly spaced frames, from 1 to 100 (default
100). The video must be readable, contain a real video track, and report a
positive duration.

If any output already exists, the command stops before processing and lists
the conflicts. With `--overwrite`, the new outputs are generated first and
then replace the earlier ones; if publishing fails, the earlier outputs are
restored. Runs that write the same outputs, including `clip.mp4` and
`clip.mov` in one directory, wait for each other.

The original video is never modified unless you pass `--faststart-in-place`.
Then MP4 and MOV files are remuxed without re-encoding so the index comes
first, and the original is replaced only after all three outputs are
published. If the remux or any later step fails, the original is kept.

The preview command finds FFmpeg and FFprobe as described in
[Platforms and FFmpeg](#platforms-and-ffmpeg).

The timestamps and the mosaic header use DejaVu Sans Mono 2.37, which is
built into musafoz, so the outputs look the same on every machine and no
font has to be installed. To use another font, pass a TrueType or OpenType
file:

```sh
./musafoz preview ./clip.mp4 --font /path/to/font.ttf
```

If the file is missing or is not a font, the command stops before
processing and names the file. The log names the font used.

On a terminal, steps that take a while without printing a log line, such
as frame extraction, the cover, the preview video encoding, the mosaic, and
the faststart remux, show a progress line on standard error with the work
done and the elapsed time. When standard error is not a terminal, only the
log lines are printed.

### Hardware acceleration

On macOS, `--accel auto` (the default) chooses decoding and encoding
separately:

- Frames are decoded with VideoToolbox when FFmpeg lists it and a short
  test decode of the input succeeds with hardware frames. Otherwise they
  are decoded on the CPU.
- The preview video is encoded with h264_videotoolbox when FFmpeg lists it
  and a test encode at the preview size succeeds. Otherwise it is encoded
  with libx264. The hardware encoder rejects some small sizes, such as
  320x180 on an Intel Mac, so small sources may use libx264.

On Linux and Windows both steps run on the CPU. NVIDIA and AMD acceleration
is not implemented yet. `--accel cpu` forces
software decoding and libx264, for diagnosis and comparison:

```sh
./musafoz preview ./clip.mp4 --accel cpu
```

The log on standard error names the path used for each step, the reason
for the choice, and the time each step took. If a hardware step fails, it
is retried once on the CPU and the log says why; a failed hardware encode
is retried with libx264 from the frames already extracted. Cancellations
and errors caused by the input itself are reported as usual. The JSON
result reports the backends that produced the outputs, after any
fallback, in `frame_decoder` and `preview_encoder` (`cpu` or
`videotoolbox`).

Hardware encoding can change visual quality and file size. The
[hardware acceleration decision](docs/decisions/0004-hardware-acceleration.md)
records measurements for one reference clip. Use `--accel cpu` to select
libx264 explicitly.

## Development

Run `go test ./...` from the repository root. It needs no network access.

Tests that run the managed FFmpeg and FFprobe on real files are separate:

```sh
go test -tags integration -count=1 ./internal/cli/
```

They use the regular musafoz cache, or the directory in
`MUSAFOZ_TEST_CACHE_DIR`, so the builds are downloaded at most once per
machine and verified before every use. The test that downloads into an empty
cache runs only when asked for, because vendors may rate-limit repeated
downloads:

```sh
MUSAFOZ_TEST_DOWNLOAD=1 go test -tags integration -count=1 -run IntegrationDownload ./internal/cli/
```

Most unit tests use shell scripts as fake FFmpeg and FFprobe and are
skipped on Windows. [docs/validation.md](docs/validation.md) describes the
end-to-end checks for Linux and Windows.

## Third-party components

musafoz embeds the DejaVu Sans Mono 2.37 font. Its license, as published
with the font, is in
[internal/media/fonts/LICENSE](internal/media/fonts/LICENSE), and
`musafoz licenses` prints it from the binary. Release archives must include
that file next to the binary. The FFmpeg and FFprobe builds are downloaded
at run time and are not part of musafoz; see the decision records for their
sources and licenses.

See [docs/decisions](docs/decisions) for platform support, version
requirements, verification details, output safety rules, and the cover and
mosaic grid rules.
