# Web UI redesign brief (G-015 SC4, R0)

Design brief for the redesign of `factory serve`, written from the "before" screenshots in `docs/images/before/` (44 PNGs, fictional demo data, regenerated with `python3 ci/screenshots.py --evidence --out docs/images/before`; see `docs/images/README.md`). The later tasks R1 to R4 build from it, and R6 takes the same screenshots again. This is our own design, from the vendored ui-ux-pro-max skill used as text and CSV data only (no script run): no Warp text, interface, code or API shape is used here, and no web source was fetched.

Skill sources are written `file#No` (the `No` column of the CSV row) or `quick-reference:<rule id>`, under `roles/plugins/goals/G-008/ui-ux-pro-max/skills/ui-ux-pro-max/` (`data/*.csv`, `references/quick-reference.md`).

## Current state in one paragraph

The shell is already a tokenised, themed app: 232 px sidebar from 1024 px, a top bar plus a fixed five-item tab bar and a "More" list on a phone, light and dark tokens, Inter vendored in `web/static/fonts/`. The desktop pages are consistent and readable. The real problems are on the phone (the org switch covers the page title on every page, tables and charts break), three data pages (tokens chart labels, quality per-team table, resources org table), and the lack of any "what needs me, by goal and team" structure (F-27 below).

## Page audit

Files are in `docs/images/before/`. Dark files are `<page>-dark-<device>.png` and show the same layout; the tool checked that each light and dark pair differs and that the dark one is darker. I read the light images; I did not compare the dark ones pixel by pixel.

| Page | Files (light; dark same name) | What is wrong now | Rule it breaks |
|---|---|---|---|
| Overview `/` | `home-light-desktop.png`, `home-light-phone.png` | Desktop: "Needs you" and "System" fight for the top with equal weight; "Open goals" shows 4 of 9 and no team level; the right column is shorter than the left, leaving holes. Phone: the org switch (All / Claude / Cursor) is drawn over the h1 so the title reads "Over"; the Needs-you rows stack 3 lines each. | `quick-reference:progressive-disclosure`, `quick-reference:truncation-strategy`, `ux-guidelines#3` |
| Goals `/goals` | `goals-light-desktop.png`, `goals-light-phone.png` | Two separate filter rows (Org, Status) before any content; the "Needs you" list repeats the Overview list; goal rows have no team or agent info. Phone: h1 "Goal" cut by the org switch; filter chips take about 200 px before content; the "Blocked (internal)" table is squeezed to 3 columns. | `quick-reference:truncation-strategy`, `quick-reference:horizontal-scroll`, `ux-guidelines#71` |
| Goal `/goal/G-002` | `goal-light-desktop.png`, `goal-light-phone.png` | Desktop: Teams is a small rail at the right and shows no agents or tasks per team; Intent comes last although it is the context for everything above. Phone: the title wraps to 4 lines because the org switch takes the right half of the header. | `ux-guidelines#6` (breadcrumbs ok), `quick-reference:truncation-strategy` |
| Tasks `/tasks` | `tasks-light-desktop.png`, `tasks-light-phone.png` | Desktop: two chip rows, one for status and one for goal, push the table down; row height is fine. Phone: rows become cards, but the id, goal, org tag and assignee sit on a second line with the state glyph far from the title; 110 tasks make a very long page (capture truncated at 4000 CSS px). | `quick-reference:touch-target-size`, `quick-reference:truncation-strategy` |
| Task `/task/T-0010` | `task-light-desktop.png`, `task-light-phone.png` | Desktop fine. Phone: title wraps to 4 lines (org switch again); the page ends in the full nav list ("More"), which is the nav menu repeated inside the page content. | `quick-reference:nav-hierarchy`, `quick-reference:bottom-nav-top-level` |
| Agents `/agents` | `agents-light-desktop.png`, `agents-light-phone.png` | Four rows of filter chips (Status, Org, Role, Goal) and a "(i)" disclosure before the list; on a phone they use the whole first screen and the agent list is cut off in the capture (4000 CSS px). Grouping by team is good and should be kept. | `quick-reference:progressive-disclosure`, `quick-reference:touch-target-size` |
| Throughput `/throughput` | `throughput-light-desktop.png`, `throughput-light-phone.png` | KPI tile grid has an orphan tile (5 tiles in a 4-wide grid; 3 tiles of different height); the "Cost per merged PR" tile is full of small print; the window switch (7 / 14 / 30 days) wraps "14 days" onto two lines. | `quick-reference:spacing-scale`, `quick-reference:compact-label-overflow` |
| Quality `/quality` | `quality-light-desktop.png`, `quality-light-phone.png` | Desktop: "Rejections by source" mixes a footnote into the bar list; "Per week" chart is 14 px tall bars in a wide empty card. Phone: the "Per team" table loses its header and shows each row as a number in one line and two numbers in the next, so the columns have no labels. | `quick-reference:data-table`, `quick-reference:responsive-chart`, `ux-guidelines#71` |
| Tokens `/tokens` | `tokens-light-desktop.png`, `tokens-light-phone.png` | The "By day" chart prints raw floats as bar labels (`4.85672e+07`, `093e+07`) that overlap; the card is wide with only 7 narrow bars. The period switch wraps ("30 days", "All time" on two lines). | `quick-reference:number-formatting`, `quick-reference:axis-labels`, `quick-reference:direct-labeling` |
| Resources `/resources` | `resources-light-desktop.png`, `resources-light-phone.png` | "Leads 5 cap 4: Over cap" is the only red item but looks like the other check rows; the Checks table repeats the Capacity bars above it. Phone: the "Agents by org" table is cut at the right edge inside its card (the Runtime column is not visible without scrolling inside the card). | `quick-reference:color-not-only`, `quick-reference:horizontal-scroll` |
| Changelog `/changelog` | `changelog-light-desktop.png`, `changelog-light-phone.png` | Desktop: one 1000 px wide list with a 40-character title on the left and everything else far right; capture truncated at 1600 CSS px. Phone: the By date / By goal switch and the goal chips take about 180 px before the first entry. | `quick-reference:line-length`, `quick-reference:progressive-disclosure` |

