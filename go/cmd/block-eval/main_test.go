package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "block-eval")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

type evalResp struct {
	Results []struct {
		Index  int    `json:"index"`
		Met    bool   `json:"met"`
		Detail string `json:"detail"`
	} `json:"results"`
	AllMet bool `json:"all_met"`
}

func runEval(t *testing.T, bin string, facts map[string]any) (evalResp, int) {
	t.Helper()
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "eval")
	cmd.Stdin = bytes.NewReader(raw)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run eval: %v", err)
		}
	}
	var resp evalResp
	if code == 0 {
		if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
			t.Fatalf("decode stdout %q: %v", stdout.String(), err)
		}
	}
	return resp, code
}

const evalNow = "2026-11-12T12:00:00Z"

func baseFacts(conds []map[string]string) map[string]any {
	return map[string]any{
		"now":             evalNow,
		"main_green":      false,
		"tasks":           map[string]string{},
		"goals":           map[string]any{},
		"releases":        []any{},
		"config":          map[string]string{},
		"config_absent":   []string{},
		"decisions":       []string{},
		"prs":             map[string]any{},
		"pr_budget":       []string{},
		"open_files":      []string{},
		"files_truncated": false,
		"conditions":      conds,
	}
}

func ghWorkerROProbe() string {
	return strings.Join([]string{"gh-worker-ro", "token-mode-600"}, "-")
}

func ghWorkerROTokFile() string {
	return "gh-worker-" + "ro" + "-token"
}

func cond(typ, arg, form string) map[string]string {
	c := map[string]string{"type": typ, "arg": arg}
	if form != "" {
		c["form"] = form
	}
	return c
}

