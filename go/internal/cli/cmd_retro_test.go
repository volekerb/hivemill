package cli

import (
	"strconv"
	"strings"
	"testing"
)

const retroFixtureSQL = `
INSERT INTO tasks VALUES('T-1','https://x/pr/1'),('T-2',NULL);
INSERT INTO rejection_incidents(cause,kind,task_id,verdict_id,created) VALUES
('test','review','T-1',5,'2026-11-14T09:00:00+01:00'),
('test','qa','T-2',NULL,'2026-11-14T10:00:00+01:00'),
('scope','review','T-1',7,'2026-11-14T11:30:00+01:00'),
('old','review','T-1',1,'2026-11-12T11:30:00+01:00');`

// Python (retro/report.py build_packet, python3 -I) on the same rows.
const retroPacketWant = `{
  "window_start": "2026-11-13T12:00:00+01:00",
  "window_end": "2026-11-14T12:00:00+01:00",
  "causes": [
    {
      "cause": "test",
      "count": 2
    },
    {
      "cause": "scope",
      "count": 1
    }
  ],
  "incidents": [
    {
      "cause": "test",
      "kind": "review",
      "task_id": "T-1",
      "pr_url": "https://x/pr/1",
      "verdict_id": 5
    },
    {
      "cause": "test",
      "kind": "qa",
      "task_id": "T-2",
      "pr_url": "",
      "verdict_id": null
    },
    {
      "cause": "scope",
      "kind": "review",
      "task_id": "T-1",
      "pr_url": "https://x/pr/1",
      "verdict_id": 7
    }
  ]
}
`

const retroBody = `{"window_end": "2026-11-14T12:00:00+01:00", "causes": [{"cause": "test", "count": 2}, {"cause": "scope", "count": 1}],
 "evidence": [{"task_id": "T-1", "pr_url": "https://x/pr/1", "cause": "test"}],
 "fixes": [{"type": "check", "cause": "test", "text": "add a check"}]}`

func retroRig(t *testing.T, agent, role string) *p4cRig {
	r := newP4cRig(t, agent, role)
	r.env["FACTORY_NOW"], r.env["TZ"] = "2026-11-14T12:00:00+01:00", "Europe/Berlin" // CI runs in UTC; the want text is +01:00
	r.sql(retroFixtureSQL)
	return r
}

func TestRetroCLI(t *testing.T) {
	r := retroRig(t, "cursor-retro", "retro")
	code, out, errs := r.run("", "retro", "packet")
	if code != 0 || out != retroPacketWant || r.execs != 0 {
		t.Fatalf("packet: %d %q %q", code, out, errs)
	}
	if code, out, errs := r.run("", "retro", "show"); code != 2 || !strings.Contains(errs, "no retro report for 2026-11-14") {
		t.Fatalf("show before filing: %d %q %q", code, out, errs)
	}
	if code, _, errs := r.run("not json", "retro", "file", "-"); code != 2 || !strings.HasPrefix(errs, "factory: stdin is not valid JSON: ") {
		t.Fatalf("bad JSON: %d %q", code, errs)
	}
	if code, _, errs := r.run(`{"window_end": "x"}`, "retro", "file", "-"); code != 2 || !strings.Contains(errs, "window_end must be an ISO timestamp") {
		t.Fatalf("invalid report: %d %q", code, errs)
	}
	if n := r.count("SELECT COUNT(*) FROM retro_reports"); n != 0 {
		t.Fatalf("a refused report must not be stored: %d rows", n)
	}
	code, out, errs = r.run(retroBody, "retro", "file", "-")
	if code != 0 || out != "filed retro report for 2026-11-14\n" {
		t.Fatalf("file: %d %q %q", code, out, errs)
	}
	if n := r.count("SELECT COUNT(*) FROM retro_reports WHERE day='2026-11-14' AND author='cursor-retro' AND created='2026-11-14T12:00:00+01:00' AND fixes='[{\"type\": \"check\", \"cause\": \"test\", \"text\": \"add a check\"}]'"); n != 1 {
		t.Fatalf("stored row: %d", n)
	}
	code, out, errs = r.run("", "retro", "show", "--day", "2026-11-14")
	if code != 0 || !strings.HasPrefix(out, "{\n  \"day\": \"2026-11-14\",\n  \"causes\": [\n") || !strings.HasSuffix(out, "  \"author\": \"cursor-retro\",\n  \"created\": \"2026-11-14T12:00:00+01:00\"\n}\n") {
		t.Fatalf("show: %d %q %q", code, out, errs)
	}
}

