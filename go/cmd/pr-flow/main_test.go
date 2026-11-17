package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

// stubGH puts a fake gh on PATH (the process boundary). It prints one PR list and appends its arguments to a log.
func stubGH(t *testing.T, exit int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\n"
	if exit != 0 {
		script += "echo 'HTTP 403: rate limit' >&2\nexit 1\n"
	} else {
		script += `echo '[{"number": 1, "state": "MERGED", "createdAt": "2026-11-10T10:00:00Z", ` +
			`"mergedAt": "2026-11-10T13:00:00Z", "headRefName": "factory/t-0001", "mergeStateStatus": "UNKNOWN"}]'` + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func eventsFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	body := `{"ts": "2026-11-10T10:00:05Z", "actor": "claude-coder-t-0001", "entity": "task", "entity_id": "T-0001", ` +
		`"action": "pr-create", "detail": "o/r factory/t-0001"}` + "\nnot json\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunText(t *testing.T) {
	log := stubGH(t, 0)
	ev := eventsFile(t)
	before, err := os.ReadFile(ev)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if rc := run([]string{"--repo", "o/r", "--since", "24h", "--events", ev}, &out, &errb, now); rc != 0 {
		t.Fatalf("exit %d, stderr: %s", rc, errb.String())
	}
	for _, prefix := range []string{"merged per day", "open-to-merge", "open over 6 h", "stage hours"} {
		if !strings.Contains("\n"+out.String(), "\n"+prefix) {
			t.Errorf("no line starting %q in:\n%s", prefix, out.String())
		}
	}
	if !strings.Contains(out.String(), "2 event lines (1 skipped)") {
		t.Errorf("header does not report the skipped line:\n%s", out.String())
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 {
		t.Fatalf("gh was called %d times, want 2:\n%s", len(lines), calls)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "pr list -R o/r --state all ") {
			t.Errorf("gh call is not a read-only pr list: %s", l)
		}
	}
	after, err := os.ReadFile(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the events file was modified")
	}
}

func TestRunJSONDefaultEvents(t *testing.T) {
	stubGH(t, 0)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(eventsFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "events.jsonl"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_HOME", home)
	var out, errb bytes.Buffer
	if rc := run([]string{"--repo", "o/r", "--since", "1d", "--json"}, &out, &errb, now); rc != 0 {
		t.Fatalf("exit %d, stderr: %s", rc, errb.String())
	}
	var got struct {
		Repo        string `json:"repo"`
		OpenToMerge []struct {
			Org    string  `json:"org"`
			N      int     `json:"n"`
			Median float64 `json:"median_h"`
		} `json:"open_to_merge"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json output does not parse: %v\n%s", err, out.String())
	}
	if got.Repo != "o/r" || len(got.OpenToMerge) != 4 || got.OpenToMerge[1].Org != "claude" ||
		got.OpenToMerge[1].N != 1 || got.OpenToMerge[1].Median != 3 {
		t.Errorf("unexpected JSON: %+v", got)
	}
}

func TestRunRefusals(t *testing.T) {
	stubGH(t, 0)
	ev := eventsFile(t)
	t.Setenv("FACTORY_HOME", "")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no repo", []string{"--events", ev}, "--repo is required"},
		{"extra argument", []string{"--repo", "o/r", "--events", ev, "extra"}, "usage: pr-flow"},
		{"bad repo", []string{"--repo", "nope", "--events", ev}, "want owner/name"},
		{"bad since", []string{"--repo", "o/r", "--since", "soon", "--events", ev}, "--since"},
		{"missing events file", []string{"--repo", "o/r", "--events", filepath.Join(t.TempDir(), "none.jsonl")}, "none.jsonl"},
		{"no FACTORY_HOME and no --events", []string{"--repo", "o/r"}, "FACTORY_HOME is not set"},
		{"unknown flag", []string{"--repo", "o/r", "--write"}, "flag provided but not defined"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if rc := run(c.args, &out, &errb, now); rc != 2 {
				t.Errorf("exit %d, want 2", rc)
			}
			if !strings.Contains(errb.String(), c.want) || out.Len() != 0 {
				t.Errorf("stderr %q (want %q), stdout %q (want empty)", errb.String(), c.want, out.String())
			}
		})
	}
}

func TestRunGhFailure(t *testing.T) {
	stubGH(t, 1)
	var out, errb bytes.Buffer
	if rc := run([]string{"--repo", "o/r", "--events", eventsFile(t)}, &out, &errb, now); rc != 2 {
		t.Errorf("exit %d, want 2", rc)
	}
	if !strings.Contains(errb.String(), "rate limit") || out.Len() != 0 {
		t.Errorf("stderr %q, stdout %q", errb.String(), out.String())
	}
}

func TestRunRedMain(t *testing.T) {
	stubGH(t, 0)
	p := filepath.Join(t.TempDir(), "events.jsonl")
	body := `{"ts": "2026-11-10T10:00:00Z", "entity": "redmain", "entity_id": "o/r", "action": "red-main"}` + "\n" +
		`{"ts": "2026-11-10T12:00:00Z", "entity": "redmain", "entity_id": "x/y", "action": "red-main"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if rc := run([]string{"--repo", "o/r", "--since", "1d", "--events", p}, &out, &errb, now); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb.String())
	}
	if !strings.Contains(out.String(), "events 1, events/day 1.00, windows 1, red hours 14.0") || strings.Contains(out.String(), "x/y") {
		t.Errorf("red main section wrong:\n%s", out.String())
	}
	out.Reset()
	if rc := run([]string{"--repo", "o/r", "--since", "1d", "--events", p, "--json", "--red-main-all"}, &out, &errb, now); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb.String())
	}
	var got struct {
		RedMain  map[string]any   `json:"red_main"`
		AllRepos []map[string]any `json:"red_main_repos"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RedMain["events"] != float64(1) || len(got.AllRepos) != 2 {
		t.Errorf("json: %s", out.String())
	}
}
