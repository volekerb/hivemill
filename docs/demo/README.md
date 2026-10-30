# Launch demo video

A silent 60-90 s product demo (1080p, burned-in captions) and a 10-15 s README hero loop. Everything is built from
this repository and the demo seed data, so it can be regenerated after any UI change.

| file | what | where it lives |
|---|---|---|
| `demo.mp4` | H.264, 1920x1080, 30 fps, no audio, under 25 MB | GitHub release asset, not committed |
| `demo.webm` | VP9, same picture, under 25 MB | GitHub release asset, not committed |
| `hero.webp` | animated WebP, 1600 px wide, 20 fps, loops forever, under 5 MB, the org chart without captions | committed (`docs/demo/hero.webp`) and shown at the top of the README |

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
- `--only SCENE` records one scene to a clip (for example `--only problem`) and stops, without encoding.
- `--board FILE` records another storyboard instead of `docs/demo/storyboard.json`, for example the LinkedIn cut
  (see [The pitch](#the-pitch-g-024)). A storyboard without a `"hero"` entry builds no hero loop, and `check --board
  FILE` then skips the hero checks; an unreadable storyboard never waives them.
- `--scale S` captures at deviceScaleFactor S (1 to 2, default: `"scale"` in `storyboard.json`, else 1). `2` records
  the 1920x1080 layout as 3840x2160 and `1.333` as 2560x1440. The MP4 and WebM are always exported at 1920x1080 with
  `scale=1920:1080:flags=lanczos`, so `check` still demands 1920x1080. At scale 1 no scale filter is added.
- `--keep-work` keeps the per-scene clips in `<work>/clips`. The frame text dumps in `<work>/text` are always kept
  because `check` scrubs them.

Exit codes: 0 ok, 1 build error or a failed check, 2 usage error, 3 refused (a preflight guard).

## Prerequisites

- `ffmpeg` and `ffprobe` on PATH. On Debian or Ubuntu: `sudo apt install ffmpeg`. The build exits 3 and prints this
  command when they are missing.
- Fonts as described in `docs/images/README.md` (the scenes use DejaVu Sans).
- Free RAM: 1500 MB `MemAvailable` at scale 1, more for a scaled capture (the PNGs have `scale^2` times the pixels):
  1500 + 500 x (scale^2 - 1) MB, so 1888 MB at 1.333 and 3000 MB at 2. The build refuses (exit 3) below that; check
  `factory res` first. About 2 GB of disk (a 2x capture needs about 4 GB for the clips). The browser
  download (113 MB, checksum-verified) is the only network access; it is cached in `<work>`.

## How it works

Scenes are HTML/CSS pages in `docs/demo/scenes/`. A recorder drives a headless browser over a pipe, seeks the page to
frame `i/30` and takes a PNG (`optimizeForSpeed`: still lossless, about 4x faster at a 2x capture), so the result is
smooth however slow the machine is. The PNGs are streamed straight
into `ffmpeg` (no frame files on disk), one scene at a time, into a clip per scene. The clips are concatenated into
the MP4 and the WebM. The hero loop records the org chart scene a second time with the captions hidden. The web UI
scenes (`ui:`) are recorded against the demo seed (`ci/demo_seed.py`), re-seeded for each such scene. One browser and
one `ffmpeg` run at a time.

Captions come from the storyboard being recorded: the recorder replaces the captions embedded in a scene file with the
scene's `captions` from the storyboard before the first frame, so one scene file serves every cut. The embedded copy
(kept equal to `storyboard.json` by the tests) is what a scene shows when opened on its own.

The web UI reloads itself every 60 s (meta refresh). A 2x capture spends longer than that on one page, so the tour
pauses the page's virtual time after loading it (`Emulation.setVirtualTimePolicy`); no timer fires while the page is
recorded, and the scroll, fade and push-in are set per frame by the recorder.

Encoder settings (tune these in `ci/demo_video.py` if a size check fails; never go below 30 fps for the video or 15
fps for the loop):

- scene clips: `libx264 -crf 12 -preset veryfast -pix_fmt yuv420p -threads 4`
- `demo.mp4`: `libx264 -crf 22 -preset slow -pix_fmt yuv420p -r 30 -movflags +faststart`, no audio
- `demo.webm`: `libvpx-vp9 -crf 34 -b:v 0 -row-mt 1 -threads 4`, no audio
- `hero.webp`: `libwebp_anim`, `scale=1600:-2`, `fps=20` (50 ms frames, so a 10 s loop times as 10.000 s; at 18 fps the WebP frame timing rounds it to 9.999 s), `-loop 0`, `-quality 85` (width, fps and quality come from the `hero` entry of `storyboard.json`)

## Tour pages (web UI scenes)

A `ui:` scene lists `pages`. Each entry is a URL path, or an object; every field but `path` is optional and the
defaults reproduce the plain tour (so a storyboard of path strings renders as before):

| field | default | meaning |
|---|---|---|
| `path` | required | URL path on the demo server |
| `duration` | the scene's `page_duration` | seconds this page is on screen |
| `hold` | `0` | seconds the page stays still before it scrolls (must be shorter than `duration`) |
| `scroll_to` | down to 600 px | pixels, or a CSS selector whose element ends at the top of the page box, just below the page's sticky title bar; capped at the end of the page |
| `fade_in` | `true` | `false` = fully visible on frame 0 (hard open), the fade-out at the end stays |
| `push_in` | `1.0` | end scale of a slow eased push-in over the page (1.0 to 2.0), for example `1.04` |
| `label` | the scene's `labels[i]` | page label |

A selector that matches nothing fails the build with the selector in the message. The scene `duration` is still the
sum of the page durations; keep them equal. The standalone recorder takes a scene id and a capture scale:

    python3 ci/demo_tour.py --out DIR --scene tour2 --scale 2 --max-frames 12

## The pitch (G-024)

`storyboard.json` is the owner-approved pitch script (12 shots, 80 s): problem, the factory working, result. Real UI
is on screen from frame 0 (hard open on the overview, slow 1.04 push-in). Every UI shot follows one seeded story: goal
G-002 "Build the checkout flow" (acme-web) and its task T-0008 "Add shipping form" (PR #105), which QA sent back once.

| scene | shots | seconds | screen |
|---|---|---|---|
| `open` | S1 | 5 | `/` overview, hard open, push-in |
| `problem` | S2 | 6 | `scenes/problem.html`: the two lines type on, "done" is struck through at 4 s |
| `request` | S3 | 6 | `scenes/request.html`: you ask the boss for the checkout flow, the boss opens G-002 |
| `orgchart` | S4 | 10 | `scenes/orgchart.html`: the team is complete by 5.8 s, pulses end by 8 s, fade 9-10 s (hero loop 0-10 s) |
| `work` | S5-S7 | 23 | `/goal/G-002` (to the Tasks table), `/agents` (Running), `/task/T-0008` (Verdicts) |
| `mergegate` | S8 | 8 | `scenes/mergegate.html`: PR #105, reviewer + QA PASS, the judge opens the gate |
| `result` | S9-S11 | 17 | `/changelog`, `/throughput`, `/quality` |
| `close` | S12 | 5 | `scenes/close.html`: name, tagline, install line, repository |

`storyboard-linkedin.json` is the 67 s LinkedIn cut: the same scenes with S10 and S11 dropped (`result` shows only the
changelog) and the org chart trimmed to 8 s with its own caption timing. Build it into its own directory:

    python3 ci/demo_video.py --board docs/demo/storyboard-linkedin.json --out docs/demo/out/linkedin

The `scroll_to` selectors (`section.sect:nth-of-type(n)`) pick a page section by position. If a UI change reorders the
sections of a page, update the selector so the shot still lands on the named section (the table above).

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

Rerun `python3 ci/demo_video.py`. The tour addresses the web UI pages by URL path (`docs/demo/storyboard.json`),
so a redesign needs no recorder change; a page that stops answering 200 fails the build, and so does a `scroll_to`
selector that matches nothing. Check that each selector still lands on the section named in the pitch table. Review the new video, then
commit the new `hero.webp` and upload the new MP4 and WebM as release assets.

## Last measured (draft build on your-box, about 10 minutes)

`demo.mp4` 76.0 s, 3.8 MB; `demo.webm` 76.0 s, 4.1 MB; `hero.webp` 13.0 s, 1.7 MB (1600x900, 18 fps encoded, 220 frames = 16.9 fps measured, quality 85). The starting crf and quality
values above met every limit, so they were not tuned.
