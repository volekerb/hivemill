# Guard port: case maps

G-048 removes `go/internal/factorytest`. Before that, every guard case in `factorytest/guard1`, `factorytest/guard2` and `factorytest/guardtools` moves into the tests of `go/internal/guard`, and no guard coverage may be lost. Each port PR proves this with a case map, checked by `ci/guard_port_check.py`.

## Map format

One line per old test:

```
# comment lines and blank lines are skipped
OldTestName -> NewTestName
OldTestName -> duplicate-of ExistingTestName
```

- `OldTestName -> NewTestName`: the old case now lives in a new `func NewTestName` in `go/internal/guard/*_test.go`.
- `OldTestName -> duplicate-of ExistingTestName`: the old case is already covered by `ExistingTestName`, which exists in `go/internal/guard/*_test.go` on the base, so no new test is added for it.

## New-name convention

The new name is the old name without its `Test` prefix, behind a package tag:

| Old package | New name | Example |
|---|---|---|
| `factorytest/guard1` | `TestG1<old name without Test>` | `TestFlagsReadHqUnchanged` -> `TestG1FlagsReadHqUnchanged` |
| `factorytest/guard2` | `TestG2<old name without Test>` | |
| `factorytest/guardtools` | `TestGT<old name without Test>` | |

## Where the maps live

One map per port batch: `go/internal/guard/testdata/portmap/<batch>.txt`.

## The check

```
python3 ci/guard_port_check.py --map go/internal/guard/testdata/portmap/<batch>.txt \
  --old go/internal/factorytest/guard1/flags_read_test.go [--old PATH ...] [--base origin/main]
```

Test files are read from git, at the merge base of the base ref (default `origin/main`) and `HEAD`, and at `HEAD`; commit before you run it. Below, "the base" means that merge base, so a batch merged to main after the branch point does not skew the counts. It exits 0 only when all of these hold, and otherwise prints each failure (prefixed `a:` to `d:`) and exits 1:

- a. the old names in the map are exactly the `func Test...` names in the `--old` files at the base: none missing, none extra, none twice;
- b. each `NewTestName` is a `func Test...` in `go/internal/guard/*_test.go` at `HEAD` and not at the base, and appears once in the map;
- c. each `ExistingTestName` is a `func Test...` in `go/internal/guard/*_test.go` at the base and still at `HEAD`;
- d. the number of `func Test...` in `go/internal/guard/*_test.go` at `HEAD` minus the number at the base equals the number of non-duplicate map lines; also, no guard test at the base is gone at `HEAD`, and every test added at `HEAD` is a `NewTestName` in the map.

Only files directly in `go/internal/guard/` count, not its subdirectories. A test is a `func TestXxx(` at the start of a line where `Xxx` does not start with a lowercase letter; `TestMain` is setup, not a case, and is skipped.

The map works per test function. It does not see table rows or subtests inside a function, nor `Fuzz...` targets; the mutation check below is meant to cover what a function's cases catch. `python3 ci/guard_port_check.py --self-test` runs a pass and a fail case for each of a to d in a temporary git repo.

## Mutation check

```
bash ci/guard_mutation_check.sh PATCH TESTREGEX
bash ci/guard_mutation_check.sh --self-test
```

PATCH is a git-style diff (`a/` `b/` paths, repo-relative) that breaks a guard rule, such as `go/internal/guard/testdata/portmap/<batch>.mutation.patch`. The guard tests matching TESTREGEX must pass before the patch and fail with it applied. The patch is applied in place and undone on exit.

- exit 0: the mutation was caught: the tests pass on the tree and a test fails with the patch.
- exit 1: not caught or not checked: the patch file is missing or does not apply, the baseline tests fail or none match, the tests still pass with the patch, or the patch breaks the build.
- exit 2: usage error (wrong arguments) or not in a git work tree.
- exit 3: the undo failed; restore the files the patch names.

`--self-test` builds a small module in a temporary directory and runs the check twice: a patch that breaks `Allow()` must be caught, and a comment-only patch must not be. It prints `self-test ok` and exits 0, else exits 1.
