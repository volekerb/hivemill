# Skills

Hivemill registers three UI/UX skills and recommends them for every task that creates or changes a user interface
(pages, components, styles, dashboards).

| Skill | Source | Note |
|---|---|---|
| ui-ux-pro-max | <https://github.com/nextlevelbuilder/ui-ux-pro-max-skill> | Its search scripts need Python 3. |
| taste-skill | <https://github.com/Leonxlnx/taste-skill> | Its `SKILL.md` declares a `Stop` hook that runs a Node script (it needs Node and Chrome). Read it before use on an unattended machine. |
| impeccable | <https://github.com/pbakaus/impeccable> | Its launcher script downloads a checksummed binary from the project's GitHub releases on first run. |

These are third-party files that a session loads and may run. The pin in `kit/skills.json` fixes what is installed;
review the upstream diff before you move it. Licences and why nothing is copied into this repository:
[kit/skills/THIRD_PARTY.md](../kit/skills/THIRD_PARTY.md).

## How sessions use them

- The planner names the skills in the spec of a UI task (`roles/hivemill/planner.md`).
- The task session is told to load them before it writes UI code (`roles/hivemill/task.md`).
- When a task's title, spec or criteria mention UI files or pages, the kickoff message adds the skill names
  (`go/internal/hm/uiskills.go`). The names are the entries of `kit/skills.json` whose `use_when` mentions the user
  interface.

## Installing

`hivemill-install` installs them in its `link` step. For each external skill of the clone's `kit/skills.json` it
fetches the pinned commit into `~/.local/share/hivemill/skills/<name>@<sha>` and links the skill directory to
`~/.claude/skills/<name>`, the skills directory of the workers. An existing `~/.claude/skills/<name>` is kept as it
is. A failed fetch is reported and does not stop the install; run the installer again with `--resume`.

On a machine that is already installed, run the `install` command of each entry:

```sh
cd ~/factory
python3 -c 'import json; [print(s["install"]) for s in json.load(open("kit/skills.json"))["skills"] if s.get("repo")]'
```

Read the printed lines, then run the ones you want. A command leaves an existing `~/.claude/skills/<name>` as it is
and exits non-zero; move that one away first to replace it.

## Adding or updating one

Add an entry to `kit/skills.json` with `name`, `description`, `use_when`, `repo`, `commit` (full SHA), `path` (the
skill directory in that repository), `license` and `install`, and name it with its commit in `THIRD_PARTY.md`.
`kit-check` verifies both. Then run `cd go && go generate ./internal/hm`. Kit plugins built in this repository are
described in [kit/ADD-A-SKILL.md](kit/ADD-A-SKILL.md).
