# Platform validation

These checks confirm that musafoz works on a machine without FFmpeg or
FFprobe. Use a sample video of your own; the commands below call it
clip.mp4. Record the results in docs/decisions/0005-linux-and-windows-tools.md.

## Linux x86-64

On the Linux machine (no font package is needed; the overlay font is built
in):

```sh
GOOS=linux GOARCH=amd64 go build -o musafoz ./cmd/musafoz   # on any machine
command -v ffmpeg ffprobe                                   # expect nothing
export MUSAFOZ_CACHE_DIR="$(mktemp -d)"                     # empty cache
./musafoz inspect clip.mp4                                  # downloads once
./musafoz preview --json clip.mp4                           # three outputs
ls clip_cover.jpg clip_preview.mp4 clip_preview.jpg
```

Then repeat the preview without network access, for example with the
network disabled or inside `unshare -rn` where available, and check that
stderr shows no download:

```sh
./musafoz preview --overwrite clip.mp4
```

With a distribution FFmpeg installed, stderr must say "found on the PATH".

## Windows x86-64

Build on any machine:

```sh
GOOS=windows GOARCH=amd64 go build -o musafoz.exe ./cmd/musafoz
```

In PowerShell on the Windows machine. The folder name has spaces on
purpose:

```powershell
Get-Command ffmpeg, ffprobe -ErrorAction SilentlyContinue   # expect nothing
$env:MUSAFOZ_CACHE_DIR = "$env:TEMP\musafoz cache test"     # empty cache
New-Item -ItemType Directory "C:\musafoz test\media" -Force
Copy-Item clip.mp4 "C:\musafoz test\media\clip.mp4"
.\musafoz.exe inspect "C:\musafoz test\media\clip.mp4"
.\musafoz.exe preview --json "C:\musafoz test\media\clip.mp4"
Get-ChildItem "C:\musafoz test\media"
```

Also run `.\musafoz.exe preview --frames 25 --cover-at 00:00:01.500 --overwrite "C:\musafoz test\media\clip.mp4"`:
the mosaic must show a 5 by 5 grid and the JSON or text output must give
the cover time 1.5 seconds.

Expect "using ffprobe ...\ffprobe.exe (managed ...; no ffprobe on the
PATH)" and "overlay font: DejaVu Sans Mono 2.37 (embedded)" on stderr, a
progress line during the long steps, one download of about 186 MiB, and clip_cover.jpg,
clip_preview.mp4, and clip_preview.jpg in the folder.

Offline reuse: disconnect the network, then run
`.\musafoz.exe preview --overwrite "C:\musafoz test\media\clip.mp4"`. It
must succeed without a download.

Explicit paths:

```powershell
.\musafoz.exe inspect --ffprobe "C:\does not exist\ffprobe.exe" "C:\musafoz test\media\clip.mp4"
$p = Get-ChildItem "$env:MUSAFOZ_CACHE_DIR\tools\ffprobe" -Recurse -Filter ffprobe.exe
.\musafoz.exe inspect --ffprobe ($p.FullName -replace '\.exe$', '') "C:\musafoz test\media\clip.mp4"
```

The first must fail and name --ffprobe; the second must work without the
.exe extension.

Two runs on the same outputs: remove the outputs, then start two previews
at once. One must succeed and the other must wait and then stop with
"outputs already exist", leaving the first run's files intact:

```powershell
Remove-Item "C:\musafoz test\media\clip_*"
$a = Start-Process .\musafoz.exe -ArgumentList 'preview "C:\musafoz test\media\clip.mp4"' -NoNewWindow -PassThru -RedirectStandardError a.txt
$b = Start-Process .\musafoz.exe -ArgumentList 'preview "C:\musafoz test\media\clip.mp4"' -NoNewWindow -PassThru -RedirectStandardError b.txt
$a.WaitForExit(); $b.WaitForExit()
$a.ExitCode; $b.ExitCode; Get-Content a.txt, b.txt | Select-String "exist|finished|waiting"
Get-FileHash "C:\musafoz test\media\clip_*"
```

Tools on the PATH: install FFmpeg 6 or newer (for example with
`winget install Gyan.FFmpeg`), open a new PowerShell, and run the preview
with `--overwrite`. stderr must say "found on the PATH".

Tests: with Go installed, `go test ./...` runs the unit tests that do not
need shell scripts, including the lock test across two processes, and

```powershell
$env:MUSAFOZ_TEST_CACHE_DIR = $env:MUSAFOZ_CACHE_DIR
go test -tags integration -count=1 ./internal/cli/
```

runs the end-to-end tests with the managed Windows builds.
