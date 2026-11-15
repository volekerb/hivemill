package s2tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_goal_tokens.py (7 of 19 cases; PARTIAL for P7a, T-1495).
// G-015 T-1114 (SC3, F-74): `factory goal tokens` over the goal_token_tally table; T-1116: token_limit and `goal edit
// --token-limit`. The table is filled by SQL here, exactly the rows the tick would write.
//
// not ported (12): the tick hooks goal_token_tally_tick and goal_token_warn_tick are Python functions that the cases
// call in-process through AlertCase (load_factory + mock.patch of web.tokens.TokenIndex, builtins.open, the module's
// send and GOAL_TOKEN_DEADLINE_S). The Go CLI has no tick, so there is no Go counterpart to drive:
//   Tally.test_off_by_default_no_log_opened_no_rows, test_reading_opens_no_jsonl, test_tally_raising_never_breaks_tick_one_throttled_event,
//   test_big_file_is_drained_whole, test_deadline_stores_nothing_keeps_old_rows_one_event,
//   test_attempt_clock_blocks_a_second_pass_after_failure - tick behaviour on session logs (Python mocks);
//   Limit.test_one_warning_to_lead_and_director_then_none_and_rearm, test_no_lead_one_message_to_director,
//   test_text_is_fixed_no_title, test_no_limit_no_message_no_throttle_row, test_cursor_goal_with_limit_is_not_warned,
//   test_failure_never_raises - goal_token_warn_tick;
// and two cases ported only in their CLI half (the tally rows are seeded, the tick/TokenIndex half is not):
//   Tally.test_same_figure_as_the_page_and_tally_definition (the comparison with web.tokens.rollup is dropped; the
//   printed figures are asserted against the tally definition) and Tally.test_unattributed_on_its_own_line.
// Follow-up: drive the tick through `factory tick` once a Go tick exists.

func gtEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	exec(e, "INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-1','g','active','t','t','claude')")
	exec(e, "INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-9','c','active','t','t','cursor')")
	exec(e, "INSERT INTO agents(name,role,status,goal_id,task_id,session_id,started) VALUES('coder-a','coder','live','G-1','T-1','s1',?)",
		"2026-11-05T09:00:00+00:00")
	return e
}

// gtTally writes one goal_token_tally row.
func gtTally(e *ft.Env, gid string, in, out, cacheRead, w5, w1 int, cost float64) {
	e.T.Helper()
	exec(e, "INSERT OR REPLACE INTO goal_token_tally(goal_id,input,output,cache_read,cache_write_5m,cache_write_1h,cost,computed_at) VALUES(?,?,?,?,?,?,?,?)",
		gid, in, out, cacheRead, w5, w1, cost, "2026-11-05T12:00:00")
}

func TestGoalTokensSameFigureAsTheTallyDefinition(t *testing.T) {
	e := gtEnv(t)
	// the figures the Python case builds from two usage rows: 100+1 input, 20+2 output, 5000+3 cache_read, 7+4 / 3+5 writes
	gtTally(e, "G-1", 101, 22, 5003, 11, 8, 0.5)
	out := ok(e, "", "goal", "tokens", "G-1").Stdout
	for _, k := range [][2]string{{"input", "101"}, {"output", "22"}, {"cache_write_5m", "11"}, {"cache_write_1h", "8"}} {
		contains(t, "stdout", out, "  "+k[0]+": "+k[1])
	}
	contains(t, "stdout", out, "cache_read: 5003 (printed apart")
	// known counters 101+22+11+8 = 142; cache_read 5003 is not in it
	contains(t, "stdout", out, "G-1 tally: 142 tokens")
	contains(t, "stdout", out, "cost estimate: $0.50")
}

