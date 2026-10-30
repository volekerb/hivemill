# README media slots

The README is laid out so its images and video can be replaced without touching the layout. Each slot is marked in
`README.md` with an HTML comment starting `SLOT:`.

| Slot | Where | What to change | Size and format |
|---|---|---|---|
| `SLOT:VIDEO` | Under the nav links | The bare `https://github.com/user-attachments/assets/...` URL on its own line (GitHub renders it as a player) and the MP4 link under it | 60-90 s, 1080p, no audio |
| `SLOT:HERO` | Under the video | The two paths in the `<picture>` (light in `img src`, dark in `source srcset`) and the `alt` text | Light and dark PNG of the overview page, shown at 860 px wide |
| `SLOT:GALLERY` | The web UI section | The six page images in the table (same pattern as the hero) and their alt text | Light and dark PNG per page, shown at 280 px wide |

Rules for a swap:

- Change file paths, URLs and alt text only. Do not move, add or remove blocks, and keep the `width` attributes.
- Upload a new video through a GitHub comment or release to get a `user-attachments` URL; do not commit video files.
- Screenshots come from `python3 ci/screenshots.py` ([images/README.md](images/README.md)) or the demo seed
  ([demo-data.md](demo-data.md)), never from real data.
- Keep a light and a dark file for each image so the `prefers-color-scheme` switch keeps working.
