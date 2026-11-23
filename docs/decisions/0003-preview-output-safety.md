# 0003 Preview output safety and faststart

Status: accepted
Date: 2026-11-23

## Context

The preview workflow writes three files beside each video and can optionally
rewrite the video with faststart. A failure must never leave a partial
output set, damage the original, or lose earlier outputs. The reference
workflow always remuxed the input in place, continued after a failed remux,
and overwrote outputs without asking.

## Decision

### Outputs

For an input <dir>/<name>.<ext> the outputs are <name>_cover.jpg,
<name>_preview.mp4, and <name>_preview.jpg in the same directory. Inputs
that share a directory and base name, such as clip.mp4 and clip.mov, share
one output set.

### Order of checks

1. Validate options and the input path. --faststart-in-place is accepted only
   for .mp4, .m4v, and .mov files.
2. Take the output-set lock (below).
3. Without --overwrite, stop if any output exists and list every conflict.
4. Resolve FFprobe and inspect the input. Preview requires a readable file, a
   real video track, and a known positive duration. The inspect command keeps
   accepting files with an unknown duration.
5. Resolve FFmpeg and check its features, then find the overlay font.

A failure in any of these steps reports the path, the step, and the reason,
and creates no outputs. FFmpeg is not resolved or downloaded for inputs that
fail inspection.

### Output-set lock

Each output set has a lock file, <dir>/.<name>.musafoz.lock, held with an
exclusive flock from the conflict check until publishing and any restore
end. A second run on the same set waits, then sees the outputs of the first.
The lock file stays in place after the run; the kernel releases the lock if
the process dies.

### Staging and publishing

- Frames go to a private directory under the system temporary directory and
  are removed when the run ends.
- The three outputs and any faststart remux are written to a staging
  directory <dir>/.<name>.musafoz-<random>/, on the same file system as the
  destination.
- Publishing moves one file at a time. A new file is placed with a hard
  link, which never replaces an existing file. With --overwrite, the earlier
  file is first hard-linked into the staging backup directory and the new
  file then replaces it with an atomic rename. On file systems without hard
  links the earlier file is renamed aside first.
- If any publish step, or the later original replacement, fails, every
  published output is rolled back in reverse order: new files are removed and
  backups are renamed back.
- If the rollback itself fails, the staging directory is kept and the error
  names it, so the remaining backups can be recovered by hand.
- The staging directory is removed after success and after a complete
  rollback.

### Faststart

- The original is preserved by default.
- With --faststart-in-place, a file whose moov box already precedes mdat is
  left unchanged. Otherwise musafoz remuxes it into the staging directory
  with stream copy, the original video track timescale, and +faststart. The
  remux is then checked against the original (see below).
- Files with Apple timed metadata tracks (codec tag mebx), as recorded by
  iPhone cameras, are refused right after inspection, before FFmpeg is
  resolved and before any remux. With FFmpeg 9.1 a stream-copy remux writes
  placeholder values for those tracks and their tags become stts. The error
  says that the original was preserved and that the preview can be generated
  without --faststart-in-place. Without the flag, such files are processed
  normally.
- The check after the remux compares, from FFprobe output: the number of
  video, audio, subtitle, attached picture, and other streams; the codec tag
  of every stream in index order; the container duration, within 0.1
  seconds; and that the moov box precedes mdat. It does not compare packet
  data or timestamps. Stream copy leaves packets unchanged, and the original
  video timescale is passed to the muxer so timestamps keep their values;
  this was confirmed with FFmpeg stream hashes during validation, not by the
  check itself. The check stays in place for tracks other than mebx that a
  muxer might rewrite.
- Outputs are generated from the remuxed copy. The original is replaced by
  an atomic rename, keeping its permission bits, only after the three outputs
  are published, and only if its size and modification time did not change
  during the run.
- A failed remux, or any later failure, leaves the original unchanged and
  ends with an error naming the step.

### Interrupted runs

While holding the lock, musafoz looks for staging directories of the same
output set left by a killed run. It reports each path as a warning and
leaves it in place, because it may hold backups. Automatic recovery is out
of scope.

### Batch behavior

A validation or processing failure is returned as a StepError with the path
and step. The batch delivery will record it per file and continue.

## Displayed aspect ratio

The outputs follow the video as it is shown, not as it is stored. The
displayed aspect ratio is width * SAR / height from FFprobe, inverted when
the rotation metadata is 90 or 270 degrees. A missing or zero sample aspect
ratio counts as square pixels.

- FFmpeg applies the rotation metadata when it decodes, so frames, the
  cover, and the preview video keep the displayed orientation. Each decoded
  frame is then scaled to square pixels with scale=round(iw*sar):ih and
  setsar=1, which changes nothing for square-pixel sources.
- The cover stays at source resolution in its displayed orientation; only
  the width of a non-square-pixel source changes.
