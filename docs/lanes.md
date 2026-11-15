# Lane check: do two branches really conflict

`lane-check` answers one question with git's own merge machinery: if these two heads were merged, would git report a conflict, and in which files? It replaces guessing from file names. Two tasks that edit the same file in different places do not conflict and can run in parallel; `lane-check` shows that in one command.

The tool is read-only. It runs `git merge-tree --write-tree`, which merges in memory: no working tree is touched, nothing is committed or pushed. Every git call goes through the hardened `gitx` package (hooks, fsmonitor, prompts and the system git config off).

Build or run it from `go/`:

```
cd go && go run ./cmd/lane-check <command> ...
```

## The three commands

### `lane-check pair <refA> <refB> [--base origin/main]`

Merges the two refs (git finds the merge base) and prints two lines:

```
CLEAN
both touch: f.txt
```

or, on a conflict, the conflicted files on one line, sorted:

```
CONFLICT: bin/factory_cli.py docs/README.md
both touch: bin/factory_cli.py docs/README.md tests/test_x.py
```

`both touch:` lists the files each side changed since its own merge base with `--base`. It is information, not a conflict: two sides can touch the same file and still be CLEAN. `both touch: (none)` means they share no changed file.

### `lane-check open [--repo owner/name] [--file <path>]`

Checks the open pull requests against each other. It makes one `gh pr list --state open` call, fetches every PR head from the remote in one `git fetch`, and merges each pair of heads that both change `--file`. Without `--file` it takes every pair that shares at least one changed file. One line per pair, ordered by PR number:

```
629 x 702 CONFLICT bin/factory_cli.py
629 x 703 CLEAN
702 x 703 CLEAN
```

`--file` only selects the pairs. The CONFLICT line lists every conflicted file of that pair, also files other than `--file`.

### `lane-check plan <ref> --against-open [--repo owner/name] [--file <path>]`

The same for one branch, for example a task's worktree `HEAD`, against every open PR head: "may this task start or push now". A PR whose head is exactly `<ref>` (the task's own PR) is skipped.

```
HEAD x 629 CONFLICT bin/factory_cli.py
HEAD x 702 CLEAN
```

### Options

| option | commands | meaning |
|---|---|---|
| `--base <ref>` | all | ref the changed files of a side are measured against; default `origin/main` |
| `--repo owner/name` | open, plan | repository passed to `gh`; default: the one `gh` finds for the directory |
| `--remote <name>` | open, plan | git remote the PR heads are fetched from; default `origin`. It must be the same repository as `--repo` |
| `--file <path>` | open, plan | only pairs where both sides change this path (relative to the repository root) |
| `--dir <path>` | all | repository directory; default the current directory |

`gh` runs in the current directory, not in `--dir`. When `--dir` names another repository, pass `--repo` too; otherwise the listed heads do not match the fetched ones and the tool exits 2.

A path with a space or a control character is printed in double quotes so it stays one token.

## Exit codes

| code | meaning |
|---|---|
| 0 | no pair conflicts (also when there was no pair to check) |
| 3 | at least one pair conflicts |
| 2 | no answer: a bad ref, a git or `gh` failure, a usage error. The reason is on stderr |

Verdict lines go to stdout. Stderr carries errors, a one-line summary for `open` and `plan` (`lane-check: 22 open PRs, 6 pairs checked, 6 conflict`) and the stale-head notes described below.

The tool never answers CLEAN when it could not decide. A git older than 2.38 has no `merge-tree --write-tree`; the result is exit 2 with a message that says to upgrade git. If a PR head changes between the `gh` listing and the fetch, the result is exit 2 and the message says to run the check again.

## What CLEAN means, and what it does not

- CLEAN means "git can merge it": the two heads merge without a textual conflict. It does not mean the merged code works. Two tasks can add the same config key in two places, or one can rename a function the other calls. CI on the refreshed head and the judge's merged-tree proof remain the check for that; the merge gate is unchanged by this tool.
- CLEAN is true only for the heads that were checked. After a later push to either branch, run the check again.
- A head that is behind main can conflict with main itself. Such a head then conflicts with every head that already contains the newer main, whatever the two tasks did. For each side of a conflicting pair that conflicts with `--base` on its own, `open` and `plan` print a note on stderr:

  ```
  lane-check: note: 629 already conflicts with origin/main on its own (bin/factory_cli.py); refresh it, its pair conflicts may only repeat that
  ```

  Refresh that head first (see [refresh.md](refresh.md)) and check again before reading its pair conflicts as a collision between two tasks. The note does not change the verdict lines or the exit code.

## What it leaves behind

- `open` and `plan` fetch the PR heads into private refs under `refs/lane-check/<pid>/` and delete them before they exit, also on an error and on Ctrl-C. `git for-each-ref refs/lane-check` is empty afterwards. If the delete itself fails, the exit code is 2 and the message names the namespace and the command that removes the refs.
- The fetched commits, and the tree objects `git merge-tree` writes, stay in the object store as unreferenced objects until git's normal garbage collection removes them. No branch, tag, index or working-tree file changes.
- At most 1000 open PRs are listed. At that count the tool exits 2 instead of answering from a listing that may be cut off.
