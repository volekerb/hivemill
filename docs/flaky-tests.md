# Flaky tests

## Policy

A flaky test is fixed, deleted or quarantined. Fix it when the root cause is found. Otherwise delete it and give the reason in a `Tests removed: <what and why>` line of the PR body, or quarantine it in ci/quarantine.json with an owner task and an expiry of at most 14 days. Never add a retry, a sleep or a skip to make a flaky test pass.

## Quarantine list

`ci/quarantine.json` is `{"entries": [...]}` and is empty on main. A quarantined test is left out of the run, so a known-flaky test cannot turn main red while its owner fixes it.

Each entry has these fields (unknown extra keys are allowed):

| field | meaning |
|---|---|
| `id` | Python unittest id as `loader.loadTestsFromName` takes it from `tests/`, e.g. `test_x.TestY.test_z`. A class id (`test_x.TestY`) or a module id (`test_x`) drops all tests under it. Ids starting with `go:` are Go entries and are ignored by the Python side. |
| `owner` | the task that will fix or delete the test, `T-NNNN` |
| `added` | `YYYY-MM-DD` |
| `expires` | `YYYY-MM-DD`, after `added` and at most 14 days after it |
| `reason` | non-empty text |
| `evidence` | non-empty URL starting with `https://` (a failed run that shows the flake) |

**Who adds an entry and how.** The owner task's coder opens a PR that adds the entry with its evidence URL and owner task. Reviewers check the evidence. There is no other way in: the file is read from the tree under test.

**Expiry.** When `expires` is in the past the entry stays excluded and every run prints `quarantine: EXPIRED <id> owner <T> expired <date>`. Alerts on expired entries come with a later task. Fix or delete the test, then remove the entry.

**Invalid entries are ignored.** An entry with a missing or empty field, a bad date, or an `expires` more than 14 days after `added` excludes nothing; the run prints `quarantine: ignored invalid entry <id>: <why>`. A missing file means no quarantine. A file that cannot be read or is not JSON of the right shape fails the run with exit 2 and a message naming the file: it never runs nothing and never skips everything.

Each excluded id prints `quarantine: excluded <id> (owner <T>, expires <date>)` and is listed under `"quarantined"` in the shard report. `ci/shard.py verify` still counts every module as run. `ci/test_count_gate.py` counts by discovery and is not affected.

## Where it applies

Python unittest: in CI (`unittest-shard` in `tests.yml`) and in the local gate (`tests/gate_shards.py`), because both run `ci/shard.py run` of the tree under test, with `ci/quarantine.json` as the default list. `ci/shard.py run` also takes `--quarantine FILE` and `--today YYYY-MM-DD` (tests always pass the date). The Go port `go/internal/citools/shard` applies the same rule and the same messages; both are tested against `ci/testdata/quarantine_fixture.json`.

Not yet covered: Go tests and the guard-unittest shards.
