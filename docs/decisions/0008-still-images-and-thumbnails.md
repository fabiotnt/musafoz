# 0008 Still images, thumbnails, and image inspection

Status: accepted
Date: 2026-11-24

## Context

The MVP specification lists standalone still images as upcoming work: a
cover without the rest of the preview, clean frames at chosen or random
times, and one extraction shared by these and the preview cover. The owner
also asked for image thumbnails, lists of sources, URL sources, and image
inspection with the full EXIF data.

## Decision

### Commands

- `thumb <image>` and `thumb batch <list.txt>` write resized copies of
  images.
- `image <video> [time...]`, `image <video> --random N`, and
  `image batch <list.tsv>` write clean frames of videos.
- `cover <video> [time]` writes only `<name>_cover.jpg`.
- `inspect` also accepts images, local or by URL.

The operations live in internal/stills, internal/imaging, internal/fetch,
and internal/preview (cover). The CLI only parses flags and prints results.

### Shared extraction, two encodings

- One FFmpeg invocation pattern extracts every clean frame: seek with -ss
  before the input, the square-pixel filter of 0003, rotation applied by
  FFmpeg, one frame. Covers and extracted images share it, together with
  the time checks (media.CheckFrameTime) and, for covers, the time choice
  (media.CoverTime).
- Covers, from `preview` and from `cover`, are encoded by FFmpeg as JPEG
  with -q:v 2 (media.ExtractCover), exactly as before this change and as
  in the reference workflow.
- Extracted images ask FFmpeg for a lossless 8-bit RGB PNG
  (media.ExtractStill) and then go through imaging.Render, which
  optionally resizes and encodes JPEG (quality 92) or PNG with the Go
  encoders. Thumbnails use the same Render step.
- A first version of this change sent covers through imaging.Render too.
  On two of the owner's videos, its JPEGs were 24 and 42 percent larger
  than FFmpeg's at a similar SSIM against the lossless frame (0.9767
  against 0.9834, and 0.9914 against 0.9878), so covers went back to the
  FFmpeg encoder.

### Images in Go

- Images are decoded in Go: JPEG, PNG, and GIF from the standard library,
  WebP, BMP, and TIFF from golang.org/x/image, which also provides the
  Catmull-Rom resampler. No FFmpeg is needed for thumbnails or image
  inspection. Images above 200 megapixels are refused.
- The format is detected from the content, not the extension. AVIF is
  recognized and refused with a clear message. HEIC was refused at first;
  0009 adds it, with the container read in Go and the pixels decoded by
  FFmpeg. GIF and WebP animations give their first frame.
- The EXIF orientation is applied to the pixels before any resize or crop.
  The encoders write pixel data only, so outputs carry no EXIF, GPS,
  device, or lens data. Color profiles are not carried over either; wide
  gamut photos are written as if they were sRGB.
- JPEG output composites transparent pixels over white. PNG output keeps
  transparency.
- `--mode fit` scales to fit inside the size without cropping or borders;
  `--mode fill` crops the centered part with the target aspect ratio and
  scales it to the exact size. Both scale up as well as down, so outputs
  have predictable sizes.

### EXIF

musafoz has its own EXIF reader (internal/imaging/exif.go) instead of a
third-party library. It reads the TIFF structure from JPEG APP1, PNG eXIf,
WebP EXIF chunks, and TIFF files, follows IFD0, the Exif, GPS, and
Interoperability directories, and IFD1. Every entry is kept with its
numeric id, its name when known (TIFF 6.0, EXIF 2.32, and common
extensions), its type, count, and value. Values outside the block, loops
between directories, and unknown types are reported per tag or as
warnings; the reader never fails on damaged data after a valid header.

Limits:

- Maker notes are proprietary and differ by maker and model. They are
  given as raw bytes in hex and not decoded. Values larger than 64 KiB are
  left out with a note.
- Text in UserComment, GPSProcessingMethod, and GPSAreaInformation is
  decoded for the ASCII and UNICODE codes; the JIS code is shown as is.
- XMP packets embedded in IFD0 are shown as text. XMP in a JPEG APP1
  segment of its own, and IPTC data outside EXIF are not read. The EXIF
  item of HEIC files is read since 0009.

### Output names and conflicts

- `thumb` names outputs `<name>_thumb.<ext>`; `--suffix` replaces `_thumb`
  and `--prefix` adds text before the name instead. `image` names outputs
  `<name>_<time><suffix>.<ext>` or `<prefix><name>_<time>.<ext>`, with the
  time as HH-MM-SS or HH-MM-SS-mmm, because colons are not allowed in file
  names on Windows. For URLs, the name is the unescaped last segment of the
  path, without the query, with characters that are invalid in file names
  replaced by underscores.
- Outputs go to the current directory, to `--output` (created when
  missing), or beside the local source with `--alongside`.
- As in 0003, existing files are conflicts unless `--overwrite` is given.
  A command with several outputs checks them all before extracting the
  first frame. Within one run, two items that would write the same file
  are a conflict for the later item; names are compared without case,
  as macOS and Windows do. A thumbnail that would replace its own source
  is refused even with `--overwrite`.
