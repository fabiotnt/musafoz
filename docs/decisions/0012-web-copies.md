# 0012 Web copies of a photo library

Status: accepted; revised on 2026-11-25 for resumable runs and shared
directories (see "Revision: resuming and privacy")
Date: 2026-11-25

## Context

A photo library of about 49,000 files (HEIC, JPEG, PNG screenshots, MOV
and MP4 videos) is to be shown on a web site. The site needs files every
browser shows, of a size that loads quickly, named so that the original
names and folders do not leak, and the metadata of each original for
dates, and later for camera and location details. Many files are copies of
others in several folders.

## Decision

### Command

`web <source>` and `web batch <list>` write web copies into `--output`
and, with `--metadata`, one record per content, which may be in the same
directory. `--jsonl` gives a report line per list line, without GPS or
camera data. `web` is the import flow, with its own size contract;
`thumb batch --size 480x480 --mode fill --quality 80 --jsonl` remains the
way to make square thumbnails with a report.

### Names and duplicates

- The UUID of a content is version 5 (RFC 9562) of "sha256:<hex>" in a
  fixed musafoz namespace. The same bytes always get the same UUID, in any
  run, task, or machine, so tasks can split a list freely.
- Content seen earlier in a run is not converted again. Its later lines
  report status `duplicate` with the same UUID and files, so an importer
  can link one file to every folder that held it.
- `--skip-existing` is described in the revision below. The first version
  trusted the paths listed in the record, wherever they were, and treated
  a content left half done as a conflict.

### Images

- Decoding, orientation, and HEIC are as in 0008, 0009, and 0011.
- The long side is at most 2560 pixels and the thumbnail is 480 pixels
  on the short side, keeping the aspect ratio. Nothing is enlarged.
  Sizes are computed once and resized with fill at that exact size, so the
  long side is exactly 2560.
- Colors are converted to sRGB (internal/imaging/color.go) from:
  - an ICC profile with rXYZ, gXYZ, bXYZ colorants and rTRC, gTRC, bTRC
    curves (curv or para), read from JPEG APP2 (chunks in sequence), PNG
    iCCP, WebP ICCP, or HEIF colr prof or rICC. Source values go through
    the tone curves to linear light, through the colorants to the D50 PCS,
    and through the inverse D50 sRGB colorants to linear sRGB. Out of gamut
    values are clipped per channel, then encoded with the sRGB curve.
  - a HEIF colr nclx with primaries 1, 9, 11, or 12 and an SDR transfer,
    using the chromaticities of ISO/IEC 23091-2.
  Profiles within 0.002 of the sRGB matrix and half a level of its curve
  are left alone. Gray, CMYK, and lookup table profiles, and HDR
  transfers, are not converted; the run logs it. The conversion runs after
  resizing, on fewer pixels.
- `thumb` uses the same conversion. Before this change it dropped the
  profile and kept the values, so wide gamut photos looked dull.
- JPEG quality is 85 for images and 80 for thumbnails. No metadata is
  written.

### Dates and XMP

