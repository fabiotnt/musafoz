# 0007 Cover time and mosaic grid

Status: accepted
Date: 2026-11-23

## Context

The cover was always taken at a random time, or at the midpoint of clips
of 10 seconds or less, with no way to choose the frame. The mosaic grid was
fixed at 10 by 10, so a preview with 25 frames filled a quarter of the
mosaic and left 75 black cells. This record amends the cover and grid
rules of 0003.

## Decision

### Cover time

- `--cover-at <timestamp>` sets the cover time. The format is HH:MM:SS with
  an optional fraction of a second, for example 00:01:15.500. Hours take
  any number of digits; minutes and seconds take two digits below 60. The
  value is rounded to milliseconds. 00:00:00 is accepted.
- A malformed or negative value is rejected by the command line before any
  other work and names --cover-at. The operation (preview.Options.CoverAt)
  also rejects negative and non-finite values.
- The time must be between zero and the start of the last video frame, the
  same limit used for the mosaic frames: the video stream duration, or the
  container duration, minus one frame. A later time decodes nothing. The
  check runs right after inspection, before FFmpeg is resolved or
  downloaded, and the error gives the start of the last frame, the
  duration, and the valid range.
- FFmpeg seeks with -ss before the input, so the cover is the first frame
  that starts at or after the given time. At 10 fps, 00:00:01.250 gives the
  frame at 1.3 seconds.
- Without the flag the time is random:
  - Videos longer than 10 seconds: between 15 and 85 percent of the
    duration, as before.
  - Videos of 10 seconds or less: anywhere from zero to the start of the
    last frame. They used the midpoint before; this is an intentional
    change, so repeated runs on short clips give different covers too.
  - The random time is rounded down to milliseconds, so it never passes the
    last frame and the reported value is exactly the one given to FFmpeg.
- The JSON field cover_at_seconds reports the time used, given or random,
  and the log says which. The text output shows the time with milliseconds
  when it has a fraction.

### Mosaic grid

- The grid follows --frames: ceil(sqrt(N)) columns and ceil(N / columns)
  rows. 1 frame gives 1x1, 25 give 5x5, 98 give 10x10 with two empty cells,
  and 100 give 10x10. With at most 100 frames there are at most 10 columns
  and 10 rows.
- The grid area does not depend on N. It is the 10 by 10 area of 0003: the
  largest area with the displayed aspect ratio that fits in 3840x2160 for
  landscape and square videos or 2160x3840 for portrait ones, with the free
  side rounded as before. The mosaic size therefore stays the same for
  every frame count, for example 3840x2200 for 16:9 and 2160x3880 for 9:16
  with the header.
- Cells are the area divided by the columns and rows, rounded down to whole
  pixels. The few pixels left over are split around the cells as a black
  border.
- Each frame is scaled to fit its cell with its aspect ratio kept and
  centered on black. When the grid has as many columns as rows the frame
  fills the cell; otherwise, as for 5 frames in a 3 by 2 grid, the cell has
  another shape and gets black bars. A gap of up to 2 pixels from the even
  rounding of the area is filled by the frame, as in the 10 by 10 grid.
  Frames are scaled before tiling, so the intermediate image stays small.
- Empty cells stay black. The 40-pixel header with the file name is
  unchanged.

| Frames | Grid | 16:9 cell and frame | 9:16 cell and frame |
| --- | --- | --- | --- |
| 1 | 1x1 | 3840x2160 | 2160x3840 |
| 5 | 3x2 | cell 1280x1080, frame 1280x720 | cell 720x1920, frame 720x1280 |
| 25 | 5x5 | 768x432 | 432x768 |
| 98 | 10x10, 2 empty | 384x216 | 216x384 |
| 100 | 10x10 | 384x216 | 216x384 |

### Timestamp size

The frames with their timestamp overlay are shared by the MP4 preview and
the mosaic. If the overlay followed the cell size, a smaller grid would
shrink the MP4 timestamps: with one frame, a 1920x1080 source would get a
6-pixel font in the MP4. The overlay therefore keeps the size set in 0003,
computed from the frame width and the 10 by 10 cell, for every frame
count:

- The MP4 timestamps have the same size for every --frames value, 64
  pixels for a 1080p 16:9 source as before.
- In the mosaic the timestamp scales with the frame. It is about 12.8
  pixels in a 10 by 10 grid, as before, 25.6 pixels with 25 frames, and 128
  pixels with one frame. Since a grid has at most 10 columns and 10 rows,
  every frame in the mosaic is at least as large as in the 10 by 10 grid,
  so no mosaic gets smaller timestamps than before.

## Differences from the reference workflow

- The reference script has no way to choose the cover time and uses the
  midpoint for clips of 10 seconds or less.
- The reference always tiles 10 by 10. With fewer frames musafoz uses a
  smaller grid in the same area; with 91 to 100 frames both use 10 by 10.

## Validation

On the Intel Mac, with the managed FFmpeg 9.1:

- Unit tests cover parsing and formatting of timestamps, the random range
  for short and long videos, the grid for every count from 1 to 100, and
  the layout for common aspect ratios. With fake tools, the preview tests
  check that --cover-at reaches the cover command unchanged for 0, 1.25,
  and the last frame, that times past the last frame fail before FFmpeg is
  resolved, and that the tile filter gets the expected grid for 1, 5, 25,
  98, and 100 frames.
- Integration test TestIntegrationCoverAt: on a 4-second, 10 fps video
  whose brightness rises with time, covers at 00:00:00, 00:00:01.250, and
  the last frame (00:00:03.900) have the brightness of the expected frame
  and report 0, 1.25, and 3.9 in cover_at_seconds. 00:00:04, 00:00:03.999,
  and 01:00:00 fail and write no cover.
- Integration test TestIntegrationMosaicGrid: for a 1280x720 and a 720x1280
  video with 1, 25, 98, and 100 frames, the mosaic is 3840x2200 or
  2160x3880 every time, the MP4 has exactly the requested number of frames,
  filled cells hold a frame, and the empty cells of the 98-frame grid are
  black. The dark area of the MP4 timestamp box on the first frame differed
  by at most 6 pixels out of about 19000 between frame counts. The
  mosaic timestamp area, divided by the squared scale of the frame, stayed
  within the test's 25 percent tolerance.
- TestIntegrationPreviewAspect now runs with 10 frames, a 4 by 3 grid whose
  cells are wider or taller than the frames, and checks orientation and
  timestamp size at the centered frame position. The timestamp areas of all
  sources stayed within 3 percent of the 1280x720 reference, except the
  320x180 source. Its frame is enlarged 3 times in that grid, against 1.2
  times in the 10 by 10 grid, together with its whole-pixel overlay (an
  11-pixel font and a 1-pixel border where 1.33 would be exact); it
  measured 34 percent below the reference, against 23 percent before. The
  overlay sizes themselves did not change. The test allows 40 percent for
  frames enlarged more than 2 times, 30 percent for other enlarged frames,
  and 20 percent for the rest.
