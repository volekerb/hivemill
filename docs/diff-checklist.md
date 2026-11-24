# Diff checklist

`diff-checklist` (Go, `go/cmd/diff-checklist`, logic in `go/internal/checklist`) matches reliability-checklist items to a diff. Each item is triggered by what the diff touches. A coder runs only the matching items before `in_review`, and the PR body lists them with results.

Planned consumers: the coder skill runs `run`; the prcheck check runs `body`; the disprove pass and the false-positive review run `validate` and `body`.

## The rule that matters

Items are read from the ref, never from the worktree. A PR that edits `ci/diff-checklist.json` to drop an item cannot hide it: `run` and `body` read the file with `git show REF:ci/diff-checklist.json`. A worktree copy is never opened.

## Items file schema

`ci/diff-checklist.json`, strict: unknown keys anywhere are an error.

```json
{
  "version": 1,
  "mode": "observe",
  "items": [
    {
      "id": "CL-01",
      "title": "Guard changes ship a reject and an accept test",
      "trigger": {
        "paths": ["roles/hooks/**", "roles/settings/*.json"],
        "added": ["deny"],
        "exclude_paths": ["**/*_test.go"]
      },
      "check": "Run the new test with the fix removed and see it fail.",
      "sources": ["PR#101", "PR#202"]
    }
  ]
}
```

| field | rule |
|---|---|
| `version` | the number `1` |
| `mode` | `"observe"` or `"enforce"` |
| `items[].id` | `CL-` and 2 or 3 digits, unique |
| `items[].title`, `check` | non-empty, one line |
| `items[].trigger` | needs `paths` and/or `added`; also accepts `exclude_paths` |
| `trigger.paths` | non-empty array of globs. `**` crosses directories, `*` and `?` do not cross `/`, `**/` also matches zero directories. `[ ] { } \` are rejected |
| `trigger.exclude_paths` | same glob syntax; a path matching one is ignored |
| `trigger.added` | non-empty array of RE2 regexes applied to the added lines of the files that matched `paths`, or of all files when `paths` is absent |
| `items[].sources` | at least 2 distinct entries of the form `PR#<digits>` |

Match rule: an item matches when some changed path (added, modified, renamed-to or deleted) matches `paths` and not `exclude_paths`, and, when `added` is set, some added line in such a file matches some `added` regex. A deleted file has no added lines. Globs work on Unicode paths. RE2 `\w \b \d \s` are ASCII-only: write explicit Unicode classes such as `\p{L}` for non-ASCII text.

A validation error names the item and the field, for example `item CL-02: field trigger.added[0]: bad regex: ...`.

## Subcommands

```
diff-checklist run      [--repo-dir D] [--base origin/main] [--items-ref REF]
diff-checklist body     --pr <url|N> [--repo owner/name] [--repo-dir D] [--items-ref REF]
diff-checklist validate --file F
```

- `run`: items from `git -C D show REF:ci/diff-checklist.json` (REF defaults to `--base`). The diff is `merge-base(base, HEAD)..HEAD`. Prints the PR-body block. When REF has no items file it prints `diff-checklist: no ci/diff-checklist.json on REF: nothing to run` (REF replaced) and exits 0.
- `body`: the diff from `gh pr diff` and the body from `gh pr view --json body` (two gh calls; `-R repo` is passed when `--repo` is given). Items from REF (default `origin/main`) in `--repo-dir`. For every matched item the block must hold a result line with `PASS`, `FAIL` or `N/A`; `N/A` needs a reason.
- `validate`: schema check of a local file.

Commands run through `exec.Command` with an argv slice, never `sh -c`. The diff is capped at 20 MiB.

### `body` output

One line per matched item:

- `OK CL-nn`
- `MISSING CL-nn: <why>` (no result line in the block)
- `TODO CL-nn: <why>` (still `TODO`, `N/A` without a reason, or an unrecognised result)
- `FAIL CL-nn: <evidence>`

Then the summary line `diff-checklist: matched M of K; problems P; mode MODE`. In observe mode, when P > 0 one more line follows: `observe: would block: CL-nn,CL-nn` and the exit is 0. In enforce mode P > 0 exits 1.

Only lines between `## Diff checklist` and `</details>` count.

## PR body block

It sits inside a details element, so it costs 0 words of the 300-word cap and the PR template is unchanged.

```
<details><summary>Diff checklist</summary>

## Diff checklist
items: REF@SHORTSHA · mode: MODE · matched M of K
- CL-nn TITLE | check: CHECK | result: TODO
</details>
```

`- none matched` replaces the item lines when M = 0. The coder replaces `TODO` with `PASS <evidence>`, `FAIL <evidence>` or `N/A <reason>`.

## Exit codes

| code | meaning |
|---|---|
| 0 | ok (also: no items file on the ref; also: problems in observe mode) |
| 1 | `body`: problems in enforce mode. `validate`: the file is invalid (errors on stderr) |
| 2 | usage error; a git or gh failure; an unreadable or invalid items file on the ref; a diff over 20 MiB (the message says which). It never reads as a pass |

## Observe and enforce

`observe` reports what would block and always exits 0, so a new list can run on real PRs before it gates anything. `enforce` turns problems into exit 1. The mode comes from the items file on the ref.
