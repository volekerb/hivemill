# Web UI design

Design pass for `factory serve` (G-008, T-0087). Files: `web/static/app.css`, `web/static/theme.js`, `web/static/favicon.svg`, and the layout shell in `web/html.py` (`page()`). Choices below come from the vendored ui-ux-pro-max skill (`roles/plugins/goals/G-008/ui-ux-pro-max/`). It was used as text and CSV data only; no skill script was run. The factory overrides in its `SKILL.md` ("Factory rules that override the data") win: system fonts, local CSS, no CDN.

| Choice | Decision | Skill source |
|---|---|---|
| Style | Data-dense dashboard with minimal Swiss restraint: flat cards, 1 px borders, no shadows, no blur, small padding, tables first | `data/styles.csv` "Data-Dense Dashboard" (operational dashboards); `data/products.csv` Analytics Dashboard / Developer Tool rows (Minimalism & Swiss); `references/quick-reference.md` section 4 (consistency, effects match style) |
| Palette | Slate neutrals plus one blue accent; semantic tokens, never raw hex in rules. Light: bg `#f8fafc`, card `#fff`, text `#0f172a`. Dark: bg `#0b1120`, card `#111a2e`, text `#e2e8f0`. Dark uses lighter tonal variants, not inverted colours | `data/colors.csv` rows Analytics Dashboard, Developer Tool / IDE; quick-reference section 6 (`color-semantic`, `color-dark-mode`, `color-accessible-pairs`), section 4 (`dark-mode-pairing`) |
| Status colours | ok green, warn amber, bad red, info blue, muted slate. Each has a `-fg` and `-bg` token; a badge is tinted fill plus dark (light theme) or light (dark theme) text. Status is always also written as text, never colour alone | colors.csv Destructive and success hints; quick-reference sections 1 (`color-not-only`) and 6 |
| Typography | System stack only (`system-ui, ... sans-serif`; `ui-monospace, ...` for ids). Scale 12 / 14 / 16 / 18 / 24 / 32 px as `--fs-xs..--fs-2xl`; body 16 px with line-height 1.5; weights 400 body, 600 labels, 700 h1; tabular figures for numbers; table headers 12 px uppercase | `data/typography.csv` "Dashboard Data" and "Minimal Swiss" rows (mood only, mapped to system fonts per the factory override); quick-reference section 6 (`font-scale`, `line-height`, `weight-hierarchy`, `number-tabular`) |
| Spacing | 4 px grid: `--s1..--s6` = 4 / 8 / 12 / 16 / 24 / 32; radius 8 px; one max width (1200 px) | quick-reference section 5 (`spacing-scale`, `container-width`) |
| Responsive | Mobile first; one breakpoint at 640 px; tables scroll inside their card (`.card` has `overflow-x:auto`), the nav becomes a horizontally scrollable strip, the KPI grid is two columns | quick-reference section 5 (`horizontal-scroll`, `viewport-meta`, `mobile-first`) |
| Touch and a11y | Tap targets at least 40 px (`--tap`: nav items, theme button, `summary`; links in tables on coarse pointers); visible 3 px focus ring; `prefers-reduced-motion` disables transitions | quick-reference sections 1-2 (`touch-target-size`, `focus-states`, `reduced-motion`) |

## Themes

`:root` holds the light tokens. Dark tokens are defined twice with identical values: in `@media (prefers-color-scheme: dark)` for `:root:not([data-theme=light])`, and for `:root[data-theme=dark]`. So the OS decides unless the reader picked a theme. `theme.js` (optional, deferred, served from `/static/`) adds a "Theme: auto / light / dark" button to the nav, stores the choice in `localStorage`, and sets `data-theme` on `<html>`. Without JS, or with storage blocked, the page follows the OS. A reader who chose a theme may see one frame of the OS theme before the deferred script runs.

## Contrast

`tests/test_web_design.py` parses the hex tokens out of `app.css` and checks WCAG contrast in both themes: text, muted text and links on bg and card, and each status `-fg` on its `-bg`, on card and on bg, all at least 4.5:1; the accent (bar fill) on card at least 3:1. Change a token and the test tells you which pair failed.

## Rules for new UI

- Views emit only the classes in `web.html.CLASSES`; add a class there and a rule in `app.css` together.
- No inline styles, web fonts, remote CSS or scripts (the CSP is `default-src 'self'`). Static files stay under 60 KB in total.
- The favicon writes its XML namespace as `xmlns="http&#58;//www.w3.org/2000/svg"` (a valid character reference) so that `grep "https\?://" web/static` stays empty.
