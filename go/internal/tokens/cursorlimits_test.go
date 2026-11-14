package tokens

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Cases are ported from the Python tests (module names):
//
//	tests/test_cursor_pane_limits.py  ParseTests, TickLimits
//	tests/test_web_cursor_limits.py   LIMIT_STATE, plan line and header pill text ("limited until 11-05")
const limitText = "You've hit your usage limit for Opus\n\nResets November 5, 2026\n\notherModelsLimitSwitchTarget composer-2.5\n"

func TestCursorLimits(t *testing.T) {
	t.Run("parse fixture limit error (test_cursor_pane_limits.ParseTests)", func(t *testing.T) {
		p, ok := ParseLimitError(limitText)
		if !ok || p.Model != "Opus" || p.Reset != "2026-11-05" || p.SwitchTarget != "composer-2.5" {
			t.Fatalf("%+v %v", p, ok)
		}
	})
	t.Run("no error returns none", func(t *testing.T) {
		if p, ok := ParseLimitError("All normal\n"); ok {
			t.Fatalf("got %+v", p)
		}
	})
	t.Run("reset forms", func(t *testing.T) {
		for in, want := range map[string]string{
			"usage limit for X. resets on 2026-12-31":          "2026-12-31",
			"usage limit for X\nReset Dec 3, 2026":             "2026-12-03",
			"usage limit for X\nresets  February 30 2026":      "February 30 2026", // invalid day: kept as written
			"usage limit for X\nno reset here":                 "",
			"USAGE LIMIT FOR  Grok 4.7\nResets january 9 2027": "2027-01-09",
		} {
			p, ok := ParseLimitError(in)
			if !ok || p.Reset != want {
				t.Errorf("%q: reset %q ok=%v, want %q", in, p.Reset, ok, want)
			}
		}
		if p, _ := ParseLimitError(`usage limit for "Opus".`); p.Model != "Opus" {
			t.Errorf("quoted model: %q", p.Model)
		}
		if _, ok := ParseLimitError("usage limit reached"); ok {
			t.Error("no 'for <model>' must not parse")
		}
	})
	t.Run("plan line and header pill (test_web_cursor_limits)", func(t *testing.T) {
		state := LimitState{
			"composer-2.5":  {"status": "limited", "reset": "2026-11-05", "switch_target": "composer-2.5"},
			"grok-4.7-high": {"status": "ok"},
		}
		d := &DisplayData{Tier: PlanTier, Models: ModelRows(state), Hours: 2}
		if got := PlanLine(d); got != "Pro · limited until 11-05" {
			t.Errorf("plan line %q", got)
		}
		if got := HeaderPill(d); got != "Cursor usage · 2 h · Pro · limited until 11-05" {
			t.Errorf("pill %q", got)
		}
		d.Models = ModelRows(LimitState{})
		d.Hours = 1.5
		if got := HeaderPill(d); got != "Cursor usage · 1.5 h · Pro · ok" {
			t.Errorf("pill %q", got)
		}
		d.Models = ModelRows(LimitState{"grok-4.7-high": {"status": "limited"}})
		if got := PlanLine(d); got != "Pro · limited until unknown" {
			t.Errorf("no reset: %q", got)
		}
	})
	t.Run("slugs", func(t *testing.T) {
		if got := CursorModelSlugs(); !reflect.DeepEqual(got, []string{"composer-2.5", "grok-4.7-high"}) {
			t.Errorf("slugs %v", got)
		}
	})
}

type tickRig struct {
	sent   []string
	events []string
	pane   map[string]string
}

func (r *tickRig) deps() TickDeps {
	return TickDeps{
		RuntimeOf: func(a LiveAgent) string {
			if strings.HasPrefix(a.Name, "cursor-") {
				return "cursor"
			}
			return "claude"
		},
		ReadPane: func(n string) string { return r.pane[n] },
		OrgOf:    func(string) string { return "cursor" },
		Send: func(to, subject, body, kind, goal, task, sender, org string) error {
			r.sent = append(r.sent, strings.Join([]string{to, subject, kind, goal, task, sender, org}, "|")+"|"+body)
			return nil
		},
		Event: func(entity, id, action, detail string) error {
			r.events = append(r.events, entity+"|"+id+"|"+action+"|"+detail)
			return nil
		},
	}
}

