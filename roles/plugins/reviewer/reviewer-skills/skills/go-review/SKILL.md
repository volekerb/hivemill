---
name: go-review
description: Numbered Go review checklist G1-G12 applied to the PR diff, each item marked pass, fail or n.a. Use when the PR diff touches .go files, go/go.mod or the CI go job.
---

# Go review checklist

Policy: docs/adr/0001-language-policy.md. Mark every item pass, fail or n.a. in your review comment; a fail is a blocking finding.

Follow these steps in order: read `gh pr diff <url>`, then answer G1 to G12.

- G1 `gofmt -l` prints nothing and vet is clean: CI job `go` is green.
- G2 Every error is checked; none is discarded with `_ =`; wrapped with `%w`.
- G3 No panic is reachable from input. Guards fail closed: exit 2.
- G4 No shell: `exec.Command` takes argv only; no `sh -c`; no string-built commands.
- G5 No `unsafe`, no cgo, no reflect on input.
- G6 `go/go.mod` has no new `require`; if it does, the owner decided it (else fail).
- G7 Tests: table tests for new logic, `go test -race -count=1 ./...` clean, a fuzz target for every parser.
- G8 regexp: RE2 has no lookaround, so a port of a Python lookaround is a hand-written func with its own test. RE2 `\w \b \d \s` are ASCII while Python `str` regexes are Unicode: a port uses explicit Unicode classes or a hand matcher and tests non-ASCII input.
- G9 Paths: `filepath.Clean` plus under-root checks; no TOCTOU between a guard decision and its use.
- G10 Resource bounds: input size limits, timeouts on I/O.
- G11 Shared state is guarded (`sync`) or absent.
- G12 Ports: a differential test against the old implementation; zero loosened and zero tightened cases.

Mark the whole list n.a. only when the diff has no Go. Anything you cannot decide from the diff is a fail, not a pass.