func TestRetroCLIRoles(t *testing.T) {
	coder := retroRig(t, "coder-1", "coder")
	if code, _, errs := coder.run("", "retro", "packet"); code != 3 || !strings.Contains(errs, "role 'coder' (coder-1) may not run `factory retro packet`") {
		t.Fatalf("coder packet: %d %q", code, errs)
	}
	if code, _, errs := coder.run(retroBody, "retro", "file", "-"); code != 3 || !strings.Contains(errs, "role 'coder' (coder-1) may not run `factory retro file`; only the retro agent files reports") {
		t.Fatalf("coder file: %d %q", code, errs)
	}
	if code, _, errs := coder.run("", "retro", "show"); code != 3 || !strings.Contains(errs, "role 'coder' (coder-1) may not do this; allowed: retro, director, boss, lead") {
		t.Fatalf("coder show: %d %q", code, errs)
	}
	dir := retroRig(t, "director-1", "director")
	if code, out, _ := dir.run("", "retro", "packet"); code != 0 || out != retroPacketWant {
		t.Fatalf("director packet: %d", code)
	}
	if code, _, errs := dir.run(retroBody, "retro", "file", "-"); code != 3 || !strings.Contains(errs, "only the retro agent files reports") {
		t.Fatalf("director may not file: %d %q", code, errs)
	}
	owner := retroRig(t, "owner", "")
	if code, out, _ := owner.run("", "retro", "packet"); code != 0 || out != retroPacketWant {
		t.Fatalf("owner packet: %d", code)
	}
	// a window of the day before has other incidents: the packet follows FACTORY_NOW
	owner.env["FACTORY_NOW"] = "2026-11-12T12:00:00+01:00"
	if code, out, _ := owner.run("", "retro", "packet"); code != 0 || !strings.Contains(out, `"cause": "old"`) || strings.Contains(out, `"cause": "scope"`) {
		t.Fatalf("shifted window: %d %q", code, out)
	}
}

// ── retro adopt / fixes (T-1695). The wanted lines are the ones tests/test_retro_report.py AdoptedFixesTests pins on
// the Python side, so both implementations answer the same text.

func adoptRig(t *testing.T, agent, role string) *p4cRig {
	r := retroRig(t, agent, role)
	r.sql("INSERT INTO retro_reports(day, causes, evidence, fixes, author, created) VALUES('2026-11-14','[]','[]',?,?,?)",
		`[{"type": "check", "cause": "test", "text": "add a check"}, {"type": "prompt_rule", "cause": "scope", "text": "stay in scope"}]`,
		"cursor-retro", "2026-11-14T12:00:00+01:00")
	return r
}

