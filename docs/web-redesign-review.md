# Web UI redesign review (G-015 SC4, R6)

This is the design review for the redesign of `factory serve`. It compares the 44 "before" screenshots (`docs/images/before/`, taken before R1) with the 44 "after" screenshots (`docs/images/after/`, taken after R0 to R4 were merged: T-1140, R1 PR 460, R2 PR 465, R3 PR 472, R4 PR 474). The source of the audit items is `docs/web-redesign-brief.md` ("Page audit"). Both sets are fictional demo data produced by `python3 ci/screenshots.py --evidence` (see `docs/images/README.md`). I read all 88 PNGs one by one before writing this. No file under `web/` and no "before" image is changed by this task; every defect I found is listed below, not fixed.

How to read the demo data: the demo seed is relative to the day it runs, so counts and times differ between the two sets (for example 110 tasks before, 113 after; "Updated 20:09" and "Updated 05:04"). That is not a redesign effect. "data -710 min old" on the usage cards is a known demo seed artefact, not a defect.

Design statement: this review and the redesign it checks are our own work. No Warp text, interface, code or API shape is used. No web source was fetched for this document.

## Summary

- The phone layout is much better. On all 11 pages the org switch (All / Claude / Cursor) no longer covers the page title; the title has its own line. Wide tables became labelled cards (Tasks, Agents, Goal, Quality, Throughput, Tokens). The "More" navigation list that was repeated at the end of every phone page is gone, and the fixed bottom tab bar stays.
- Desktop: the Overview now leads with "Needs you" and a full goal list, filters are folded into a "Filters" disclosure, the Throughput tiles form a clean 3 x 2 grid, and the Tokens chart labels are readable (3.5M, 46.1M instead of `4.85672e+07`).
- Not finished: a few chart and table details remain (list at the end). None of them breaks a page.

## Page by page

Brief item: **resolved** means the defect named in the brief's audit row is gone in the after image; **partly** means some of it is gone; **open** means it is still there. Dark images show the same layout as light for every page below; I read both.

