# obs UI design direction

The target look and behaviour of `hivemill obs`, for the tasks that build the pages. It changes no code. Token colours here are proposals and their contrast is unverified.

## Direction

Design read: a read-only product UI for engineers who scan it to find what is running, stuck or costly. It should feel modern, calm and dense.

The inspiration is Apple product pages, Awwwards and CSS Design Awards winners and maple.dev. The brief names them and no page of theirs was reviewed for this document. What we borrow is a set of qualities, not a layout:

- **Dense information.** Tables are the main content. No cards around numbers and no big-number hero blocks.
- **Calm type.** One system family, few sizes, tabular figures.
- **Strong hierarchy.** Each page answers one question first. Size, weight, space and hairlines carry the order, not boxes or colour.
- **Motion only where it explains state.** Hover and focus feedback, nothing else.

Dials: low layout variance, very low motion, high density. The brand stays: the name `hivemill`, honey as the one accent, hairlines, no shadows. Navigation labels and URLs stay.

## Tokens

Both themes use the same names, so components never hold a raw colour. The theme follows `prefers-color-scheme` unless `?theme=light` or `?theme=dark` is set, as today.

### Colour (proposed, contrast unverified)

| Token | Light | Dark | Use |
|---|---|---|---|
| `--bg` | `#f7f7f5` | `#161618` | page |
| `--panel` | `#ffffff` | `#1d1d20` | sidebar, header, inputs |
| `--head` | `#eeeeeb` | `#26262a` | table head, row hover |
| `--fg` | `#1c1c1a` | `#ececea` | text |
| `--muted` | `#5f5f58` | `#a5a59e` | secondary text |
| `--line` | `#dcdcd7` | `#343439` | decorative hairlines |
| `--edge` (new) | `#85857d` | `#6e6e76` | input and button edges |
| `--honey` | `#b7791f` | `#f0b23a` | accent marks: active item, focus ring, first series |
| `--honey-fg` | `#85560c` | `#f0b23a` | accent text: links |
| `--ok` | `#256b39` | `#6cc686` | success |
| `--warn` | `#7a5000` | `#e8b64a` | held, pending |
| `--bad` | `#a8221b` | `#ff8a80` | failure |
| `--s1` | `#4d6a8a` | `#7fa6d1` | second chart series |
| `--s2` | `#7d7a6e` | `#9a9788` | third chart series |

`--s0` stays equal to `--honey`. The change from today is a near-neutral light page in place of the cream one, so honey is the only warm note. Status colours mean state only and always come with a word.

### Type scale

Family: `system-ui, -apple-system, "Segoe UI", sans-serif`. Code, keys and ids use `ui-monospace`. No web fonts.

| Step | Size / line | Weight | Use |
|---|---|---|---|
| title | 1.25rem / 1.3 | 600 | `h1` |
| section | 1rem / 1.4 | 600 | `h2`, in `--fg`, sentence case |
| body | 0.875rem / 1.45 | 400 | text, tables, controls |
| small | 0.75rem / 1.4 | 400 | footnotes, footer, group labels, chart text |

Nothing renders below 12px; today's 10px chart text goes up. Uppercase letter-spaced headings go. Numbers use `font-variant-numeric: tabular-nums` and align right. Prose notes stop at 70ch.

### Spacing and shape

- Scale: 4, 8, 12, 16, 24, 32px, replacing today's mixed values.
- Table cells 4px by 8px. 32px above a section heading, 8px below it.
- Main area: 24px side padding, 1280px maximum width.
- Radius: 4px on inputs and buttons, 0 elsewhere. No shadows.

## Pages

### Shared shell

- Keep the 180px sidebar and its groups. Mark the current page with weight, a honey mark and `aria-current="page"`.
- Header: scope ("project, last N days") and the render time on the left, filters and theme links on the right. The render time moves up from the footer, so stale data is visible.
- Below 760px the sidebar stacks above the content. Wide tables scroll inside their own wrapper, never the whole page.
- Tables: hairline row separators, a tinted head, row hover, no zebra, no vertical rules.
- Charts: server-built SVG with a legend beside it, hairline gridlines and a one-line text summary. A `<details>` block under each chart holds its values as a table.
- Banners use a full hairline border in place of the left bar.

### Now

Question: what needs me, and what is running?

