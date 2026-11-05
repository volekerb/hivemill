# Web UI design

Design pass for `factory serve` (G-008, T-0087). Files: `web/static/app.css`, `web/static/theme.js`, `web/static/favicon.svg`, and the layout shell in `web/html.py` (`page()`). Choices below come from the vendored ui-ux-pro-max skill (`roles/plugins/goals/G-008/ui-ux-pro-max/`). It was used as text and CSV data only; no skill script was run. The factory overrides in its `SKILL.md` ("Factory rules that override the data") win: local fonts and CSS only, no CDN, no remote fonts.

The redesign plan, the page audit and the per-task split are in `docs/web-redesign-brief.md` (G-015 SC4). This file describes the tree as it is.

| Choice | Decision | Skill source |
|---|---|---|
| Style | Data-dense dashboard with minimal Swiss restraint: flat cards, 1 px hairlines, at most a faint 1 to 2 px shadow (`--e1`; a hairline ring in dark), no blur, small padding, tables first | `data/styles.csv` "Data-Dense Dashboard" (operational dashboards); `data/products.csv` Analytics Dashboard / Developer Tool rows (Minimalism & Swiss); `references/quick-reference.md` section 4 (consistency, effects match style) |
| Palette | Neutral greys plus one blue accent; semantic tokens (`--surface-0..2`, `--text-1..3`, `--accent`, status `-fg` / `-bg` / `-solid`), never raw hex in rules. Light: surface-0 `#F2F2F7`, surface-1 `#FFFFFF`, text-1 `#1D1D1F`, accent `#0066CC`. Dark: surface-0 `#000000`, surface-1 `#1C1C1E`, text-1 `#F5F5F7`, accent `#4DA3FF`. Dark uses lighter tonal variants, not inverted colours | `data/colors.csv` rows Analytics Dashboard, Developer Tool / IDE; quick-reference section 6 (`color-semantic`, `color-dark-mode`, `color-accessible-pairs`), section 4 (`dark-mode-pairing`) |
| Status colours | ok green, warn amber, bad red, info blue, muted neutral grey. Each has a `-fg` and `-bg` token; a badge is tinted fill plus dark (light theme) or light (dark theme) text. Status is always also written as text, never colour alone | colors.csv Destructive and success hints; quick-reference sections 1 (`color-not-only`) and 6 |
| Typography | Inter, self-hosted (`web/static/fonts/`: Regular, Medium, SemiBold, Bold woff2), then a system fallback stack (`system-ui, ... sans-serif`); ids use `ui-monospace, ...` (JetBrains Mono is named first in `--mono` but not vendored, see `test_fonts_inter_self_hosted_jetbrains_not_vendored`). Body 14 px / 20 px on `body`, h1 22 px / 28 px (28 px / 34 px from 1024 px), 15 px section titles, 12 px table headers and badges; weights 400 body, 500 labels, 600 titles and h1, 700 bold; tabular figures for numbers | `data/typography.csv` "Minimal Swiss" row (Inter); quick-reference section 6 (`font-scale`, `line-height`, `weight-hierarchy`, `number-tabular`) |
| Spacing | 4 px grid: `--s1..--s7` = 4 / 8 / 12 / 16 / 24 / 32 / 48; radius `--r-sm` 6 / `--r-md` 10 / `--r-lg` 14 px; row height `--row` 44 px (40 px from 1024 px); card padding 12 px (16 px from 1024 px); page max width 1240 px from 1440 px | quick-reference section 5 (`spacing-scale`, `container-width`) |
| Responsive | Mobile first; breakpoints at 640, 1024, 1280 and 1440 px. Under 640 px: top bar, fixed five-item bottom tab bar (`--tabbar` 56 px) and a "More" list; tables become stacked rows. 640 to 1023 px: the nav is a strip under the top bar. From 1024 px: a 232 px sticky sidebar. Tables scroll inside their card (`.card` and `.sect-box` have `overflow-x:auto`); the KPI grid is two columns on a phone | quick-reference section 5 (`horizontal-scroll`, `viewport-meta`, `mobile-first`) |
| Touch and a11y | Tap targets at least 44 px on a phone and 36 px from 1024 px (`--tap`: chips, segmented links, `summary`; links in tables on coarse pointers); visible 2 px focus ring; `prefers-reduced-motion` disables transitions | quick-reference sections 1-2 (`touch-target-size`, `focus-states`, `reduced-motion`) |

## Themes

`:root` holds the light tokens. Dark tokens are defined twice with identical values: in `@media (prefers-color-scheme: dark)` for `:root:not([data-theme=light])`, and for `:root[data-theme=dark]`. So the OS decides unless the reader picked a theme. `theme.js` (optional, deferred, served from `/static/`) adds a "Theme: auto / light / dark" button to the nav, stores the choice in `localStorage`, and sets `data-theme` on `<html>`. Without JS, or with storage blocked, the page follows the OS. A reader who chose a theme may see one frame of the OS theme before the deferred script runs.

## Contrast

`tests/test_web_design.py` parses the hex tokens out of `app.css` and checks WCAG contrast in both themes: text, muted text and links on bg and card, and each status `-fg` on its `-bg`, on card and on bg, all at least 4.5:1; the accent (bar fill) on card at least 3:1. Change a token and the test tells you which pair failed.

## Rules for new UI

- Views emit only the classes in `web.html.CLASSES`; add a class there and a rule in `app.css` together.
- No inline styles, no remote fonts, remote CSS or scripts (the CSP is `default-src 'self'`); fonts are self-hosted under `/static/fonts/`.
- The top-level files in `web/static/` (`app.css`, `theme.js`, `favicon.svg`) stay under 60 KB in total: `test_static_size_budget` counts only files directly in that directory. `web/static/fonts/` (about 460 KB of Inter woff2 plus its licence) is not counted.
- The favicon writes its XML namespace as `xmlns="http&#58;//www.w3.org/2000/svg"` (a valid character reference) so that `grep "https\?://" web/static` stays empty.
