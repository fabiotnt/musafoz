# Web copies: pilot and production runs

This guide covers a reproducible Linux pilot of `musafoz web`, how a Cloud
Run job runs it, how its files go to a Cloud Storage bucket with
fine-grained access, and the checks to do before the first production
batch. Decisions are in
[0012](decisions/0012-web-copies.md).

## Local Linux pilot

`scripts/web-pilot.sh` builds a linux/amd64 binary of the current tree,
records the revision, the SHA-256 of any uncommitted diff, and the binary,
and runs every phase in its own container of a pinned `debian:13-slim`
image with GNU time. FFmpeg is the managed BtbN build that musafoz
downloads and verifies, the same build a Cloud Run job without FFmpeg on
the PATH uses.

```sh
scripts/web-pilot.sh <sources-dir> list.txt <results-dir> 25
```

- The sources are copied into a Docker volume once and mounted read only,
  standing for the mounted source bucket. Outputs go to another volume,
  standing for the local disk of a task.
- Phases: `full` (every source), `reuse` (`--skip-existing`), `killed`
  (a new output directory, killed with SIGKILL after the given seconds),
  `resume` (`--skip-existing` on the killed directory), and `thumbs`
  (`thumb batch --size 480x480 --mode fill --quality 80 --jsonl`).
- Each phase drops the page cache first and records `<phase>.time`
  (elapsed seconds, peak RSS of musafoz and its children), the cgroup
  `memory.peak` (which includes the page cache), and `io.stat` (`rbytes`
  is the bytes read from the volume).
- The last step lists the FFmpeg filters, encoders, and decoders the
  conversion needs (`zscale`, `tonemap`, `setparams`, `libx264`, `aac`,
  `hevc`), probes the web videos, looks for metadata markers in every
  output, and lists sizes.

Use a list with at least a phone JPEG, a PNG screenshot, a HEIC with an odd
grid width or an alpha grid, a large HDR video, and the same file in two
folders. The list names files relative to the sources directory. Keep
library files and results out of the repository.

## Cloud Run job

- Mount the source bucket read only (`--add-volume
  name=src,type=cloud-storage,bucket=<source-bucket>,readonly=true`).
  `web` reads every original twice: once for the SHA-256 that makes the
  UUID, once to convert it. A reused content still costs the first read.
- Write outputs to local temporary storage, one directory per task, with
  `--output` and `--metadata` set to the same directory. The local disk of
  Cloud Run is in memory: size the task memory for the peak RSS measured
  below plus the largest outputs. Upload and remove each finished content,
  or a few at a time, to keep the disk small. Never upload
  `.musafoz-staging/` or `.<uuid>.musafoz-pending.json`.
- Split the list between tasks with `CLOUD_RUN_TASK_INDEX` and
  `CLOUD_RUN_TASK_COUNT`, one list per task, and keep each task's JSONL
  report. A task that is retried runs the same list again with
  `--skip-existing`.

## Two tasks and one UUID

The same content in two folders gives the same UUID. If those folders fall
in different tasks, both make the same outputs. Within one output
directory musafoz serializes the work on a UUID with a lock. Across tasks,
make one task the owner of each UUID at upload time:

1. Before uploading a content, create a claim object with a precondition
   that it does not exist, for example `gcloud storage cp --if-generation-match=0
   --predefined-acl=private claim.txt gs://<bucket>/Musa-Web/.claims/<uuid>`.
2. The task whose claim succeeds uploads the media and then the record. A
   task whose claim fails skips the upload and reports the line as a
   duplicate of the other task. It never replaces the other task's files.
3. A retried task that already owns the claim uploads again: musafoz made
   the same outputs, and the record is uploaded last.

Deduplicating the list before splitting it avoids most of these cases.
Cheap keys such as name and size find likely copies, but only the SHA-256
proves them.

## Upload with per-object access

Media and records of a UUID live in the same folder. Privacy is set on
each object. Before the first upload, check the bucket:

```sh
gcloud storage buckets describe gs://<bucket> \
  --format="yaml(uniform_bucket_level_access,public_access_prevention,default_object_acl)"
gcloud storage buckets get-iam-policy gs://<bucket> --format=json
```

- Uniform bucket-level access must be off (fine-grained), and public access
  prevention must not be enforced, or no object can be public.
- The IAM policy must not grant `allUsers` or `allAuthenticatedUsers` any
  role that reads objects, such as `roles/storage.objectViewer` or
  `roles/storage.legacyObjectReader`. Such a grant makes every object
  public, the records too, whatever their ACL says.
- Set the default object ACL to private
  (`gcloud storage buckets update gs://<bucket> --predefined-default-object-acl=private`),
  so an object uploaded without an explicit ACL is private.

Upload private by default, and make public only the media the site serves:

```sh
# The media files of finished content, from the task report
jq -r 'select(.status=="ok" or .status=="resumed" or .status=="exists") | .files[]' report.jsonl | sort -u > media.txt
# 1. media, public, never replacing another task's object
gcloud storage cp -I gs://<bucket>/Musa-Web/ --predefined-acl=publicRead --no-clobber < media.txt
# 2. records last, always private, also when one is replaced on a resume
jq -r 'select(.metadata) | .metadata' report.jsonl | sort -u |
  gcloud storage cp -I gs://<bucket>/Musa-Web/ --predefined-acl=private
```

