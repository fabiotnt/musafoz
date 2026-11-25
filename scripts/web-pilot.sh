#!/usr/bin/env bash
# Reproducible Linux pilot of `musafoz web`. See docs/web-pilot.md.
#
# usage: scripts/web-pilot.sh <sources-dir> <list> <results-dir> [kill-after-seconds]
#
# <list> names files relative to <sources-dir>, one per line. The sources
# are copied once into a Docker volume and mounted read only. Each phase
# runs in a fresh container, so its cgroup memory and I/O counters are its
# own. Outputs go to a Docker volume, standing for the local temporary disk
# of a Cloud Run task. Results and measurements go to <results-dir>.
set -euo pipefail

SRC=$(cd "$1" && pwd)
LIST=$2
RES=$(mkdir -p "$3" && cd "$3" && pwd)
KILL_AFTER=${4:-20}
REPO=$(cd "$(dirname "$0")/.." && pwd)
BASE=${PILOT_BASE_IMAGE:-debian:13-slim}
TAG=musafoz-web-pilot:local
VOL=musafoz-web-pilot

cd "$REPO"
{
  echo "revision=$(git rev-parse HEAD)"
  echo "uncommitted_files=$(git status --porcelain | wc -l | tr -d ' ')"
  echo "uncommitted_diff_sha256=$(git diff HEAD | shasum -a 256 | cut -d' ' -f1)"
  echo "go=$(go version)"
} > "$RES/build.txt"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o "$RES/musafoz" ./cmd/musafoz
echo "binary_sha256=$(shasum -a 256 "$RES/musafoz" | cut -d' ' -f1)" >> "$RES/build.txt"

docker pull -q "$BASE" > /dev/null
printf 'FROM %s\nRUN apt-get update -qq && apt-get install -y -qq --no-install-recommends time ca-certificates > /dev/null && rm -rf /var/lib/apt/lists/*\n' "$BASE" |
  docker build -q -t "$TAG" - > /dev/null
echo "base_image=$BASE $(docker image inspect --format '{{.Id}}' "$BASE")" >> "$RES/build.txt"

for v in src work cache; do docker volume rm -f "$VOL-$v" > /dev/null 2>&1 || true; docker volume create "$VOL-$v" > /dev/null; done
docker run --rm -v "$SRC":/in:ro -v "$VOL-src":/src "$TAG" sh -c 'cp -a /in/. /src/'

# phase NAME [timeout] -- musafoz arguments
phase() {
  local name=$1 limit=$2
  shift 3
  echo "phase $name"
  docker run --rm --privileged \
    -v "$VOL-src":/src:ro -v "$VOL-work":/work -v "$VOL-cache":/cache -v "$RES":/res \
    -e MUSAFOZ_CACHE_DIR=/cache -w /src "$TAG" sh -c '
      name=$1 limit=$2; shift 2
      sync; echo 3 > /proc/sys/vm/drop_caches
      run() { /usr/bin/time -f "elapsed_s=%e user_s=%U sys_s=%S max_rss_kb=%M fs_inputs_512b=%I" -o /res/$name.time /res/musafoz "$@"; }
      if [ "$limit" = 0 ]; then run "$@" > /res/$name.out 2> /res/$name.err; echo $? > /res/$name.exit
      else timeout -s KILL "$limit" /usr/bin/time -f "elapsed_s=%e user_s=%U sys_s=%S max_rss_kb=%M" -o /res/$name.time /res/musafoz "$@" > /res/$name.out 2> /res/$name.err; echo $? > /res/$name.exit; fi
      cat /sys/fs/cgroup/memory.peak > /res/$name.memory_peak 2>/dev/null || true
      cat /sys/fs/cgroup/io.stat > /res/$name.io_stat 2>/dev/null || true
    ' sh "$name" "$limit" "$@"
}

phase full 0 -- web batch "$LIST" --output /work/out --metadata /work/out --jsonl
phase reuse 0 -- web batch "$LIST" --output /work/out --metadata /work/out --skip-existing --jsonl
phase killed "$KILL_AFTER" -- web batch "$LIST" --output /work/out2 --metadata /work/out2 --jsonl
phase resume 0 -- web batch "$LIST" --output /work/out2 --metadata /work/out2 --skip-existing --jsonl
phase thumbs 0 -- thumb batch "$LIST" --size 480x480 --mode fill --quality 80 --output /work/thumbs --jsonl

# FFmpeg features, outputs, and leaks.
docker run --rm -v "$VOL-work":/work:ro -v "$VOL-cache":/cache:ro -v "$RES":/res "$TAG" sh -c '
  ff=$(ls /cache/tools/ffmpeg/*/ffmpeg | head -1); fp=$(ls /cache/tools/ffprobe/*/ffprobe | head -1)
  { echo "ffmpeg=$ff"; $ff -hide_banner -version | head -1
    for f in zscale tonemap scale setparams; do $ff -hide_banner -filters 2>/dev/null | grep -q " $f " && echo "filter $f: yes" || echo "filter $f: MISSING"; done
    for e in libx264 aac; do $ff -hide_banner -encoders 2>/dev/null | grep -q " $e " && echo "encoder $e: yes" || echo "encoder $e: MISSING"; done
    for d in hevc h264; do $ff -hide_banner -decoders 2>/dev/null | grep -q " $d " && echo "decoder $d: yes" || echo "decoder $d: MISSING"; done
  } > /res/ffmpeg.txt
  for f in /work/out/*.mp4; do echo "== $(basename $f)"; $fp -v error -show_entries stream=codec_name,profile,width,height,pix_fmt,color_transfer,color_primaries,bit_rate:format=duration,bit_rate:format_tags -of compact "$f"; done > /res/videos.txt
  { for d in out out2 thumbs; do echo "== $d"; ls -la /work/$d; du -sb /work/$d; done; } > /res/files.txt
  : > /res/leaks.txt
  # Markers of metadata blocks and tags. Short strings such as "GPS" are not
  # searched: they occur by chance in compressed image and video data.
  for f in /work/out/*.jpg /work/out/*.mp4 /work/thumbs/*; do
    for s in Exif iPhone xmpmeta ICC_PROFILE ISO6709 com.apple.quicktime; do grep -aq "$s" "$f" && echo "$(basename $f): $s" >> /res/leaks.txt; done
  done
  for f in /work/out/*.mp4; do $fp -v error -show_entries format_tags:stream_tags -of compact "$f" | grep -iE "location|creation|make|model|quicktime" | sed "s|^|$(basename $f): |" >> /res/leaks.txt; done
  cmp -s /work/out/$(ls /work/out | grep -m1 "\.json$") /work/out2/$(ls /work/out | grep -m1 "\.json$") && echo same > /res/records.txt || echo differ > /res/records.txt
'
echo "done: $RES"
