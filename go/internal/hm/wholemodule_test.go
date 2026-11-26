package hm

import (
	"strings"
	"testing"
)

func TestLintRejectsWholeModuleGoTest(t *testing.T) {
	for _, proof := range []string{
		"cmd:cd go && go test ./... => exit 0",
		"cmd:cd go && go test -count=1 ./... => exit 0",
		"cmd:go test -race ./... => exit 0",
		"cmd:`cd go && go test -count=1 -race ./... 2>&1 | tail -5` => ok",
		"cmd:go -C go test ./... => exit 0",
		"cmd:cd go;GOFLAGS=-mod=mod go test ./...;echo done => done",
		"cmd:(cd go && go test ./...) => exit 0",
		"test:cd go && go test ./...",
		"test:go test -count=1 ./...",
		"cmd:cd go && ~/.local/go/bin/go test ./... => exit 0",
		"cmd:(cd go/internal/hm && true); go test ./... => exit 0",
		"cmd:bash -c 'cd go && go test ./...' => exit 0",
	} {
		p := FilingProblems("1. x - " + proof)
		if len(p) != 1 || !strings.Contains(p[0], "name the packages the task touches (go test ./internal/hm/...); CI runs the full suite") {
			t.Errorf("%q: %v", proof, p)
		}
		// A task filed before the rule is still dispatched.
		if p := LintProblems("1. x - " + proof); len(p) != 0 {
			t.Errorf("%q: dispatch gate %v", proof, p)
		}
	}
}

func TestLintAllowsPackageGoTest(t *testing.T) {
	for _, proof := range []string{
		"cmd:cd go && go test -count=1 ./internal/hm/... => exit 0",
		"cmd:cd go && go test ./internal/hm ./cmd/hivemill => exit 0",
		"cmd:cd go/internal/hm && go test ./... => exit 0",
		"cmd:go -C go/internal/hm test ./... => exit 0",
		"cmd:cd go && go vet ./... && go test ./internal/hm => exit 0",
		"cmd:cd go && go test ./internal/hm && go build ./... => exit 0",
		"cmd:grep -c 'go test ./...' docs/testing.md => 1",
		"test:go test ./internal/x",
		"cmd:go test -C go/internal/hm ./... => exit 0",
		"cmd:cd go/internal && cd hm && go test ./... => exit 0",
		"cmd:pushd go/internal/hm && go test ./... => exit 0",
		"cmd:cd go/internal/hm && bash -c 'go test ./...' => exit 0",
		"manual: the owner runs go test ./... on the device",
	} {
		if p := FilingProblems("1. x - " + proof); len(p) != 0 {
			t.Errorf("%q: %v", proof, p)
		}
	}
}
