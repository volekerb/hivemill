# Hivemill commercial film: storyboard and sources

`storyboard.json` is the storyboard for a customer commercial of at most 60 seconds (1920x1080, 30 fps, 8 scenes). This film replaces the rejected launch film and shares nothing with it. Fable is used only for the scene design of this film; the storyboard itself is plain data.

Scene 7 (`herdr-core`) is a placeholder. It stays empty until the owner chooses which view to show: status bar, dashboard tab, or org filter variant. Nothing is drawn for it.

The film makes no predicted-performance claim, never mentions the worker sandbox, and never says a release newer than v0.4.0 is out.

## Claims, sources and re-check commands

| Claim | Source (file:line) | Re-check |
|---|---|---|
| Hivemill is a self-hosted factory of AI coding agents on Claude Code and Cursor, with separate review, QA and a judge | README.md:8 | `sed -n 8p README.md` |
| Two runtimes (Claude and Cursor), each a full team, one box | README.md:55, README.md:106, README.md:259, web/views/agents.py:36 | `sed -n '55p;106p;259p' README.md; sed -n 36p web/views/agents.py` |
| The web UI shows goals, agents, cost and throughput | README.md:36, web/views/agents.py:36 | `sed -n 36p README.md` |
| Go guard about 8 ms against about 69 ms in Python, 8.5x to 9x, measured; Python stays the default | docs/worker-guard-go.md:421 | `sed -n 421p docs/worker-guard-go.md` |
| 41 role skills across 7 roles | roles/plugins/*/*/skills/*/SKILL.md | `git ls-files roles/plugins \| grep SKILL.md \| grep -v /goals/ \| wc -l` (prints 41); `git ls-files roles/plugins \| grep SKILL.md \| grep -v /goals/ \| cut -d/ -f3 \| sort -u \| wc -l` (prints 7) |
| Coder self-review skill | roles/plugins/coder/coder-skills/skills/self-review/SKILL.md:2 | `sed -n 2p` of that file |
| Reviewer multi-angle review skill | roles/plugins/reviewer/reviewer-skills/skills/multi-angle-review/SKILL.md:2 | `sed -n 2p` of that file |
| Researcher benchmark protocol skill | roles/plugins/researcher/research-skills/skills/benchmark-protocol/SKILL.md:2 | `sed -n 2p` of that file |
| Setup command `/hivemill:setup` | plugins/hivemill/skills/setup/SKILL.md:2 | `sed -n 2p plugins/hivemill/skills/setup/SKILL.md` |
| Up to five recurring jobs, token cap per run, `factory schedule add` | docs/schedule.md:3, docs/schedule.md:9 | `sed -n '3p;9p' docs/schedule.md` |
| Release v0.4.0 | README.md:274 | `git tag -l v0.4.0` |

Scene text must contain no ids, PR numbers or other bookkeeping words; the only allowed exception is the "PR 633" mention in scene 7's visual note.
