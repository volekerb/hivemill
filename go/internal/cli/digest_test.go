package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestDigestParseArgv(t *testing.T) {
	p, ok := parseDigest([]string{"--goal", "G-1", "--hours", "6", "--date", "2026-11-12"})
	if !ok || p.(digestCmd) != (digestCmd{goal: "G-1", date: "2026-11-12", hours: "6"}) {
		t.Fatalf("got %+v %v", p, ok)
	}
	for _, bad := range [][]string{nil, {"--send"}, {"--goal", "G-1", "--send"}, {"--goal=G-1"}, {"--go", "G-1"},
		{"--goal", "G-1", "--goal", "G-2"}, {"--goal", "G-1", "--hours", "-3"}} {
		if _, ok := parseDigest(bad); ok {
			t.Errorf("%v should delegate", bad)
		}
	}
}

func TestDigestHoursBound(t *testing.T) {
	// The bound is checked before the database is touched, so a nil ctx is enough for the refusal.
	if _, err := runDigest(nil, digestCmd{goal: "G-1", hours: "100001"}); !errors.Is(err, errDelegate) {
		t.Fatalf("hours above the bound must delegate, got %v", err)
	}
	if _, err := runDigest(nil, digestCmd{goal: "G-1", hours: "x"}); !errors.Is(err, errDelegate) {
		t.Fatalf("a non-integer must delegate, got %v", err)
	}
}

func FuzzParseDigest(f *testing.F) {
	for _, s := range []string{"--goal\x00G-1", "--goal\x00G-1\x00--hours\x006\x00--date\x002026-11-12", "--goal\x00a,b", "--send", "--goal", "--goal\x00G-1\x00--goal\x00G-2", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Split(joined, "\x00")
		p, ok := parseDigest(args)
		if !ok {
			return
		}
		c := p.(digestCmd)
		// S2s forms: only --goal/--date/--hours, each at most once with a non-empty value not starting with "-"; --goal required.
		seen := map[string]bool{}
		for i := 0; i < len(args); i += 2 {
			a := args[i]
			if a != "--goal" && a != "--date" && a != "--hours" {
				t.Fatalf("%q accepted with unlisted flag %q", args, a)
			}
			if seen[a] || i+1 >= len(args) || args[i+1] == "" || args[i+1][0] == '-' {
				t.Fatalf("%q accepted with a repeated flag or bad value", args)
			}
			seen[a] = true
		}
		if c.goal == "" || !seen["--goal"] {
			t.Fatalf("%q accepted without --goal", args)
		}
	})
}