| Page | Before | After | What changed | Brief audit item |
|---|---|---|---|---|
| Overview | `home-light-desktop.png`, `home-light-phone.png` (dark: same names) | `home-light-desktop.png`, `home-light-phone.png` in `after/` | "Needs you" is full width at the top; "Open goals" became "Goals" and lists all 9 goals, with the teams of the goals that need attention (G-009, G-004) shown as a tree under them; System and Agents running sit side by side below. Phone: the title "Overview" is whole and on its own line. | **Resolved** (equal weight of Needs you / System, 4 of 9 goals with no team level, org switch over the title). The desktop capture is cut at the window, see below. |
| Goals | `goals-light-desktop.png`, `goals-light-phone.png` | same names in `after/` | The Org and Goal-state chip rows are folded: the state chips stay, an "Filters" disclosure holds the rest. Goal rows show "needs you N". Phone: title whole. | **Partly.** Filter rows and title fixed. The "Needs you" list still repeats the Overview list, and on the phone the "Blocked (internal)" table is still 3 squeezed columns (ids wrap as `T-` / `0022`). |
| Goal | `goal-light-desktop.png`, `goal-light-phone.png` | same names in `after/` | Intent comes first, then Teams as a full-width card with each team's agents and their tasks, then progress, criteria and tasks. Phone: title on one line (it was 4 lines); the task list is a card list. | **Resolved** (Teams rail without agents, Intent last, 4-line title). |
| Tasks | `tasks-light-desktop.png`, `tasks-light-phone.png` | same names in `after/` | The goal chip row is folded into "Filters". Phone: each task is a card with labelled fields (Goal, Assignee, PR, Rejections); title and state glyph sit together. | **Partly.** Chip rows and card clarity fixed. The page is still very long on a phone (113 tasks) and the capture is cut at the window (see below). |
| Task | `task-light-desktop.png`, `task-light-phone.png` | same names in `after/` | Phone: the 4-line title is now one line; the repeated nav list at the end of the page is gone. Desktop is the same as before. | **Resolved** (4-line title, nav repeated in the page). A new small finding on the desktop title is in the open list. |
| Agents | `agents-light-desktop.png`, `agents-light-phone.png` | same names in `after/` | Only the Status chips stay; Org, Role and Goal are in "Filters". The grouping by team is kept. Phone: agents are cards with Task / Model / Last event labels. | **Resolved** (four chip rows before the list). The capture is cut, see below. |
| Throughput | `throughput-light-desktop.png`, `throughput-light-phone.png` | same names in `after/` | Six tiles in a clean 3 x 2 grid (was 5 tiles with an orphan and a small-print cost tile); "14 days" no longer wraps; the cost tile is short ("55 of 55 tasks priced"). | **Resolved** (orphan tile, small print, wrapped window switch). A text defect in the Merges tile remains, see the open list. |
| Quality | `quality-light-desktop.png`, `quality-light-phone.png` | same names in `after/` | Phone: "Per team" is a card per team with "Merged 3, Rejections 3, Per merged PR 1.00" (columns were unlabelled). The footnote in "Rejections by source" now has space above and below. | **Partly.** Phone table fixed. The "Per week" chart is still thin bars in a wide empty card (open list). |
| Tokens | `tokens-light-desktop.png`, `tokens-light-phone.png` | same names in `after/` | The "By day" bar labels are formatted (3.5M, 6.4M, 46.1M) and no longer overlap; the period switch (Today / 7 days / 30 days / All time) no longer wraps. | **Partly.** Labels and wrapping fixed. The chart is still 7 narrow bars in a wide card, and on the phone the Breakdown tab strip is clipped (open list). |
| Resources | `resources-light-desktop.png`, `resources-light-phone.png` | same names in `after/` | "Leads 5 cap 4 Over cap" is now a tinted row, so it no longer looks like the OK rows. Phone: "Agents by org" is two cards (it was a table clipped at the right edge). | **Partly.** Over-cap and the clipped table are fixed. The Checks list still repeats the Capacity bars above it. |
| Changelog | `changelog-light-desktop.png`, `changelog-light-phone.png` | same names in `after/` | Goal chips are folded into "Filters"; entries have a limited line length (about 770 px wide). Phone: the first entry starts higher. | **Resolved** (very wide list, filter space before the first entry). The desktop capture is cut at the window, see below. |

## Phone layout

The independent width table (document width at 390 CSS px on all 11 pages) is not in this document: see QA note on this task. What I can say from the phone images:

- Page title: whole on its own line on all 11 pages (before: clipped to "Over", "Goal", "Quali", "Tokens"-stub and so on on every page).
- The bottom tab bar is present on all 11 pages, with the active item highlighted ("More" for Throughput, Quality, Tokens, Changelog, Resources).
- Pages cut at the window (phone, 4000 CSS px): **tasks**, **agents**, **changelog**. What lies below the cut is not in the evidence: the rest of the To do list, Done and Cancelled groups on Tasks; the Teams section after its first entries on Agents; the Changelog entries older than Oct 25.
- Pages cut at the window (desktop, 1600 CSS px): **home** (the "Shipped" and "Quality" cards, visible in the before image, are below the cut), **agents** (the Teams section), **throughput** ("Slowest 10 tasks", which the phone image shows), **changelog**. The image is cut at a section boundary.
- All other phone pages end naturally: the capture shows the whole page.
- Nothing is clipped at the right edge in the after images, except the Tokens Breakdown tab strip (its last tab "By org" is cut to "By" and has to be scrolled inside its own box). This is a reading of the images, not a measurement.

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

Notes. The before phone tasks value 47.1 is the case that showed the old last-row rule was wrong (it reported 0.0 because the page ended inside a card); the before images were cut by the old rule, so the before column is a re-measure of the saved images against the true background, not what the old tool printed. The light values are higher than the dark ones on throughput, quality and task: in the light theme the tile fill and bar track (237 to 240) are within the tolerance of 8 of the page background (242), so tiles count as "blank" there. Light numbers are therefore upper bounds. Text check at 1x: the 44 after images score 982 to 2816 distinct colours in the top 400 px (minimum `agents-light-phone.png`, maximum `home-light-desktop.png`), clearly above `MIN_COLOURS` = 200, which is unchanged. Size: `du -sb docs/images/after` is 7107023 bytes for 44 files (cap 8388608); the largest file is `changelog-light-phone.png` at 366966 bytes (cap 716800); two files are above the 307200 target and print WARN.

