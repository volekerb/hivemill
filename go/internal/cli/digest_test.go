package cli

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDigestMDTable(t *testing.T) {
	if got := dgMDTable(nil, []string{"a"}); got != "_none_\n" {
		t.Fatalf("empty table: %q", got)
	}
	long := strings.Repeat("é", 130)
	got := dgMDTable([][]string{{"a|b", "x\ny", long}}, []string{"c1", "c2", "c3"})
	want := "| c1 | c2 | c3 |\n|---|---|---|\n| a\\|b | x y | " + strings.Repeat("é", 120) + " |\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDigestParseDeps(t *testing.T) {
	d, err := dgParseDeps(`["T-1", 5, "T-2"]`)
	if err != nil || !d.extra || !reflect.DeepEqual(d.ids, map[string]bool{"T-1": true, "T-2": true}) {
		t.Fatalf("got %+v, %v", d, err)
	}
	if d.subsetOf(map[string]bool{"T-1": true, "T-2": true}) {
		t.Fatal("a non-string member can never be done")
	}
	if d, err := dgParseDeps(""); err != nil || !d.subsetOf(nil) {
		t.Fatalf("empty deps are ready: %+v %v", d, err)
	}
	for _, bad := range []string{`{"a": 1}`, `[["T-1"]]`, `not json`, `null`} {
		if _, err := dgParseDeps(bad); !errors.Is(err, errDelegate) {
			t.Errorf("%q: want delegate, got %v", bad, err)
		}
	}
}

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

func FuzzDgParseDeps(f *testing.F) {
	for _, s := range []string{"", "[]", `["T-1","T-2"]`, `["T-1",3]`, `[[1]]`, `{"a":1}`, "NaN", "["} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		d, err := dgParseDeps(raw)
		if err != nil {
			return
		}
		if raw == "" {
			if len(d.ids) != 0 || d.extra {
				t.Fatalf("empty input must give an empty set, got %+v", d)
			}
			return
		}
		// documented rule: ids are exactly the string members of the JSON list
		var list []any
		if jerr := json.Unmarshal([]byte(raw), &list); jerr != nil {
			t.Fatalf("%q accepted but is not a JSON list: %v", raw, jerr)
		}
		want := map[string]bool{}
		extra := false
		for _, x := range list {
			if s, ok := x.(string); ok {
				want[s] = true
			} else {
				extra = true
			}
		}
		if !reflect.DeepEqual(d.ids, want) || d.extra != extra {
			t.Fatalf("%q: got %+v, want ids=%v extra=%v", raw, d, want, extra)
		}
	})
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