- Pass `--predefined-acl=private` on every record upload. A replaced object
  is a new object, and it gets the ACL of that upload, not the one of the
  object it replaces.
- Keep JSONL reports out of the public folder: they map original paths to
  UUIDs. Store them private, for example under a reports prefix with
  `--predefined-acl=private`.

## Access checks

For one image and one video of the pilot, after the upload:

```sh
# 1. The media opens without authentication: 200
curl -s -o /dev/null -w "%{http_code}\n" https://storage.googleapis.com/<bucket>/Musa-Web/<uuid>.jpg
# 2. Anonymous reading of its record is denied: 403 (or 401)
curl -s -o /dev/null -w "%{http_code}\n" https://storage.googleapis.com/<bucket>/Musa-Web/<uuid>.json
# 3. The importer identity reads the record
gcloud storage cat gs://<bucket>/Musa-Web/<uuid>.json \
  --impersonate-service-account=<importer-service-account> | head -c 200
# The record ACL has no allUsers or allAuthenticatedUsers entry
gcloud storage objects describe gs://<bucket>/Musa-Web/<uuid>.json --format="json(acl)"
```

Run check 2 again after a resume that replaced a record, and re-read the
bucket IAM policy just before the first production batch.

## Results

### Linux pilot, 2026-11-25

Docker Desktop on the Intel Mac: a linux/amd64 VM with 12 CPUs and 7.7 GiB,
`debian:13-slim` (sha256:8aa5a7bf5a19...), musafoz built from 3b6809e with
the uncommitted changes of this review, and managed FFmpeg
n9.0.2-39-g7adfbe40dc. All the needed filters, encoders, and decoders were
present.

Sources: six lines, 300 MB: a phone JPEG (3664x2062), the same JPEG in
another folder, a PNG screenshot (1170x2532), a HEIC whose alpha plane is a
grid, a HEIC with an odd grid width (1737x3088), both of which failed
before 0011, and a 4K HLG HEVC video of 294 MB and 49 seconds, portrait.

| Phase | Elapsed | Peak RSS | cgroup memory.peak | Bytes read | Statuses |
| --- | --- | --- | --- | --- | --- |
| full | 185 s | 1.81 GiB | 2.78 GiB | 306 MiB | 5 ok, 1 duplicate |
| reuse | 2.4 s | 42 MiB | 0.52 GiB | 508 MiB | 5 exists, 1 duplicate |
| killed at 25 s | 25 s | n/a | 2.92 GiB | 635 MiB | 4 ok, 1 duplicate, video cut |
| resume | 430 s | 1.83 GiB | 2.48 GiB | 636 MiB | 4 exists, 1 duplicate, video ok |
| thumbs | 62 s | 200 MiB | n/a | 172 MiB | 4 ok, 2 errors (expected) |

- SHA-256: 1.2 seconds for the 294 MB video, under 15 ms for each photo.
  Checking the outputs of a finished content took 0.14 seconds for the
  video and its previews.
- The first HEIC took 53 seconds in `full` because FFmpeg was downloaded
  and verified on first use; 2 seconds in later phases.
- The video conversion, with its 100-frame preview, took 126 seconds in
  `full` and 422 seconds in `resume`. Run alone afterwards, it took 440
  seconds without the preview and 405 seconds with it, with 1.8 GiB peak
  RSS both times. The container used about 16 of the host's logical CPUs,
  and the laptop slowed down under sustained load, so on this machine wall
  time does not measure the cost of the conversion or of the preview. The
  script now also records user and system CPU seconds.
- After the kill, the four images were complete and the video had no file
  in the output directory: it was cut while being made in staging. The
  resumed run reused the images and made the video. A record compared
  between the two output directories was identical.
- The web video: H.264 High, yuv420p, BT.709, 1080x1920, 4.9 Mbit/s video
  and 130 kbit/s AAC, 31.1 MB, with a 7.4 MB preview clip, a 0.36 MB
  mosaic, and a 0.09 MB cover. Dated from its QuickTime tag with the
  offset. The preview clip carries no color tags, as `preview` makes it.
- No output had metadata markers. A first check that looked for "GPS"
  matched once inside the compressed video data (`mdat`), by chance; the
  `moov` box has no location, QuickTime, or device tags. The check now
  looks only for longer markers and for container tags.
- `thumb batch` gave 480x480 thumbnails for the four images. The second
  copy of the JPEG collided with the first by name, and the video is not
  an image, both as designed.
- Web images were 0.18 to 0.49 MB and thumbnails 41 to 45 KB. Records
  were 1.7 to 17 KB for images and 13 KB for the video. All outputs of the
  six lines: 40.6 MB, of which 38.9 MB is the video and its previews.

## Pending for the Cloud Run pilot

- The same phases on Cloud Run, with the source bucket mounted read only
  and the outputs in memory: time, peak memory, and bytes read from the
  mount.
- The upload with the claims and ACLs above, and the three access checks.
- Video conversion time and CPU seconds on Cloud Run CPUs, which the
  laptop could not measure reliably, to size tasks and the job timeout.
