package cli

import "testing"

// TestReads2Parse: the exact argv shapes parse, every other form is left to Python (ok=false).
func TestReads2Parse(t *testing.T) {
	cases := []struct {
		name string
		args []string
		fn   func([]string) (any, bool)
		ok   bool
	}{
		{"team list extra", []string{"--bogus"}, r2NoArgs, false},
		{"team list", nil, r2NoArgs, true},
		{"alerts", nil, parseAlerts, true},
		{"alerts --json", []string{"--json"}, parseAlerts, true},
		{"alerts abbrev", []string{"--jso"}, parseAlerts, false},
		{"alerts repeated", []string{"--json", "--json"}, parseAlerts, false},
		{"gate show none", nil, parseGateShow, true},
		{"gate show id", []string{"T-1"}, parseGateShow, true},
		{"gate show two", []string{"T-1", "T-2"}, parseGateShow, false},
		{"gate show dash", []string{"-x"}, parseGateShow, false},
		{"gate list", nil, parseGateList, true},
		{"gate list limit", []string{"--limit", "0"}, parseGateList, true},
		{"gate list neg", []string{"--limit", "-5"}, parseGateList, true},
		{"gate list limit x", []string{"--limit", "x"}, parseGateList, false},
		{"gate list limit eq", []string{"--limit=3"}, parseGateList, false},
		{"gate list neg flag", []string{"--limit", "-x"}, parseGateList, false},
		{"gate list project dash", []string{"--project", "-p"}, parseGateList, false},
		{"gate list dup", []string{"--limit", "1", "--limit", "2"}, parseGateList, false},
		{"gate list missing value", []string{"--project"}, parseGateList, false},
		{"gate list positional", []string{"T-1"}, parseGateList, false},
		{"tokens", []string{"G-1"}, parseGoalTokens, true},
		{"tokens none", nil, parseGoalTokens, false},
		{"tokens dash", []string{"-G"}, parseGoalTokens, false},
		{"tokens two", []string{"G-1", "G-2"}, parseGoalTokens, false},
	}
	for _, c := range cases {
		if _, ok := c.fn(c.args); ok != c.ok {
			t.Errorf("%s: parse ok=%v, want %v", c.name, ok, c.ok)
		}
	}
}

// TestGateListLimitParsed: --limit keeps its Python int value, the default is 20.
func TestGateListLimitParsed(t *testing.T) {
	for args, want := range map[string]int64{"": 20, "0": 0, "-5": -5, "+7": 7} {
		var rest []string
		if args != "" {
			rest = []string{"--limit", args}
		}
		p, ok := parseGateList(rest)
		if !ok || p.(gateListCmd).limit != want {
			t.Errorf("--limit %q: got %v ok=%v, want %d", args, p, ok, want)
		}
	}
}
