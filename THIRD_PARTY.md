# Third-party content

This repository is MIT licensed (`LICENSE`) except the files listed below, which keep their upstream licences. Each section gives where the files live, the upstream repository, the pinned commit, the licence, the licence file kept in this repo, and the copyright holder as written in that upstream licence. Vendoring details (fetch date, exact file lists, what was not copied) are in `roles/plugins/staff-engineer/README.md` and `roles/plugins/goals/README.md`.

Files under `roles/plugins/` that are not covered by a section below are written by this project and are MIT: `roles/plugins/AUTHORING.md`, the `coder`, `director`, `lead` and `pm` plugin directories, `roles/plugins/staff-engineer/README.md`, `roles/plugins/goals/README.md`, and these `plugin.json` files only ("ours"): `staff-engineer/staff-engineer-skills/.claude-plugin/plugin.json`, `goals/G-008/ui-ux-pro-max/.claude-plugin/plugin.json` and the ones inside the `coder`, `director`, `lead` and `pm` plugin directories. The `plugin.json` files of `feature-dev` and `pr-review-toolkit` are upstream Apache-2.0 files (see their sections).

## feature-dev

- Path: `roles/plugins/staff-engineer/feature-dev/` (all files are upstream, including `.claude-plugin/plugin.json`, which is byte-identical to upstream and Apache-2.0)
- Upstream: https://github.com/anthropics/claude-plugins-official, `plugins/feature-dev/`
- Pinned SHA: `58267944f92187b7d6a1c822165c96c81c864806`
- Licence: Apache-2.0
- Licence file: `roles/plugins/staff-engineer/feature-dev/LICENSE`
- Copyright holder: the upstream `LICENSE` is the unmodified Apache-2.0 text. It names no holder (its appendix still reads `Copyright [yyyy] [name of copyright owner]`). The upstream `plugin.json` lists the author as "Anthropic".
- Modifications: none, byte-for-byte (see the table in `roles/plugins/staff-engineer/README.md`).
- NOTICE file (Apache-2.0 section 4(d)): none exists upstream, so there is nothing to reproduce. Evidence: `gh api "repos/anthropics/claude-plugins-official/contents/plugins/feature-dev?ref=58267944f92187b7d6a1c822165c96c81c864806" --jq '.[].name'` lists `.claude-plugin LICENSE README.md agents commands`; the repository root at the same SHA has no NOTICE either. The README table says the copy is byte-for-byte, with the LICENSE file only.
- Compatibility with MIT: compatible. Apache-2.0 files stay under Apache-2.0 and are not relicensed. Redistribution needs the licence text kept (it is) and the modification notice rule (section 4(b)) does not trigger because the files are unmodified.

## pr-review-toolkit

- Path: `roles/plugins/staff-engineer/pr-review-toolkit/` (all files are upstream, including `.claude-plugin/plugin.json`, which is byte-identical to upstream and Apache-2.0)
- Upstream: https://github.com/anthropics/claude-plugins-official, `plugins/pr-review-toolkit/`
- Pinned SHA: `58267944f92187b7d6a1c822165c96c81c864806`
- Licence: Apache-2.0
- Licence file: `roles/plugins/staff-engineer/pr-review-toolkit/LICENSE`
- Copyright holder: as for feature-dev, the upstream `LICENSE` names no holder (unfilled appendix); upstream `plugin.json` author is "Anthropic".
- Modifications: none, byte-for-byte (see the table in `roles/plugins/staff-engineer/README.md`).
- NOTICE file (Apache-2.0 section 4(d)): none exists upstream. Evidence: `gh api "repos/anthropics/claude-plugins-official/contents/plugins/pr-review-toolkit?ref=58267944f92187b7d6a1c822165c96c81c864806" --jq '.[].name'` lists `.claude-plugin LICENSE README.md agents commands`.
- Compatibility with MIT: compatible, same as feature-dev. Not relicensed.

## mattpocock skills (codebase-design, domain-modeling, improve-codebase-architecture)

- Path: `roles/plugins/staff-engineer/staff-engineer-skills/skills/codebase-design/`, `.../skills/domain-modeling/`, `.../skills/improve-codebase-architecture/`
- Upstream: https://github.com/mattpocock/skills, `skills/engineering/<name>/`
- Pinned SHA: `5beb35b040e3de97e54fa9e386d7e42d15c295c2`
- Licence: MIT
- Licence file: `roles/plugins/staff-engineer/staff-engineer-skills/LICENSE-mattpocock`
- Copyright holder: Copyright (c) 2026 Matt Pocock
- Modifications: codebase-design (SKILL.md, DESIGN-IT-TWICE.md) and improve-codebase-architecture (SKILL.md) are adapted to factory rules; domain-modeling is unchanged. Details: `roles/plugins/staff-engineer/README.md`.
- Compatibility with MIT: same licence. Keep the upstream copyright notice (`LICENSE-mattpocock`) with these files.

## trailofbits differential-review

- Path: `roles/plugins/staff-engineer/staff-engineer-skills/skills/differential-review/` (SKILL.md, methodology.md, adversarial.md, patterns.md, reporting.md, and `adversarial-modeler.md`)
- Upstream: https://github.com/trailofbits/skills, `plugins/differential-review/skills/differential-review/` and `plugins/differential-review/agents/adversarial-modeler.md`
- Pinned SHA: `196a9073ab10d35bacf111f945e02c00b2c6883a`
- Licence: CC-BY-SA-4.0
- Licence file: `roles/plugins/staff-engineer/staff-engineer-skills/LICENSE-trailofbits` (attribution notice, then the licence text)
- Copyright holder: Trail of Bits (the author named in the attribution notice; the upstream licence file as kept here carries no separate copyright line)
- Modifications: SKILL.md, reporting.md and methodology.md are modified, adversarial-modeler.md has an attribution comment added, adversarial.md and patterns.md are verbatim. Details: `roles/plugins/staff-engineer/README.md`.
- Compatibility with MIT: this licence is share-alike. These files are NOT relicensed under MIT; they stay under CC-BY-SA-4.0, and any adaptation of them must also be CC-BY-SA-4.0 with attribution and a statement of changes. They sit as separate files next to MIT files. Owner decision Q3 (M3357/M3360): these files are kept in the public export as separate, attributed files under their CC-BY-SA-4.0 licence.

