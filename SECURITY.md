# Security

This document says what the factory protects, what it does not, and how to report a problem. It is a tripwire-and-guardrails design, not a sandbox. Read the [Known limits](#known-limits) before you run it on a machine you care about.

## Reporting

Report vulnerabilities privately through GitHub: on the repository, open the **Security** tab and choose **Report a vulnerability** (GitHub private vulnerability reporting / security advisories). Do not open a public issue or pull request for a vulnerability, and do not post exploit details publicly. Private vulnerability reporting must be enabled in the repository settings when the repository is published, or the button above does not exist.

The maintainer is the GitHub user `volekerb`. There is no security email address. This is a small project: expect a reply when the maintainer next looks, not on a fixed schedule.

## Threat model

### Assets

- The user's repositories and the `gh` token the agents use to push and open PRs.
- The user's Claude credentials.
- The factory database, which holds every goal, task, verdict and agent message.
- The box itself (the user account the factory runs under).

### Actors

- **The agents.** They read repository content, PR text and fetched skills, any of which can carry a prompt injection. An agent that is injected acts with its role's permissions.
- **Outside PR authors.** Anyone who can get text in front of an agent (a PR, an issue, a file in a repo the agents read).
- **LAN users of the web UI.** Anyone who can reach the port the UI listens on.

## Controls

Each control below lists what it does and what it is not.

- **Per-role Claude Code permission deny-lists** (`roles/settings/<role>.json`). They block listed tool calls for a role (for example `gh pr merge`, `sqlite3`, force pushes, `herdr` pane and workspace management, edits of the live database). They are prefix-matched, so a form that is not listed (a wrapper, an interpreter, another spelling) is not blocked. That is why the worker guard exists.
- **The worker-guard PreToolUse hook** ([docs/worker-guard.md](docs/worker-guard.md)). It parses shell commands and refuses ones that break the rules for the role. It is a tripwire aimed at an agent that makes an honest mistake, not at an adversary, and it is not a sandbox. Its documented gaps are listed in the "Known gaps" section of [docs/worker-guard.md](docs/worker-guard.md). In summary: command-running sinks it does not parse (`find -exec` placeholders, `trap`, `at`, `BASH_ENV`, `GIT_SSH_COMMAND` and similar), interpreter string-building and scripts that run a forbidden command themselves, a script written and run in one command, and a worker re-parenting out of its process tree and claiming another agent name (E1, open).
- **Factory CLI role checks.** `bin/factory` decides who is calling from `FACTORY_AGENT` and the process tree, and refuses commands outside the caller's role. These are environment-based checks inside the same user account: a process that can scrub its environment and re-parent can defeat them (the E1 gap above).
- **The merge gate.** A change merges only through `factory judge`, after reviewer and QA verdicts, and `gh pr merge` is denied to every agent. Branch protection on `main` (no direct pushes) is the repository owner's job to configure on GitHub; the factory does not set it.
- **nsbox.** `scout/nsbox.py` runs scout and skill-evaluation code with no network, no token, and `~/.claude` and the factory home hidden. It is used for scout and skilleval only.
- **Opt-in worker sandbox** ([docs/worker-sandbox.md](docs/worker-sandbox.md)). The config key `sandbox_roles` (empty by default, so off) lists roles whose `claude` runs in a namespace sandbox with the home, `/tmp` and other worktrees hidden. Only `reviewer`, `qa` and `coder` can be listed. Network stays on inside it, so it limits what a worker can read, not what it can send out. **By default no worker is sandboxed.**
- **Fetched skills** are triaged as text only and never executed by the scout; see [docs/skill-scout.md](docs/skill-scout.md).
- **The agents run with `--permission-mode auto`** (set in `bin/factory` when it launches Claude), so Claude Code does not stop to ask a human before most tool calls. The deny-lists and the hook are the only brakes.

## Known limits

- **By default workers are not sandboxed** (`sandbox_roles` is empty). A coder runs as the user's account in its own git worktree, with nothing stopping it from touching anything else that account can touch. The opt-in sandbox above narrows this for the roles you list.
- **By default agents can read anything the user account can read**, including other repositories, dotfiles and credentials stored in the home directory.
- **Network egress is not restricted for workers**, sandboxed or not. Anything an agent can read it can send out.
- **The deny-lists are prefix-matched** and bypassable by forms that are not listed.
- **The worker guard is a tripwire, not a sandbox.** Its path and write detection in shell commands is best-effort, and interpreters bypass it. The hard line is the settings deny-lists, which are themselves prefix-matched (above). Gaps: [docs/worker-guard.md](docs/worker-guard.md). Planned: protecting the live-home config file (`~/.config/factory/`) from worker writes.
- **`--permission-mode auto`** means agents act without per-call human approval.
- **The web UI has no authentication.** It is read-only (GET and HEAD only) and checks the `Host` header against an allow-list, but anyone who can reach the port can read the dashboard, including task text and agent messages. Today `factory serve` binds one private LAN address (by default the source address of the default route, overridable with `--bind` or `serve_bind`) and also accepts a configured LAN hostname (`serve_hostnames`). Bind it to `127.0.0.1` or to a trusted LAN only. Planned: a default that does not name any host.
- **The state branch holds the full database, including all agent messages.** The tick commits it to a local `state` branch. It is pushed to the remote only when both `git_push` and `state_push` are `1` (`state_push` defaults to `0`), at most every `push_interval_s` seconds (`factory export` pushes immediately). Never enable `state_push` for a public remote. Details and restore steps: [docs/state-branch.md](docs/state-branch.md).
- **Fetched third-party skills are vetted as text only** ([docs/skill-scout.md](docs/skill-scout.md)). A skill that reads as harmless can still steer an agent once adopted; adoption is a human decision.

## Supported versions

`main` only. There are no releases to patch; fixes land on `main`.
