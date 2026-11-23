# 0005 FFmpeg and FFprobe on Linux and Windows

Status: accepted for Linux; Windows partly validated on a native machine
Date: 2026-11-23

## Context

A user should be able to download only musafoz, run
`musafoz preview clip.mp4`, and get the three outputs on a machine without
FFmpeg or FFprobe. 0001 and 0002 pin evermeet.cx builds for Intel macOS
only and never search the PATH. Linux x86-64 comes first, then Windows
x86-64. The preview stays on the CPU on both; NVIDIA and AMD acceleration
will be measured separately on real hardware.

## Decision

### Resolution order

For each tool:

1. The --ffprobe or --ffmpeg flag.
2. MUSAFOZ_FFPROBE or MUSAFOZ_FFMPEG.
3. A path saved in a local configuration file. There is no configuration
   file yet; adding one is a separate change, and resolution has a marked
   place for it.
4. Automatic resolution:
   - Linux and Windows: the tool found on the PATH, if it passes the checks
     below; otherwise the managed build, from the cache or downloaded.
   - macOS: the managed build, as before. The PATH is not searched.

A flag or variable that points to a missing, non-executable, or
incompatible file stops the run with an error that names the flag or
variable and the path. A PATH candidate that fails a check is skipped.
Standard error always reports the chosen executable and the reason, for
example "no ffprobe on the PATH" or "ffmpeg on the PATH (/usr/bin/ffmpeg)
is not compatible: ffmpeg 4.4 is too old".

### Checks for explicit and PATH tools

- The file is a regular file. On Windows it must end in .exe or .com; a
  path without an extension is tried with .exe added. Elsewhere it must
  have an execute permission bit.
- "-version" identifies the expected tool, and a release build is version
  6 or newer. Snapshot builds pass with a warning, as in 0001.
- For FFmpeg found on the PATH, the encoders and filters of 0002 are
  required: libx264, mjpeg, drawtext, tile, scale, pad, and setsar. An
  explicit FFmpeg gets the same check before the preview starts and fails
  with the missing features listed.

### Pinned builds

Linux and Windows use BtbN FFmpeg-Builds (github.com/BtbN/FFmpeg-Builds),
release autobuild-2026-10-31-12-59, FFmpeg release branch 9.0, GPL static
variant. BtbN publishes daily builds and deletes them after about two
weeks, but keeps the last build of each month; that is why a month-end
release is pinned. BtbN has no 9.1 branch, so Linux and Windows run
n9.0.2-39-g7adfbe40dc while macOS runs 9.1.

| Field | Linux x86-64 | Windows x86-64 |
| --- | --- | --- |
| Archive | ffmpeg-n9.0.2-39-g7adfbe40dc-linux64-gpl-9.0.tar.xz | ffmpeg-n9.0.2-39-g7adfbe40dc-win64-gpl-9.0.zip |
| Archive size | 153175100 bytes | 195110184 bytes |
| Archive SHA-256 | 73e29a56974ae5a651133a04584c7483b21afe941e17dc91d79814a758b608d3 | 693f3e224bc184b40524a62ca47526cfebaf2504414021b92fe75f677ad868b9 |
| ffmpeg entry | bin/ffmpeg, 173534088 bytes | bin/ffmpeg.exe, 165984256 bytes |
| ffmpeg SHA-256 | 384742b75419584d065bbf372613f9f7e8ccf22525b421c16a2ee679a78ee748 | c930b63cb1ee8b7e2eaf9c15161160edd5c9acad34f0303b5855c8eb142a7e8d |
| ffprobe entry | bin/ffprobe, 173300552 bytes | bin/ffprobe.exe, 165783040 bytes |
| ffprobe SHA-256 | ae9051af68f52e7ad5706b442577689c74c02da618ca1590caf1ac7199f2931c | 075848c1bbb8b4e2e0b0ecba06e8ad06c889a8902ed219d6a9f5758c417ca108 |
| Version line | ffmpeg version n9.0.2-39-g7adfbe40dc-20261031 | same |

