# QA test-first pilot (pilot-test-first)

For the next 5 guard, sandbox and merge-gate code tasks, QA writes the failing
acceptance tests before the coder starts. The coder makes them pass and may not
weaken them. QA keeps its permissions: it cannot write tests/ or commit, so it
delivers the test text as a task note and the build coder commits it.

P1 Trigger: the build task's FILES touch roles/hooks/, roles/settings/,
   scout/nsbox.py, scout/workerbox.py, or bin/factory cmd_judge /
   refresh_branch / cmd_refresh / head_has_main / verdict_carries. The 5 pilot
   slots are counted across all goals. The lead finds earlier slots with
   `factory task list` and `factory task show` (notes) before assigning k, then
   runs `factory task note <T> "pilot-test-first k/5"` on the build task and on
   its qa task.
P2 The lead adds a qa-role task in the same goal, titled "Test-first:
   acceptance tests for <T-build>", with the build task's acceptance as its
   spec, and runs `factory task update <T-build> --after <T-qa>` so the coder
   spawns only after it is done.
P3 QA (no PR, no worktree commit) exports origin/main into its session
   scratchpad: `factory src origin/main <scratch>/main` (a config-scrubbed
   `git archive | tar -x`; see "factory src" below). It writes
   test files under <scratch>/main/tests/ covering every acceptance item that
   a unit test can check (CLI as a subprocess, temp FACTORY_HOME, gh stub
   written by the test; coder.md C1-C6). It runs
   `python3 -m unittest discover -s tests -p <file>` there and checks that
   each test FAILS for the expected reason (an assertion or exit code, never
   ImportError, SyntaxError or NameError). QA records ONE note on its own qa
   task: for each file `PATH: tests/<file>`, then the full file text in a
   fenced block whose fence is 4 backticks (or more than the longest backtick
   run inside the file, so a file with triple backticks survives), then the
   failing-run summary (Ran N, failures=M, one line per test with its failure
   reason). Then `factory done`.
P4 The lead adds to the build coder's spec: "Pilot test-first: your FIRST
   commit adds the test files from <T-qa>'s note verbatim (byte-identical);
   never edit or delete those tests; make them pass." If the coder is already
   spawned, use the spec-change skill.
P5 Build QA check (a numbered acceptance item of the build task, P4 of
   plan-quality): `git show <first commit>:tests/<file> | diff - <note-file>`
   is empty (file text, not a patch), and
   `git diff <first commit>..HEAD -- tests/<file> | grep '^-' | grep -v '^--- a/'`
   is empty (additions only to pilot tests; `grep '^-[^-]'` misses deleted blank
   lines and lines that start with a dash). Any edit or deletion is a FAIL.
P6 After the 5th pilot build task merges or is cancelled, the lead reports to
   the director (`factory msg director ... --kind report`): per pilot task the
   rounds (head SHAs reviewed) and the rejection events with their rubric
   class, against the G-013 cohort baseline (0.70 residual avoidable per
   merged PR). If fewer than 5 tasks were reached, report the count at the
   goal audit. The pilot ends there; continuing it is a director decision.
P7 This is not a gate change: the test-first tests are added coverage.
   Reviewer, QA and judge run as before and QA's deny rules are unchanged.

## factory src

`factory src <ref> <dest> [<path>...]` exports a ref's tree into a directory in
your own scratchpad. The caller is `FACTORY_AGENT` (refused when unset or not a
registered agent). Run it from your registered working directory; your
scratchpad is derived from the agent's DB row as
`<tmp>/claude-<uid>/<registered cwd as a path slug>/<your session>/scratchpad`,
never from the cwd or an env var. Another agent's or HQ's scratchpad is
refused. Never run `git archive | tar` yourself.

- `<dest>` must resolve (realpath) to a new or empty directory below the
  scratchpad. Refused: outside the scratchpad, a symlink, non-empty, or
  overlapping a shared checkout (the factory home, a registered project
  `repo_path`) or any parent of one.
- `<ref>` and `<dest>` may not start with `-`; `<ref>` must resolve to a tree
  (`git rev-parse --verify --end-of-options <ref>^{tree}`).
- The git and tar children run with an empty environment (`PATH=/usr/bin:/bin`),
  a fresh empty `HOME`, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`,
  `GIT_ATTR_NOSYSTEM=1` and `core.attributesFile=/dev/null`, so no caller
  config, `tar.*.command` or smudge filter runs. The temp HOME is removed after.
- The archive comes from the project's `repo_path` (your goal's project, else
  the factory home). On any failure the exit code is non-zero and `<dest>` is
  removed (or emptied, if it already existed).
- The source repo's own config is checked first with the same allow-list
  as spawn/refresh/judge (`git_config_refusal`): a `filter.*`, include or other
  unlisted key in `.git/config`, or a non-empty `info/attributes`, is refused
  (fail closed) with the offending keys named. Symlinks in the
  exported tree are extracted as symlinks: never write through one.
