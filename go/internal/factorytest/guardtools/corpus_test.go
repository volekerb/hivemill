package guardtools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/guard"
)

// Ports of the Go-testable cases of tests/test_guard_diff.py (corpus sanity: ci/guard_corpus.json against the guard) and
// tests/test_guard_build.py (the guard stays dependency-free). The rest of both modules tests Python-only tools
// (ci/guard_diff.py, bin/factory build/impl-flag code) and is listed as "not ported" in the PR.

// placeholder matches the corpus stand-ins (@LIVE_HOME@, @SHARED@, @SCRATCH@): they need a prepared tree, not ported here.
var placeholder = regexp.MustCompile(`@[A-Z_]+@`)

type corpusEntry struct {
	Cmd    string            `json:"cmd"`
	Modes  []string          `json:"modes"`
	Role   string            `json:"role"`
	Env    map[string]string `json:"env"`
	Config map[string]string `json:"config"`
}

func (e *corpusEntry) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &e.Cmd)
	}
	type plain corpusEntry
	return json.Unmarshal(b, (*plain)(e))
}

func loadCorpus(t *testing.T) (block, allow []corpusEntry) {
	t.Helper()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "ci", "guard_corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		MustBlock []corpusEntry `json:"must_block"`
		MustAllow []corpusEntry `json:"must_allow"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("corpus is not valid JSON: %v", err)
	}
	return c.MustBlock, c.MustAllow
}

// corpusCheck runs every entry that needs no env, config or placeholder through guard.Check; wantBlock is
// the expected verdict. Entries with a role run in worker mode under it; strings and mode lists follow the corpus doc.
func corpusCheck(t *testing.T, entries []corpusEntry, wantBlock bool, defaultModes []string) int {
	t.Helper()
	checked := 0
	for _, e := range entries {
		if len(e.Env) > 0 || len(e.Config) > 0 || placeholder.MatchString(e.Cmd) {
			continue
		}
		modes := e.Modes
		if len(modes) == 0 {
			modes = defaultModes
		}
		for _, m := range modes {
			role, hq := e.Role, m == "hq"
			if role == "" && !hq {
				role = "coder"
			}
			if e.Role != "" {
				hq = false
			}
			err := guard.Check(e.Cmd, t.TempDir(), role, hq)
			if wantBlock && err == nil {
				t.Errorf("must_block %q (mode %s, role %q) was allowed", e.Cmd, m, role)
			}
			if !wantBlock && err != nil {
				t.Errorf("must_allow %q (mode %s, role %q) was blocked: %v", e.Cmd, m, role, err)
			}
			checked++
		}
	}
	return checked
}

func TestGuardDiffCorpusMustBlock(t *testing.T) {
	t.Setenv("FACTORY_HOME", t.TempDir())
	block, _ := loadCorpus(t)
	if n := corpusCheck(t, block, true, []string{"worker"}); n < 1000 {
		t.Fatalf("only %d must_block entries checked; the corpus or the filter is broken", n)
	}
}

func TestGuardDiffCorpusMustAllow(t *testing.T) {
	t.Setenv("FACTORY_HOME", t.TempDir())
	_, allow := loadCorpus(t)
	if n := corpusCheck(t, allow, false, []string{"worker", "hq"}); n < 500 {
		t.Fatalf("only %d must_allow entries checked; the corpus or the filter is broken", n)
	}
}

func TestGuardBuildWorkerGuardHasNoDependencyOutsideThisModule(t *testing.T) {
	root, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	gobin, err := goTool()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gobin, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./cmd/worker-guard")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v; run it by hand in go/", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if !strings.HasPrefix(p, "factory/go") {
			t.Errorf("worker-guard depends on %s: the guard stays dependency-free (ADR 0001)", p)
		}
	}
}