## Home tree marker

T-1148 changed the CSS of the goal and team tree marker (the small triangle in front of a goal or team) after its last capture. In `after/home-light-desktop.png` and `home-light-phone.png` (and the dark ones) the marker is drawn a few pixels above the text: the filled triangle of "G-009 Localize the shop..." sits at about y=306 while the text line is centred at about y=314, so it reads as sitting above the baseline, not centred on the line. The same offset shows for the team rows (the smaller triangle in front of `g-009-a`, `g-004-a`) and for the collapsed goals below (G-002 and the others). This matches the earlier reading. It is cosmetic.

## Open visual findings

Things I see in the after images that are wrong or unfinished. Not fixed here. Files are in `docs/images/after/` unless said otherwise.

1. **Quality "Per week": thin bars in a wide card.** `quality-light-desktop.png`, `quality-dark-desktop.png`. The five visible bars are about 16 px wide and fill about a third of a 1000 px card; the rest is empty. Confirmed (the brief said 14 px; it is 16 px now, about the same). The phone version looks fine.
2. **Resources "Per day": desktop column headers do not line up with the numbers.** `resources-light-desktop.png`, `resources-dark-desktop.png`. The headers "Spawns", "Queued", "Merges", "Escalations" are left-aligned but the numbers are right-aligned, so each header sits 100 to 150 px left of its numbers. The "Agents by org" header row has the same, smaller offset. Confirmed.
3. **"55in 14 days": no space in the Merges tile.** `throughput-light-desktop.png`, `throughput-dark-desktop.png`, and the phone images. Confirmed, the digits and "in 14 days" touch.
4. **Throughput "Merges per day" and Tokens "By day": the chart uses a third of its card.** `throughput-light-desktop.png`, `tokens-light-desktop.png`. 14 or 7 fixed-width bars at the left of a 1000 px card.
5. **Tokens phone: Breakdown tab strip is clipped.** `tokens-light-phone.png`, `tokens-dark-phone.png`. The tabs read "By goal, By task, By agent, By model, By" and the last tab is cut at the right edge. Also, the phone breakdown rows show only tokens and share; the cost, requests and in/out columns of the desktop table are not shown.
6. **Task desktop: the title wraps into a narrow column.** `task-light-desktop.png`, `task-dark-desktop.png`. "Add payment step [SC3]" breaks onto two lines at about 190 px although the header has room (the Goal title does the same in `goal-light-desktop.png`).
7. **Home: tree marker above the text baseline.** See the section above. `home-light-desktop.png`, `home-light-phone.png`.
8. **Goals phone: ids wrap in the Blocked (internal) table.** `goals-light-phone.png`: `T-` and `0022` on two lines; the table is still 3 columns.
9. **Resources phone: Checks rows are crowded.** `resources-light-phone.png`: "Memory available 3900 MB floor 1024 MB OK" wraps over 3 lines and the next row ("Claude usage") starts directly under it with no gap, while the other rows have gaps. The "Agents by org" cards have about 40 px of empty space between their lines.
10. **Phone cards with empty space.** `goal-light-phone.png` (Tasks card), `quality-light-phone.png` (Per team cards): about 30 to 40 px of unused space inside each card under the text.
11. **Changelog desktop: entries end at about 770 px, leaving an empty right quarter.** `changelog-light-desktop.png`. May be intended (line length), but it is the only page that does not fill the main column.
12. **Duplicate content kept.** Goals "Needs you" repeats the Overview list (`goals-light-desktop.png`); Resources "Checks" repeats the Capacity bars (`resources-light-desktop.png`). Both were named in the brief.
13. **Tool: light theme counts tile fills as blank** (see the notes under the tool numbers). This is a property of `CROP_TOL` = 8 and is not changed here.

Known non-defects: "data -715 min old" on the usage cards (demo seed artefact); `T-0084` before versus `T-0088` after (seed ids).