func TestRetroAdoptMergedAndPending(t *testing.T) {
	r := adoptRig(t, "claude-lead", "lead")
	r.sql("INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES('2026-11-14T10:00:00+01:00','judge','task','T-1','merged','')")
	code, out, errs := r.run("", "retro", "adopt", "--day", "2026-11-14", "--fix", "1", "--task", "T-1", "--signature", " foo bar | baz qux ")
	if code != 0 || out != "adopted F-001 (check, test) T-1 adopted_at 2026-11-14T10:00:00+01:00\n" {
		t.Fatalf("adopt merged: %d %q %q", code, out, errs)
	}
	if n := r.count("SELECT COUNT(*) FROM adopted_fixes WHERE id='F-001' AND status='adopted' AND signature='foo bar|baz qux' AND pr_url='https://x/pr/1' AND author='claude-lead' AND text='add a check'"); n != 1 {
		t.Fatalf("row F-001: %d", n)
	}
	if n := r.count("SELECT COUNT(*) FROM events WHERE action='fix-adopted' AND entity='retro' AND entity_id='F-001' AND detail='2026-11-14#1 T-1'"); n != 1 {
		t.Fatalf("fix-adopted event: %d", n)
	}
	code, out, errs = r.run("", "retro", "adopt", "--day", "2026-11-14", "--fix", "2", "--task", "T-2", "--signature", "stay in")
	if code != 0 || out != "linked F-002 (prompt_rule, scope) T-2: pending until T-2 merges\n" {
		t.Fatalf("adopt pending: %d %q %q", code, out, errs)
	}
	if n := r.count("SELECT COUNT(*) FROM adopted_fixes WHERE id='F-002' AND status='pending' AND adopted_at IS NULL AND pr_url IS NULL"); n != 1 {
		t.Fatalf("row F-002: %d", n)
	}
	// the merge lands later: `retro fixes` syncs before it lists
	r.sql("INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES('2026-11-14T11:00:00+01:00','judge','task','T-2','merged','')")
	code, out, errs = r.run("", "retro", "fixes")
	want := "F-001 adopted check test T-1 adopted_at=2026-11-14T10:00:00+01:00 checked_at=- sig=foo bar|baz qux\n" +
		"F-002 adopted prompt_rule scope T-2 adopted_at=2026-11-14T11:00:00+01:00 checked_at=- sig=stay in\n" +
		"cycles: 0 (check/tool: 0)\n"
	if code != 0 || out != want {
		t.Fatalf("fixes: %d %q %q", code, out, errs)
	}
	if code, out, _ := r.run("", "retro", "fixes", "--status", "held"); code != 0 || out != "no adopted fixes\ncycles: 0 (check/tool: 0)\n" {
		t.Fatalf("fixes --status held: %d %q", code, out)
	}
	if code, _, errs := r.run("", "retro", "fixes", "--status", "bogus"); code != 2 || !strings.Contains(errs, "--status must be one of pending, adopted, held, reopened, noise") {
		t.Fatalf("fixes bad status: %d %q", code, errs)
	}
}

func TestRetroAdoptRefusals(t *testing.T) {
	r := adoptRig(t, "claude-lead", "lead")
	base := []string{"retro", "adopt", "--day", "2026-11-14", "--fix", "1", "--task", "T-1", "--signature", "foo bar"}
	with := func(kv ...string) []string {
		a := append([]string{}, base...)
		for i := 0; i < len(kv); i += 2 {
			for j := range a {
				if a[j] == kv[i] {
					a[j+1] = kv[i+1]
				}
			}
		}
		return a
	}
	for _, c := range []struct {
		name string
		argv []string
		want string
	}{
		{"no report", with("--day", "2026-01-01"), "no retro report for 2026-01-01"},
		{"fix out of range", with("--fix", "3"), "report 2026-11-14 has 2 fixes; --fix must be 1..2"},
		{"fix zero", with("--fix", "0"), "report 2026-11-14 has 2 fixes; --fix must be 1..2"},
		{"unknown task", with("--task", "T-99"), "unknown task T-99"},
		{"bad signature", with("--signature", "ab"), "signature needs 1-10 '|'-separated terms of 3-80 characters"},
		{"empty term", with("--signature", "foo bar||baz qux"), "signature needs 1-10 '|'-separated terms of 3-80 characters"},
	} {
		if code, out, errs := r.run("", c.argv...); code != 2 || out != "" || !strings.Contains(errs, c.want) {
			t.Fatalf("%s: %d %q %q", c.name, code, out, errs)
		}
	}
	if n := r.count("SELECT COUNT(*) FROM adopted_fixes"); n != 0 {
		t.Fatalf("a refused adopt must write nothing: %d rows", n)
	}
	if code, _, errs := r.run("", base...); code != 0 {
		t.Fatalf("adopt: %d %q", code, errs)
	}
	if code, _, errs := r.run("", with("--task", "T-2")...); code != 2 || !strings.Contains(errs, "fix 2026-11-14#1 is already F-001") {
		t.Fatalf("duplicate: %d %q", code, errs)
	}
}

