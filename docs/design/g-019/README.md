# G-019 B1: static mockups for the Hivemill web UI redesign

Hand-written HTML pages and one stylesheet that show the redesign described in the G-019 design brief (task T-0429, notes 1-6), for director and owner approval **before** any build. Nothing in `web/` changes. No JavaScript, no external URLs, no web fonts, inline SVG only. Open `index.html` in a browser; each page follows the OS light/dark setting.

## Direction in short

A calm "macOS utility" look: neutral grey canvas, grouped lists instead of boxes, one system font in three weights, one blue accent for links and selection. Colour is reserved for state (green running or done, amber waiting, red blocked, violet in review), and every state also has a shape and a word. Every page opens with the answer (what needs me, is it healthy, what changed). Rows are 36 px on desktop and 44 px on phone. Light and dark are designed together, and the token pairs meet WCAG AA.

## Pages

| Page | Route it stands for | Screenshots |
|---|---|---|
| [Overview](overview.html) | `/` | [light 1280](shots/overview-light-1280.png) [light 390](shots/overview-light-390.png) [dark 1280](shots/overview-dark-1280.png) [dark 390](shots/overview-dark-390.png) |
| [Goals](goals.html) | `/goals` | [light 1280](shots/goals-light-1280.png) [light 390](shots/goals-light-390.png) [dark 1280](shots/goals-dark-1280.png) [dark 390](shots/goals-dark-390.png) |
| [Goal detail](goal.html) | `/goal/G-002` | [light 1280](shots/goal-light-1280.png) [light 390](shots/goal-light-390.png) [dark 1280](shots/goal-dark-1280.png) [dark 390](shots/goal-dark-390.png) |
| [Agents](agents.html) | `/agents` | [light 1280](shots/agents-light-1280.png) [light 390](shots/agents-light-390.png) [dark 1280](shots/agents-dark-1280.png) [dark 390](shots/agents-dark-390.png) |
| [Tasks](tasks.html) | `/tasks` | [light 1280](shots/tasks-light-1280.png) [light 390](shots/tasks-light-390.png) [dark 1280](shots/tasks-dark-1280.png) [dark 390](shots/tasks-dark-390.png) |
| [Quality](quality.html) | `/quality` | [light 1280](shots/quality-light-1280.png) [light 390](shots/quality-light-390.png) [dark 1280](shots/quality-dark-1280.png) [dark 390](shots/quality-dark-390.png) |

`index.html` links the six pages and summarises the direction.

## Screenshot table

24 PNGs in `shots/`, named `<page>-<theme>-<width>.png`: 6 pages x light/dark x 1280 (viewport 1280x860) and 390 (viewport 390x844). The links are in the table above. The headless browser here only has DejaVu Sans, which is wider than the system fonts of a real Mac, Windows or Android device, so text in real use is a little narrower than in these shots.

## What the checks say

Regenerate everything with `python3 docs/design/g-019/capture.py` (downloads the pinned headless Chrome through the helpers of `ci/screenshots.py`, imported read-only; `--chrome PATH` skips the download).

**Blank area on the overview** (`python3 docs/design/g-019/capture.py --blank-report overview`), measured in 8x8 px cells of one uniform colour equal to the page or section surface:

| Shot | Largest empty block | Uniform cells overall | Uniform cells in the content column |
|---|---|---|---|
| overview-light-1280 | 6.9% of the viewport | 63.7% | 57.4% |
| overview-dark-1280 | 6.9% of the viewport | 63.5% | 57.0% |

The gate (lead decision M6791) is the largest axis-aligned rectangular block of blank 8x8 px cells, as a share of the 1280x860 viewport; it must be 35% or less. Both shots print `blank <= 35%` (the largest block is the unused bottom of the sidebar). The share of blank cells over the whole viewport and over the content column (excluding sidebar and header; the grid has 107 whole rows, so the denominator is 856 px, not 860) are INFO only, not gating: any calm layout has uniform cells between words, so those numbers stay high without the page being empty.

