# 0010 Thumbnail report and JPEG quality

Status: accepted
Date: 2026-11-25

## Context

A photo library import needs thumbnails of about 28,000 images. Each
thumbnail must come with the displayed size of the source and the date the
picture was taken, so the library can lay out its grid and order albums.
Running `inspect` next to `thumb` would read every image twice and, for
URLs, download it twice. The default JPEG quality of 92 (0008) also gives
thumbnails about twice as large as a grid needs.

Out of scope: streaming video by URL, reading XMP (where iOS keeps the
date of screenshots), and processing several images at once. A job can
split its list between tasks.

## Decision

### `thumb batch --jsonl`

- stdout gets one JSON object per non-empty list line, in list order,
  instead of the written paths. Without `--jsonl` the output is unchanged.
- Every record has `line`, `source`, and `status` (`ok` or `error`).
  `source` is the list entry as written, and URLs go through fetch.Redact,
  so credentials, query, and fragment are left out.
- An `ok` record adds `output`, `source_width` and `source_height` (the
  displayed size, after the EXIF orientation or the HEIF transforms),
  `output_width` and `output_height`, `date`, and `date_source`. `date`
  and `date_source` are always present and `null` when there is no date.
- An `error` record adds `error`, the same reason that is printed on
  stderr. Errors stay on stderr, so logs work as before, and the command
  still fails at the end with the count of failed lines.
- The record holds only these fields. GPS, camera, lens, and the rest of
  the EXIF block stay out; `inspect --json` still reports them for one
  image.
- The data comes from the read that produces the pixels: imaging.Decode
  already parses the EXIF block, and the HEIF path parses the EXIF item of
  the primary image. stills.Runner.Thumb returns a ThumbResult with the
  sizes and the date, and BatchHooks.OnThumb passes it to the CLI. The
  output size is computed with imaging.RenderedSize, the same rule as
  imaging.Resize.

### Date selection

imaging.SelectDate picks:

1. `DateTimeOriginal` (Exif directory), with `SubSecTimeOriginal` and
   `OffsetTimeOriginal`.
2. Otherwise `DateTime` (IFD0), with `SubSecTime` and `OffsetTime`, and
   `date_source` set to `DateTime`. Editors rewrite this tag, so it is
   labeled as a fallback rather than mixed with capture dates.

The value is ISO 8601: `2025-01-19T10:15:32`, then `.120` when a valid
fraction exists, then `-03:00` when a valid offset exists. Without an
offset no `Z` or `+00:00` is added: the time zone is unknown. A date that
is blank, zero (`0000:00:00 00:00:00`), out of range, or malformed is
skipped and the next tag is tried. A malformed fraction or offset is left
out instead of failing the date. An EXIF block that cannot be read gives
no date, and the thumbnail is still written.

### `--quality`

- `thumb` and `thumb batch` take `--quality N`, 1 to 100, default 92. It
  is checked with the other flags, before the list is read or a directory
  is created. With `--format png` an explicit `--quality` is an error,
  because PNG is lossless.
- imaging.Encode and imaging.Render take the quality; 0 keeps 92. `image`
  does not register the flag and keeps 92. Covers are still encoded by
  FFmpeg (0008).
- Orientation, the removal of metadata, the output names, and the
  overwrite protection do not change.

## Validation

macOS Intel, 2026-11-25, `go vet ./...` and `go test ./...`:

- imaging: dates with an offset and a fraction, without an offset, the
  fallback to `DateTime` with its own offset, a zero and an impossible
  `DateTimeOriginal`, malformed offset and fraction, no usable date, and a
  nil block. JPEG size grows with quality, 0 equals 92, and 0, -1, and 101
  are refused.
- stills: displayed and output sizes for a rotated phone JPEG, a fill
  crop, and a PNG; the date and its source for each; the HEIC result
  through the fake FFmpeg, with the date from the EXIF item. Quality 20
  writes a smaller file than 95, both upright and without EXIF, and an
  existing output is still a conflict.
- cli: a list with a local JPEG, a missing file, an empty line, a URL with
  credentials, a query, and a fragment, a text file, and a PNG without
  EXIF. Five records in list order, exact JSON for the written lines,
  `null` dates for the PNG, two error records, the URL requested once,
  no query, credentials, GPS, or camera strings, errors on stderr, and
  the exit error "2 of 5 list lines failed". Without `--jsonl`, stdout is
  the three paths. Bad `--quality` values fail before any output, and
  `image` does not accept the flag.

A pilot on 12 real photos from a phone library (JPEG, PNG screenshots, and
HEIC), read as local files laid out in folders as a mounted bucket would
give them, plus one missing path, at 480x480 fill and `--quality 80`:

- 13 records in list order. Phone exports gave `DateTimeOriginal` with
  milliseconds and the offset. Two images edited in an app had only
  `DateTime` and were labeled so. Screenshots and images without EXIF gave
  `null`. The displayed sizes matched macOS `sips`.
- The thumbnails averaged 39 KB, against about 75 KB at quality 92.
- Two folders held a file with the same name. The second one failed as an
  output conflict, as designed. A job that mixes folders writes each
  folder to its own output directory.
- One HEIC failed to decode because its alpha plane is itself a tile grid.
  Its record showed the error, and the batch went on. 0011 fixes the
  decoding.
- A URL list gave the redacted URL for a successful line and the HTTP
  status for a missing object.