- The preview video takes the size of the frames, rounded down to even
  values, with a 1:1 sample aspect ratio, so its display aspect ratio is the
  source's.
- The 10 by 10 grid is the largest grid with the displayed aspect ratio
  that fits in 4K in the video's orientation: 3840x2160 for landscape and
  square sources, 2160x3840 for portrait ones. The source resolution does
  not matter: a 320x180 and an 8K 16:9 source both give 3840x2160. Cells
  are at most 384x216 or 216x384, with the free side rounded to an even
  number: 384x216 for 16:9, 216x384 for 9:16, 288x216 for 4:3, 216x288 for
  3:4, and 216x216 for 1:1. The grids are therefore 3840x2160, 2160x3840,
  2880x2160, 2160x2880, and 2160x2160. The 40-pixel header is added above
  the grid and is not part of the grid aspect ratio, so the JPEG height is
  the grid height plus 40.
- The timestamp overlay is sized from the mosaic cell rather than in fixed
  frame pixels. In the mosaic, the font is 12.8 pixels, the margin 5, and
  the box border 1.6, for every source. On the extracted frame these are
  multiplied by frame width / cell width, which gives the reference values
  64, 25, and 8 for a 1920x1080 source and 128, 50, and 16 for a 2160x3840
  phone video. The preview video uses the same frames, so the overlay keeps
  the same size relative to the picture there too.

## Accepted differences from the reference workflow

- Frame timestamps are i * duration / frames with fractions, capped at the
  start of the last video frame, instead of whole seconds. The overlay still
  shows HH:MM:SS. Very short clips can repeat a frame.
- The cover timestamp is a fractional value in the same 15 to 85 percent
  range, also capped at the last frame.
- The mosaic header shows the real file name, including its extension.
- The preview video is converted to limited-range yuv420p. With FFmpeg 9.1
  the reference command produces full-range yuvj420p from JPEG frames.
- Odd frame dimensions are rounded down to even values for libx264.
- The reference script can use h264_nvenc for preview encoding. MusaFoz
  uses h264_videotoolbox on macOS when the hardware probe succeeds, and
  libx264 as the fallback or when --accel cpu is set (see 0004).
- The mosaic grid is fixed at 10 by 10 and --frames accepts 1 to 100; empty
  cells are black.
- The mosaic grid takes the displayed aspect ratio of the source instead of
  a fixed 16:9 canvas (see Displayed aspect ratio below). For 16:9 sources
  the result matches the reference size, 3840x2200. For other sources it
  differs on purpose: the reference always produces 3840x2200 and fills the
  space around a non-16:9 grid with black, while musafoz produces the grid
  alone, for example 2880x2200 for 4:3 and 2160x2920 for 3:4.
- The reference draws the timestamp at 64 pixels on every frame, so its size
  in the mosaic shrinks as the source resolution grows (about 6 pixels for a
  4K phone video against about 13 for 1080p). musafoz sizes the overlay from
  the mosaic cell; the result matches the reference only for 1080p 16:9
  sources.
- Each frame is scaled to its cell before tiling. The reference tiles
  full-size frames and scales the grid afterwards; for 4K sources that grid
  (for example 21600x38400) exceeds FFmpeg's image size limit, the reference
  mosaic is not written, and the reference script still reports success.
- Frames, the cover, and the preview video are converted to square pixels at
  the displayed aspect ratio. The reference keeps non-square pixels in the
  frames and then sets square pixels on the preview, which distorts
  anamorphic sources; for square-pixel sources nothing changes.
- The reference remuxes every input with the MP4 muxer and continues with
  the original if that fails. musafoz uses the muxer that matches the
  extension and stops with an error, as described above.

## Performance note

Fractional frame timestamps are kept. On long-GOP sources they cost more
decoding than whole seconds: each frame is decoded from the previous
keyframe. On a 15-second iPhone clip (HEVC, 3840x2160 at 60 fps, displayed
as 2160x3840, keyframes about one second apart) on the Intel Mac used for
validation, musafoz took about 129 seconds and the reference script about
54 seconds. Extracting one frame took about 0.9 seconds just after a
keyframe and 1.6 seconds just before the next one. The reference script did
not produce the mosaic for that input (see above), so its time does not
cover a complete run. Decoding the source once for all frames is a
candidate optimization for a later delivery.

## Parity check method

The reference script targets Linux. For comparisons on macOS, a temporary
copy outside the repository is adjusted in three places only: the temporary
frame directory uses mktemp instead of /dev/shm, the CPU count falls back to
sysctl, and the overlay font is the macOS copy of Andale Mono. The copy runs
with the managed FFmpeg and FFprobe first on its PATH. internal/cli/parity_test.go
(build tag parity) runs both sides on separate copies of the same inputs.