func TestCursorTickRecord(t *testing.T) {
	setup(t)
	db, _ := openDB(t, false)
	if _, err := db.Exec(`INSERT INTO agents(name,role,goal_id,task_id,status,model,spawn_args,org) VALUES
	 ('cursor-coder-t-0878','coder','G-027','T-0878','live','grok-4.7-high','{"runtime":"cursor"}','cursor'),
	 ('claude-coder-t-1','coder','G-027','T-1','live','claude-sonnet-5-5',NULL,'claude')`); err != nil {
		t.Fatal(err)
	}
	rig := &tickRig{pane: map[string]string{"cursor-coder-t-0878": "working normally\n"}}
	// test_ok_until_limit_error: no error, no config row
	if err := TickRecord(db, rig.deps()); err != nil {
		t.Fatal(err)
	}
	if st, _ := LoadState(db); len(st) != 0 {
		t.Fatalf("state written without a limit: %v", st)
	}
	// test_limit_recorded_and_director_alerted
	rig.pane["cursor-coder-t-0878"] = limitText
	rig.pane["claude-coder-t-1"] = limitText // a Claude agent is never scanned
	if err := TickRecord(db, rig.deps()); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(db)
	g := st["grok-4.7-high"]
	if g["status"] != "limited" || g["reset"] != "2026-11-05" || g["switch_target"] != "composer-2.5" || g["agent"] != "cursor-coder-t-0878" {
		t.Fatalf("state %v", st)
	}
	if st["composer-2.5"]["status"] != "ok" {
		t.Fatalf("other slug: %v", st)
	}
	if len(rig.sent) != 1 || !strings.HasPrefix(rig.sent[0], "director|Cursor limit: grok-4.7-high|alert|G-027|T-0878|tick|cursor|cursor-coder-t-0878 hit the Cursor usage limit for Opus; limited until 2026-11-05; switch target composer-2.5.") {
		t.Fatalf("alert: %q", rig.sent)
	}
	if len(rig.events) != 1 || rig.events[0] != `agent|cursor-coder-t-0878|cursor-limit|{"model": "grok-4.7-high", "reset": "2026-11-05", "switch_target": "composer-2.5"}` {
		t.Fatalf("event: %q", rig.events)
	}
	// same error again: throttled, no second alert
	if err := TickRecord(db, rig.deps()); err != nil {
		t.Fatal(err)
	}
	if len(rig.sent) != 1 {
		t.Fatalf("alerted twice: %q", rig.sent)
	}
	// test_ok_after_limit_clears
	rig.pane["cursor-coder-t-0878"] = "working normally\n"
	if err := TickRecord(db, rig.deps()); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(db)
	if st["grok-4.7-high"]["status"] != "ok" || st["composer-2.5"]["status"] != "ok" {
		t.Fatalf("not cleared: %v", st)
	}
	// a state value that is not an object reads as empty
	db.Exec("UPDATE config SET value='[1]' WHERE key=?", ConfigKey)
	if st, _ := LoadState(db); len(st) != 0 {
		t.Fatalf("array state: %v", st)
	}
}

