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

On Intel macOS (darwin/amd64), the first inspection with the managed build
downloads and verifies FFprobe. Subsequent runs can reuse the cache offline.
Set `MUSAFOZ_CACHE_DIR` to choose a different cache location.

To use an existing FFprobe executable, including on platforms without a
managed build, pass its path explicitly:

```sh
./musafoz inspect ./clip.mp4 --ffprobe /path/to/ffprobe
```

musafoz checks `--ffprobe` first, then `MUSAFOZ_FFPROBE`, then the managed
cache. It does not search `PATH`. With `--json`, the result goes to standard
output and warnings and download progress go to standard error.

## Generate a preview

```sh
./musafoz preview ./clip.mp4
./musafoz preview ./clip.mp4 --frames 50 --overwrite
./musafoz preview ./clip.mp4 --faststart-in-place --json
```

The command writes three files beside the video:

- `clip_cover.jpg`: a clean frame at source resolution, taken at a random
  point between 15 and 85 percent of the duration, or at the midpoint for
  videos of 10 seconds or less.
- `clip_preview.mp4`: the frames with a timestamp overlay, as H.264 at 4 fps
  in yuv420p.
- `clip_preview.jpg`: the frames in a 10 by 10 grid with a 40-pixel header
  showing the file name. The grid has the aspect ratio of the video and is
  the largest that fits in 4K in the video's orientation, whatever the
  source resolution: 3840x2160 for 16:9, 2160x3840 for 9:16, 2880x2160 for
  4:3, 2160x2880 for 3:4, and 2160x2160 for 1:1, plus the header.

The timestamp overlay is sized from the mosaic cell, so it has the same size
in every mosaic, for any resolution and orientation. It matches the
reference size (64 pixels) for 1080p 16:9 sources.

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

The preview command uses managed FFmpeg and FFprobe builds, downloaded and
verified on first use. Use `--ffmpeg` or `MUSAFOZ_FFMPEG` to choose your own
FFmpeg, and `--font` to choose the overlay font. By default it uses Andale
Mono, or DejaVu Sans Mono on Linux.

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

On other platforms both steps run on the CPU. `--accel cpu` forces
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
cache runs only when asked for, because the vendor rate-limits repeated
downloads:

```sh
MUSAFOZ_TEST_DOWNLOAD=1 go test -tags integration -count=1 -run IntegrationDownload ./internal/cli/
```

See [docs/decisions](docs/decisions) for platform support, version
requirements, verification details, and output safety rules.