### Sideways scroll at phone width (390 CSS px)

The tool does not measure document width: a headless screenshot shows only the 390 px viewport, and a measurement needs a script run in the page, which the tool cannot do without a new dependency (a DevTools client). So this table is read from the phone screenshots: a page scrolls sideways when something is clipped at the right edge outside a scrolling card. I did not measure it, and a clip hidden in the part of a page beyond the capture (tasks, agents, throughput, changelog are cut at 4000 CSS px) could be missed. R4 must measure it (the check goes in its acceptance, see below).

| Page | Result from the screenshot |
|---|---|
| home | no clipping at the right edge seen |
| goals | no clipping seen |
| goal | no clipping seen |
| tasks | no clipping seen (first 4000 CSS px) |
| task | no clipping seen |
| agents | no clipping seen (first 4000 CSS px) |
| throughput | no clipping seen (first 4000 CSS px) |
| quality | no clipping seen; the per-team table is not clipped but its columns are unlabelled |
| tokens | the bar labels of the By day chart run past the chart and overlap, but stay inside the card |
| resources | the "Agents by org" table is clipped inside its card, which scrolls (`.card` has `overflow-x:auto`); the page itself does not move |
| changelog | no clipping seen (first 4000 CSS px) |

The one recurring defect is not scroll but overlap: the org switch sits over the page title at 390 px on all 11 pages.

## Decisions

R1 to R4 keep the factory's look (Data-Dense Dashboard on a Swiss base, Inter, neutral greys and one blue accent) and fix structure and phone behaviour. No restyle for its own sake.

