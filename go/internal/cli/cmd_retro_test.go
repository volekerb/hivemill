package cli

import (
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
