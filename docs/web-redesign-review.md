# Web UI redesign review (G-015 SC4, R6)

This is the design review for the redesign of `factory serve`. It compares the 44 "before" screenshots (`docs/images/before/`, taken before R1) with the 44 "after" screenshots (`docs/images/after/`, taken after R0 to R4 were merged: T-1140, R1 PR 460, R2 PR 465, R3 PR 472, R4 PR 474, then T-1176 fixes). The source of the audit items is `docs/web-redesign-brief.md` ("Page audit"). Both sets are fictional demo data produced by `python3 ci/screenshots.py --evidence` (see `docs/images/README.md`). I read all 88 PNGs one by one before writing this. T-1176 changed `web/` and regenerated `after/`; "before" is unchanged.

How to read the demo data: the demo seed is relative to the day it runs, so counts and times differ between the two sets (for example 110 tasks before, 113 after; "Updated 20:09" and "Updated 05:04"). That is not a redesign effect. "data -710 min old" on the usage cards is a known demo seed artefact, not a defect.

Design statement: this review and the redesign it checks are our own work. No Warp text, interface, code or API shape is used. No web source was fetched for this document.

## Summary

- The phone layout is much better. On all 11 pages the org switch (All / Claude / Cursor) no longer covers the page title; the title has its own line. Wide tables became labelled cards (Tasks, Agents, Goal, Quality, Throughput, Tokens). The "More" navigation list that was repeated at the end of every phone page is gone, and the fixed bottom tab bar stays.
- Desktop: the Overview now leads with "Needs you" and a full goal list, filters are folded into a "Filters" disclosure, the Throughput tiles form a clean 3 x 2 grid, and the Tokens chart labels are readable (3.5M, 46.1M instead of `4.85672e+07`).
- T-1176 fixed the open chart, tile, alignment and phone-breakdown items in the list at the end; a few subjective layout choices stay noted there.

## Page by page

Brief item: **resolved** means the defect named in the brief's audit row is gone in the after image; **partly** means some of it is gone; **open** means it is still there. Dark images show the same layout as light for every page below; I read both.

| Page | Before | After | What changed | Brief audit item |
|---|---|---|---|---|
| Overview | `home-light-desktop.png`, `home-light-phone.png` (dark: same names) | `home-light-desktop.png`, `home-light-phone.png` in `after/` | "Needs you" is full width at the top; "Open goals" became "Goals" and lists all 9 goals, with the teams of the goals that need attention (G-009, G-004) shown as a tree under them; System and Agents running sit side by side below. Phone: the title "Overview" is whole and on its own line. | **Resolved** (equal weight of Needs you / System, 4 of 9 goals with no team level, org switch over the title). The desktop capture is cut at the window, see below. |
| Goals | `goals-light-desktop.png`, `goals-light-phone.png` | same names in `after/` | The Org and Goal-state chip rows are folded: the state chips stay, a "Filters" disclosure holds the rest. Goal rows show "needs you N". Phone: title whole. | **Partly.** Filter rows and title fixed. Needs-you is summarized with a link to the Overview (T-1176); internal-blocked ids no longer break mid-id on desktop. |
| Goal | `goal-light-desktop.png`, `goal-light-phone.png` | same names in `after/` | Intent comes first, then Teams as a full-width card with each team's agents and their tasks, then progress, criteria and tasks. Phone: title on one line (it was 4 lines); the task list is a card list. | **Resolved** (Teams rail without agents, Intent last, 4-line title). |
| Tasks | `tasks-light-desktop.png`, `tasks-light-phone.png` | same names in `after/` | The goal chip row is folded into "Filters". Phone: each task is a card with labelled fields (Goal, Assignee, PR, Rejections); title and state glyph sit together. | **Partly.** Chip rows and card clarity fixed. The page is still very long on a phone (113 tasks) and the capture is cut at the window (see below). |
| Task | `task-light-desktop.png`, `task-light-phone.png` | same names in `after/` | Phone: the 4-line title is now one line; the repeated nav list at the end of the page is gone. Desktop: long titles use the full header width (T-1176). | **Resolved** (4-line title, nav repeated in the page). |
| Agents | `agents-light-desktop.png`, `agents-light-phone.png` | same names in `after/` | Only the Status chips stay; Org, Role and Goal are in "Filters". The grouping by team is kept. Phone: agents are cards with Task / Model / Last event labels. | **Resolved** (four chip rows before the list). The capture is cut, see below. |
| Throughput | `throughput-light-desktop.png`, `throughput-light-phone.png` | same names in `after/` | Six tiles in a clean 3 x 2 grid (was 5 tiles with an orphan and a small-print cost tile); merge count and period sit on separate lines; column charts scale to the card. | **Resolved** (orphan tile, small print, wrapped window switch, merges tile line break, chart width — T-1176). |
| Quality | `quality-light-desktop.png`, `quality-light-phone.png` | same names in `after/` | Phone: "Per team" is a card per team with "Merged 3, Rejections 3, Per merged PR 1.00" (columns were unlabelled). The footnote in "Rejections by source" now has space above and below. | **Resolved** on desktop for the per-week chart (bars use full SVG width, T-1176). |
| Tokens | `tokens-light-desktop.png`, `tokens-light-phone.png` | same names in `after/` | The "By day" bar labels are formatted (3.5M, 6.4M, 46.1M) and no longer overlap; the period switch (Today / 7 days / 30 days / All time) no longer wraps; charts scale to width. | **Resolved** for chart width and phone Breakdown labels/columns (T-1176). |
| Resources | `resources-light-desktop.png`, `resources-light-phone.png` | same names in `after/` | "Leads 5 cap 4 Over cap" is now a tinted row, so it no longer looks like the OK rows. Phone: "Agents by org" is two cards (it was a table clipped at the right edge). Per-day numbers align under headers; Checks framed vs Capacity. | **Partly.** Over-cap and the clipped table are fixed; Per day alignment fixed (T-1176). Checks vs Capacity is explained in copy (T-1176). |
| Changelog | `changelog-light-desktop.png`, `changelog-light-phone.png` | same names in `after/` | Goal chips are folded into "Filters"; entries have a limited line length (about 770 px wide). Phone: the first entry starts higher. | **Resolved** (very wide list, filter space before the first entry). The desktop capture is cut at the window, see below. |

