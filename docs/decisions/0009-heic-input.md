# 0009 HEIC input for inspect and thumb

Status: accepted; Linux verified in a container, Windows and native Linux
pending
Date: 2026-11-25

## Context

0008 recognized HEIF/HEIC files and refused them. Phones save photos as
HEIC by default, so `inspect`, `thumb`, and `thumb batch` should read them
directly. HEIC and AVIF encoding, auxiliary images (depth maps, alpha
planes as images of their own, and so on), and image sequences are out of
scope.

## Options considered

- The FFmpeg builds that musafoz already manages. FFmpeg 7.1 added HEIF
  demuxing with tile grids and the irot and imir properties. The pinned
  builds are 9.1 on macOS (evermeet.cx) and n9.0.2 on Linux and Windows
  (BtbN), both with the HEVC decoder. No new dependency, no change to the
  builds.
- libheif with libde265 through cgo. It reads every HEIF feature, but
  musafoz would need a C toolchain and per-platform libraries, and could no
  longer be cross-compiled with CGO_ENABLED=0 as it is today. Not taken.
- A pure Go HEVC decoder. None is mature enough. Not taken.

## Decision

### Container in Go, pixels in FFmpeg

- internal/imaging/heif.go reads the HEIF boxes in Go: the primary item
  (pitm), the item types (iinf), the item locations (iloc, from the file
  or from idat), the properties and their associations (iprp, ipco, ipma),
  and the references (iref). It returns the primary item type, its stored
  size (ispe), its irot and imir transforms in their listed order, whether
  an alpha plane (auxl with an alpha auxC type) belongs to it, and the
  EXIF item that describes it (cdsc).
- The EXIF item holds a 4-byte offset to the TIFF header, then the block.
  The block goes to the existing EXIF reader, so tags, names, values, and
  the maker note handling are the same as for JPEG. FFprobe was not used
  for EXIF: it reports the EXIF block only as side data of a given size.
- `inspect` uses only this reader, so inspecting a HEIC file needs no
  FFmpeg or FFprobe and downloads nothing. The pixels are not decoded, so
  `transparent_pixels` is left out of the JSON for HEIC. New fields for
  HEIC only: `heif_transforms`, a list of `{"rotation_ccw": 270}` or
  `{"mirror": "top-bottom", "mirror_axis": 0}`. The JSON of videos and of
  the other image formats is unchanged.
- `thumb` decodes HEIC with FFmpeg (media.DecodeImage):
  `ffmpeg -i file:<src> -frames:v 1 -update 1 -pix_fmt rgb24 -c:v png`, or
  rgba when the container declares an alpha plane. FFmpeg is resolved like
  for the video commands, so the managed build may be downloaded on first
  use. The PNG then goes through the same fit, fill, naming, output, and
  overwrite rules as other images, and the output carries no metadata.

### Orientation

The HEIF specification makes irot and imir the transforms that give the
displayed image; the EXIF orientation of a HEIF file is informative.
iPhone files carry both, with the same meaning: irot 270 (counterclockwise)
and EXIF orientation 6 (rotate 90 degrees clockwise).

Measured with the managed FFmpeg 9.1 on synthetic files whose picture has
four colored quadrants:

- FFmpeg picks the primary image by default: the tile grid of a grid file,
  and not a thumbnail item.
- FFmpeg applies irot while it decodes, also with -noautorotate and also
  for tile grids.
- FFmpeg does not apply the EXIF orientation. A file with EXIF orientation
  6 and no irot decodes unrotated, and a file with both rotates once.
- imir axis 0 flips top and bottom and axis 1 flips left and right, as in
  libheif. musafoz labels them "top-bottom" and "left-right".
- FFmpeg applies the rotation before the mirroring, whatever their order
  in the file. When a file lists imir before irot, musafoz corrects the
  decoded pixels so the result follows the listed order.

musafoz therefore never applies the EXIF orientation to HEIC pixels.
After decoding it checks that the image has the size the container
predicts after its rotation. A build that decodes a single tile or ignores
irot fails with an error that names the expected and decoded sizes and
says that FFmpeg 7.1 or newer is needed, instead of writing a wrong
thumbnail. An FFmpeg without the HEVC decoder fails with a message that
names the same requirement.

### Limitations

- AVIF stays unsupported, and only hvc1 and grid primary items are
  decoded.
- Alpha planes are requested as RGBA but not tested: phones do not write
  them, and FFmpeg may not merge them into the grid image.
- Clean aperture (clap) cropping is not read; the stored and displayed
  sizes come from ispe and irot.
- Color profiles are not carried over, as for other formats (0008).
- 10-bit HEIC is reduced to 8 bits per channel.

## Validation

macOS Intel, managed FFmpeg 9.1, 2026-11-25:

- Synthetic HEIC files built in tests from a 778-byte HEVC still
  (testdata/image/quadrants-64x48.hevc, generated with libx265 from four
  solid colors) and testimages.HEIF: a single image, a 2x1 tile grid, a
  thumbnail item, irot of 90, 180, and 270 degrees, imir axes 0 and 1,
  rotation before and after mirroring, and EXIF orientation 6 with and
  without irot. Unit tests check the container reader, the EXIF block,
  the HEIF JSON, truncated files, and the size check with a fake FFmpeg.
  The integration test decodes every case with the managed FFmpeg and
  checks the quadrant colors of the thumbnails, fit at 240x320 and fill at
  300x300 of the grid, JPEG and PNG without EXIF, a URL served by a local
  test server, and a list with a HEIC file named .jpg, a HEIC URL, and a
  missing file.
- A real iPhone HEIC from the owner, on a temporary copy: `inspect --json`
  read 59 EXIF tags from the HEIC itself, all named and none with errors,
  with camera, lens, aperture, exposure, ISO, focal length, date,
  orientation 6, maker note, and GPS position present, 4032x3024 stored,
  3024x4032 displayed, and irot 270. The same tags as in a copy converted
  with `sips` had the same values, apart from directory offsets and a GPS
  time that `sips` rewrote. Thumbnails at 240x320 fit and 300x300 fill,
  in JPEG and PNG, had no EXIF and no camera or GPS strings, and differed
  by 0.7 levels on average from the thumbnails of the verified `sips`
  JPEG. The same file served from a local HTTP server gave the same
  thumbnail, the query was not shown, and the downloads were removed. The
  original file did not change.

Linux x86-64 in Docker, 2026-11-25: a debian:13-slim container (glibc
2.41) on the Intel Mac, with the network disabled, the linux/amd64 musafoz
binary built with CGO_ENABLED=0, and the managed BtbN n9.0.2 FFmpeg from
an existing cache. No FFmpeg was on the PATH. This is a real Linux kernel
and userland, but not a separate machine.

- All ten synthetic cases above gave the expected size and quadrant colors,
  the same as on macOS, so FFmpeg n9.0.2 picks the primary image, applies
  irot, ignores the EXIF orientation, and orders and reads imir in the same
  way as 9.1.
- On the real HEIC copy, `inspect --json` gave the same JSON as on macOS
  apart from the path. The 240x320 fit and 300x300 fill PNG thumbnails were
  identical to the macOS ones (mean difference 0.0), and the JPEG and PNG
  outputs had no EXIF and no camera or GPS strings. The copy and the
  original did not change.

Not verified:

- Windows. The musafoz binary and test binaries cross-compile for
  windows/amd64 and go vet passes, but the BtbN n9.0.2 Windows FFmpeg was
  not run on HEIC files.
- Linux on a separate physical or virtual machine.
- FFmpeg found on the PATH. Builds older than 7.1 are expected to fail the
  size check with the error above.
