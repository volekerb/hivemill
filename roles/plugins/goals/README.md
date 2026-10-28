# Goal plugins

`roles/plugins/goals/<goal-id>/` holds plugins that only the **coders of that goal** load. `bin/factory spawn` passes `--plugin-dir roles/plugins/goals/<goal-id>` to a coder started with `--goal <goal-id>` when the directory exists (T-0079, `tests/test_goal_plugins.py`). No other role gets it, and nothing is installed into the shared `~/.claude`. Claude loads each child folder as a plugin.

Everything vendored here follows the rules of `roles/plugins/staff-engineer/README.md`: text and data only, no hooks, no MCP servers, no scripts, no executables. `tests/test_goal_plugin_content.py` enforces this for every goal directory.

## Vendored items (fetched 2026-10-24)

| Item | Upstream repo | Upstream path | Pinned SHA | Licence | Modifications |
|---|---|---|---|---|---|
| `G-008/ui-ux-pro-max/LICENSE` | nextlevelbuilder/ui-ux-pro-max-skill | `LICENSE` | d14efff8ed1faa4187f004f79926f428c8074ec7 | MIT (`G-008/ui-ux-pro-max/LICENSE`) | none, byte-for-byte |
| `G-008/ui-ux-pro-max/skills/ui-ux-pro-max/SKILL.md` | nextlevelbuilder/ui-ux-pro-max-skill | `.claude/skills/ui-ux-pro-max/SKILL.md` | d14efff8ed1faa4187f004f79926f428c8074ec7 | MIT | rewritten, see below |
| `.../ui-ux-pro-max/references/` (`pro-rules.md`, `quick-reference.md`) | same | `.claude/skills/ui-ux-pro-max/references/` | same | MIT | none, byte-for-byte |
| `.../ui-ux-pro-max/data/` (12 CSVs: `app-interface`, `charts`, `colors`, `icons`, `landing`, `motion`, `products`, `react-performance`, `styles`, `ui-reasoning`, `ux-guidelines`, `typography`) | same | `.claude/skills/ui-ux-pro-max/data/` | same | MIT | 11 byte-for-byte; `typography.csv` modified, see below |
| `.../ui-ux-pro-max/data/stacks/html-tailwind.csv` (the only stack file) | same | `.claude/skills/ui-ux-pro-max/data/stacks/html-tailwind.csv` | same | MIT | `Docs URL` column blanked, see below |
| `G-008/ui-ux-pro-max/.claude-plugin/plugin.json` | this repo | n/a | n/a | n/a | ours (name, description, version only) |

Chosen by the director (M908, option (a)) after the T-0073 inspection: the skill's text and CSV data only; its Python search scripts are not copied and nothing is run.

## Modifications

- `SKILL.md`: rewritten. The "Running the search tool", `--design-system`, `--persist`/`--output-dir`, design-dials and example steps that run the search script are replaced by "grep and read the CSV and Markdown files directly". The Google Fonts and CDN advice is replaced by a factory rule: system font stack, local CSS only. A source comment with the pinned SHA is added. Front matter keeps `name` equal to its directory and a "Use when" trigger (`tests/test_skill_authoring.py`).
- `data/typography.csv`: the `Google Fonts URL` and `CSS Import` columns are blanked in all 74 rows (they hold fonts.googleapis links and `@import` rules). All other columns are unchanged.
- `data/stacks/html-tailwind.csv`: the `Docs URL` column (tailwindcss.com links) is blanked in all 46 rows that had one. All other columns are unchanged.

## Upstream parts NOT copied

- All scripts (`scripts/*.py`, their tests and fixtures, `validate_data.py`): executables are banned here; the skill works from the data files.
- `data/google-fonts.csv`, `data/google-font-licenses.json`, `data/phosphor-icons-upstream.json`, `data/catalog-summary.json`, `data/data-provenance.json`: font and icon catalogs and upstream bookkeeping (large, only useful with the search script or web fonts).
- All stack CSVs except `data/stacks/html-tailwind.csv` (the director-approved set, M908): `angular`, `astro`, `avalonia`, `flutter`, `javafx`, `jetpack-compose`, `laravel`, `nextjs`, `nuxt-ui`, `nuxtjs`, `react`, `react-native`, `shadcn`, `svelte`, `swiftui`, `threejs`, `uno`, `uwp`, `vue`, `winui`, `wpf`. They do not apply to a stdlib server-rendered HTML UI.
- The rest of the upstream repo: the other skills under `.claude/skills/` (brand, design, slides, ui-styling with its `.ttf` fonts, ...), `cli/`, `.github/`, `.claude-plugin/` (upstream plugin/marketplace manifests), translations of the README.

## Updating

1. Pick a new SHA (`gh api repos/nextlevelbuilder/ui-ux-pro-max-skill/commits/main --jq .sha`).
2. Re-fetch each file with `gh api "repos/nextlevelbuilder/ui-ux-pro-max-skill/contents/<path>?ref=<SHA>" --jq .content | base64 -d`. Never git clone, never fetch without `?ref=`, never run a fetched file.
3. Re-apply the modifications above (rewrite SKILL.md steps that call scripts, blank the typography and html-tailwind URL columns, re-check the dropped stacks), update the SHA and date here and in the SKILL.md source comment.
4. The reviewer reads the full diff, and `python3 -m unittest tests.test_goal_plugin_content tests.test_plugins tests.test_goal_plugins tests.test_skill_authoring` must pass.
