---
name: language-choice
description: Pick and state the implementation language in a coder spec per the language-policy ADR. Use when writing or amending a coder task spec, or when a goal might involve a parser, daemon or port.
---

# Language choice

Source of truth: docs/adr/0001-language-policy.md. Read its component table before writing the spec.

Follow these steps in order.

1. Name the language in the spec's first lines: "Language: Go" or "Language: Python".
2. Go is the default for new parsers, long-running daemons and ports of hot or security-critical code. Go task FILES go under `go/`.
3. Python stays for the CLI (`bin/factory`), tick, `factory serve`, scout, CI scripts, tests and glue. A rewrite needs a measured reason in the ADR, not a preference.
4. Rust only with an exception ADR approved by the owner. If the work seems to need Rust, stop and ask your lead; do not write a Rust spec.
5. A Go task's acceptance includes: the CI `go` job green, `go test -race -count=1 ./...` from `go/` -> OK, `gofmt -l .` -> empty, `go/go.mod` has no `require`, and for a port a differential test (zero loosened, zero tightened).
6. A task needing a new Go dependency is owner-gated: ask your lead first.

Stop and ask your lead when the ADR does not cover the component.
