package cli

import (
	"regexp"
	"strings"
	"testing"
)

func TestGoalShowParse(t *testing.T) {
	ok := map[string]goalShowCmd{
		"G-1":               {id: "G-1"},
		"G-1 --full":        {id: "G-1", full: true},
		"--full G-1":        {id: "G-1", full: true},
		"--full G-1 --full": {id: "G-1", full: true},
	}
	for in, want := range ok {
		got, good := parseGoalShow(strings.Fields(in))
		if !good || got.(goalShowCmd) != want {
			t.Errorf("%q: got %v ok=%v, want %v", in, got, good, want)
		}
	}
	for _, in := range [][]string{{}, {"--full"}, {"G-1", "x"}, {"--full=1", "G-1"}, {"--ful", "G-1"}, {"-h"}, {""}, {"G-1", ""}, {"--", "G-1"}} {
		if _, good := parseGoalShow(in); good {
			t.Errorf("%q must delegate", in)
		}
	}
}

// TestTallyCountersMatchPython: gsTallyCounters is the literal TALLY_COUNTERS of bin/factory_cli.py, in order.
func TestTallyCountersMatchPython(t *testing.T) {
	m := regexp.MustCompile(`\nTALLY_COUNTERS = \(([^)]*)\)`).FindStringSubmatch(pySource(t))
	if m == nil {
		t.Fatal("TALLY_COUNTERS not found in the Python source")
	}
	got := unquoteAll(regexp.MustCompile(`"[^"]+"`).FindAllString(m[1], -1))
	if strings.Join(got, ",") != strings.Join(gsTallyCounters, ",") {
		t.Fatalf("gsTallyCounters %v != Python TALLY_COUNTERS %v", gsTallyCounters, got)
	}
}

// TestGoalShowStandardStatusesMatchPython: the compact task counts list the standard statuses of show_goal first.
func TestGoalShowStandardStatusesMatchPython(t *testing.T) {
	want := `std = ("` + strings.Join(gsStandard, `", "`) + `")`
	if !strings.Contains(pySource(t), want) {
		t.Fatalf("show_goal no longer has %s", want)
	}
}

func FuzzParseGoalShow(f *testing.F) {
	f.Add("G-1", "--full", "")
	f.Add("", "-h", "x")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		got, ok := parseGoalShow([]string{a, b, c})
		if !ok {
			return
		}
		ids := 0
		for _, x := range []string{a, b, c} {
			switch {
			case x == "--full":
			case x != "" && !strings.HasPrefix(x, "-"):
				ids++
			default:
				t.Fatalf("accepted %q", x)
			}
		}
		if ids != 1 || got.(goalShowCmd).id == "" {
			t.Fatalf("accepted %q %q %q", a, b, c)
		}
	})
}
