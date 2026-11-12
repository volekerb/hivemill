package blockcond

import (
	"strings"
	"testing"
)

const errSubstrUnknown = "unknown condition"

func TestParseEachType(t *testing.T) {
	cases := []struct {
		token string
		want  Cond
	}{
		{"task:T-0123", Cond{Type: "task", Arg: "T-0123"}},
		{"pr:#554:merged", Cond{Type: "pr", Arg: "#554", Form: "merged"}},
		{"pr:#554:closed", Cond{Type: "pr", Arg: "#554", Form: "closed"}},
		{"file:rel/a.txt", Cond{Type: "file", Arg: "rel/a.txt"}},
		{"main:green", Cond{Type: "main", Arg: "green"}},
		{"config:task_clones=1", Cond{Type: "config", Arg: "task_clones=1"}},
		{"owner:M31382", Cond{Type: "owner", Arg: "M31382"}},
		{"check:gh-worker-ro-token-mode-600", Cond{Type: "check", Arg: "gh-worker-ro-token-mode-600"}},
		{"check:gh-worker-rw-token-mode-600", Cond{Type: "check", Arg: "gh-worker-rw-token-mode-600"}},
		{"until:2026-11-19T00:00:00+01:00", Cond{Type: "until", Arg: "2026-11-19T00:00:00+01:00"}},
		{"until:2026-11-19T00:00:00Z", Cond{Type: "until", Arg: "2026-11-19T00:00:00Z"}},
		{"goal:G-041:done", Cond{Type: "goal", Arg: "G-041", Form: "done"}},
		{"goal:G-041:released", Cond{Type: "goal", Arg: "G-041", Form: "released"}},
	}
	for _, tc := range cases {
		got, err := Parse(tc.token)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.token, err)
		}
		if got.Met != nil {
			t.Fatalf("Parse(%q): Met should be nil", tc.token)
		}
		if got != tc.want {
			t.Fatalf("Parse(%q) = %#v, want %#v", tc.token, got, tc.want)
		}
	}
}

func TestParseRejectsUnknown(t *testing.T) {
	bad := []string{
		"repo:upstream",
		"shell:true",
		"task:T-abc",
		"task:0123",
		"pr:#554:open",
		"pr:554:merged",
		"file:",
		"file:/abs.txt",
		"file:../secret",
		"file:lane:extra",
		"main:red",
		"config:task_clones",
		"owner:M",
		"owner:31382",
		"check:rm-rf",
		"check:gh-worker-ro-token-mode-601",
		"goal:G-041:open",
		"goal:G041:done",
		"nocolon",
		":onlyrest",
		"task:",
	}
	for _, token := range bad {
		_, err := Parse(token)
		if err == nil {
			t.Fatalf("Parse(%q): want error", token)
		}
		if !strings.Contains(err.Error(), errSubstrUnknown) {
			t.Fatalf("Parse(%q): error %q missing %q", token, err.Error(), errSubstrUnknown)
		}
	}
}

func TestUntilTimezone(t *testing.T) {
	ok := []string{
		"until:2026-11-19T00:00:00+01:00",
		"until:2026-11-19T00:00:00Z",
	}
	for _, token := range ok {
		if _, err := Parse(token); err != nil {
			t.Fatalf("Parse(%q): %v", token, err)
		}
	}
	naive := "until:2026-11-19T00:00:00"
	_, err := Parse(naive)
	if err == nil {
		t.Fatalf("Parse(%q): want error for missing timezone", naive)
	}
	if !strings.Contains(err.Error(), errSubstrUnknown) {
		t.Fatalf("Parse(%q): error %q missing %q", naive, err.Error(), errSubstrUnknown)
	}
}

func TestParseRejectsExtraColonsInTask(t *testing.T) {
	_, err := Parse("task:T-1:extra")
	if err == nil || !strings.Contains(err.Error(), errSubstrUnknown) {
		t.Fatalf("task with extra segment: got %v", err)
	}
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		"task:T-1",
		"pr:#1:merged",
		"file:a/b",
		"check:gh-worker-rw-token-mode-600",
		"until:2026-11-19T00:00:00Z",
		"repo:x",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, token string) {
		got, err := Parse(token)
		if err != nil {
			if !strings.Contains(err.Error(), errSubstrUnknown) {
				t.Fatalf("error must contain %q: %v", errSubstrUnknown, err)
			}
			return
		}
		if got.Type == "" || got.Arg == "" {
			t.Fatalf("success with empty fields: %#v", got)
		}
	})
}