## obra/superpowers systematic-debugging

- Path: `roles/plugins/staff-engineer/staff-engineer-skills/skills/systematic-debugging/` (SKILL.md, condition-based-waiting.md, defense-in-depth.md, root-cause-tracing.md)
- Upstream: https://github.com/obra/superpowers, `skills/systematic-debugging/`
- Pinned SHA: `2ce8aa15d05bd57213e6f746f953aafb7ded7a8b`
- Licence: MIT
- Licence file: `roles/plugins/staff-engineer/staff-engineer-skills/LICENSE-superpowers`
- Copyright holder: Copyright (c) 2025 Jesse Vincent
- Modifications: root-cause-tracing.md and condition-based-waiting.md replace references to unvendored scripts with prose; the other files are verbatim. Details: `roles/plugins/staff-engineer/README.md`.
- Compatibility with MIT: same licence. Keep the upstream copyright notice with these files.

## ui-ux-pro-max

- Path: `roles/plugins/goals/G-008/ui-ux-pro-max/` (`.claude-plugin/plugin.json` is ours; the LICENSE, `skills/ui-ux-pro-max/SKILL.md`, `references/` and `data/` are upstream)
- Upstream: https://github.com/nextlevelbuilder/ui-ux-pro-max-skill
- Pinned SHA: `d14efff8ed1faa4187f004f79926f428c8074ec7`
- Licence: MIT
- Licence file: `roles/plugins/goals/G-008/ui-ux-pro-max/LICENSE`
- Copyright holder: Copyright (c) 2024 Next Level Builder
- Modifications: SKILL.md rewritten (no search scripts, no web fonts or CDNs); `typography.csv` and `stacks/html-tailwind.csv` have URL columns blanked; the other files are byte-for-byte. Details: `roles/plugins/goals/README.md`.
- Compatibility with MIT: same licence. Keep the upstream copyright notice with these files.

## coding-policy (credited inspiration, no copied text)

- Path: none. No files from this project are vendored.
- Upstream: https://github.com/gAmUssA/coding-policy
- Pinned SHA: `7a25169`
- Licence: declares MIT, but ships no LICENSE file, so no licence file is kept here.
- Copyright holder: not stated upstream (no LICENSE file).
- Use: ideas only. The rules were reworded and the code is our own. Each place that draws on it says so in a `Source:` line: `docs/coder-rules.md`, `docs/finishing-coders.md`, `docs/planning-rules.md`, `docs/review-rules.md`, `docs/tests-and-ci.md`, `docs/verdict-gates.md`, and `roles/plugins/pm/pm-skills/skills/plan-quality/SKILL.md`.
- Compatibility with MIT: no copied text, so nothing to relicense; credit only.

## Inter and JetBrains Mono (fonts for the launch demo and the web UI)

- Path: `docs/demo/fonts/` (woff2 files used by `docs/demo/scenes/theme.css`; licence texts `OFL-Inter.txt`, `OFL-JetBrainsMono.txt`).
- Inter: version v4.1, https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip (zip sha256 `9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e`), files taken unmodified from `web/` in the zip.
  - `Inter-Regular.woff2` sha256 `e06f6b1bc553aaea4e4668023ed0ab0a147129c3107f511bc7d03d361b0ae085`
  - `Inter-Bold.woff2` sha256 `fa888127b6da015b65569f0351f3b5c391ad928904951f1c20e9f8462a8d95ea`
- JetBrains Mono: version v2.304, https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip (zip sha256 `6f6376c6ed2960ea8a963cd7387ec9d76e3f629125bc33d1fdcd7eb7012f7bbf`), files taken unmodified from `fonts/webfonts/` in the zip.
  - `JetBrainsMono-Regular.woff2` sha256 `a9cb1cd82332b23a47e3a1239d25d13c86d16c4220695e34b243effa999f45f2`
  - `JetBrainsMono-Bold.woff2` sha256 `c503cc5ec5f8b2c7666b7ecda1adf44bd45f2e6579b2eba0fc292150416588a2`
- Licence: SIL Open Font License 1.1 (both). Copyright: The Inter Project Authors; The JetBrains Mono Project Authors.
- Compatibility with MIT: the OFL allows bundling and redistribution of the unmodified fonts with software; the font files stay under the OFL, and the licence texts are kept next to them.

## Other third-party content

None found outside the sections above. Searches run on the tracked files (`git ls-files` and `git grep`):

- `git ls-files | grep -i -E '(^|/)(LICENSE|NOTICE|COPYING)|\.(woff2?|ttf|otf|svg|png|jpg|ico)$|\.min\.(js|css)$'` returns only the six licence files above and `web/static/favicon.svg` (ours).
- `git grep -n -i -l -E 'SPDX-License|licensed under|Copyright \(c\)|vendored from|adapted from'` outside `roles/plugins/staff-engineer` and `roles/plugins/goals` returns nothing.
- `web/static` holds `app.css`, `favicon.svg` and `theme.js` (ours); `skilleval/tasks/` and `tests/fixtures/` (including `tests/fixtures/scout/`) hold project-written or synthetic data.