## Phone layout

The independent width table (document width at 390 CSS px on all 11 pages) is not in this document: see QA note on this task. What I can say from the phone images:

- Page title: whole on its own line on all 11 pages (before: clipped to "Over", "Goal", "Quali", "Tokens"-stub and so on on every page).
- The bottom tab bar is present on all 11 pages, with the active item highlighted ("More" for Throughput, Quality, Tokens, Changelog, Resources).
- Pages cut at the window (phone, 4000 CSS px): **tasks**, **agents**, **changelog**. What lies below the cut is not in the evidence: the rest of the To do list, Done and Cancelled groups on Tasks; the Teams section after its first entries on Agents; the Changelog entries older than Oct 25.
- Pages cut at the window (desktop, 1600 CSS px): **home** (the "Shipped" and "Quality" cards, visible in the before image, are below the cut), **agents** (the Teams section), **throughput** ("Slowest 10 tasks", which the phone image shows), **changelog**. The image is cut at a section boundary.
- All other phone pages end naturally: the capture shows the whole page.
- Nothing is clipped at the right edge in the after images after T-1176: the Tokens Breakdown tabs sit in a horizontal scroller (scroll to see "By org"). This is a reading of the images, not a measurement.

## Contrast

`python3 -m unittest discover -s tests -p test_web_design.py` result: `Ran 21 tests ... OK` on this branch.

What it covers: it reads the colour tokens of `web/static/app.css` for the light and the dark theme and computes WCAG contrast from the hex values. Text pairs must reach 4.5 (AA): the three text tiers and the accent on every surface, each status tone's text on its tint and on the card, selected nav and chips, and the org chip text on its tint. Non-text pairs must reach 3.0: control borders, focus ring, status dots and bar solids, chart series colours on the surfaces and on the bar track. It also checks the bar label over its fill and that a low-contrast pair would be caught. What it does not cover: contrast of rendered pixels (anti-aliasing, text over an image), and any colour that is not a token. I did not compute contrast from the screenshots.

## Tool numbers: blank rows against the true background

The tool (`capture_evidence`, T-1164) now measures the crop and the blank rows against the theme's real page background (`--surface-0`: light 242,242,247; dark 0,0,0). The run log prints no blank percent; the numbers below are the same rule (a row is blank when 99% of the main-column pixels are within 8 per channel of the background; only runs of at least 24 blank rows at 1x count) recomputed by me on the saved 1x PNGs, so they match the tool's 2x figures only approximately. Format: before → after, percent of blank rows. The limit is 35%. The run passed (exit 0): every after page is under the limit, no page was refused.

| Page | Desktop light | Desktop dark | Phone light | Phone dark |
|---|---|---|---|---|
| home | 3.2 → 0.0 | 3.0 → 0.0 | 4.1 → 2.1 | 4.0 → 2.0 |
| goals | 2.2 → 5.2 | 2.2 → 5.2 | 5.2 → 5.6 | 5.2 → 5.6 |
| goal | 3.0 → 5.1 | 3.0 → 5.1 | 8.2 → 6.5 | 8.2 → 6.5 |
| tasks | 7.9 → 8.1 | 7.9 → 8.1 | 47.1 → 6.4 | 47.1 → 6.4 |
| agents | 4.2 → 4.6 | 4.2 → 4.6 | 1.8 → 1.8 | 1.8 → 1.8 |
| throughput | 7.8 → 6.2 | 0.0 → 0.0 | 6.8 → 5.3 | 3.5 → 1.9 |
| quality | 7.3 → 7.1 | 2.4 → 2.4 | 8.1 → 6.8 | 4.6 → 2.6 |
| resources | 7.1 → 7.0 | 7.1 → 7.0 | 7.9 → 6.0 | 7.9 → 6.0 |
| task | 19.0 → 19.2 | 8.5 → 8.6 | 12.2 → 14.7 | 7.8 → 7.0 |
| tokens | 10.9 → 11.8 | 5.0 → 5.4 | 9.4 → 11.0 | 5.3 → 5.1 |
| changelog | 0.0 → 2.7 | 0.0 → 2.7 | 0.0 → 1.2 | 0.0 → 1.2 |