| Area | Decision | Skill row |
|---|---|---|
| Style | Keep flat, dense, tables first: 1 px hairlines, faint `--e1` on cards and tiles, `--e2` on the phone tab bar, no blur. Cards only where a group needs a frame. | `styles.csv#28` (Data-Dense Dashboard), `styles.csv#1` (Minimalism & Swiss Style), `quick-reference:style-match` (section 4) |
| Palette and tokens | Keep the existing semantic tokens (`--surface-*`, `--text-*`, `--accent`, status `-fg` / `-bg` / `-solid`). Add none unless a change needs one; a new token is added in both themes in the same PR with its contrast pair in `tests/test_web_design.py`. Dark is a tonal variant, not an inversion (already so). | `colors.csv#7` (Analytics Dashboard), `colors.csv#81` (Developer Tool / IDE), `quick-reference:color-semantic`, `quick-reference:color-dark-mode` |
| Status | Keep status as glyph plus word (never colour alone). The Resources "Over cap" row gets the `danger` tone on the whole row, not only the dot. | `quick-reference:color-not-only` |
| Type | Keep Inter (self-hosted, `web/static/fonts/`), the 12 / 13 / 14 / 15 px sizes in `app.css` (h1 22 px on a phone and 28 px from 1024 px), tabular figures for numbers. Chart labels use the formatted value (`97.3M`, not `9.73e+07`). | `typography.csv#5` (Minimal Swiss: Inter), `quick-reference:number-tabular`, `quick-reference:number-formatting` |
| Spacing | Keep the 4 px scale `--s1..--s7`. Rows keep `--row` (44 px phone, 40 px desktop). | `quick-reference:spacing-scale` |
| Density | Desktop stays dense. Filters collapse: one row of chips for the main filter, the others behind a "Filters" disclosure with a count of active ones. Phone: first screen shows content, not filters. | `quick-reference:progressive-disclosure` |
| Navigation | Keep the sidebar (desktop) and the five-item tab bar (phone, already at the limit). The org switch moves out of the title row: on a phone it goes below the title, on desktop it stays right. The in-page "More" nav list stays on the More page only, never at the end of a content page. | `quick-reference:bottom-nav-limit`, `quick-reference:nav-label-icon`, `quick-reference:nav-state-active`, `quick-reference:nav-hierarchy`, `quick-reference:bottom-nav-top-level`, `ux-guidelines#3` |
| Phone | Breakpoints stay 640 / 1024 / 1280 / 1440 (they are systematic enough). Page title never shares a row with another control. Tap targets 44 px (`--tap`). A table either becomes cards with labelled fields (not bare numbers) or scrolls inside its own card. Window switches (7 / 14 / 30 days) do not wrap: they are a scrolling segmented control or a select. | `quick-reference:mobile-first`, `quick-reference:breakpoint-consistency`, `quick-reference:horizontal-scroll`, `quick-reference:touch-target-size`, `ux-guidelines#71` (Table Handling), `ux-guidelines#23` (Touch Spacing) |
| Charts | Direct labels with formatted values; bars wider than 24 px or fewer ticks on a phone; a table (the breakdown list) sits next to every chart already. | `quick-reference:direct-labeling`, `quick-reference:responsive-chart`, `quick-reference:data-table`, `quick-reference:axis-labels` |

## F-27: goal-and-team tree, attention first

A page structure for R2. Read-only (GET), no new action, built from data the views already load.

**Where:** the Overview (`/`) gets the tree as its main section, replacing "Open goals" (today 4 rows and "5 more"). `/goals` keeps its list and links to the same rows. Each goal and team already has a page (`/goal/<id>`; the team has no page, see "not doing").

**Order:** attention first. Goals and teams that need the owner or a decision sort above the rest, then by priority.

| Level | Shows | Existing data |
|---|---|---|
| 0. Needs you (strip above the tree) | count and the top 3 items, link "All N" to `/goals` | `queries.owner_pending` (escalations, requests, blocked and proposed goals, owner-blocked tasks, auto-merge-off tasks) |
| 1. Goal row | state glyph, id, title, priority chip, org tag, progress bar with `done/total tasks`, criteria `n/m`, open PRs, last activity; a red "needs you" count when the goal has an owner wait; expands to level 2 | `queries.goals` (counts, `teams`, `open_prs`, `tasks_blocked_owner`), `queries.goal_detail` (criteria), `queries.last_activity_by_goal` |
| 2. Team row (under its goal) | team id, scope text, lead agent and its status, running agents of the team, tasks by status (todo / in progress / in review / blocked) | `queries.teams` (`id, goal_id, scope, lead_agent, status`), `queries.agents` (`team_id`, `role`, `status`, `task_id`), `queries.tasks` |
| 3. Task and agent rows (under its team) | in review, blocked and in progress only (the rest behind "N more"): task id, title, state glyph, assignee, PR link, rejections and tries; link to `/task/<id>` | `queries.tasks`, `queries.pr_cycle`, `queries.bouncing_tasks` |