Entries sit under a top directory named after the archive. The URLs are
https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-10-31-12-59/
followed by the archive name. The values live in
internal/tools/manifest.go.

### How the pins were verified

On 2026-11-23:

1. Downloaded both archives and the release checksums.sha256 file.
2. Compared the SHA-256 of each archive with three sources: the local
   computation, checksums.sha256, and the asset digest that the GitHub API
   reports. All three matched. BtbN publishes no signatures and no GitHub
   artifact attestations, so the trust anchor is the GitHub release of the
   BtbN repository over HTTPS, pinned by hash from then on.
3. Extracted ffmpeg and ffprobe and recorded their sizes and SHA-256.
4. On Debian 13 (glibc 2.41) in an amd64 container, confirmed the version
   lines and that ffmpeg lists the libx264 and mjpeg encoders and the
   drawtext, tile, scale, pad, setsar, transpose, hflip, and vflip
   filters.
5. Ran the Linux ffmpeg on Rocky Linux 8 (glibc 2.28) and Ubuntu 20.04: it
   works. On Alpine (musl) it does not start. The binary is static except
   for glibc, so glibc 2.28 or newer is required; on other systems the
   install fails with a message that points to the PATH and --ffmpeg.

The GPL variant is needed for libx264. It also lists NVENC, QSV, VAAPI,
and AMF, which are not used in this delivery.

### Installer changes

- Artifacts gain ArchiveEntry, the path inside the archive, separate from
  BinaryName, the installed name (ffmpeg.exe on Windows).
- .tar.xz archives are read with github.com/ulikunitz/xz and archive/tar.
  Only the expected entries are written, each checked for type, size, and
  SHA-256, as for ZIP files.
- Tools that share an archive share one lock file,
  tools/.<first 16 hex digits of the archive SHA-256>.lock, and one
  download: the first tool to need the archive extracts every pinned tool
  in it that is missing or invalid. Temporary directories are named
  tools/.tmp-<same digits>-*, so cleanup touches only that archive's
  leftovers. For evermeet each tool keeps its own archive, so the macOS
  behavior is unchanged apart from the lock file location.
- The cached executable is still checked by size and SHA-256 before every
  use. On Linux this reads about 331 MiB for a preview, which takes well
  under a second on an SSD, a cost accepted for the same integrity rule as
  0001.
- On systems without a certificate store, such as minimal Linux images,
  HTTPS verification failed. musafoz now embeds Go's fallback root
  certificates (golang.org/x/crypto/x509roots/fallback). They are used only
  when the system has no roots of its own.

### Windows changes

- Executable checks use the file extension instead of permission bits, in
  resolution and in cache verification.
- FFmpeg filters no longer receive absolute paths. FFmpeg runs with the
  work directory as its current directory, and filters name the font and
  header files by plain relative names. A drive letter, backslashes, or
  spaces in the temporary directory therefore need no escaping, and the
  earlier refusal of such temporary directories is gone.
- The overlay font is copied into the work directory instead of linked,
  because symbolic links need extra privileges on Windows. Which font is
  used is decided in 0006.
- Output-set and cache locks use LockFileEx (golang.org/x/sys/windows)
  with an exclusive, non-blocking request retried every 200 ms, like flock
  on Unix. The system releases the lock if the process dies. Before this
  change the Windows lock did nothing. Platforms with neither flock nor
  LockFileEx now fail instead of running unlocked.
- Publishing is unchanged. A new output is placed with a hard link, which
  fails instead of replacing an existing file; os.Link reports that as
  "exists" on NTFS too. A replacement uses os.Rename, which on Windows is
  MoveFileEx with MOVEFILE_REPLACE_EXISTING. Together with the lock, two
  runs on the same outputs run one after the other, and the second sees
  the first one's outputs as conflicts unless --overwrite is given. A file
  that another program holds open cannot be replaced on Windows; the run
  then fails and restores the earlier set.