- XMP is read from JPEG APP1 (http://ns.adobe.com/xap/1.0/), PNG iTXt
  XML:com.adobe.xmp (compressed or not), WebP XMP chunks, and HEIF mime
  items of type application/rdf+xml that describe the primary item.
  ParseXMP keeps the simple properties of known namespaces (exif, exifEX,
  tiff, xmp, photoshop, dc, aux, xmpMM, Iptc4xmpCore, HDRGainMap) as
  "prefix:Name", from attributes and from text elements, with the first
  item of a list.
- imaging.SelectDate now tries the EXIF DateTimeOriginal, then the XMP
  exif:DateTimeOriginal, photoshop:DateCreated, and xmp:CreateDate, then
  the EXIF DateTime. Phones keep the date of screenshots in
  photoshop:DateCreated. The source is named as "XMP
  photoshop:DateCreated".
- An XMP exif:UserComment of "Screenshot" sets `screenshot` in the
  result.
- Dates are parsed with strconv. fmt.Sscan, used before, reads "08" and
  "09" as bad octal, so EXIF dates in August and September, or at 08 and
  09 hours, were dropped.

### Videos

- media.EncodeWeb: libx264 High, yuv420p, CRF 21 capped by `-maxrate` at
  the bitrate (5 Mbps) with twice that buffer, AAC stereo at 128 kbps, the
  first video and audio tracks only, faststart, and `-map_metadata -1`,
  `-map_chapters -1`, `-sn`, `-dn`, so camera, date, and location tags do
  not reach the copy.
- The size fits 1920x1080 in the orientation of the video, after the
  rotation that FFmpeg applies on decode, never enlarged, with sides
  rounded down to even numbers.
- HLG (arib-std-b67) and PQ (smpte2084) video is scaled, linearized with
  zscale at a 100 nit reference, converted to BT.709 primaries, tone
  mapped with the Hable curve, and encoded as BT.709. Every output is
  tagged BT.709 with setparams, because encoders take the color tags of
  the frames.
- The date is the QuickTime com.apple.quicktime.creationdate (local time
  with its offset), else creation_time (UTC), ignoring 1904 and 1970
  placeholders.
- With previews on, `preview` runs on the web MP4, so its cover, preview
  video, and mosaic are made from the 1080p copy, beside it. The callback
  is passed in by the CLI because internal/preview imports internal/stills.

### Metadata files

`<uuid>.json` holds the UUID, SHA-256, source as written, the result, and
either the full image inspection (EXIF with GPS, XMP, color profile,
sizes) or the full FFprobe JSON of a video. The report only has the
result fields. With the revision below it is also the completion record.

## Revision: resuming and privacy

### Context

Before the first production batch, a review asked for: batches that can be
stopped at any point and run again safely, including after a video or an
image was placed but before the other outputs; a check that the recorded
outputs were made with the current settings and are in the output
directory of the current run; and media and records of a UUID in one
folder, as the site will keep them in a bucket with per-object access.

### Decision

- Each content is made in `.musafoz-staging/<uuid>-*/` inside the output
  directory, on the same file system, so outputs are placed with a link or
  rename. The preview of a video runs on the staged MP4 and writes beside
  it, so its files are staged too.
- The SHA-256 and size of every staged output go to a pending file,
  `.<uuid>.musafoz-pending.json`, in the output directory. It holds no
  metadata of the source. Then the outputs are placed, the record
  `<uuid>.json` is written, and the pending file is removed. The record is
  the last write, so it marks a finished content.
- The record (format `musafoz-web-record/1`) stores the outputs by name,
  with SHA-256 and size, and the settings that made them (WebConfig): the
  pipeline version, image size and qualities for images; video size,
  bitrate, CRF, audio bitrate, preview, and preview frames for videos.
  Settings of the other kind are left out, so a change of image size does
  not concern videos. WebPipeline is raised when musafoz changes its
  outputs for the same settings.
- With `--skip-existing`:
  - a record whose settings differ is an error naming each change, such as
    `image_quality 85 -> 70`. `--overwrite` makes the content again.
  - a record whose outputs are all in the output directory of this run,
    with the recorded SHA-256, is reused (`exists`). Outputs listed in the
    record but found elsewhere do not count.
  - otherwise the content is made again (`resumed`). A file at an output
    name is replaced only when its SHA-256 is the one in the record or in
    the pending file, that is, when musafoz wrote it, or when it already
    has the new content. Any other file is a conflict and is kept.
  - a pending file without a record means a run stopped while placing
    outputs; the content is made again the same way.
- Without `--skip-existing` or `--overwrite`, an existing record, a pending
  file, or a file at an output name is an error, as before.
- Two processes on the same output directory take an advisory lock for
  the UUID (shared by UUIDs with the same first two hex digits, at most
  256 lock files), so they never work on one content at once. Staging
  directories of the UUID left by a dead run are removed under that lock.
  Tasks on different machines claim the UUID at upload time; see
  docs/web-pilot.md.
- The UUID still names the original content. Reuse still reads the whole
  original for its SHA-256 before anything else. `timings` in each result
  separate that read (`hash`) from the conversion (`convert`) and from the
  check of existing outputs (`verify`).
- `--output` and `--metadata` may be the same directory. Media carry no
  metadata. Records hold GPS, the full EXIF, XMP, and raw FFprobe tags. The
  JSONL report has no GPS or camera data but maps original paths to UUIDs,
  so it is internal. docs/web-pilot.md describes private-by-default
  uploads, public read on an explicit list of media, records uploaded
  last and private on every replacement, the bucket IAM check, and the
  access checks.

### Validation

- stills: an interruption right after the first output is placed (a test
  hook stops the content there) leaves the image, a pending file, and no
  thumbnail or record; a plain rerun refuses it and names
  `--skip-existing`; a run with it finishes the content (`resumed`) and
  removes the pending file. Then: a finished content is reused without
  writing (`exists`); a removed thumbnail is made again; a thumbnail
  replaced by another file is kept and reported as a conflict; a change of
  image quality is reported with the setting and its values, and
  `--overwrite` remakes the content and records the new value; a change of
  video bitrate does not concern an image; another output directory makes
  the outputs there (`resumed`); a file of unknown origin at the name of a
  new content is kept. All with `--output` and `--metadata` in one
  directory.
- cli: a batch with media and records in one directory; the records keep
  GPS and XMP; the media have no EXIF, GPS, XMP, ICC, or device strings;
  the report has no GPS or camera strings.
- Integration with the real FFmpeg: the video batch in one directory; a
  second run reuses every video after checking its four outputs; a change
  of preview frames is reported as `preview_frames 9 -> 16`.
- Linux pilot in Docker, with real phone files and a 4K HLG video: see
  docs/web-pilot.md for the measurements, the kill and resume, and what
  remains for the Cloud Run pilot.

## Validation

macOS Intel, managed FFmpeg 9.1, 2026-11-25, `go test ./...` and
`go test -tags integration ./internal/cli/`:

- imaging: Display P3 to sRGB from an ICC profile and from nclx, against
  the reference P3 to sRGB matrix, within 2 levels for colors and 1 for
  grays, alpha kept; sRGB profiles left alone; gray, truncated, and HDR
  inputs refused; every truncation of a profile parsed without panic; XMP
  and ICC read from PNG and from JPEG with ICC chunks out of order; XMP
  dates with and without seconds, Z, and offsets; lists and broken XML;
  HEIF colr prof, colr nclx, and XMP items; dates with 08 and 09.
- stills: a 3000x1000 Display P3 JPEG with GPS gives a 2560x853 image and
  a 1441x480 thumbnail, both converted and without metadata, and a
  metadata file with GPS; a 100x200 screenshot is not enlarged and gets
  its date from XMP; a copy in another folder is a duplicate; a new run
  is a conflict, `--skip-existing` reuses the result without writing, and
  a missing output is a conflict again; refused options create nothing.
- media: sizes for 4K, portrait, ultra wide, square, and odd sizes;
  filters for SDR, HLG, and PQ; QuickTime and UTC dates.
- cli: flag errors; a batch with a duplicate and a missing file; no GPS or
  camera strings in the report; a second run with `--skip-existing`.
- Integration with the real FFmpeg: a rotated clip, a clip with audio, and
  a 4K HLG HEVC clip with QuickTime date and location tags. All are H.264
  High yuv420p BT.709, at most 1080p, faststart, without QuickTime or
  location tags, with the four outputs named by the UUID. The HLG clip is
  tone mapped to 1920x1080 and dated from its tag, and its location is
  only in the metadata file.
- Eight real library files (three PNG screenshots, two HEIC, three JPEG),
  local copies: all converted with their sizes, thumbnails, and dates; the
  two HEIC were detected as Display P3 and converted. macOS `sips
  --matchTo` was tried as a reference for the colors, but it relabeled
  the profile without changing pixel values, so the color check rests on
  the unit tests.
