---
name: web
description: File a web-pipeline goal from one sentence. Use when the boss wires a project repo for web work or the owner describes a change they want shipped through review, QA, and merge.
disable-model-invocation: true
---

# Hivemill web pipeline

You tell the boss how to file a goal. You do not write the product change, review it, test it, or merge it. The coder writes; reviewer, QA, and the judge finish the pull request.

## Project repo

The user's project repo holds `.hivemill/web.json`. Copy the shape from `plugins/hivemill/skills/web/web.example.json` in the Hivemill checkout. Keys are only `schemaVersion`, `defaultBranch`, and `checks`. No tokens, passwords, or key paths in that file. GitHub auth stays the user's existing `gh auth` login; this pipeline adds no secret file under `~/.config/hivemill/`.

Each check is an argv array, or an object with exactly `cwd` and `argv`. Every `cwd` stays inside the repo. `checks` must be non-empty.

Register the repo with the factory before filing the goal (`factory project add` if it is new).

## Goal from one sentence

The owner gives one sentence: what should change in the web project. Turn that sentence into a goal with this template.

**Title:** the sentence.

**Intent:** the sentence, then this line on its own:

Web pipeline. The agent that writes the change does not review it, test it, or merge it. Checks are the argv list in .hivemill/web.json.

**Success:**

1. The change matches the sentence.
2. Every check in `.hivemill/web.json` passes.
3. Reviewer pass, QA pass, and the judge merges.

The merged pull request URL is the done result.

### Commands

Use the project id from `factory project list`. Only one `-` field per call.

```
factory goal new --title "<sentence>" --project <id> --priority 2 --intent - <<'EOF'
<sentence>

Web pipeline. The agent that writes the change does not review it, test it, or merge it. Checks are the argv list in .hivemill/web.json.
EOF
```

Then set success (replace `G-xxx` with the new goal id):

```
factory goal edit G-xxx --success - <<'EOF'
1. The change matches the sentence.
2. Every check in .hivemill/web.json passes.
3. Reviewer pass, QA pass, and the judge merges.

The merged pull request URL is the done result.
EOF
```

Hand off to the director: `factory msg director "new goal G-xxx" "<one paragraph: sentence, repo, checks summary>" --kind request --goal G-xxx`.

## What the user sees when it finishes

The done result is the merged pull request URL. It shows in the web UI changelog and on the Herdr project card. The owner does not need to merge or run checks themselves.

## Out of scope for this skill

- Do not run checks in the chat, spawn coders, or merge pull requests.
- Do not use the factory checkout (`~/factory`) or `volekerb/hivemill` as the sample web project repo.
- iOS, research, and desktop pipelines use other skills; staff them only when the director says that stage is ready.