### Embedded font

Minimal Linux images ship no fonts, and the preview used to stop at the
font step there. musafoz first embedded DejaVu Sans Mono 2.37 as a last
resort after installed fonts; 0006 then made the embedded font the default
on every platform and removed the lookup of installed fonts. 0006 records
the font source and license.

## Validation

### Linux x86-64

Run in Docker on the Intel Mac (linux/amd64 containers, not emulated).
This is a real Linux kernel and userland but not a separate physical
machine.

- Debian 13 slim, empty cache, no ffmpeg or ffprobe on the PATH, no
  certificate store: inspect downloaded the archive once (146.1 MiB),
  installed both tools, and reported "no ffprobe on the PATH". Before the
  embedded font, the preview then stopped at the font step. With
  fonts-dejavu-core installed, previews of a 4:3 clip, its rotated copy,
  and the 4K HEVC phone clip from 0003 produced all three outputs:
  H.264 previews in yuv420p at 4 fps with 100 frames and SAR 1:1, covers
  at the displayed size, and mosaics of 2880x2200, 2160x2920, and
  2160x3880.
- The same cache with --network none: the preview used the cached builds
  and downloaded nothing.
- Fake ffmpeg and ffprobe 4.4 first on the PATH: both skipped as too old,
  managed builds used. A fake ffmpeg 7.1 without libx264 and drawtext:
  skipped with the missing features listed.
- Debian's ffmpeg 7.1.5 installed: both tools taken from the PATH, outputs
  correct.
- An invalid --ffprobe path and an old MUSAFOZ_FFMPEG: errors name the
  flag or variable and the path.
- go test -race ./... and the integration tests, with
  MUSAFOZ_TEST_CACHE_DIR pointing at the cache, pass in a golang:1.27.3
  Debian container. With DejaVu Sans Mono, the overlay area of the
  320x180 case was 23 percent below the 16:9 reference, outside the 20
  percent the aspect test allowed; with Andale Mono on macOS it was
  inside. Overlay sizes are whole frame pixels, and a 320-pixel frame is
  enlarged 1.2 times into its 384-pixel cell, so the box border of 1.33
  pixels becomes 1 and the error grows. The font size itself is within 3
  percent. The test now allows 30 percent for frames narrower than their
  cell and keeps 20 percent for the others, which measured within 4
  percent.

Pending: a run on a physical or virtual Linux machine outside Docker,
following docs/validation.md.

### Windows x86-64

Done on macOS: the binary and all test binaries build for windows/amd64,
go vet passes for windows, and the unit tests cover the .exe rules, the
shared-archive install, relative filter names, and a lock held by a
second process (run on macOS).

Done on a Windows x86-64 machine by the owner, reported on 2026-11-23,
with a build from before --cover-at and the dynamic mosaic grid of 0007:

- musafoz inspect worked.
- musafoz preview produced the outputs with 25 and with 100 frames.
- FFmpeg and FFprobe installed on the PATH were found and used.

Pending on Windows, following docs/validation.md:

- First run with an empty cache and no tools on the PATH, which downloads
  the managed archive.
- Offline reuse of the cached managed builds.
- The other checks in docs/validation.md that were not reported: explicit
  paths with and without .exe, and two concurrent runs on the same outputs.
- A run of the current build, with --cover-at and the dynamic grid.

## Consequences

- First use on Linux or Windows downloads one archive of 146 or 186 MiB.
  Installed builds take about 331 or 316 MiB.
- Linux and Windows results can differ slightly from macOS because the
  FFmpeg versions differ (9.0.2 against 9.1).
- The builds are GPL licensed and downloaded at run time; musafoz does not
  redistribute them.
- To move to a newer BtbN release, repeat the verification above with a
  month-end release, update the manifest and this record together, and
  keep Linux and Windows on the same release.
