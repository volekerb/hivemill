# Size classes (G-056)

`ci-size-class` maps a pull request diff to a size class, an open-to-merge budget and the checks to run. Owner target (2026-11-15): a few lines move within 10 minutes, 100 to 300 lines within 20 to 30 minutes, larger within 40 minutes, and nothing waits hours.

| class | when | budget | checks |
|---|---|---|---|
| small | 40 changed lines or fewer, not risky | 10 min | `smoke`; `docs` when the ci-tier mode is docs |
| medium | 41 to 300 changed lines, not risky | 30 min | the ci-tier mode (`affected`, `docs` or `full`) |
| large | more than 300 lines, or risky, or undecidable | 40 min | the ci-tier mode; `full` for a risky diff |

Changed lines are added plus deleted lines from `git diff --numstat --no-renames BASE...HEAD`, so a rename counts as an add and a delete. A binary or other non-line numstat row makes the diff risky. The risky decision is `tier.Classify` in `go/internal/citools/tier` (the same lists the CI policy already runs in full); this tool does not keep its own list. The ci-tier mode comes from `tier.Plan`.

## Command

```
ci-size-class --base REF [--head REF] [--root DIR] [--github-output]
```

`--head` defaults to `HEAD`, `--root` to `.`. It prints one line each: `class=`, `budget_min=`, `checks=`, `lines=`, `reason=`. `--github-output` appends the same lines to the file named by `$GITHUB_OUTPUT` (exit 2 if it is unset).

Build it from the base commit, as `ci-tier` is, so a PR cannot change its own class:

```
cd go && go build -o ci-size-class ./cmd/ci-size-class
```

## Fail-safe

Any git or classify error (bad ref, malformed numstat) prints `class=large`, `budget_min=40`, `checks=full` and exits 0. A usage error (no `--base`, unknown flag, extra argument) exits 2. An empty diff is small.

A push to main still runs the full suite whatever this tool says.