func TestEvalCommand(t *testing.T) {
	bin := buildBin(t)

	t.Run("task", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("task", "T-1", "")})
		f["tasks"] = map[string]string{"T-1": "done"}
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met || r.Results[0].Detail != "met" {
			t.Fatalf("met: code=%d resp=%+v", code, r)
		}
		f["tasks"] = map[string]string{"T-1": "cancelled"}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met || r.Results[0].Detail != "cancelled" {
			t.Fatalf("cancelled: code=%d resp=%+v", code, r)
		}
	})

	t.Run("pr_merged", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("pr", "#9", "merged")})
		f["prs"] = map[string]any{"9": map[string]string{"state": "MERGED"}}
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met || r.Results[0].Detail != "merged" {
			t.Fatalf("merged met: %+v", r)
		}
		f["prs"] = map[string]any{"9": map[string]string{"state": "OPEN"}}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met || r.Results[0].Detail != "unmet" {
			t.Fatalf("merged unmet: %+v", r)
		}
	})

	t.Run("pr_closed", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("pr", "#9", "closed")})
		f["prs"] = map[string]any{"9": map[string]string{"state": "CLOSED"}}
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met || r.Results[0].Detail != "closed" {
			t.Fatalf("closed met: %+v", r)
		}
		f["prs"] = map[string]any{"9": map[string]string{"state": "OPEN"}}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("closed unmet: %+v", r)
		}
	})

	t.Run("pr_budget", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("pr", "#99", "merged")})
		f["pr_budget"] = []string{"99"}
		f["prs"] = map[string]any{"99": map[string]string{"state": "MERGED"}}
		r, code := runEval(t, bin, f)
		if code != 0 || r.Results[0].Met || r.Results[0].Detail != "gh-budget" {
			t.Fatalf("budget: %+v", r)
		}
	})

	t.Run("file", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("file", "bin/factory_cli.py", "")})
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("file met: %+v", r)
		}
		f["open_files"] = []string{"bin/factory_cli.py"}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("file in list: %+v", r)
		}
		f["open_files"] = []string{}
		f["files_truncated"] = true
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met || r.Results[0].Detail != "gh-budget" {
			t.Fatalf("file truncated: %+v", r)
		}
	})

	t.Run("main", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("main", "green", "")})
		f["main_green"] = true
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("main met: %+v", r)
		}
		f["main_green"] = false
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("main unmet: %+v", r)
		}
	})

	t.Run("config", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("config", "task_clones=1", "")})
		f["config"] = map[string]string{"task_clones": "1"}
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("config met: %+v", r)
		}
		f["config"] = map[string]string{"task_clones": "2"}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("config wrong: %+v", r)
		}
		f = baseFacts([]map[string]string{cond("config", "missing_key=1", "")})
		f["config_absent"] = []string{"missing_key"}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met || r.Results[0].Detail != "absent" {
			t.Fatalf("config absent: %+v", r)
		}
	})

	t.Run("owner", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("owner", "M1", "")})
		f["decisions"] = []string{"M1"}
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("owner met: %+v", r)
		}
		f["decisions"] = []string{}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("owner unmet: %+v", r)
		}
	})

	t.Run("until", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("until", "2026-11-12T00:00:00Z", "")})
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("until met: %+v", r)
		}
		f["conditions"] = []map[string]string{cond("until", "2026-11-13T00:00:00Z", "")}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("until unmet: %+v", r)
		}
	})

	t.Run("goal", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("goal", "G-1", "done")})
		f["goals"] = map[string]any{"G-1": map[string]string{"status": "done", "closed_at": evalNow}}
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("goal done met: %+v", r)
		}
		f["goals"] = map[string]any{"G-1": map[string]string{"status": "cancelled", "closed_at": evalNow}}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met || r.Results[0].Detail != "cancelled" {
			t.Fatalf("goal cancelled: %+v", r)
		}
	})

	t.Run("goal_released", func(t *testing.T) {
		f := baseFacts([]map[string]string{cond("goal", "G-1", "released")})
		f["goals"] = map[string]any{"G-1": map[string]string{"status": "done", "closed_at": "2026-11-01T00:00:00Z"}}
		f["releases"] = []any{map[string]string{"tag": "v1.0.0", "published_at": "2026-11-02T00:00:00Z"}}
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("released met: %+v", r)
		}
		f["releases"] = []any{}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("released unmet: %+v", r)
		}
	})

	t.Run("check", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".config", "factory")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ghWorkerROTokFile()), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		f := baseFacts([]map[string]string{cond("check", ghWorkerROProbe(), "")})
		r, code := runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("check ro met: %+v", r)
		}
		if err := os.Remove(filepath.Join(dir, ghWorkerROTokFile())); err != nil {
			t.Fatal(err)
		}
		r, code = runEval(t, bin, f)
		if code != 0 || r.Results[0].Met {
			t.Fatalf("check ro unmet: %+v", r)
		}
		rwTok := "gh-worker-" + "rw" + "-token"
		rwProbe := strings.Join([]string{"gh-worker-rw", "token-mode-600"}, "-")
		if err := os.WriteFile(filepath.Join(dir, rwTok), []byte("y"), 0o600); err != nil {
			t.Fatal(err)
		}
		f = baseFacts([]map[string]string{cond("check", rwProbe, "")})
		r, code = runEval(t, bin, f)
		if code != 0 || !r.Results[0].Met {
			t.Fatalf("check rw met: %+v", r)
		}
	})
}

func TestParseCommand(t *testing.T) {
	bin := buildBin(t)
	for _, in := range []string{"", "\n\n", "  \n"} {
		cmd := exec.Command(bin, "parse")
		cmd.Stdin = strings.NewReader(in)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("parse empty %q: %v", in, err)
		}
		trim := strings.TrimSpace(string(out))
		if trim != "[]" {
			t.Fatalf("parse empty %q: got %q want []", in, trim)
		}
	}
	cmd := exec.Command(bin, "parse")
	cmd.Stdin = strings.NewReader("task:T-1\ncheck:" + ghWorkerROProbe() + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(string(out), `"type":"task"`) || !strings.Contains(string(out), `"T-1"`) {
		t.Fatalf("parse out: %s", out)
	}
	cmd = exec.Command(bin, "parse")
	cmd.Stdin = strings.NewReader("task:T-abc\n")
	if err := cmd.Run(); err == nil {
		t.Fatal("want exit 2 on bad token")
	}
}

func TestEvalBadJSON(t *testing.T) {
	bin := buildBin(t)
	cmd := exec.Command(bin, "eval")
	cmd.Stdin = strings.NewReader("{")
	if err := cmd.Run(); err == nil {
		t.Fatal("want exit 2 on bad json")
	}
}

func TestEvalUnknownProbeExit(t *testing.T) {
	bin := buildBin(t)
	f := baseFacts([]map[string]string{cond("check", "not-allowed", "")})
	_, code := runEval(t, bin, f)
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}
