# Hivemill commercial film: storyboard and sources

`storyboard.json` is the storyboard for a customer commercial of at most 60 seconds (1920x1080, 30 fps, 9 scenes). This film replaces the rejected launch film and shares nothing with it. Fable is used only for the scene design of this film; the storyboard itself is plain data.

Scene 7: Herdr app panel. The stills in `docs/film/herdr/` are crops of the owner's demo-mode capture of the Hivemill dashboard tab in the Herdr desktop app (source file `hivemill-dashboard-clean.png`, 1920x960, Mac mini, taken 2026-11-14 about 20:33, sha256 `a3b86dc1733e3ac8fd52207011f4053962262051625c63efee9678b6488efb74`). The full frame is not committed because it holds a live request list and the most expensive tasks list. It is from an open pull request and is not part of v0.4.0. The scene composes the crops at 1920x1080 with no upscaling beyond native size.

| File | Crop | Frame rectangle | Content |
|---|---|---|---|
| panel-resources.png | `crop=617:489:23:222` | 23..639, 222..710 | Whole Resources card: machines, spawn gate, merges per day |
| panel-budget.png | `crop=616:528:652:131` | 652..1267, 131..658 | Budget & cost upper half: Claude budget bars, Cursor plan, spend tiles, cost per merged PR, spend per day |
| panel-factory.png | `crop=617:194:1280:131` | 1280..1896, 131..324 | Factory header, four tiles, Claude and Cursor lanes |
| panel-active-goals.png | `crop=617:409:1280:447` | 1280..1896, 447..855 | Active goals header and all 11 goal rows |
| panel-status-bar.png | `crop=1920:28:0:932` | 0..1919, 932..959 | The status bar |

The film makes no predicted-performance claim, shows the worker sandbox as opt-in, Linux only and with its rollout still in verification (scene "sandbox", 29-34 s, and the Active goals row in scene herdr-core); it never claims the sandbox is finished, on by default or a full security boundary. The film carries no version number (the release notes and the LinkedIn post carry the version).

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
| Herdr app panel (Hivemill dashboard tab and status bar in the Herdr desktop app; from an open pull request, not part of v0.4.0) | docs/film/herdr/CAPTURE.md:3, docs/film/herdr/CAPTURE.md:5 | `sha256sum docs/film/herdr/*.png` and `ffmpeg -loglevel error -i <source frame> -vf crop=W:H:X:Y -f md5 -` for each crop above, compared with `ffmpeg -loglevel error -i docs/film/herdr/<file> -f md5 -` |
| Hivemill is open source (MIT) on GitHub at github.com/volekerb/hivemill | README.md:11, README.md:288 | `sed -n '11p;288p' README.md` |
| Worker sandbox: opt-in (off by default), Linux user namespaces, own worker tokens, owner logins and keys hidden, rollout in progress | docs/worker-sandbox.md:3, docs/worker-sandbox.md:7, docs/worker-sandbox.md:155, README.md:263, docs/SETUP.md:19 | `sed -n '3p;7p;155p' docs/worker-sandbox.md; sed -n 263p README.md; sed -n 19p docs/SETUP.md` |
| Terminal backends: Hivemill supports two terminal backends, the classic Herdr terminal UI and herdr-core; watch in the web view or the Herdr app panel | docs/terminal-backends.md:9, docs/terminal-backends.md:10, README.md:36, docs/film/herdr/CAPTURE.md:3 | `sed -n '9p;10p' docs/terminal-backends.md; sed -n 36p README.md` |

Scene text must contain no ids, PR numbers or other bookkeeping words; there is no exception.