- Order: Held for the owner and Open questions first, then Running, Planner, Projects, Machines.
- Add one plain text line under the title with the counts of held, questions and running, each linking to its table.
- Empty attention tables shrink to their one-line message.
- Projects keeps its total row. Its explanatory footnote moves into a `<details>` block, and "(N pending)" gets a visible note, not only a `title` tooltip.

### Spend

Question: where did the money go?

- The total for the range moves above the chart, at title size, as text rather than a tile.
- Then the "Cost per day, by role" chart, the "group by" links as one row with the active one marked, and the table.
- Unattributed and total rows stay, set apart by a stronger rule and weight.

### Flow

Question: what is stuck, and which stage is slow?

- Order stays: Stuck now, Time per stage, Merged per day.
- Stuck rows lead with the task link and the time in stage.
- Time per stage separates the held and total columns with a rule. The stage meanings move directly under its heading.

### Machines

Question: is the data fresh from every machine?

- It stays a section of Now at `/#machines`; no new route.
- Columns stay: machine, source, newest event, pulled. Add an age beside each timestamp and the word "stale" past a limit the build task sets.

### The /task/T-x trace page

The real route is `/task?k=<key>`, and it does not change.

Question: what happened to this task, and what did it cost?

- Title, then a summary line with outcome, cost, hours to merge and the PR link.
- The other `dl.kv` fields follow in a two-column list, one column below 760px.
- Timeline moves above Sessions, since it is the trace. Events read top to bottom in time order, with time, event, role, session and detail.
- Sessions keeps its columns in a scrolling wrapper with the first column kept visible.
- Breadcrumbs keep project, goal, task. The project, goal and session drill pages follow the same table and list rules.

## States

### Empty

Say what is empty and why, in one muted line where the content would be. Keep today's wording and add the next step where one exists:

- Tables: "no task in this range" plus a link to a wider range.
- Charts: a message in place of an empty frame.
- Machines and Timeline have no empty message today; add one.

### Loading

Pages are rendered whole on the server, so the browser's own loading indicator is the loading state. No skeletons and no spinners. No auto-refresh is added.

### Error

- Unknown page, task or session: keep the 404, show the message in `--bad` inside the shell, and link back to Now.
- Load failure: today a plain-text 500. Show the same shell with what failed and "reload to retry", without leaking more detail than today.
- A render failure mid-page stays visible and is never styled to look like empty data.

## Accessibility

- Targets are WCAG AA: 4.5:1 for body text, 3:1 for large text and for control edges.
- **Every proposed token colour above is unverified.** The task that builds the pages measures each text and background pair, and each control edge, in both themes, and adjusts the values that fail. Chart series aim for 3:1 against their background, also unmeasured.
- Colour is never the only signal: outcomes, warnings and series also carry a word or a legend label.
- A 2px honey `:focus-visible` outline on every link, input and button.
- A "skip to content" link, headings in order, `th` with `scope`, labelled filters, and SVG charts with `role="img"` and a label.
- Scrolling table wrappers are focusable and named. Click targets are at least 24px high.
- Zoom to 200% keeps content usable.
- Reduced motion: the only motion is a 150ms colour transition on hover and focus, declared inside `@media (prefers-reduced-motion: no-preference)`.

## Constraints

- Read-only: only GET and HEAD, loopback only, no action that writes. The filter form stays a GET.
- Works without script where it does today, which is everywhere. Use links, forms and `<details>`.
- The content security policy stays: no script, no inline style, nothing from another origin, no web fonts.
- Go `html/template` and the one embedded stylesheet. No framework, no build step, no icon library.
- No new data collection. An addition that needs a value the page does not already load is a separate decision.

## UI skills

The three skills registered in `kit/skills.json`. Only taste-skill was installed in the session that wrote this. For the other two, the guidance files were read at the registered commits; nothing was installed or run.

- **ui-ux-pro-max** (read, not run): the 12px floor, the data table behind each chart, colour never alone, focus outlines, the skip link, empty states with a next step, and 24px targets. Its 16px body size was not adopted, to keep table density.
- **taste-skill** (loaded; it says dashboards are outside its scope, so only general rules were used): the design read and dials, one accent, one radius rule, no cards at high density, the neutral light page instead of cream, and keeping navigation, URLs and brand.
- **impeccable** (read, not run): the fixed rem type scale with a small step ratio, no uppercase eyebrow headings, no side-bar banners, no hero-metric tiles, more space above headings than below, and 150ms state-only motion.
