package cli

import (
	"reflect"
	"testing"

	"factory/go/internal/identity"
)

// TestStmtOrderLifecycle: the ordered (verb, table) list per argv form, transcribed from bin/factory_cli.py
// (goal_close, goal_move_org, cmd_team, cmd_project, close_refused, goal_auto_active).
func TestStmtOrderLifecycle(t *testing.T) {
	ev := Stmt{"INSERT", "events"}
	cases := []struct {
		name string
		who  string
		args []string
		code int
		want []Stmt
	}{
		// goal_close: UPDATE goals, 2 events, then one INSERT messages per recipient (director, then each lead)
		{"close ok", "claude-director", []string{"goal", "close", "G-1", "--reason", "r"}, 0,
			[]Stmt{{"UPDATE", "goals"}, ev, ev, {"INSERT", "messages"}, {"INSERT", "messages"}}},
		// close_refused: the event is written, then die(4): the partial write the golden db.diff pins
		{"close refused", "claude-director", []string{"goal", "close", "G-3", "--reason", "r"}, 4, []Stmt{ev}},
		// goal_move_org: UPDATE goals, event, one message per org of {old, new}
		{"org move", "claude-director", []string{"goal", "org", "G-8", "cursor"}, 0,
			[]Stmt{{"UPDATE", "goals"}, ev, {"INSERT", "messages"}, {"INSERT", "messages"}}},
		{"org same", "claude-director", []string{"goal", "org", "G-8", "claude"}, 0, nil},
		// cmd_team new: INSERT teams, event, goal_auto_active (UPDATE goals + event only when it changed a row)
		{"team new active goal", "claude-director", []string{"team", "new", "--goal", "G-1", "--no-lead"}, 0,
			[]Stmt{{"INSERT", "teams"}, ev, {"UPDATE", "goals"}}},
		{"team new proposed goal", "claude-director", []string{"team", "new", "--goal", "G-6", "--no-lead"}, 0,
			[]Stmt{{"INSERT", "teams"}, ev, {"UPDATE", "goals"}, ev}},
		// team close: UPDATE teams, (lead) UPDATE agents, event
		{"team close lead", "claude-director", []string{"team", "close", "g-1-a"}, 0, []Stmt{{"UPDATE", "teams"}, {"UPDATE", "agents"}, ev}},
		{"team close no lead", "claude-director", []string{"team", "close", "g-3-a"}, 0, []Stmt{{"UPDATE", "teams"}, ev}},
		{"team reopen", "claude-director", []string{"team", "reopen", "g-8-a"}, 0, []Stmt{{"UPDATE", "teams"}, ev}},
		// cmd_project add: INSERT projects, event; update: checks, then gate columns in order, then gh_repo
		{"project add", "claude-boss", []string{"project", "add", "p2"}, 0, []Stmt{{"INSERT", "projects"}, ev}},
		{"project update all", "claude-director", []string{"project", "update", "P-1", "--required-checks", "a", "--gate-cmd", "x",
			"--gate-count-cmd", "y", "--gate-timeout-s", "5", "--gh", "o/n"}, 0,
			[]Stmt{{"UPDATE", "projects"}, ev, {"UPDATE", "projects"}, ev, {"UPDATE", "projects"}, ev, {"UPDATE", "projects"}, ev, {"UPDATE", "projects"}, ev}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errs, trace := gwTraceRun(t, "lifecycle", c.who, "", c.args...)
			if code != c.code {
				t.Fatalf("exit %d, want %d: %s%s", code, c.code, out, errs)
			}
			if !reflect.DeepEqual(trace, c.want) {
				t.Fatalf("statements = %v, want %v", trace, c.want)
			}
		})
	}
}

// Refusals Python makes before its first write write nothing in Go.
func TestLifecycleRefusalsWriteNothing(t *testing.T) {
	for _, c := range []struct {
		who  string
		args []string
		code int
	}{
		{"claude-lead-1", []string{"goal", "close", "G-1", "--reason", "x"}, 3},
		{"claude-director", []string{"goal", "close", "G-1"}, 2},
		{"claude-director", []string{"goal", "close", "G-99", "--reason", "x"}, 1},
		{"claude-director", []string{"goal", "org", "G-8", "mars"}, 2},
		{"claude-boss", []string{"team", "new", "--goal", "G-1", "--no-lead"}, 3},
		{"claude-director", []string{"team", "reopen", "g-3-a"}, 3},
		{"claude-director", []string{"team", "reopen", "g-2-a"}, 3},
		{"claude-lead-1", []string{"project", "add", "p"}, 3},
		{"claude-boss", []string{"project", "update", "P-1", "--gate-cmd", "x"}, 3},
		{"claude-director", []string{"project", "update", "P-1", "--gate-timeout-s", "0"}, 2},
		{"claude-director", []string{"project", "update", "P-1", "--gh", "bad"}, 2},
		{"claude-director", []string{"project", "update", "P-1"}, 1},
	} {
		code, _, _, trace := gwTraceRun(t, "lifecycle", c.who, "", c.args...)
		if len(trace) != 0 || code != c.code {
			t.Errorf("%v: exit %d (want %d), statements %v", c.args, code, c.code, trace)
		}
	}
}

