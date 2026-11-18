package digest

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
