# 0001 Managed FFprobe

Status: accepted; platform support and resolution order amended by 0005
Date: 2026-11-22

## Context

The first MVP delivery inspects a local video with FFprobe. The workflow must
work without a manual FFmpeg installation, and the result must not depend on
whatever ffprobe happens to be on the PATH.

## Decision

### Platform

The first supported platform is Intel macOS (darwin/amd64). Linux and
Apple Silicon (darwin/arm64) are left for a later delivery. On any other
platform musafoz reports that no managed build exists and points to
--ffprobe and MUSAFOZ_FFPROBE.

### Source and pinned build

| Field | Value |
| --- | --- |
| Vendor | evermeet.cx static FFmpeg builds for macOS |
| Release | FFprobe 9.1 (reported as "ffprobe version 9.1-tessus") |
| URL | https://evermeet.cx/ffmpeg/ffprobe-9.1.zip |
| Archive size | 26451133 bytes |
| Archive SHA-256 | fe81b028c213d6387a5c6b315692b3ceddcd31eb6dae55bbcae257a862573918 |
| Binary entry | ffprobe, 81576216 bytes, Mach-O 64-bit x86_64 |
| Binary SHA-256 | 272759075855b9d34a37b60dfef2274f599f12fe97735019aa5c763a2bf89e75 |

The versioned URL answers with an HTTP 302 redirect to the vendor download
mirror (e.deolaha.ca, port 4242, HTTPS). musafoz follows the redirect. Trust
does not depend on the mirror, because the archive is checked against the
pinned size and SHA-256 before it is opened.

The values above live in internal/tools/manifest.go.

### How the pin was verified

On 2026-11-22, before pinning the hashes:

1. Downloaded the ZIP and its detached signature
   (https://evermeet.cx/ffmpeg/ffprobe-9.1.zip.sig).
2. Downloaded the vendor signing key from two independent places:
   https://evermeet.cx/ffmpeg/0x1A660874.asc and keys.openpgp.org. Both
   contain one key, "static FFmpeg binaries (signing key)
   <ffmpeg@evermeet.cx>", with the fingerprint published on the vendor page:
   20F6 EA3E 0CFD 6B4C 5344 7A73 476C 4B61 1A66 0874.
3. Verified the detached signature against each key copy with an OpenPGP
   verifier (github.com/ProtonMail/go-crypto, used only for this check and
   not a dependency of musafoz). Both reported a good signature from that
   fingerprint. The same signature failed against altered data.
4. Computed the archive SHA-256, extracted the binary, computed its SHA-256,
   and confirmed that "ffprobe -version" reports 9.1-tessus.

To move to a new release, repeat these steps and update the manifest and this
record together.

### Resolution order

1. The --ffprobe flag.
2. The MUSAFOZ_FFPROBE environment variable.
3. The managed cache, populated on first use.

The PATH is never searched. 0005 keeps this order on macOS and adds a
compatible tool on the PATH before the managed build on Linux and Windows.

### Compatibility policy for explicit paths

An executable given through --ffprobe or MUSAFOZ_FFPROBE must be a regular,
executable file whose "-version" output starts with "ffprobe version". A
release build must be version 6.0 or newer, otherwise musafoz refuses it.
Snapshot builds whose release number cannot be read (for example
"ffprobe version N-121000-g...") are accepted with a warning on stderr.
Explicit binaries are not hash-checked. The user is responsible for them.
Tested versions are 9.1 (managed) and 8.0.1 (explicit path).

### Cache, integrity, and concurrency

- Location: MUSAFOZ_CACHE_DIR when set, otherwise the user cache directory
  (~/Library/Caches/musafoz on macOS). The binary lives at
  tools/ffprobe/9.1-darwin-amd64/ffprobe.
- Before every use, the cached binary is checked for type, size, executable
  bit, and SHA-256. A binary that fails these checks is removed and
  reinstalled.
- Install steps: take an exclusive flock on tools/ffprobe/.lock, re-check the
  cache, delete stale .tmp-* directories, download into a new .tmp-*
  directory, check the archive size and SHA-256, extract only the expected
  entry, check its size and SHA-256, fsync, run "-version" and compare the
  prefix, then rename the staging directory into place. A process that finds
  the lock held prints a waiting message and polls until the lock is free.
  The final directory only appears through the rename, so an interrupted
  install never leaves a partial binary at the final path.
- Progress and status go to stderr. With --json, stdout carries only the JSON
  document.

## Consequences

- First use needs network access and about 25 MiB of download. Later runs
  work offline.
- Verifying the 78 MiB binary before every run adds about 0.2 seconds on
  the Intel Mac used for validation.
- The evermeet binary is not code signed. On Intel Macs it runs without a
  signature. Go downloads do not set com.apple.quarantine, and this was
  validated with Gatekeeper assessments and System Integrity Protection
  enabled. Apple Silicon requires at least an ad-hoc signature. Handle that
  when darwin/arm64 is added.
- The build is GPL licensed. musafoz downloads it at run time and does not
  redistribute it in the repository.
- FFmpeg itself (needed for previews) is not managed yet. It should follow
  the same manifest and verification process in the next delivery.
