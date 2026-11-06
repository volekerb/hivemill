# Unittest shard split for oversized modules (T-1168)

## Problem

Greedy module packing can leave one slow module alone on a shard. If that module’s wall time is still above the job timeout (30 minutes), the shard is cancelled with no failing assertion, which blocks merges.

## Approach

1. **Detect**: After computing per-module weights from `ci/shard_timings.json`, a module is “split by test id” when it is listed in `SPLIT_TEST_IDS_MODULES` in `ci/shard.py` *and* its seconds exceed `sum(all weights) / SHARDS * 1.15` (same threshold as `tests/test_shard.py`’s pack rule for 10 shards).

2. **Packing**: Split modules are excluded from greedy load assignment, then appended to **every** shard’s module list. Remaining modules are packed as before.

3. **Running**: For a split module, `shard.py run` loads the sorted list of unittest ids (`collect_test_ids`), assigns ids to shard `k` with `shard_of_test_ids(ids, k, total)` (round-robin on sorted position: index `i` goes to shard `i % total + 1`), and runs `loadTestsFromName` for each id. Other modules still use `discover` with the module basename as the pattern.

4. **Verify**: `shard.py verify` requires `test_ids` in each report for split modules, checks that the union equals the full id set, that ids do not overlap across shards, and that each shard’s list matches the deterministic rule.

5. **Timings**: `shard.py timings` sums per-module seconds across shards (for split modules, wall time is split across jobs).

No change to test source files under `tests/` is required for the split mechanism.
