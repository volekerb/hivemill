package tick

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Unit is one row of the unit table (TICK_UNITS in bin/factory_cli.py): the smallest piece `tick --phases` can run.
// Lap is the timer name, "" for a lapless unit (its time flows into the next lap).
type Unit struct{ Name, Lap, Block string }

// OfflineUnit runs only when the herdr list (unit herdr_list) came back empty; it is never part of the normal walk.
const (
	OfflineUnit = "offline"
	EmitUnit    = "emit"
	pruneUnit   = "judge_ci_prune" // the last unit before the offline branch
)

// Units is the ordered table; TestUnitsMatchPython pins it to testdata/units.txt.
var Units = []Unit{
	{"migrate_db", "migrate_db", "boot"}, {"render_settings", "render_settings", "boot"},
	{"guard_build", "guard_build", "boot"}, {"herdr_list", "herdr_list", "boot"},
	{"digest", "digest", "hygiene"}, {"tmp_alert", "tmp_alert", "hygiene"},
	{"remote_url_alert", "remote_url_alert", "hygiene"}, {"main_protection", "main_protection", "hygiene"},
	{"tmp_clean", "tmp_clean", "hygiene"}, {"tmp_harness", "tmp_harness", "hygiene"},
	{"red_main", "red_main", "hygiene"}, {"issue_intake", "issue_intake", "hygiene"},
	{"reap_serves", "reap_serves", "hygiene"}, {"reap_orphans", "reap_orphans", "hygiene"},
	{"test_orphans", "", "hygiene"}, {"judge_ci_prune", "judge_ci_prune", "hygiene"},
	{OfflineUnit, "", "offline"},
	{"scout", "scout", "side"}, {"retro", "retro", "side"}, {"skilleval", "skilleval", "side"},
	{"pending_nudges", "pending_nudges", "reap"}, {"reap_loop", "reap_loop", "reap"},
	{"exited_panes", "exited_panes", "reap"}, {"idle_coders", "idle_coders", "reap"},
	{"drift", "drift", "reap"}, {"flagless", "flagless", "reap"}, {"claude_audit", "claude_audit", "reap"},
	{"stop_fallback", "stop_fallback", "flow"}, {"gate", "gate", "flow"}, {"mech_merge", "mech_merge", "flow"},
	{"ci_wake", "ci_wake", "flow"}, {"wake_leads", "wake_leads", "flow"},
	{"dequeue", "dequeue", "start"}, {"dispatch", "dispatch", "start"}, {"start_confirm", "start_confirm", "start"},
	{"nudge", "nudge", "nudge"}, {"sidebar", "sidebar", "nudge"}, {"cursor_limits", "cursor_limits", "nudge"},
	{"export", "export", "publish"}, {"freshness", "freshness", "publish"},
	{"recycle", "recycle", "tail"}, {"relaunches", "relaunches", "tail"}, {"util", "util", "tail"},
	{EmitUnit, "", "tail"},
}

// walkUnits is the normal order: the table without the offline branch and without emit (emit is the driver's).
func walkUnits() []Unit {
	var w []Unit
	for _, u := range Units {
		if u.Name != OfflineUnit && u.Name != EmitUnit {
			w = append(w, u)
		}
	}
	return w
}

// UnitCtx is what a Go unit gets. State is the shared cross-unit state of the handoff (agents, t, hc_back,
// starting); a unit may read and replace its keys, the driver passes it on to the next unit or child.
type UnitCtx struct {
	Ctx    context.Context
	DB     *sql.DB
	Home   string
	State  State
	Stdout func(string) // writes to the tick's stdout in call order
}

// GoFunc runs one unit in-process. A non-nil error ends the tick: stderr "factory(go): <err>", exit 1.
type GoFunc func(*UnitCtx) error

var goUnits = map[string]GoFunc{}

// registerGo registers the Go implementation of a unit, from the unit file's init(). Unknown, offline, emit and
// duplicate names are programming errors and panic at start-up.
func registerGo(unit string, fn GoFunc) {
	ok := false
	for _, u := range walkUnits() {
		ok = ok || u.Name == unit
	}
	if !ok {
		panic(fmt.Sprintf("tick: registerGo of unknown unit %q", unit))
	}
	if _, dup := goUnits[unit]; dup {
		panic(fmt.Sprintf("tick: duplicate registerGo for %q", unit))
	}
	goUnits[unit] = fn
}

// Blocks lists the block names in table order.
func Blocks() []string {
	var b []string
	seen := map[string]bool{}
	for _, u := range Units {
		if !seen[u.Block] {
			seen[u.Block] = true
			b = append(b, u.Block)
		}
	}
	return b
}

// formatTable is the testdata/units.txt text of Units.
func formatTable() string {
	var sb strings.Builder
	for _, u := range Units {
		lap := u.Lap
		if lap == "" {
			lap = "-"
		}
		sb.WriteString(u.Name + "\t" + lap + "\t" + u.Block + "\n")
	}
	return sb.String()
}

func sortedKeys(m map[string]GoFunc) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// unitTableIndex is the row in Units and TICK_UNITS; Python --phases requires consecutive indices.
func unitTableIndex(name string) int {
	for i, u := range Units {
		if u.Name == name {
			return i
		}
	}
	return -1
}

// blockRuns lists each block name with its units in table order (Python TICK_BLOCKS).
func blockRuns() []struct {
	name  string
	units []string
} {
	seen := map[string]bool{}
	var out []struct {
		name  string
		units []string
	}
	for _, u := range Units {
		if seen[u.Block] {
			continue
		}
		seen[u.Block] = true
		var mem []string
		for _, v := range Units {
			if v.Block == u.Block {
				mem = append(mem, v.Name)
			}
		}
		out = append(out, struct {
			name  string
			units []string
		}{u.Block, mem})
	}
	return out
}

// phaseSpec is the tick --phases argument. Full contiguous block runs use the block name because Python resolves
// block names before unit names (a unit named "nudge" would otherwise expand the whole nudge block).
func phaseSpec(units []string) string {
	blocks := blockRuns()
	var tokens []string
	for i := 0; i < len(units); {
		best := -1
		bestLen := 0
		for bi, b := range blocks {
			n := len(b.units)
			if i+n > len(units) || n <= bestLen {
				continue
			}
			ok := true
			for j := 0; j < n; j++ {
				if units[i+j] != b.units[j] {
					ok = false
					break
				}
			}
			if ok {
				best, bestLen = bi, n
			}
		}
		if best >= 0 {
			tokens = append(tokens, blocks[best].name)
			i += bestLen
			continue
		}
		tokens = append(tokens, units[i])
		i++
	}
	return strings.Join(tokens, ",")
}