func TestLifecycleParse(t *testing.T) {
	type tc struct {
		parse func([]string) (any, bool)
		args  []string
		ok    bool
	}
	team, project := lcParseTeam("new"), lcParseProject("update")
	for _, c := range []tc{
		{team, []string{"--goal", "G-1", "--no-lead"}, true},
		{team, []string{"--goal=G-1", "--scope=-x", "--no-lead"}, true},
		{team, []string{"--goal", "G-1"}, false},                           // spawns a lead
		{team, []string{"--no-lead"}, false},                               // no goal: Python crashes
		{team, []string{"--goal", "G-1", "--no-lead", "--no-lead"}, false}, // repeated
		{team, []string{"--goal", "G-1", "--no-l"}, false},                 // abbreviation
		{team, []string{"--goal", "Ġ-1", "--no-lead"}, false},              // non-ASCII: lower() is Python's
		{lcParseTeam("close"), []string{"t"}, true},
		{lcParseTeam("close"), []string{}, false},
		{lcParseTeam("close"), []string{"t", "u"}, false},
		{project, []string{"P-1", "--gate-timeout-s", "5"}, true},
		{project, []string{"P-1", "--gate-timeout-s=x"}, false},
		{project, []string{"P-1", "--gate-timeout-s", "١"}, false}, // Arabic-Indic digit: int() accepts, RE2 does not
		{project, []string{"P-1", "--repo", "/x"}, false},          // git rev-parse: Python
		{project, []string{"P-1", "--gate-cmd", "-x"}, false},
		{project, []string{"--gh", "o/n"}, false},
		{lcParseProject("add"), []string{"p", "--no-auto-merge", "--name", "n"}, true},
		{lcParseProject("add"), []string{"p", "--no-auto-merge=1"}, false},
		{lcParseProject("add"), []string{""}, false},
	} {
		if _, ok := c.parse(c.args); ok != c.ok {
			t.Errorf("%v: ok=%v, want %v", c.args, ok, c.ok)
		}
	}
}

// G8: \d and \w differ between Python and RE2; the --gh pattern is an explicit ASCII class, so non-ASCII letters and
// digits never match, and a trailing newline (Python's `$` would match it; fullmatch does not) is refused.
func TestLifecycleGHRepoASCII(t *testing.T) {
	for s, want := range map[string]bool{"o/n": true, "a.b_c-d/e": true, "é/x": false, "x/١": false, "o/n\n": false, "o/n/p": false, "o": false, "/n": false} {
		if got := lcGHRepoRE.MatchString(s); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
}

func TestLifecycleNormChecks(t *testing.T) {
	for in, want := range map[string]string{" a, b ,,c ": "a,b,c", "": "", ",,": "", "a , b": "a,b", "x": "x"} {
		if got := lcNormChecks(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestLifecycleExpandUser(t *testing.T) {
	c := &ctx{env: identity.Env{Getenv: func(k string) string {
		if k == "HOME" {
			return "/srv/h/"
		}
		return ""
	}}}
	for in, want := range map[string]string{"~": "/srv/h", "~/x//y/": "/srv/h/x/y", "/a/./b/": "/a/b", "rel": "rel", "./r": "r"} {
		got, err := lcExpandUser(c, in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"~bob/x", "//net/x"} {
		if _, err := lcExpandUser(c, in); err != errDelegate {
			t.Errorf("%q should delegate, got %v", in, err)
		}
	}
}

func FuzzLcSplit(f *testing.F) {
	f.Add("P-1", "--gate-timeout-s", "5")
	f.Add("--goal=G-1", "--no-lead", "x")
	f.Add("--gh", "-١", "")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		for _, flags := range []map[string]byte{lcTeamFlags, lcProjectFlags} {
			got, ok := lcSplit([]string{a, b, c}, 1, flags)
			if !ok {
				continue
			}
			if len(got.pos) > 1 {
				t.Fatalf("accepted %d positionals", len(got.pos))
			}
			for fl := range got.str {
				if flags[fl] != 's' {
					t.Fatalf("string flag %s has kind %c", fl, flags[fl])
				}
			}
			for fl := range got.num {
				if flags[fl] != 'i' {
					t.Fatalf("int flag %s has kind %c", fl, flags[fl])
				}
			}
			for fl := range got.bools {
				if flags[fl] != 'b' {
					t.Fatalf("bool flag %s has kind %c", fl, flags[fl])
				}
			}
		}
	})
}

func FuzzLcParse(f *testing.F) {
	f.Add("G-1", "--no-lead", "x")
	f.Add("P-1", "--repo", "/x")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		args := []string{a, b, c}
		for _, p := range []func([]string) (any, bool){lcParseTeam("new"), lcParseTeam("close"), lcParseProject("add"), lcParseProject("update")} {
			if v, ok := p(args); ok {
				if got := v.(gwArgs); len(got.pos) > 1 {
					t.Fatalf("accepted %d positionals", len(got.pos))
				}
			}
		}
	})
}
