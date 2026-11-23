# 0006 Embedded overlay font

Status: accepted
Date: 2026-11-23

## Context

The timestamps on the frames and the file name in the mosaic header are
drawn with a font file. Until now musafoz looked for an installed font:
Andale Mono on macOS, as the reference workflow does, DejaVu Sans Mono or
Liberation Mono on Linux, Consolas or Lucida Console on Windows, and, since
0005, an embedded DejaVu Sans Mono as the last resort. The same video could
therefore get different overlays on different machines, and a missing or
changed system font changed the output.

## Decision

- The default overlay font is DejaVu Sans Mono 2.37, embedded in the
  musafoz binary, on every platform. musafoz no longer looks for installed
  fonts. Andale Mono is not used even where it is installed.
- --font <path> replaces it with the given file. The path is checked before
  FFprobe and FFmpeg are resolved: a missing file, a directory, an
  unreadable file, or a file that does not start like a TrueType, OpenType,
  or TrueType collection font (.ttf, .otf, .ttc) stops the run with an error
  that names the path. There is no special value for the embedded font;
  omitting --font selects it.
- The log names the font used, for example
  "overlay font: DejaVu Sans Mono 2.37 (embedded)".
- The font, embedded or given, is written into the work directory as
  font.ttf (or the given file's plain extension) and filters refer to it by
  that relative name, as described in 0005.

### Font source

- dejavu-fonts-ttf-2.37.tar.bz2 from the version_2_37 release of
  github.com/dejavu-fonts/dejavu-fonts (archive SHA-256
  fa9ca4d13871dd122f61258a80d01751d603b4d3ee14095d65453b4e846e17d7).
- ttf/DejaVuSansMono.ttf, 340712 bytes, SHA-256
  b4a6c3e4faab8773f4ff761d56451646409f29abedd68f05d38c2df667d3c582, stored
  as internal/media/fonts/DejaVuSansMono.ttf.
- LICENSE, 8816 bytes, SHA-256
  7a083b136e64d064794c3419751e5c7dd10d2f64c108fe5ba161eae5e5958a93, stored
  unchanged as internal/media/fonts/LICENSE. It is the only file in the
  repository that is not ASCII: it is the release file byte for byte,
  including its typographic quotation marks.

### License notice

The Bitstream Vera license, with DejaVu changes in the public domain,
allows embedding and redistribution as long as the copyright and
permission notices are included with every copy of the font. Because the
font is inside the binary, the notice is embedded too:

- `musafoz licenses` prints the notice from the binary itself, so every
  copy of musafoz carries it, however it is packaged.
- A test checks that the embedded notice is identical to
  internal/media/fonts/LICENSE.
- Release archives must also include internal/media/fonts/LICENSE as a
  separate file, for example as LICENSE-DejaVu.txt next to the binary.
  There is no release tooling yet; whoever adds it must keep this rule.

## Validation

On the Intel Mac, with Andale Mono installed:

- Without --font the log reported "overlay font: DejaVu Sans Mono 2.37
  (embedded)". With --font pointing to Andale Mono it reported that path.
- A missing path, a text file named .ttf, and a directory each stopped the
  run with an error naming the path, before FFprobe and FFmpeg were
  resolved (covered by TestInvalidFontFailsFirst).
- Timestamps and the header were legible at full size in a 16:9 mosaic
  (3840x2200, 1280x720 source) and a 9:16 mosaic (2160x3880, 4K phone
  source).
- Two 1280x720 16:9 videos were processed twice with --accel cpu, once
  with the embedded font and once with Andale Mono. The mosaics had SSIM
  0.9976 and 0.9974; every differing pixel was in the header or in a
  timestamp box, none elsewhere.
- The parity test against the reference script passes. Its mosaic SSIM
  values (0.49 and 0.85 for the two 16:9 videos) also reflect the
  fractional frame timestamps of 0003, so they do not isolate the font.

## Consequences

- Overlays look the same on macOS, Linux, and Windows, and do not depend on
  installed fonts.
- The binary grows by about 340 KB.
- Compared with the reference workflow, the timestamps and the header use
  DejaVu Sans Mono instead of Andale Mono. The sizes and positions are
  unchanged; the glyph shapes differ. This is an intentional difference
  (see 0003).
- This replaces the font lookup of 0005 ("Embedded fallback font").
