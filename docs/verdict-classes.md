# Verdict classes (G-058 fix 1)

`factory verdict <T> --fail --class <class>` records why a FAIL failed. The class is stored in `verdicts.verdict_class` and shown by `factory task show`.

| class | meaning | blocks the merge? |
|---|---|---|
| `code_defect` | the change is wrong (default when `--class` is omitted) | always |
| `evidence_missing` | the PR lacks proof (tests, AC walk) | only if the PR touches a risky path |
| `env_gap` | the box could not run the check | never; files one box-fix task |
| `nit` | style or taste | never |

Risky paths: `roles/hooks/`, `roles/settings/`, `ci/`, `.github/`, and any file whose name contains judge, merge, gate, credential, secret or token. If the diff cannot be read, the PR counts as risky.

An `env_gap` FAIL files exactly one coder task `Box fix: <finding> [<digest>]` in the same goal; the digest is a hash of the finding's first line, so a repeat verdict files nothing.

A non-blocking FAIL holds only at the PR head it was recorded on. A new push needs a fresh verdict, as for a PASS.

Rollback: revert the PR. The `verdict_class` column is nullable and old rows read as `code_defect`.
