package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factory/go/internal/hm"
)

func TestGuardRefusesReleaseSubmit(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(home, "db", "factory.db")
	s, err := hm.CreateSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const refusal = "App Store release refused: the owner has not answered release build N for this build"
	run := func(command string) (int, string) {
		payload, err := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": home,
			"tool_input": map[string]any{"command": command}})
		if err != nil {
			t.Fatal(err)
		}
		var out, errs bytes.Buffer
		code := cmdGuard([]string{"--home", home, "--role", "task"}, bytes.NewReader(payload), &out, &errs)
		return code, errs.String()
	}
	const submit = "fastlane release build_number:7"
	if code, msg := run(submit); code != 2 || !strings.Contains(msg, refusal) {
		t.Fatalf("no answer: exit %d, %q; want 2 and the refusal", code, msg)
	}

	answer := func(by, text string) {
		id, _, err := s.AddAsk(hm.Ask{Question: "release?", AskedBy: "planner"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CloseAsk(id, "answered", text, by); err != nil {
			t.Fatal(err)
		}
	}
	answer("planner", "release build 7")
	answer("owner", "release build 71")
	if code, msg := run(submit); code != 2 || !strings.Contains(msg, refusal) {
		t.Fatalf("no owner answer for build 7: exit %d, %q; want 2 and the refusal", code, msg)
	}

	answer("owner", "release build 7")
	if _, msg := run(submit); strings.Contains(msg, "App Store release refused") {
		t.Fatalf("answered by the owner: still refused: %q", msg)
	}

	// no DB, no answer
	home = t.TempDir()
	if code, msg := run(submit); code != 2 || !strings.Contains(msg, refusal) {
		t.Fatalf("no DB: exit %d, %q; want 2 and the refusal", code, msg)
	}
	// the same bytes reach the guard: a payload it cannot parse still blocks there
	var out, errs bytes.Buffer
	if code := cmdGuard([]string{"--home", home, "--role", "task"}, strings.NewReader("{"), &out, &errs); code != 2 ||
		!strings.Contains(errs.String(), "worker-guard") {
		t.Fatalf("bad payload: exit %d, %q; want the guard's block", code, errs.String())
	}
}
