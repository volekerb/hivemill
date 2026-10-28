# Launch demo video

A silent 60-90 s product demo (1080p, burned-in captions) and a 10-15 s README hero loop. Everything is built from
this repository and the demo seed data, so it can be regenerated after any UI change.

| file | what | where it lives |
|---|---|---|
| `demo.mp4` | H.264, 1920x1080, 30 fps, no audio, under 25 MB | GitHub release asset, not committed |
| `demo.webm` | VP9, same picture, under 25 MB | GitHub release asset, not committed |
| `hero.webp` | animated WebP, 1600 px wide, 18 fps, loops forever, under 5 MB, the org chart without captions | committed (`docs/demo/hero.webp`) and shown at the top of the README |

## Build it

    python3 ci/demo_video.py

That records every scene of `docs/demo/storyboard.json`, encodes the three files into `docs/demo/out/` and then runs
the checks. Allow 10-20 minutes. Run `python3 ci/demo_video.py build` or `python3 ci/demo_video.py check` for one half.

Options for `build`:

- `--draft` allows `{{placeholders}}` in `docs/demo/brand.json` (a review build). Without it the build refuses with
  exit code 3 while any brand value still contains `{{`, so a placeholder can never reach the final video.
- `--browser PATH` uses an already-downloaded `chrome-headless-shell` instead of downloading the pinned one.
- `--out DIR` changes the output directory (default `docs/demo/out`, gitignored). `--work DIR` changes the work
  directory (default `<out>/work`). Neither may be under `/tmp`: it is RAM-backed and the build refuses (exit 3).
- `--only SCENE` records one scene to a clip (for example `--only hook`) and stops, without encoding.
- `--keep-work` keeps the per-scene clips in `<work>/clips`. The frame text dumps in `<work>/text` are always kept
  because `check` scrubs them.

Exit codes: 0 ok, 1 build error or a failed check, 2 usage error, 3 refused (a preflight guard).

## Prerequisites

- `ffmpeg` and `ffprobe` on PATH. On Debian or Ubuntu: `sudo apt install ffmpeg`. The build exits 3 and prints this
  command when they are missing.
- Fonts as described in `docs/images/README.md` (the scenes use DejaVu Sans).
- About 1.5 GB of free RAM (the build refuses below 1500 MB `MemAvailable`) and about 2 GB of disk. The browser
  download (113 MB, checksum-verified) is the only network access; it is cached in `<work>`.

## How it works

Scenes are HTML/CSS pages in `docs/demo/scenes/`. A recorder drives a headless browser over a pipe, seeks the page to
frame `i/30` and takes a PNG, so the result is smooth however slow the machine is. The PNGs are streamed straight
into `ffmpeg` (no frame files on disk), one scene at a time, into a clip per scene. The clips are concatenated into
the MP4 and the WebM. The hero loop records the org chart scene a second time with the captions hidden. The web UI
tour scene is recorded against the demo seed (`ci/demo_seed.py`). One browser and one `ffmpeg` run at a time.

Encoder settings (tune these in `ci/demo_video.py` if a size check fails; never go below 30 fps for the video or 15
fps for the loop):

- scene clips: `libx264 -crf 12 -preset veryfast -pix_fmt yuv420p -threads 4`
- `demo.mp4`: `libx264 -crf 22 -preset slow -pix_fmt yuv420p -r 30 -movflags +faststart`, no audio
- `demo.webm`: `libvpx-vp9 -crf 34 -b:v 0 -row-mt 1 -threads 4`, no audio
- `hero.webp`: `libwebp_anim`, `scale=1600:-2`, `fps=18`, `-loop 0`, `-quality 85` (width, fps and quality come from the `hero` entry of `storyboard.json`)

## Checks

`python3 ci/demo_video.py check` prints one `ok` or `FAIL` line per check and exits 1 on any FAIL:

- `demo.mp4` is H.264 and `demo.webm` is VP9, both 1920x1080, at least 30 fps, 60-90 s, under 25 MB (26214400 bytes).
- `hero.webp` is WebP, 10-15 s, at least 15 fps, under 5 MB (5242880 bytes). `ffprobe` cannot time animated WebP, so
  the duration comes from the frame headers of the file itself.
- The privacy scrub (`ci/demo_scrub.py`) finds nothing in the demo sources or in the text of the sampled frames.

## Privacy and publishing

The video is built only from fictional scene content and the demo seed: no owner name, hostnames, IPs or home paths.
The scrub above runs on every `check`. The boss reviews the final video before anything is published.

`demo.mp4` and `demo.webm` are not committed. The owner uploads them as GitHub release assets at launch; only
`hero.webp` is committed, and it is replaced only on a deliberate rebuild.

## After a UI redesign

Rerun `python3 ci/demo_video.py`. The tour addresses the web UI pages by URL path only (`docs/demo/storyboard.json`),
so a redesign needs no recorder change; a page that stops answering 200 fails the build. Review the new video, then
commit the new `hero.webp` and upload the new MP4 and WebM as release assets.

## Last measured (draft build on your-box, about 10 minutes)

`demo.mp4` 76.0 s, 3.8 MB; `demo.webm` 76.0 s, 4.1 MB; `hero.webp` 13.0 s, 1.7 MB (1600x900, 18 fps encoded, 220 frames = 16.9 fps measured, quality 85). The starting crf and quality
values above met every limit, so they were not tuned.
