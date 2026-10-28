# Coder and common rules

`roles/coder.md` has a "Quality bar" list (C1-C10): outcome-based and deterministic tests, reject+accept tests for every guard through the real entry point, a new test shown failing without the fix, zero-test runs fail, no live-home paths, fail-closed errors, out-of-scope finds reported as `follow-ups:` in `factory done`, plus two notes on the worker guard and the test command. `roles/coder.md` and `roles/qa.md` both carry K1 (no machine-level changes) and K2 (no writes to external GitHub repos unless the owner named the repo and the action); they are not in `roles/_common.md`, which is at its size budget. `tests/test_prompt_rules_coder.py` asserts one marker per rule. Prompt changes reach only newly spawned workers.

Source: coder rules: ideas from gAmUssA/coding-policy (testing-standards, error-handling, verify-before-done, environment-changes, external-repo-contributions, boy-scout) @7a25169 (declares MIT, no LICENSE file); our wording.
