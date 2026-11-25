# 0011 HEIC decoding by item

Status: accepted; supersedes the decoding part of 0009
Date: 2026-11-25

## Context

0009 let FFmpeg build the primary image of a HEIC file: grid assembly,
alpha merge, and the irot and imir transforms. On 31 real phone photos,
10 failed:

- 9 grids with an odd output width, such as 1737x3088 or 3213x5712.
  FFmpeg 9.1 crops the assembled grid to an even width, 1736 or 3212, so
  the size check of 0009 refused the result.
- 1 screenshot whose alpha plane is itself a grid (auxC
  `urn:mpeg:hevc:2015:auxid:1`). FFmpeg 9.1 failed while merging it, and
  musafoz did not see the plane at all, because it only looked for "alpha"
  in the auxC type.

The size check was right to refuse these images. Accepting a size that is
one pixel off would hide other decoder bugs.

## Decision

- imaging.ParseHEIF also returns the primary item id, the grid layout (rows,
  columns, output size, and tile item ids from the dimg references, in
  order), and the alpha item with its own grid layout. Alpha types are the
  MPEG-B `...:auxiliary:alpha` and the HEVC `urn:mpeg:hevc:2015:auxid:1`.
  Depth maps (auxid 2) and HDR gain maps are not alpha.
- media.DecodeItems runs FFmpeg once per image with `-noautorotate` and one
  output per item, each selected by id (`-map 0:i:<id>`): RGB for the
  picture and 8-bit gray for the alpha plane, as uncompressed PNG. An item
  selected this way is decoded alone: no grid assembly, no alpha merge,
  and no rotation. Measured with the managed FFmpeg 9.1: with
  `-noautorotate`, a rotated hvc1 item decodes at its stored size.
- imaging.ComposeGrid places the tiles row by row and keeps the top left
  output size, so odd sizes are exact. Tiles must share a size and cover
  the output. imaging.MergeAlpha sets the alpha from the gray plane.
- HEIF.Transform applies irot and imir in the order the file lists them.
  This replaces the FFmpeg order correction of 0009, which is removed with
  media.DecodeImage.
- A single hvc1 item must decode to exactly its ispe size. A failure of the
  alpha plane is logged and the image is kept opaque, because transparency
  is not needed to show the photo.

## Validation

macOS Intel, managed FFmpeg 9.1, 2026-11-25:

- Unit tests: grid layout and alpha item parsing from synthetic files
  (2x1 grid cropped to 127 pixels, alpha as an item and as a grid), alpha
  types, grid assembly of a 2x2 grid cropped to 7x5 with every corner
  checked, refused layouts, alpha merge, and the transform order.
- Integration with the real FFmpeg: the ten cases of 0009 give the same
  quadrant colors, now rotated by musafoz. Grids cropped to an odd width,
  with and without rotation, report 127x48 and 48x127. Alpha as an item and
  as a grid gives transparent pixels.
- 31 real phone HEIC photos: 30 random photos from a library and the
  screenshot with the alpha grid, including odd widths, irot, and imir. All
  31 were written. Against `sips` conversions made upright by musafoz, the
  640 pixel thumbnails had a PSNR of 48.5 dB or more. The same thumbnails
  turned 180 degrees scored 8 to 15 dB. 31 photos took 47 seconds on the
  Intel Mac.
