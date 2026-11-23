# 0002 Managed FFmpeg

Status: accepted; platform support and resolution order amended by 0005
Date: 2026-11-23

## Context

The preview workflow needs FFmpeg for remuxing, frame extraction, H.264
encoding, and the mosaic. Like FFprobe (see 0001), it must work without a
manual installation and must not depend on the PATH.

## Decision

FFmpeg reuses the FFprobe mechanism unchanged:

- The manifest in internal/tools/manifest.go holds one entry per tool and
  platform. LookupArtifact(tool, goos, goarch) replaces the FFprobe-only
  lookup.
- The same Manager installs it into tools/ffmpeg/<version>-<os>-<arch>/ with
  its own lock file, the same archive and binary size and SHA-256 checks, the
  same version check, and the same atomic rename.
- Resolution order: --ffmpeg, then MUSAFOZ_FFMPEG, then the managed cache.
  The PATH is never searched. Explicit paths follow the compatibility policy
  of 0001: the program must report "ffmpeg version", and release builds must
  be 6.0 or newer.
- Before processing, musafoz checks that the resolved FFmpeg offers the
  libx264 and mjpeg encoders and the drawtext, tile, scale, pad, and setsar
  filters. This applies to managed and explicit builds.

The supported platform stays darwin/amd64. 0005 adds Linux and Windows
x86-64 builds and the PATH search on those platforms.

### Pinned build

| Field | Value |
| --- | --- |
| Vendor | evermeet.cx static FFmpeg builds for macOS |
| Release | FFmpeg 9.1 (reported as "ffmpeg version 9.1-tessus") |
| URL | https://evermeet.cx/ffmpeg/ffmpeg-9.1.zip |
| Archive size | 26555632 bytes |
| Archive SHA-256 | 9962f7b3c01af9ac89673c91df9ef998343a280ac0857c295e2d2a301820598a |
| Binary entry | ffmpeg, 81778888 bytes, Mach-O 64-bit x86_64 |
| Binary SHA-256 | 1e3026f19f8b8d67c5fa0987aa3bac376d45c7b0e0485dc22515fc011546f8c2 |

As with FFprobe, the versioned URL redirects (HTTP 302) to the vendor mirror
e.deolaha.ca over HTTPS. The pinned size and SHA-256 are checked before the
archive is opened.

### How the pin was verified

On 2026-11-23, before pinning the hashes:

1. Downloaded the ZIP and its detached signature
   (https://evermeet.cx/ffmpeg/ffmpeg-9.1.zip.sig).
2. Downloaded the vendor signing key from https://evermeet.cx/ffmpeg/0x1A660874.asc
   and from keys.openpgp.org. Both hold the key with fingerprint
   20F6 EA3E 0CFD 6B4C 5344 7A73 476C 4B61 1A66 0874, the same key used for
   FFprobe in 0001. The copy from the vendor site was byte-identical to the
   one fetched on 2026-11-22.
3. Verified the detached signature against each key copy with the same
   OpenPGP verifier as in 0001. Both reported a good signature from that
   fingerprint.
4. Computed the archive SHA-256, extracted the binary, computed its SHA-256,
   confirmed that "ffmpeg -version" reports 9.1-tessus, and confirmed that
   the build lists libx264, drawtext (libfreetype), and tile.

### Validation note

The first integration runs downloaded FFmpeg into empty caches. After
repeated downloads on 2026-11-23 the vendor answered HTTP 429 Too Many
Requests, and musafoz reported the status with the URL. Later preview
integration runs used an existing cache (MUSAFOZ_TEST_CACHE_DIR) that had
been populated by musafoz and that it verified by size and SHA-256 before
each use.

To avoid repeating this, the integration tests now use the regular musafoz
cache, or MUSAFOZ_TEST_CACHE_DIR, by default. Downloading both builds into
an empty cache is a separate test, TestIntegrationDownload, that runs only
with MUSAFOZ_TEST_DOWNLOAD=1.

## Consequences

- First use of the preview command downloads about 25 MiB more.
- The binary is not code signed. The notes in 0001 about Intel Macs,
  Gatekeeper, and Apple Silicon apply unchanged.
- Updating FFmpeg and FFprobe together keeps one tested pair. Both entries
  should move to a new release at the same time.