// TestCursorStateInterop: the config row Go writes is read by web.cursor_limits.load_state, and the reverse.
func TestCursorStateInterop(t *testing.T) {
	setup(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed: cursor state interop is not checked here")
	}
	db, path := openDB(t, false)
	want := LimitState{
		"grok-4.7-high": {"status": "limited", "reset": "2026-11-05", "switch_target": nil, "agent": "é-agent"},
		"composer-2.5":  {"status": "ok"},
	}
	if err := SaveState(db, want); err != nil {
		t.Fatal(err)
	}
	read := `
import json, sqlite3, sys
sys.path.insert(0, sys.argv[1])
from web import cursor_limits
con = sqlite3.connect(sys.argv[2]); con.row_factory = sqlite3.Row
print(json.dumps(cursor_limits.load_state(con)))
`
	var got map[string]map[string]any
	if err := json.Unmarshal([]byte(python(t, read, path)), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(toGeneric(t, got), toGeneric(t, want)) {
		t.Errorf("Python read %v, Go wrote %v", got, want)
	}
	raw := readRow(t, path)
	if raw != `{"composer-2.5": {"status": "ok"}, "grok-4.7-high": {"agent": "\u00e9-agent", "reset": "2026-11-05", "status": "limited", "switch_target": null}}` {
		t.Errorf("row text is not what Python's json.dumps(sort_keys=True) writes: %q", raw)
	}
	// Python writes, Go reads
	python(t, `
import sqlite3, sys
sys.path.insert(0, sys.argv[1])
from web import cursor_limits
con = sqlite3.connect(sys.argv[2]); con.row_factory = sqlite3.Row
cursor_limits.save_state(con, {"composer-2.5": {"status": "limited", "reset": "2026-12-01", "switch_target": None}})
con.commit()
`, path)
	st, err := LoadState(db)
	if err != nil {
		t.Fatal(err)
	}
	if st["composer-2.5"]["status"] != "limited" || st["composer-2.5"]["reset"] != "2026-12-01" || filepath.Base(path) == "" {
		t.Errorf("Go read %v", st)
	}
}

func readRow(t *testing.T, path string) string {
	t.Helper()
	db, _ := openDBAt(t, path)
	var v string
	if err := db.QueryRow("SELECT value FROM config WHERE key=?", ConfigKey).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestCursorModelSlugsMatchPython: the Go slug list is what scout/orgs.py and web.cursor_limits.cursor_model_slugs give.
func TestCursorModelSlugsMatchPython(t *testing.T) {
	out := python(t, `
import json, sys
sys.path.insert(0, sys.argv[1])
from web import cursor_limits
print(json.dumps(cursor_limits.cursor_model_slugs()))
`)
	var want []string
	if err := json.Unmarshal([]byte(out), &want); err != nil {
		t.Fatal(err)
	}
	if got := CursorModelSlugs(); !reflect.DeepEqual(got, want) {
		t.Errorf("Go %v, Python %v", got, want)
	}
}

// TestPySumAndDumpsMatchPython: float sums use CPython's compensated sum, and the JSON writer escapes like
// json.dumps (DEL included).
func TestPySumAndDumpsMatchPython(t *testing.T) {
	xs := []float64{0.1, 0.2, 0.3, 1e-9, 0.7, 0.000123, 5.5, 0.125, 0.01, 0.0123}
	var lits []string
	for _, x := range xs {
		lits = append(lits, strconv.FormatFloat(x, 'g', -1, 64))
	}
	out := python(t, `
import json, sys
sys.path.insert(0, sys.argv[1])
xs = [float(a) for a in sys.argv[2:]]
print(json.dumps([sum(xs[:n]) for n in range(1, len(xs) + 1)]))
print(json.dumps({"b": "a\x7fbé\U0001f600\x01\"", "a": None}, sort_keys=True))
`, lits...)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var want []float64
	if err := json.Unmarshal([]byte(lines[0]), &want); err != nil {
		t.Fatal(err)
	}
	for n := range xs {
		if got := pySum(xs[:n+1]...); got != want[n] {
			t.Errorf("pySum(%v) = %v, Python %v", xs[:n+1], got, want[n])
		}
	}
	if got := pyDumps(map[string]any{"b": "a\x7fbé\U0001f600\x01\"", "a": nil}, true); got != lines[1] {
		t.Errorf("pyDumps %s, Python %s", got, lines[1])
	}
}
