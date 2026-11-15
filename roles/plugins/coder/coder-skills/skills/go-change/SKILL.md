---
name: go-change
description: Checklist for writing idiomatic, safe, stdlib-only Go under go/ with tests and the local toolchain. Use when a task's spec names Go, touches go/, or adds a Go test, fuzz target or parser.
---

# Go change

Policy: docs/adr/0001-language-policy.md. Toolchain setup: docs/go-toolchain.md. Go files live under `go/`.

Follow these steps in order.

1. Check the toolchain: `export PATH=~/.local/go/bin:$PATH GOTOOLCHAIN=local; go version`. If `go` is missing, stop and report to your lead (you do not install toolchains).
2. Read the neighbouring Go code and match its package layout, naming and error style.
3. Write the code. Rules:
   - Errors are always checked and wrapped with `%w` (`fmt.Errorf("read %s: %w", p, err)`); never discard one with `_ =`.
   - No `panic` on input. Guards fail closed: `recover` in `main` -> exit 2.
   - `exec.Command` takes an argv slice only. Never `sh -c` with a built string.
   - No `unsafe`, no cgo, no reflect on input.
   - Stdlib only: no `require` in `go/go.mod` without an owner decision. If you need one, stop and report to your lead.
   - No binaries in git (build output stays gitignored).
4. Write tests: table tests for new logic, a fuzz target (`func FuzzX(f *testing.F)`) for every parser.
5. From `go/` run, and fix until clean: `gofmt -l .` (empty output), `go vet ./...`, `go test -race -count=1 ./...`.
6. Gate tree: after `git merge origin/main`, run the tests the way the merge gate does: `(t=$(mktemp -d) && git archive --format=tar HEAD | tar -x -C "$t" --no-same-owner && cd "$t/go" && PATH=/usr/bin:/bin GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off GOWORK=off ~/.local/go/bin/go test -count=1 ./<changed pkgs>; rc=$?; rm -rf "$t"; exit $rc)` -> ok. The subshell deletes the temp dir on success and failure; never leave $t behind. That tree has no .git and no untracked or gitignored files. Test cases: code that walks, copies, extracts, exports or resolves files or symlinks gets a dangling symlink, a symlink chain through a missing component, and a loop (`l -> l`), each matching Python's result; a test that reads a repo file outside its package reads the real file (no ErrNotExist-to-testdata fallback); product code that takes a repo path gets a missing-path test that returns an error naming the path.
7. A port of existing Python behaviour needs a differential test against the old implementation: zero loosened and zero tightened cases. RE2 has no lookaround and its `\w \b \d \s` are ASCII-only; use explicit Unicode classes or a hand matcher and test non-ASCII input.
8. Put these commands and their output in the PR body; CI job `go` must be green before in_review.

Stop and report to your lead when: `go` is missing, a dependency is needed, or the spec does not name Go but asks for a parser or daemon.