func TestGoalTokensUnattributedOnItsOwnLine(t *testing.T) {
	e := gtEnv(t)
	gtTally(e, "G-1", 10, 10, 0, 0, 0, 0)
	gtTally(e, "unattributed", 40, 2, 99, 0, 0, 0)
	out := ok(e, "", "goal", "tokens", "G-1").Stdout
	var hits []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "not counted in any goal") {
			hits = append(hits, ln)
		}
	}
	eq(t, "unattributed lines", len(hits), 1)
	if !strings.HasPrefix(hits[0], "unattributed: 42 tokens") {
		t.Fatalf("unattributed line = %q, want prefix %q", hits[0], "unattributed: 42 tokens")
	}
	notContains(t, "rest of stdout", strings.Replace(out, hits[0], "", 1), "not counted")
}

func TestGoalTokensCliNoCacheRowSaysNoTallyYet(t *testing.T) {
	e := gtEnv(t)
	contains(t, "stdout", ok(e, "", "goal", "tokens", "G-1").Stdout, "no tally yet")
}

func TestGoalTokensCliCursorGoalSaysAgentHours(t *testing.T) {
	e := gtEnv(t)
	gtTally(e, "G-1", 7, 3, 0, 0, 0, 0)
	gtTally(e, "unattributed", 0, 0, 0, 0, 0, 0)
	out := ok(e, "", "goal", "tokens", "G-9").Stdout
	contains(t, "stdout", out, "agent-hours")
	notContains(t, "stdout", out, "tally:")
	notContains(t, "stdout", out, "unattributed")
}

func TestGoalTokensCliUnknownGoalFails(t *testing.T) {
	e := gtEnv(t)
	if r := run(e, "", "goal", "tokens", "G-404"); r.Exit == 0 {
		t.Fatalf("goal tokens G-404 exited 0: %s", r.Stdout)
	}
}

func TestGoalTokensCliTableAndFlagRegistered(t *testing.T) {
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	path := filepath.Join(root, "bin", "flags.json")
	b, err := os.ReadFile(path) // the real file (G10: the merge gate's tree has it)
	if err != nil {
		t.Fatalf("read %s: %v; run the test inside a full checkout", path, err)
	}
	var doc struct {
		Keys []struct {
			Key     string `json:"key"`
			Default any    `json:"default"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	var defaults []any
	for _, k := range doc.Keys {
		if k.Key == "goal_token_tally_s" {
			defaults = append(defaults, k.Default)
		}
	}
	if len(defaults) != 1 || defaults[0] != "0" {
		t.Fatalf("goal_token_tally_s defaults = %v, want exactly [\"0\"]", defaults)
	}
}

func TestGoalTokensLimitCliDirectorSetsLeadRefusedZeroClears(t *testing.T) {
	e := gtEnv(t)
	for _, a := range [][2]string{{"claude-director", "director"}, {"claude-lead-a", "lead"}, {"claude-lead-z", "lead"}} {
		exec(e, "INSERT INTO agents(name,role,status) VALUES(?,?,'live')", a[0], a[1])
	}
	exec(e, "INSERT INTO teams(id,goal_id,lead_agent,status,created) VALUES('g-1-a','G-1','claude-lead-a','active','t')")
	stored := func() string { return val(e, "SELECT COALESCE(token_limit,'NULL') FROM goals WHERE id='G-1'") }
	p := run(e, "claude-director", "goal", "edit", "G-1", "--token-limit", "5000")
	eq(t, "set exit", p.Exit, 0)
	eq(t, "stored", stored(), "5000")
	contains(t, "goal show", ok(e, "", "goal", "show", "G-1").Stdout, "token limit: 5000 tokens")
	p = run(e, "claude-lead-a", "goal", "edit", "G-1", "--token-limit", "9")
	eq(t, "lead exit", p.Exit, 3)
	eq(t, "stored", stored(), "5000")
	p = run(e, "claude-director", "goal", "edit", "G-1", "--token-limit", "0")
	eq(t, "clear exit", p.Exit, 0)
	eq(t, "stored", stored(), "NULL")
	notContains(t, "goal show", ok(e, "", "goal", "show", "G-1").Stdout, "token limit")
	p = run(e, "claude-director", "goal", "edit", "G-1", "--token-limit", "-1")
	eq(t, "negative exit", p.Exit, 2)
}