Notes. The before phone tasks value 47.1 is the case that showed the old last-row rule was wrong (it reported 0.0 because the page ended inside a card); the before images were cut by the old rule, so the before column is a re-measure of the saved images against the true background, not what the old tool printed. The light values are higher than the dark ones on throughput, quality and task: in the light theme the tile fill and bar track (237 to 240) are within the tolerance of 8 of the page background (242), so tiles count as "blank" there. Light numbers are therefore upper bounds. Text check at 1x: the 44 after images score 980 to 2803 distinct colours in the top 400 px (minimum `agents-light-phone.png`, maximum `home-light-desktop.png`), clearly above `MIN_COLOURS` = 200, which is unchanged. Size: `du -sb docs/images/after` is 7174367 bytes for 44 files (cap 8388608); the largest file is `changelog-light-phone.png` at 361315 bytes (cap 716800); two files are above the 307200 target and print WARN.

## Home tree marker

T-1148 changed the disclosure marker CSS after an earlier capture. T-1176 aligns the summary row content with the marker (`align-items: center` on `.gsum` / team `.tsum`). Re-check `after/home-*.png` after regeneration.

## Open visual findings

Status after T-1176 (G-015 SC4). Files are in `docs/images/after/` unless said otherwise.

1. **Fixed** — Quality / Tokens / Throughput column charts: SVG scales to the card width (`preserveAspectRatio`, no fixed `width` on the root `<svg>`); per-week bars use the full chart area. After: `quality-light-desktop.png`, `quality-dark-desktop.png`, `throughput-light-desktop.png`, `tokens-light-desktop.png` (and phone pairs).
2. **Fixed** — Resources "Per day": numeric columns are right-aligned under their headers; short caption on phone. After: `resources-light-desktop.png`, `resources-dark-desktop.png`.
3. **Fixed** — Throughput Merges tile: count and `in N days` render on separate lines (`tile` `unit_nl`). After: `throughput-light-desktop.png`, `throughput-dark-desktop.png`, `throughput-light-phone.png`, `throughput-dark-phone.png`.
4. *(same as 1)* — covered by the shared chart width fix.
5. **Fixed** — Tokens Breakdown on phone: tab row scrolls (existing `.seg-links`); breakdown cards include tokens, share, requests, cost, and in/out (`.dt-wrap--cards` labels). After: `tokens-light-phone.png`, `tokens-dark-phone.png`.
6. **Fixed** — Task desktop h1 uses full title bar width (`tblock--wide`). After: `task-light-desktop.png`, `task-dark-desktop.png`.
7. **Fixed** — Home tree rows: summary content is vertically centered with the disclosure marker (`align-items: center` on `.gsum` / `.tsum`). After: `home-light-desktop.png`, `home-light-phone.png`, dark pairs.
8. **Fixed** — Goals phone Blocked (internal): task id cell is `nowrap` with ellipsis on desktop; full id on narrow viewports. After: `goals-light-phone.png`, `goals-dark-phone.png`.
9. **Fixed** — Resources Checks: intro line distinguishes gate checks from Capacity gauges; list spacing unchanged structurally. After: `resources-light-phone.png`, `resources-dark-phone.png`.
10. **Open** — Residual padding in some phone cards (`goal-light-phone.png`, `quality-light-phone.png`): acceptable trade-off for readable tap targets; not tightened further in this pass.
11. **Open by design** — Changelog bounded line length (~48 rem) is kept for reading comfort; a short note on desktop explains it (`hide-phone`). Filling the full main column would hurt scanability. After: `changelog-light-desktop.png`.
12. **Partially addressed** — Goals: the section now starts with a summary + link to the Overview list instead of duplicating rows alone. Resources Checks vs Capacity: still related numbers, framed as snapshot vs live gauges (see #9). After: `goals-light-desktop.png`, `resources-light-desktop.png`.
13. **No UI change** — Tool: light theme counts tile fills as blank (property of `CROP_TOL` = 8 in `ci/screenshots.py`); not altered.

Known non-defects: "data -715 min old" on the usage cards (demo seed artefact); `T-0084` before versus `T-0088` after (seed ids).
