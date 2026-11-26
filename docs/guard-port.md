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

Files are read from git, at the base ref (default `origin/main`) and at `HEAD`; commit before you run it. It exits 0 only when all of these hold, and otherwise prints each failure (prefixed `a:` to `d:`) and exits 1:

- a. the old names in the map are exactly the `func Test...` names in the `--old` files at the base: none missing, none extra, none twice;
- b. each `NewTestName` is a `func Test...` in `go/internal/guard/*_test.go` at `HEAD` and not at the base, and appears once in the map;
- c. each `ExistingTestName` is a `func Test...` in `go/internal/guard/*_test.go` at the base;
- d. the number of `func Test...` in `go/internal/guard/*_test.go` at `HEAD` minus the number at the base equals the number of non-duplicate map lines.

Only files directly in `go/internal/guard/` count, not its subdirectories. `python3 ci/guard_port_check.py --self-test` runs a pass and a fail case for each of a to d in a temporary git repo.

## Mutation check

The mutation check `ci/guard_mutation_check.sh` (next task) takes `go/internal/guard/testdata/portmap/<batch>.mutation.patch`.