**No sideways scroll at 390** (`python3 docs/design/g-019/capture.py --check-width`, exit 0): every `*-390.png` is 390 px wide and the page `scrollWidth` equals the 390 px window width.

| Page | PNG width | window width | scrollWidth (light and dark) | result |
|---|---|---|---|---|
| Overview | 390 | 390 | 390 | ok |
| Goals | 390 | 390 | 390 | ok |
| Goal detail | 390 | 390 | 390 | ok |
| Agents | 390 | 390 | 390 | ok |
| Tasks | 390 | 390 | 390 | ok |
| Quality | 390 | 390 | 390 | ok |

## Owner-question defaults used (T-0429 note 6)

| Question | Default used here |
|---|---|
| Q1 device | Desktop-first cockpit with full phone parity; "Needs you" is first on phone. Sidebar at 1024 px and wider, top bar plus 5-item bottom tab bar under 640 px ("More" jumps to the full grouped nav at the end of the page, no JS). |
| Q2 busy/idle | Only "last event" age per agent, with an (i) note on the agents page. |
| Q3 escalations | Subject only, linked to the goal or task when there is one, otherwise "Message #26" as plain text. |
| Q4 JavaScript | None in the mockups. The theme control is drawn as three buttons with `aria-pressed` but does nothing here; in the build it is added by `theme.js`, and without JS the theme follows the OS. |
| Q5 brand | "Hivemill" wordmark and hexagon mark in the sidebar/top bar, neutral palette, one blue accent. |
| Q6 navigation | Grouped Work / Insights / System. |

The sixth "review" violet tone (design decision D3) is used for "In review". The director may veto it at review; only the `--review-*` tokens and the glyph tone would change.

## Owner jobs on the overview (above the fold at 1280x860)

| Job | Where on `overview.html` | y range at 1280x860 (both themes) |
|---|---|---|
| J1 what are the agents doing | "Agents running" (right column, last): summary line (8 running, 3 workers, 3 leads, 2 HQ, 2 queued), the first team with last event age, link "5 more - All 8 agents" | 604-846 |
| J2 how far are the goals | "Open goals" (left column): meter, criteria, open PRs, last activity, blocking reason | 284-621 |
| J3 what needs me | "Needs you" (top left) and the "Needs you 5" badge in the status header on every page | 62-260 (badge 16-40) |
| J4 what shipped | "Shipped" (left column): 14-day sparkline and the newest five merges | 646-858 |
| J5 are things bouncing | "Quality" tiles (rejections per merged PR 0.50, first-try rate 57%, median 3.4 h) and "Bouncing now" (2 rows), right column directly under System | Quality 308-456, Bouncing now 480-580 |
| J6 is the system healthy, how much usage is left | "System" gauges (agents vs cap, memory vs floor, load vs CPUs, Claude usage with reset time 17:00) and the health pill in the header | 62-283 (pill 16-40) |

All six sections end above y=860 in `overview-light-1280.png` and `overview-dark-1280.png`; the J5 values are visible, not only the labels. To get there (QA finding on acceptance 9) the right column order is now System, Quality, Bouncing now, Agents running, and the Agents list shows the first team plus a "5 more" link instead of every agent. On the phone the same order applies after the left column sections.

## Files

`mock.css` holds tokens v2 (light under `:root`, dark under `prefers-color-scheme` and `[data-theme=dark]`) and the components C1-C18 that the six pages use. `capture.py` renders and checks. The dark shots are rendered from a temp copy with `data-theme="dark"` on `<html>`, light ones with `data-theme="light"`; the mockups themselves have no `?theme` parameter.

Not drawn in these mockups (not among the six pages): the task detail timeline (C11/C12), `/throughput`, `/resources`, `/tokens`, `/changelog`; they are built later from the same tokens and components. Charts are drawn at a fixed 96 px height on both widths; the build makes the phone chart 80 px.
