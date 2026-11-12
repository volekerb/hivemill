package cli

import (
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