- Each file is written to a temporary name in its directory and then
  placed with a hard link, which fails if the name exists, or replaced
  with a rename under `--overwrite`. `cover` also takes the preview
  output-set lock, so it waits for a preview of the same video.

### Random frames

- `image` without times, `image --random N`, and list lines without a time
  draw each time uniformly between zero and the start of the last frame,
  rounded down to milliseconds. No grid of frame positions is built from
  the average frame rate, which can be wrong for variable frame rate
  video, and the number of frames is not estimated or used as a limit.
- Draws are independent, so two times can select the same frame, and N
  draws always write N files. When a random time gives a name already used
  in the run, by the same request or by an earlier list line, an index
  follows the time: `clip_00-00-05-120-2.jpg`, then `-3`. Existing files
  are still conflicts unless `--overwrite` is given; they are not skipped
  with an index.
- Given times keep their names. A time given twice is a conflict, as
  before.
- Without a time, `cover` keeps the preview rule (0007).

### Lists

Lists are UTF-8 text, with LF or CRLF line ends and an optional byte order
mark. Empty lines are skipped, columns are separated by TAB, and spaces
around a column are trimmed, so names that start or end with a space are
not supported. Relative paths are resolved from the list's directory. A
failed line is reported with its number and reason, the others continue,
and the command fails at the end. Thumbnail lists have one column; frame
lists have a source and an optional time.

### Downloads

URL sources must be direct HTTP or HTTPS links. A run downloads each URL
once into a temporary directory removed at the end, including after an
interruption. Limits: 100 MiB and 2 minutes for images, 8 GiB and 60
minutes for videos, and 30 seconds for the response headers. Content
length is checked before and during the download. Logs and errors show the
scheme, host, and path only.

### Inspect

`inspect` sniffs local files: images are inspected in Go, and anything
else goes to FFprobe as before, with the same JSON. A PNG used to fail
with "no video stream found" and is now reported as an image; `preview`
still rejects it. Image URLs are downloaded and inspected; videos are
inspected from local files only, so a large video is not downloaded just
to read its header.

## Validation

On the Intel Mac:

- Unit tests build EXIF blocks in both byte orders with camera, lens,
  exposure, GPS, maker note, user comment, and unknown tags, and check ids,
  names, values, the GPS position, and the JSON. Every truncation of a block
  parses without panicking. All eight orientations are checked pixel by
  pixel, and a phone-style JPEG and PNG stored 64x32 with orientation 6
  decode as 32x64 with the top of the picture on top.
- Fit and fill sizes and center cropping, JPEG and PNG output,
  transparency to white in JPEG and kept in PNG, and outputs without EXIF,
  maker, or model bytes.
- Thumbnails and frames with a local test server: URL names, query strings
  absent from errors, one download for repeated URLs, size limits with and
  without Content-Length, timeouts, and cancellation.
- Lists with a byte order mark, CRLF, empty lines, spaces in paths,
  absolute and relative paths, URLs, empty and missing times, a malformed
  time, a time past the end, a missing file, a non-image, three columns,
  and two lines writing the same file. The other lines were written and
  the run failed with the count.
- Integration with the managed FFmpeg on a video whose brightness rises
  with time: frames at 0, 1.25, 2, and 3 seconds have the expected
  brightness, fit gives 100x56 and fill 100x100 for 320x180, the rotated
  fixture gives 240x320, a list mixing a local file and the same video by
  URL downloads it once, and `cover` writes only the cover. The `cover`
  output and the preview cover at the same time are byte for byte the
  JPEG of a direct FFmpeg run with -q:v 2, and a random `cover` stays in
  range.
- With a fixed random source, 4 draws at the same time write 4 files named
  with indexes 1 (none) to 4, a later draw continues with 5, a new run keeps
  the existing files as conflicts, and a time given twice is still a
  conflict. 50 draws on a clip of 20 frames write 50 files with distinct
  names.
- On copies of a real 1280x1920 JPEG and a 3840x2160 PNG: inspection,
  thumbnails at 320x240 with fit and fill, and outputs without EXIF.

- A real iPhone photo, 2026-11-24: a HEIC from the owner, converted to
  JPEG with `sips -s format jpeg` on macOS, which keeps the EXIF data
  (a JPEG exported from the Photos app had lost it). `inspect` read 69 tags
  in IFD0, Exif, GPS, and IFD1, all named and none with errors, with
  orientation 6, 4032x3024 stored and 3024x4032 displayed, camera, lens,
  exposure, date, maker note, and GPS position. Thumbnails at 240x320 fit
  and 300x300 fill had no EXIF and no maker or GPS strings. Compared with
  FFmpeg's rendering of the stored pixels at the same size, the fit
  thumbnail differed by 1.2 levels on average when those pixels were
  rotated 90 degrees clockwise, against 74.8 unrotated and 77.3 rotated
  counterclockwise, so it is upright. The source was not changed.

Pending: the Windows and Linux runs of these commands. HEIC input is
covered by 0009.
