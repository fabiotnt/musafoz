# 0004 Hardware acceleration for previews

Status: accepted
Date: 2026-11-23

## Context

Frame extraction was the slowest preview step for long-GOP sources (see the
performance note in 0003). This decision adds hardware acceleration on
macOS through VideoToolbox. The reference script used CPU frame extraction
and selected h264_nvenc for MP4 encoding when it detected NVIDIA NVENC
support, with libx264 as the fallback. MusaFoz defers NVIDIA and AMD
acceleration on Linux and Windows until both can be tested on compatible
hardware.

## Decision

### Modes

`preview --accel auto` (the default) uses VideoToolbox on macOS for frame
decoding and for the preview video where a probe confirms that it works;
`--accel cpu` forces software decoding and libx264 for diagnosis and
comparison. Decoding and encoding are chosen separately, so either can use
the hardware while the other runs on the CPU. The cover is always decoded
on the CPU; it is a single frame. The mosaic is built from the extracted
JPEG frames on the CPU.

### Decoding

The input is validated with FFprobe first, as before. Then, before frame
extraction:

1. Triage: `ffmpeg -hwaccels` must list videotoolbox, and the decoded pixel
   format must be one VideoToolbox frames can be downloaded to (yuv420p and
   yuvj420p as nv12, yuv420p10le as p010le).
2. Probe: decode two frames of the input with
   `-hwaccel videotoolbox -hwaccel_output_format videotoolbox_vld` and an
   explicit `hwdownload`. With the frames kept on the GPU, a software
   fallback inside FFmpeg makes `hwdownload` fail, so success proves that
   the hardware decoder handled this input. Requesting `-hwaccel` alone is
   not enough: FFmpeg then falls back to software silently, for example for
   PNG input.

Extraction uses the same options. FFmpeg does not apply the rotation
metadata to hardware frames, so the hardware path adds `-noautorotate` and
applies the rotation after `hwdownload` (`transpose=clock` for 90 degrees
clockwise, `transpose=cclock` for 270, `hflip,vflip` for 180). Frames from
both paths were identical (SSIM 1.000000) on the reference clip.

VideoToolbox extraction uses half as many parallel ffmpeg processes as there
are CPUs: on the 12-thread Intel Mac used for validation, 12, 6, and 3
processes took 40.3, 38.5, and 49.1 seconds for the same 100 frames.

### Encoding

In auto mode on macOS, musafoz checks that FFmpeg lists h264_videotoolbox
and runs a test encode at the real preview size. If either check fails,
the preview video is encoded with libx264. The size matters: on the Intel
Mac the encoder rejected 256x256, 320x180, and 320x240 (error -12903) but accepted
the other preview sizes in the integration test. The encoder does not fall
back to software by default (allow_sw is 0), so a passing test encode
means the hardware encoder is used. The target bit rate is 0.62 bits per
pixel per frame at 4 fps, which matched the libx264 output size on the
reference clip.

The hardware encoder was accepted for auto mode after reviewing the
measurements below: on the reference clip it saved about 20 seconds at a
small, measurable quality cost. `--accel cpu` keeps libx264 available.

### Fallback

If a hardware step fails, that step is retried once on the CPU (frame
extraction) or with libx264 (preview encoding). The libx264 retry reuses
the frames already extracted. A failure on both paths, such as a damaged
file, reports the CPU error, so media errors keep their message. A
cancellation is never retried.

### Reporting

Standard error names the backend chosen for each step with its reason, any
fallback with the hardware error, the time of the extraction and encoding
steps, and the total time. The JSON result reports `frame_decoder` and
`preview_encoder` after any fallback.

## Measurements

These are results for one clip on one machine. They show the effect of each
path on this input; they do not predict the gain or the quality cost for
other sources, resolutions, or Macs.

Reference clip: the 15.4-second iPhone video from 0003 (HEVC Main,
3840x2160 at 60 fps, rotated 90 degrees, displayed as 2160x3840). Default
preview settings: 100 frames, 4 fps, overlay and mosaic as in 0003. Intel
Core i7-9750H with Intel UHD 630 and Radeon Pro 5300M, macOS 26.7, FFmpeg
9.1. Each run started after the 1-minute load average dropped below 3;
earlier runs on a hot, loaded machine took about twice as long and are not
used. The third row is the accepted auto mode.

| Configuration | Extraction | MP4 encoding | Total | CPU time |
| --- | --- | --- | --- | --- |
| CPU decode, libx264 | 79.9 s | 30.4 s | 116.0 s | 963 s |
| VideoToolbox decode, libx264 | 32.3 s | 23.2 s | 61.2 s | 278 s |
| VideoToolbox decode and encode | 32.1 s | 2.8 s | 39.9 s | 64 s |

Output checks on the same clip, against the CPU run:

- All three runs: cover 2160x3840, preview 2160x3840 H.264 High, yuv420p,
  limited range, SAR 1:1, DAR 9:16, 4 fps, 100 frames, no rotation
  metadata; mosaic 2160x3880.
- VideoToolbox decode with libx264: mosaic and preview frames matched the
  CPU run (SSIM 1.000000), and the MP4 had the same file size.
- VideoToolbox decode and encode: mosaic identical; preview video SSIM
  0.985 against libx264 at a similar size (65.0 MB against 64.6 MB). In a
  separate encode-only test, libx264 scored 0.992 against the source frames
  and h264_videotoolbox at the same bit rate 0.987.

The integration test runs every aspect ratio case from 0003 in both modes
and requires VideoToolbox decoding in auto mode on macOS; geometry, SAR,
DAR, rotation, orientation, and overlay size match the CPU path.
