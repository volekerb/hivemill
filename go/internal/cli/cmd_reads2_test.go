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

// The Fuzz targets below: no panic; an accepted argv is exactly one of the S2q spec forms, everything else delegates.

func FuzzParseAlerts(f *testing.F) {
	f.Add("--json", "", "")
	f.Add("--jso", "--json", "")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		rest := []string{a, b, c}
		for n := 0; n <= 3; n++ {
			got, ok := parseAlerts(rest[:n])
			if !ok {
				continue
			}
			if n > 1 || (n == 1 && rest[0] != "--json") || got.(alertsCmd).json != (n == 1) {
				t.Fatalf("alerts accepted %q", rest[:n])
			}
		}
	})
}

func FuzzParseGateShow(f *testing.F) {
	f.Add("T-1", "")
	f.Add("-x", "y")
	f.Fuzz(func(t *testing.T, a, b string) {
		rest := []string{a, b}
		for n := 0; n <= 2; n++ {
			got, ok := parseGateShow(rest[:n])
			if !ok {
				continue
			}
			if n > 1 || (n == 1 && (rest[0] == "" || rest[0][0] == '-' || got.(gateShowCmd).task != rest[0])) {
				t.Fatalf("gate show accepted %q", rest[:n])
			}
		}
	})
}

func FuzzParseGateList(f *testing.F) {
	f.Add("--limit", "-5", "--project", "p")
	f.Add("--limit=3", "", "--", "x")
	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		rest := []string{a, b, c, d}
		for n := 0; n <= 4; n++ {
			got, ok := parseGateList(rest[:n])
			if !ok {
				continue
			}
			g := got.(gateListCmd)
			if n%2 != 0 {
				t.Fatalf("gate list accepted odd argv %q", rest[:n])
			}
			for i := 0; i < n; i += 2 {
				if (rest[i] != "--project" && rest[i] != "--limit") || rest[i+1] == "" {
					t.Fatalf("gate list accepted %q", rest[:n])
				}
				if rest[i] == "--project" && g.project != rest[i+1] {
					t.Fatalf("gate list project %q", rest[:n])
				}
			}
		}
	})
}

func FuzzParseGoalTokens(f *testing.F) {
	f.Add("G-1", "")
	f.Add("-h", "")
	f.Fuzz(func(t *testing.T, a, b string) {
		rest := []string{a, b}
		for n := 0; n <= 2; n++ {
			got, ok := parseGoalTokens(rest[:n])
			if ok && (n != 1 || rest[0] == "" || rest[0][0] == '-' || got.(goalTokensCmd).id != rest[0]) {
				t.Fatalf("goal tokens accepted %q", rest[:n])
			}
		}
	})
}
