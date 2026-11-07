# Guard unittest shards (`ci/guard_impl_unittest.py`)

CI runs `GUARD_IMPL=diff python3 ci/guard_impl_unittest.py run --shard I --shards N` in the `guard-unittest-shard` matrix jobs (see `docs/guard-diff.md`). The tool loads the guard-diff test modules (`ci/guard_impl_unittest.diff_module_names`), collects every test id, assigns ids to shards by round-robin on the sorted list, and runs only the tests in the current shard.

## Trustworthy id list

`collect_ids` (used by `run --shard`) treats these as failures for the shard step (they appear in the “test list is not trustworthy” block and make the job exit non-zero):

- Loader errors and `_FailedTest` (import or load failures).
- **Import-time `SkipTest`**: the stdlib discover path synthesizes a `unittest.loader.ModuleSkipped` placeholder; that is not a substitute for the module’s real test ids — the shard must not go green without them (T-1172).
- **Duplicate test id**: the same id must not appear twice; the failure names the id.
- A module that yields no loadable tests.

Decorator skips and `setUpClass` / `setUpModule` skips that still load the real `TestCase` classes are unchanged: they contribute real ids to the partition and are covered by the existing “zero tests executed” check on the shard runner.
