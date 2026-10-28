# staff-engineer plugins

`bin/factory spawn` passes `--plugin-dir roles/plugins/staff-engineer` to the **staff-engineer role only**. Claude loads each child folder here as a plugin. Everything is text: no hooks, no MCP servers, no scripts, no executables, no HTML. `tests/test_plugins.py` enforces this.

## Vendored items (fetched 2026-10-23)

| Item | Upstream repo | Upstream path | Pinned SHA | Licence | Modifications |
|---|---|---|---|---|---|
| `feature-dev/` (all 9 files: plugin.json, LICENSE, README.md, 3 agents, 1 command) | anthropics/claude-plugins-official | `plugins/feature-dev/` | 58267944f92187b7d6a1c822165c96c81c864806 | Apache-2.0 (`feature-dev/LICENSE`) | none, byte-for-byte |
| `pr-review-toolkit/` (all 11 files: plugin.json, LICENSE, README.md, 6 agents, 1 command) | anthropics/claude-plugins-official | `plugins/pr-review-toolkit/` | 58267944f92187b7d6a1c822165c96c81c864806 | Apache-2.0 (`pr-review-toolkit/LICENSE`) | none, byte-for-byte |
| `staff-engineer-skills/skills/codebase-design/` (SKILL.md, DEEPENING.md, DESIGN-IT-TWICE.md) | mattpocock/skills | `skills/engineering/codebase-design/` | 5beb35b040e3de97e54fa9e386d7e42d15c295c2 | MIT (`LICENSE-mattpocock`) | DESIGN-IT-TWICE.md and SKILL.md: parallel sub-agents capped at 3 (factory rule), fourth angle folded into the three; `agents/openai.yaml` not copied |
| `staff-engineer-skills/skills/domain-modeling/` (SKILL.md, ADR-FORMAT.md, GLOSSARY-FORMAT.md) | mattpocock/skills | `skills/engineering/domain-modeling/` | 5beb35b040e3de97e54fa9e386d7e42d15c295c2 | MIT (`LICENSE-mattpocock`) | none; `agents/openai.yaml` not copied |
| `staff-engineer-skills/skills/improve-codebase-architecture/SKILL.md` | mattpocock/skills | `skills/engineering/improve-codebase-architecture/SKILL.md` | 5beb35b040e3de97e54fa9e386d7e42d15c295c2 | MIT (`LICENSE-mattpocock`) | HTML report, Tailwind/Mermaid CDN and file-write steps removed; report goes to `factory report "<headline>" - --goal G`; Step 3 "Grilling loop" replaced by a "Discussion loop": open questions go through `factory msg`, glossary and ADR changes are returned as text proposals in the report (no GLOSSARY.md/ADR file writes, no call to the unvendored `grilling` skill); HTML-REPORT.md and openai.yaml not copied; source comment added. |
| `staff-engineer-skills/skills/differential-review/` (SKILL.md, methodology.md, adversarial.md, patterns.md, reporting.md) | trailofbits/skills | `plugins/differential-review/skills/differential-review/` | 196a9073ab10d35bacf111f945e02c00b2c6883a | CC-BY-SA-4.0 (`LICENSE-trailofbits`) | SKILL.md: `Write` removed from allowed-tools, report step rewired to `factory report`, issue-writer integration and agent dispatch removed, "output file" tips reworded to `factory report` (no file), attribution header added. reporting.md: file naming/location section replaced by a `factory report` delivery section. methodology.md: `git checkout <rev>` replaced by `git show <rev>:<path>` (no branch switching). adversarial.md, patterns.md verbatim. assets/ and openai.yaml not copied. |
| `staff-engineer-skills/skills/differential-review/adversarial-modeler.md` | trailofbits/skills | `plugins/differential-review/agents/adversarial-modeler.md` | 196a9073ab10d35bacf111f945e02c00b2c6883a | CC-BY-SA-4.0 | kept as a reference file, not a registered agent; attribution comment added |
| `staff-engineer-skills/skills/systematic-debugging/` (SKILL.md, condition-based-waiting.md, defense-in-depth.md, root-cause-tracing.md) | obra/superpowers | `skills/systematic-debugging/` | 2ce8aa15d05bd57213e6f746f953aafb7ded7a8b | MIT (`LICENSE-superpowers`) | root-cause-tracing.md: reference to find-polluter.sh replaced by a prose description; condition-based-waiting.md: reference to the .ts example replaced by prose. find-polluter.sh and the .ts example not copied; CREATION-LOG.md, test-academic.md and test-pressure-1..3.md not copied (upstream authoring/pressure-test material, not instructions); no hooks. Others verbatim. |
| `staff-engineer-skills/.claude-plugin/plugin.json`, `LICENSE-*` | this repo / upstream LICENSE files | root `LICENSE` of each repo (trailofbits copy has an attribution notice prepended) | as above | as above | plugin.json is ours |

## Upstream parts NOT copied

- `agents/` dirs (mattpocock `openai.yaml`, trailofbits agents other than `adversarial-modeler.md` as a reference file): the factory spawns agents itself, vendored text must not register agents.
- `assets/`, HTML-REPORT.md, `find-polluter.sh`, the `.ts` example: executables, HTML and scripts are banned here (`tests/test_plugins.py`).
- systematic-debugging CREATION-LOG.md and test-*.md: skill authoring and pressure-test scenarios, not guidance.
- Other plugins and skills of the upstream repos: out of scope for T-0026.

## Using the vendored text

The staff-engineer advises only. Of the vendored text it runs feature-dev phases 1-4 (explore, clarify, architecture); implement/commit/push/PR/file-write steps (feature-dev phases 5-6, code-simplifier, domain-modeling file writes, review-pr workflow integration) become proposed coder tasks. See `roles/staff-engineer.md`.

## Updating

1. Pick a new SHA per upstream repo (`gh api repos/<owner>/<repo>/commits/main --jq .sha`).
2. Re-fetch each file with `gh api "repos/<owner>/<repo>/contents/<path>?ref=<SHA>" --jq .content | base64 -d`. Never git clone, never fetch without `?ref=`, never run a fetched file.
3. Re-apply the modifications in the table, update the SHAs and date here and in the source comments.
4. The reviewer reads the full diff, and `python3 -m unittest tests.test_plugins` must pass.