func TestRetroAdoptRoles(t *testing.T) {
	retroAgent := adoptRig(t, "cursor-retro", "retro")
	argv := []string{"retro", "adopt", "--day", "2026-11-14", "--fix", "1", "--task", "T-1", "--signature", "foo bar"}
	if code, _, errs := retroAgent.run("", argv...); code != 3 || !strings.Contains(errs, "role 'retro' may not adopt fixes; the retro agent only proposes") {
		t.Fatalf("retro adopt: %d %q", code, errs)
	}
	if n := retroAgent.count("SELECT COUNT(*) FROM adopted_fixes"); n != 0 {
		t.Fatalf("retro adopt wrote %d rows", n)
	}
	if code, out, _ := retroAgent.run("", "retro", "fixes"); code != 0 || out != "no adopted fixes\ncycles: 0 (check/tool: 0)\n" {
		t.Fatalf("retro may list fixes: %d %q", code, out)
	}
	coder := adoptRig(t, "coder-1", "coder")
	if code, _, errs := coder.run("", argv...); code != 3 || !strings.Contains(errs, "role 'coder' (coder-1) may not do this; allowed: pm, lead, director, boss") {
		t.Fatalf("coder adopt: %d %q", code, errs)
	}
	if code, _, errs := coder.run("", "retro", "fixes"); code != 3 || !strings.Contains(errs, "allowed: pm, lead, director, boss, retro") {
		t.Fatalf("coder fixes: %d %q", code, errs)
	}
	for _, who := range [][2]string{{"claude-pm", "pm"}, {"claude-director", "director"}, {"claude-boss", "boss"}, {"owner", ""}} {
		r := adoptRig(t, who[0], who[1])
		if code, _, errs := r.run("", argv...); code != 0 {
			t.Fatalf("%s adopt: %d %q", who[0], code, errs)
		}
	}
}

func TestParseRetroAdoptDelegates(t *testing.T) {
	ok := []string{"--day", "2026-11-14", "--fix", "1", "--task", "T-1", "--signature", "foo bar"}
	if got, good := parseRetroAdopt(ok); !good || got.(retroAdoptCmd).fix != 1 {
		t.Fatalf("complete argv: %v %v", got, good)
	}
	for _, bad := range [][]string{
		{}, ok[:6], // a missing flag: Python prints the usage
		{"--day", "d", "--fix", "-1", "--task", "T", "--signature", "foo"}, // argparse owns negative numbers
		{"--day", "d", "--fix", "x", "--task", "T", "--signature", "foo"},
		{"--day", "d", "--fix", "1", "--task", "T", "--signature", "-foo"},
		{"--day", "d", "--fix", "1", "--task", "T", "--signature", "foo", "--signature", "bar"},
	} {
		if _, good := parseRetroAdopt(bad); good {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func FuzzParseRetroAdopt(f *testing.F) {
	f.Add("2026-11-14", "1", "T-1", "foo bar")
	f.Add("", "-1", "-x", "--signature")
	f.Fuzz(func(t *testing.T, day, fix, task, sig string) {
		got, ok := parseRetroAdopt([]string{"--day", day, "--fix", fix, "--task", task, "--signature", sig})
		if !ok {
			return
		}
		c := got.(retroAdoptCmd)
		if c.day != day || c.task != task || c.signature != sig || strconv.FormatInt(c.fix, 10) != strings.TrimLeft(fix, "0") && c.fix != 0 {
			t.Fatalf("parsed %+v from %q %q %q %q", c, day, fix, task, sig)
		}
		for _, x := range []string{day, task, sig, fix} {
			if x == "" || x[0] == '-' {
				t.Fatalf("accepted %q", x)
			}
		}
	})
}

func FuzzParseRetroFixes(f *testing.F) {
	f.Add("--status", "held")
	f.Add("--status", "-x")
	f.Fuzz(func(t *testing.T, a, b string) {
		got, ok := parseRetroFixes([]string{a, b})
		if !ok {
			return
		}
		if a != "--status" || b == "" || b[0] == '-' || got.(retroFixesCmd).status != b {
			t.Fatalf("accepted %q %q -> %+v", a, b, got)
		}
	})
}