Behaviour: levels 2 and 3 are `<details>` (works without script, matches the existing `.disc` markup); a goal with something that needs you is open by default, others are closed. On a phone each level is one column and the counts wrap under the title. Nothing here needs a new query function except one that groups the tasks and agents by `(goal, team)`, which is a join of `teams`, `agents` and `tasks` already in `web/queries.py`.

## Split into tasks

Each is under half a day (T-1108: 0.9 points). Files are the ones the change should touch; a task that finds it needs more tells its lead first.

| Task | Changes | Files |
|---|---|---|
| R1 tokens and shell | (1) Org switch out of the title row; title and switch stack below 640 px. (2) Window switches (`7 / 14 / 30 days`, `Today / 7 days / ...`) no-wrap and scroll inside their own control. (3) Filter chips collapse behind a "Filters" disclosure (Tasks, Agents, Goals, Changelog). (4) Remove the in-page nav list from content pages. (5) "Over cap" row tone. | `web/static/app.css`, `web/html.py` (shell, `CLASSES`), `web/components.py`, `tests/test_web_design.py` (new contrast pairs only if a token is added), `tests/test_web_shell.py` |
| R2 overview and tree | The F-27 tree and the Needs-you strip on `/`; the grouped query; the same row on `/goals`. | `web/views/home.py`, `web/views/goals.py`, `web/queries.py`, `web/static/app.css`, `tests/test_web_overview.py`, `tests/test_web_views_goals.py`, `tests/test_web_queries.py` |
| R3 tasks and agents pages | Tasks: one filter row plus the disclosure, card layout with labelled fields on a phone. Agents: same filters, the team grouping kept, phone cards. Task page: Verdicts above PR cycle, title and meta on one block. Goal page: Intent first, Teams under it with their agents. | `web/views/tasks.py`, `web/views/agents.py`, `web/views/goals.py`, `web/static/app.css`, `tests/test_web_views_agents.py`, `tests/test_web_views_agents_filter.py`, `tests/test_web_detail.py` |
| R4 insights and phone pass | Tokens chart labels (formatted values), Throughput tile grid (no orphan tile, the cost tile with less small print), Quality per-team table as labelled cards on a phone, Resources org table as cards, Changelog list. Then a phone pass: measure document width at 390 px for all 11 pages (needs a script run in a browser; the tool cannot, see above) and fix every page over 390. | `web/views/tokens.py`, `web/views/throughput.py`, `web/views/quality.py`, `web/views/resources.py`, `web/views/changelog.py`, `web/charts.py`, `web/static/app.css`, `tests/test_web_charts.py`, `tests/test_web_tokens.py`, `tests/test_web_insights.py` |

R6 re-runs `python3 ci/screenshots.py --evidence --out docs/images/after` and compares with `before` page by page (same 11 pages, same file names).

## Not doing

| Item | Reason |
|---|---|
| A team page (`/team/<id>`) | New route and view, over half a day with R2; level 2 of the tree covers the need. |
| Any action (approve, stop, spawn, reply) in the UI | Pages are GET-only with no login; actions are out of the limits below. |
| Auto-refresh, push or live updates | Needs script and a new endpoint; `theme.js` is the only script. |
| A new colour system or a second accent | The tokens pass AA in both themes; a rewrite buys nothing. |
| Remote fonts, icon fonts or a CSS framework | CSP is `default-src 'self'`. |
| Replacing the charts with a chart library | Inline SVG (`web/charts.py`) is enough; a library is a remote script or a big vendored file. |
| Measuring page width in the screenshot tool | Needs a DevTools client or a page script; R4 measures once, by hand or with a one-off script it owns. |
| Re-taking `docs/images/*.png` (hero, social preview, README images) | Not part of R0; the owner decides after the visual OK. |

## Hard limits (every R task)

- CSP stays `default-src 'self'`: no remote fonts, CSS or scripts.
- Views emit only classes in `web.html.CLASSES`; add a class there and a rule in `app.css` together.
- AA contrast in both themes (`tests/test_web_design.py`): text 4.5:1, accent 3:1 for bars.
- No sideways page scroll at 390 CSS px (a card may scroll inside itself).
- GET-only pages, no login, no new actions.
- No inline styles; static top-level files stay under 60 KB (`test_static_size_budget`; `fonts/` is not counted).
