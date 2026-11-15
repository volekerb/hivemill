package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"factory/go/internal/gate"
	"factory/go/internal/herdr"
)

// res (Python cmd_res, line 14549): resources and spawn-gate state. It takes no arguments. It never writes the
// database: where Python would write (owner_blockers keeps a first-seen row in `throttle`) the command is handed to
// Python instead, so the first run after /tmp fills up, and the one after it drains, stay Python's.

func init() {
	register("res", "", handler{parse: parseRes, run: runRes})
}

func parseRes(rest []string) (any, bool) { return nil, len(rest) == 0 }

// Constants carried over from the Python source.
const (
	psTimeout        = 60 * time.Second // Python has no ps timeout; this only keeps a wedged ps from hanging the command
	resUsageRelPath  = ".claude/usage-limits.json"
	resCPUOnlinePath = "/sys/devices/system/cpu/online"
)

var (
	utilWorkerRoles = []string{"pm", "coder", "qa", "reviewer", "researcher"} // UTIL_WORKER_ROLES
	utilGoalRoles   = append(append([]string(nil), utilWorkerRoles...), "judge")
	resHQRoles      = []string{"boss", "director", "staff-engineer"} // _RES_HQ_ROLES
)

// resStore feeds the herdr client from the database, read-only. The first database error is kept and reported.
type resStore struct {
	db  *sql.DB
	err error
}

func (s *resStore) Config(key string) (string, bool) {
	var v sql.NullString
	err := s.db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil && s.err == nil {
		s.err = err
	}
	return v.String, err == nil
}

func (s *resStore) AgentRow(name string) *herdr.Row {
	var r herdr.Row
	var sa, pane sql.NullString
	err := s.db.QueryRow("SELECT name, spawn_args, pane_id FROM agents WHERE name=?", name).Scan(&r.Name, &sa, &pane)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		return nil
	}
	r.SpawnArgs, r.PaneID = sa.String, pane.String
	return &r
}

// Event is never called on the `agent list` path; res must not write, so it records nothing.
func (s *resStore) Event(entity, id, action, detail string) {}

func homeDir(c *ctx) (string, error) {
	if h := c.env.Getenv("HOME"); h != "" {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", errDelegate)
	}
	return h, nil
}

// resCfgInt is int(cfg(con, key)); a value Python reads differently goes to Python.
func resCfgInt(c *ctx, key string) (int, error) {
	v, err := cfgText(c, key)
	if err != nil {
		return 0, err
	}
	n, err := gate.PyInt(v)
	if err != nil {
		return 0, errDelegate
	}
	return n, nil
}

func mapGateErr(err error) error {
	if errors.Is(err, gate.ErrUnparsable) {
		return errDelegate
	}
	return err
}

// resSnapshot collects the host and database state gate() reads.
func resSnapshot(c *ctx, now time.Time) (gate.Snapshot, gate.Config, error) {
	var snap gate.Snapshot
	cfg, err := gate.LoadConfig(func(k string) (string, error) { return cfgText(c, k) })
	if err != nil {
		return snap, cfg, mapGateErr(err)
	}
	snap.Now = now
	store := &resStore{db: c.db}
	cl := &herdr.Client{Store: store}
	role, del := callerIsWorker(c)
	if del {
		return snap, cfg, errDelegate
	}
	cl.IsWorker = func() bool { return role != "" }
	live, herr := c.env.HomeIsLive()
	if herr != nil {
		return snap, cfg, errDelegate
	}
	cl.HomeLive = func() bool { return live }
	agents, aerr := cl.Agents()
	if store.err != nil {
		return snap, cfg, fmt.Errorf("res: %w", store.err)
	}
	switch {
	case aerr == nil:
		snap.HerdrReachable = true
		for _, a := range agents {
			ag, _ := a["agent"].(string)
			snap.HerdrAgents = append(snap.HerdrAgents, gate.HerdrAgent{Name: a.Name(), PaneID: a.PaneID(), Agent: ag})
		}
	case errors.Is(aerr, herdr.ErrUnreachable):
	default:
		return snap, cfg, errDelegate
	}
	if snap.LiveRows, err = resLiveRows(c); err != nil {
		return snap, cfg, err
	}
	rs, err := c.db.Query("SELECT role FROM agents WHERE status IN ('live','starting')")
	if err != nil {
		return snap, cfg, fmt.Errorf("res: roles: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var r string
		if err := rs.Scan(&r); err != nil {
			return snap, cfg, fmt.Errorf("res: roles: %w", err)
		}
		snap.HQWorkerRow = append(snap.HQWorkerRow, gate.RoleRow{Role: r})
	}
	if err := rs.Err(); err != nil {
		return snap, cfg, fmt.Errorf("res: roles: %w", err)
	}
	if snap.ReviewHeld, err = sbCount(c.db, "SELECT count(*) c FROM agents WHERE status IN ('live','starting') AND role IN ('reviewer','qa')"); err != nil {
		return snap, cfg, fmt.Errorf("res: reviewer count: %w", err)
	}
	mem, err := gate.ReadFile("/proc/meminfo")
	if err != nil {
		return snap, cfg, errDelegate
	}
	if snap.MemMB, err = gate.MemAvailableMB(mem); err != nil {
		return snap, cfg, errDelegate
	}
	home, err := homeDir(c)
	if err != nil {
		return snap, cfg, err
	}
	u, amb := gate.ReadUsage(filepath.Join(home, resUsageRelPath), func() time.Time { return time.Now() }, time.Sleep)
	if amb {
		return snap, cfg, errDelegate
	}
	snap.Usage = u
	root, del := c.procRoot()
	if del {
		return snap, cfg, errDelegate
	}
	snap.Procs = gate.ScanProcs(root)
	tmpDir := c.env.Getenv("FACTORY_TMP_DIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	snap.Tmp = gate.TmpUsage(tmpDir, c.env.Getenv("FACTORY_TMP_STATVFS"), "/proc/mounts")
	if snap.Load1, err = resLoad1(c); err != nil {
		return snap, cfg, err
	}
	snap.NumCPU = gate.CPUCount(resCPUOnlinePath, runtime.NumCPU())
	return snap, cfg, nil
}

func resLoad1(c *ctx) (float64, error) {
	if pinned := c.env.Getenv("FACTORY_LOADAVG"); pinned != "" {
		f, err := gate.PyFloat(pinned)
		if err != nil {
			return 0, errDelegate // Python raises ValueError
		}
		return f, nil
	}
	s, err := gate.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, errDelegate
	}
	f, err := gate.Load1(s)
	if err != nil {
		return 0, errDelegate
	}
	return f, nil
}

func resLiveRows(c *ctx) ([]gate.AgentRow, error) {
	rs, err := c.db.Query("SELECT name, pane_id, started, session_id, spawn_args FROM agents WHERE status='live'")
	if err != nil {
		return nil, fmt.Errorf("res: live agents: %w", err)
	}
	defer rs.Close()
	var out []gate.AgentRow
	for rs.Next() {
		var name string
		var pane, started, sess, sa sql.NullString
		if err := rs.Scan(&name, &pane, &started, &sess, &sa); err != nil {
			return nil, fmt.Errorf("res: live agents: %w", err)
		}
		row := gate.AgentRow{Name: name, PaneID: pane.String, Started: started.String, SessionID: sess.String, Runtime: "claude"}
		var spawn map[string]any
		if txt := sa.String; txt != "" {
			var any0 any
			if json.Unmarshal([]byte(txt), &any0) == nil {
				switch v := any0.(type) {
				case map[string]any:
					spawn = v
				case nil, bool, float64, string:
					// falsy or scalar: Python's `or {}` / .get on a scalar. A truthy scalar raises there: hand it over.
					if v != nil && v != false && v != float64(0) && v != "" {
						return nil, errDelegate
					}
				default:
					if l, isList := v.([]any); !isList || len(l) > 0 {
						return nil, errDelegate
					}
				}
			}
		}
		if rt, _ := spawn["runtime"].(string); rt == "cursor" {
			row.Runtime = "cursor"
			ws, _ := spawn["cursor_workspace"].(string)
			if ws == "" {
				// agent_cursor_workspace derives the policy dir from the cursor harness: Python's.
				return nil, errDelegate
			}
			row.CursorWorkspace = gate.ResolvePath(ws)
		}
		out = append(out, row)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("res: live agents: %w", err)
	}
	return out, nil
}

// resTestSlots is test_slot_usage: (held, total) full-suite slots, a slot being held while another process has its flock.
func resTestSlots(c *ctx) (int, int, error) {
	n, err := resCfgInt(c, "test_slots")
	if err != nil {
		return 0, 0, err
	}
	home, err := homeDir(c)
	if err != nil {
		return 0, 0, err
	}
	d := filepath.Join(home, ".cache", "factory-test-slots")
	held := 0
	for i := 0; i < n; i++ {
		f, err := os.OpenFile(filepath.Join(d, fmt.Sprintf("slot-%d.lock", i)), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o666)
		if err != nil {
			continue // no slot dir yet: nobody ever ran a full suite
		}
		ferr := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if errors.Is(ferr, syscall.EWOULDBLOCK) {
			held++
		} else if ferr == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // closing the file drops the lock anyway; explicit for clarity
		}
		if cerr := f.Close(); cerr != nil {
			return 0, 0, fmt.Errorf("close slot %d: %w", i, cerr)
		}
	}
	return held, n, nil
}

func runRes(c *ctx, _ any) (int, error) {
	loc, err := sbLocation(c.env.Getenv)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	if c.env.Now != nil {
		now = c.env.Now()
	}
	snap, cfg, err := resSnapshot(c, now)
	if err != nil {
		return 0, err
	}
	d := gate.Decide(snap, cfg, gate.Options{})
	s := d.Stats
	w := c.out

	cl := &herdr.Client{Store: &resStore{db: c.db}}
	tbRaw := cl.TerminalBackendConfig()
	tbEff, tbExe := cl.TerminalBackendEffective()
	var q int
	if q, err = sbCount(c.db, "SELECT count(*) c FROM agents WHERE status='queued'"); err != nil {
		return 0, fmt.Errorf("res: queued: %w", err)
	}
	if d.OK {
		fmt.Fprintln(w, "spawn gate: OPEN")
	} else {
		fmt.Fprintf(w, "spawn gate: CLOSED — %s\n", d.Reason)
	}
	fmt.Fprintf(w, "terminal backend: %s (config=%s; effective=%s)\n", tbExe, tbRaw, tbEff)
	hold := "open"
	if s.Load1 > s.LoadCap {
		hold = "HOLDING spawns"
	}
	fmt.Fprintf(w, "load gate: load1 %.1f vs cap %.1f (%s x %d CPUs): %s\n", s.Load1, s.LoadCap, cfg.LoadRatioText, snap.NumCPU, hold)
	held, slots, err := resTestSlots(c)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(w, "test runs: %d full suites running (slots %d/%d)\n", held, held, slots)
	if err := resGateRuns(c, w); err != nil {
		return 0, err
	}
	if s.Tmp != nil && s.Tmp.Pct >= cfg.TmpMaxPct {
		cd := gate.Decide(snap, cfg, gate.Options{Role: "cleaner"})
		if cd.OK {
			fmt.Fprintln(w, "cleaner gate: OPEN — exempt from the /tmp gate (tmp_max_pct) so it can free /tmp; every other cap still applies")
		} else {
			fmt.Fprintf(w, "cleaner gate: CLOSED — %s\n", cd.Reason)
		}
	}
	fmt.Fprintf(w, "agents live %d/%d · queued %d · other claude panes %d (counted in host total)\n", s.Live, s.Cap, q, s.Other)
	lc, err := sbLeadCounts(c.db)
	if err != nil {
		return 0, fmt.Errorf("res: lead counts: %w", err)
	}
	reserved, err := resCfgInt(c, "reserved_worker_slots")
	if err != nil {
		return 0, err
	}
	maxLeads, err := cfgText(c, "max_live_leads")
	if err != nil {
		return 0, err
	}
	parking, err := cfgText(c, "lead_parking")
	if err != nil {
		return 0, err
	}
	suffix := ""
	if parking != "1" {
		suffix = " · lead parking off"
	}
	fmt.Fprintf(w, "workers live %d · leads live %d/%s (parked %d, wake-queued %d) · hq %d/%d (reserved for workers %d)%s\n",
		lc.Workers, lc.Leads, maxLeads, lc.Parked, lc.Queued, lc.HQ, s.HQCap, reserved, suffix)
	clock, err := sbClock(c.env.Getenv, c.env.Now, loc)
	if err != nil {
		return 0, err
	}
	u2, err := resUtilization(c, clock)
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(w, u2.line)
	if u2.waitingLine != "" {
		fmt.Fprintln(w, u2.waitingLine)
	}
	util := fmt.Sprintf("utilization: workers %d/%d target · leads+judges %d/%d (max 1/3)", u2.workers, u2.target, u2.leads+u2.judges, u2.live)
	if u2.workers < u2.target && u2.demand {
		util += " · UNDER TARGET"
	}
	if (u2.leads+u2.judges)*3 > u2.live {
		util += " · HQ-HEAVY"
	}
	fmt.Fprintln(w, util)
	mix, err := resOrgMix(c)
	if err != nil {
		return 0, err
	}
	for _, o := range orgNames {
		m := mix[o]
		fmt.Fprintf(w, "org %s: live %d · workers %d · leads %d · hq %d · queued %d\n", o, m[0], m[1], m[2], m[3], m[4])
	}
	owner, err := sbCount(c.db, "SELECT count(*) c FROM tasks WHERE status='blocked' AND waits='owner'")
	if err != nil {
		return 0, fmt.Errorf("res: blocked tasks: %w", err)
	}
	internal, err := sbCount(c.db, "SELECT count(*) c FROM tasks WHERE status='blocked' AND waits='internal'")
	if err != nil {
		return 0, fmt.Errorf("res: blocked tasks: %w", err)
	}
	fmt.Fprintf(w, "blocked tasks: %d owner-blocked · %d internal-blocked\n", owner, internal)
	meminfo, err := gate.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, errDelegate
	}
	swapMB, err := gate.SwapUsedMB(meminfo)
	if err != nil {
		return 0, errDelegate
	}
	load, err := resLoad1(c)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(w, "mem available %d MB (floor %s) · swap used %d MB · load %.1f/%d\n", s.MemMB, cfg.MinMemText, swapMB, load, snap.NumCPU)
	if t := s.Tmp; t == nil {
		fmt.Fprintln(w, "tmp unknown")
	} else {
		on := ""
		if t.FSType != "" {
			on = " on " + t.FSType
		}
		fmt.Fprintf(w, "tmp used %d MB / %d MB (%.1f%%)%s (gate closes at %s%%)\n", t.UsedMB, t.TotalMB, t.Pct, on, cfg.TmpMaxPctText)
		sw, err := resLastSweep(c)
		if err != nil {
			return 0, err
		}
		if sw == "" {
			sw = "never swept"
		}
		sweepPct, err := cfgText(c, "tmp_sweep_pct")
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(w, "tmp sweep (over %s%%): %s\n", sweepPct, sw)
	}
	if s.Total == nil {
		fmt.Fprintln(w, "claude processes unknown")
	} else {
		fmt.Fprintf(w, "claude processes %d/%d on host (factory %d · other %d)\n", *s.Total, s.TotalCap, *s.FactoryProcs, *s.Total-*s.FactoryProcs)
	}
	fmt.Fprintln(w, s.Usage.Text())
	mode, err := cfgText(c, "lead_less_dispatch")
	if err != nil {
		return 0, err
	}
	if mode != "0" {
		cut := time.Now().In(loc).Add(-time.Hour).Format("2006-01-02T15:04:05-07:00")
		var n [2]int
		for i, k := range []string{"dispatch", "dispatch-shadow"} {
			if n[i], err = sbCount(c.db, "SELECT count(*) c FROM events WHERE action=? AND ts >= ?", k, cut); err != nil {
				return 0, fmt.Errorf("res: dispatch events: %w", err)
			}
		}
		fmt.Fprintf(w, "lead-less dispatch: %s; last hour: %d spawned, %d shadow\n", mode, n[0], n[1])
	}
	ob, err := resOwnerBlockers(c, clock, loc, s.Tmp, cfg)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(w, "owner-only blockers: %d", len(ob))
	for _, b := range ob {
		fmt.Fprintf(w, "\n  %s (open %d min)", b.text, b.ageMin)
	}
	fmt.Fprintln(w)
	out, _, code, err := c.deps.runCmd("ps", []string{"-eo", "rss,args", "--sort=-rss"}, "", nil, psTimeout)
	if err != nil || code != 0 {
		return 0, errDelegate
	}
	lines := pySplitlines(out)
	fmt.Fprintln(w, "top memory:")
	if len(lines) > 1 {
		lines = lines[1:]
	} else {
		lines = nil
	}
	if len(lines) > 5 {
		lines = lines[:5]
	}
	for _, line := range lines {
		rss, cmd, ok := strings.Cut(pyStrip(line), " ")
		kb, err := gate.PyInt(rss)
		if !ok || err != nil {
			return 0, errDelegate // Python's unpack or int() raises
		}
		if utf8.RuneCountInString(cmd) > 90 {
			cmd = string([]rune(cmd)[:90])
		}
		fmt.Fprintf(w, "  %5d MB  %s\n", gate.FloorDiv(int64(kb), 1024), cmd)
	}
	return 0, nil
}

func resGateRuns(c *ctx, w interface{ Write([]byte) (int, error) }) error {
	rs, err := c.db.Query("SELECT id,kind,task_id,project_id,pid FROM gate_runs WHERE status='running' ORDER BY id")
	if err != nil {
		return fmt.Errorf("res: gate runs: %w", err)
	}
	defer rs.Close()
	var parts []string
	for rs.Next() {
		var id int64
		var kind string
		var task, proj sql.NullString
		var pid sql.NullInt64
		if err := rs.Scan(&id, &kind, &task, &proj, &pid); err != nil {
			return fmt.Errorf("res: gate runs: %w", err)
		}
		who := task.String
		if who == "" {
			if !proj.Valid {
				return errDelegate // Python: str + None raises TypeError
			}
			who = kind + ":" + proj.String
		}
		parts = append(parts, fmt.Sprintf(" · #%d %s", id, who))
	}
	if err := rs.Err(); err != nil {
		return fmt.Errorf("res: gate runs: %w", err)
	}
	maxPar, err := cfgText(c, "gate_max_parallel")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "gate runs: %d running (max %s)%s\n", len(parts), maxPar, strings.Join(parts, ""))
	return err
}

type resUtil struct {
	workers, leads, judges, hq, other, live, queued, queuedWorkers, cap, target int
	demand                                                                      bool
	line, waitingLine                                                           string
}

func inQ(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func strArgs(l []string) []any {
	a := make([]any, len(l))
	for i, s := range l {
		a[i] = s
	}
	return a
}

// resUtilization is utilization(con): the shared count of live agents by role.
func resUtilization(c *ctx, clock time.Time) (resUtil, error) {
	var u resUtil
	fail := func(err error) (resUtil, error) { return u, fmt.Errorf("res: utilization: %w", err) }
	rs, err := c.db.Query("SELECT role,count(*) c FROM agents WHERE status IN ('live','starting') GROUP BY role")
	if err != nil {
		return fail(err)
	}
	for rs.Next() {
		var role string
		var n int
		if err := rs.Scan(&role, &n); err != nil {
			_ = rs.Close() // read-only cursor already failed: its error is the one reported
			return fail(err)
		}
		switch {
		case inList(utilWorkerRoles, role):
			u.workers += n
		case role == "lead":
			u.leads += n
		case role == "judge":
			u.judges += n
		case role == "boss" || role == "director":
			u.hq += n
		default:
			u.other += n
		}
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close() // read-only cursor already failed: its error is the one reported
		return fail(err)
	}
	if err := rs.Close(); err != nil {
		return fail(err)
	}
	u.live = u.workers + u.leads + u.judges + u.hq + u.other
	if u.queued, err = sbCount(c.db, "SELECT count(*) FROM agents WHERE status='queued'"); err != nil {
		return fail(err)
	}
	marks := inQ(len(utilWorkerRoles))
	if u.queuedWorkers, err = sbCount(c.db, "SELECT count(*) FROM agents WHERE status='queued' AND role IN ("+marks+")", strArgs(utilWorkerRoles)...); err != nil {
		return fail(err)
	}
	capText, err := cfgText(c, "max_live_agents")
	if err != nil {
		return u, err
	}
	if u.cap, err = gate.PyInt(capText); err != nil {
		if def, ok := defaultMap()["max_live_agents"]; ok {
			if u.cap, err = gate.PyInt(def); err != nil {
				return u, errDelegate
			}
		} else {
			return u, errDelegate
		}
		if hasNonASCIIText(capText) {
			return u, errDelegate
		}
	}
	u.target = int(math.Min(10, math.Ceil(0.6*float64(u.cap))))
	busy, err := strSet(c, "SELECT task_id FROM agents WHERE status IN ('live','starting','queued') AND task_id IS NOT NULL")
	if err != nil {
		return fail(err)
	}
	done, err := strSet(c, "SELECT id FROM tasks WHERE status='done'")
	if err != nil {
		return fail(err)
	}
	ready := 0
	ts, err := c.db.Query("SELECT id,depends_on FROM tasks WHERE status='todo' AND role IN ("+marks+")", strArgs(utilWorkerRoles)...)
	if err != nil {
		return fail(err)
	}
	type trow struct{ id, deps string }
	var todo []trow
	for ts.Next() {
		var r trow
		var deps sql.NullString
		if err := ts.Scan(&r.id, &deps); err != nil {
			_ = ts.Close() // read-only cursor already failed: its error is the one reported
			return fail(err)
		}
		r.deps = deps.String
		todo = append(todo, r)
	}
	if err := ts.Err(); err != nil {
		_ = ts.Close() // read-only cursor already failed: its error is the one reported
		return fail(err)
	}
	if err := ts.Close(); err != nil {
		return fail(err)
	}
	for _, t := range todo {
		deps, err := resDeps(t.deps)
		if err != nil {
			return u, err
		}
		if !busy[t.id] && allDone(deps, done) {
			ready++
		}
	}
	staffedRows, err := strSet(c, "SELECT goal_id FROM agents WHERE goal_id IS NOT NULL AND role IN ("+inQ(len(utilGoalRoles))+") AND status IN ('live','starting','queued')", strArgs(utilGoalRoles)...)
	if err != nil {
		return fail(err)
	}
	unstaffed := func(prio int) ([]string, error) {
		rows, err := c.db.Query("SELECT id FROM goals WHERE status IN ('planning','active') AND priority=?", prio)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			if !staffedRows[id] {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		return ids, rows.Err()
	}
	p1all, err := unstaffed(1)
	if err != nil {
		return fail(err)
	}
	var p1, p1Waiting []string
	for _, gid := range p1all {
		w, err := resWaiting(c, gid, done)
		if err != nil {
			return u, err
		}
		if w {
			p1Waiting = append(p1Waiting, gid)
		} else {
			p1 = append(p1, gid)
		}
	}
	nowS := float64(clock.UnixNano()) / 1e9
	var wait []string
	for _, gid := range p1Waiting {
		var last sql.NullFloat64
		if err := c.db.QueryRow("SELECT max(julianday(ended)) FROM agents WHERE goal_id=? AND role IN ("+marks+") AND ended IS NOT NULL",
			append([]any{gid}, strArgs(utilWorkerRoles)...)...).Scan(&last); err != nil {
			return fail(err)
		}
		if !last.Valid {
			if err := c.db.QueryRow("SELECT julianday(created) FROM goals WHERE id=?", gid).Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fail(err)
			}
		}
		mins := int64(0)
		if last.Valid && last.Float64 != 0 {
			m := int64(math.Floor((nowS - (last.Float64-2440587.5)*86400) / 60))
			if m > 0 {
				mins = m
			}
		}
		wait = append(wait, fmt.Sprintf("%s (%d min)", gid, mins))
	}
	none := "none"
	if len(p1) > 0 {
		none = strings.Join(p1, ", ")
	}
	u.line = fmt.Sprintf("live %d: workers %d / leads %d / judges %d / HQ %d / queued %d; P1 goals without workers: %s",
		u.live, u.workers, u.leads, u.judges, u.hq, u.queued, none)
	if len(wait) > 0 {
		u.waitingLine = "P1 waiting on owner/CI: " + strings.Join(wait, ", ")
	}
	u.demand = u.queuedWorkers > 0 || ready > 0
	return u, nil
}

func hasNonASCIIText(s string) bool {
	for _, r := range s {
		if r > 0x7f {
			return true
		}
	}
	return false
}

func strSet(c *ctx, q string, args ...any) (map[string]bool, error) {
	rows, err := c.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s.String] = true
	}
	return out, rows.Err()
}

// resDeps is json.loads(depends_on or "[]"): ValueError is []; anything but a list of strings goes to Python.
func resDeps(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, nil
	}
	l, ok := v.([]any)
	if !ok {
		return nil, errDelegate
	}
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, ok := x.(string)
		if !ok {
			return nil, errDelegate
		}
		out = append(out, s)
	}
	return out, nil
}

func allDone(deps []string, done map[string]bool) bool {
	for _, d := range deps {
		if !done[d] {
			return false
		}
	}
	return true
}

// resWaiting is waiting_on_owner_ci(gid): open tasks exist, none is ready, each waits on the owner, CI/review or a task.
func resWaiting(c *ctx, gid string, done map[string]bool) (bool, error) {
	rows, err := c.db.Query("SELECT status,depends_on FROM tasks WHERE goal_id=? AND status NOT IN ('done','cancelled')", gid)
	if err != nil {
		return false, fmt.Errorf("res: open tasks: %w", err)
	}
	type orow struct{ status, deps string }
	var open []orow
	for rows.Next() {
		var r orow
		var deps sql.NullString
		if err := rows.Scan(&r.status, &deps); err != nil {
			_ = rows.Close() // read-only cursor already failed: its error is the one reported
			return false, fmt.Errorf("res: open tasks: %w", err)
		}
		r.deps = deps.String
		open = append(open, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // read-only cursor already failed: its error is the one reported
		return false, fmt.Errorf("res: open tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("res: open tasks: %w", err)
	}
	if len(open) == 0 {
		return false, nil
	}
	for _, t := range open {
		if t.status == "in_review" || t.status == "blocked" {
			continue
		}
		if t.status != "todo" {
			return false, nil
		}
		deps, err := resDeps(t.deps)
		if err != nil {
			return false, err
		}
		if allDone(deps, done) {
			return false, nil // a ready task: the goal is starved
		}
	}
	return true, nil
}

// resOrgMix is res_org_mix: per org [live, workers, leads, hq, queued].
func resOrgMix(c *ctx) (map[string][5]int, error) {
	mix := map[string][5]int{}
	for _, o := range orgNames {
		mix[o] = [5]int{}
	}
	rows, err := c.db.Query("SELECT org, name, spawn_args, status, role FROM agents")
	if err != nil {
		return nil, fmt.Errorf("res: org mix: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var org, sa sql.NullString
		var name, status, role string
		if err := rows.Scan(&org, &name, &sa, &status, &role); err != nil {
			return nil, fmt.Errorf("res: org mix: %w", err)
		}
		o := orgOfAgent(agentRow{org: org.String, name: name, spawnArgs: sa.String})
		m, ok := mix[o]
		if !ok {
			continue
		}
		if status == "queued" {
			m[4]++
		}
		if status == "live" {
			m[0]++
			switch {
			case role == "lead":
				m[2]++
			case inList(resHQRoles, role):
				m[3]++
			default:
				m[1]++
			}
		}
		mix[o] = m
	}
	return mix, rows.Err()
}

// resLastSweep is the text after "tmp sweep (over N%): " for the last tmp-swept event, "" for none.
func resLastSweep(c *ctx) (string, error) {
	var ts string
	var detail sql.NullString
	err := c.db.QueryRow("SELECT ts,detail FROM events WHERE action='tmp-swept' ORDER BY id DESC LIMIT 1").Scan(&ts, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("res: last tmp sweep: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(detail.String))
	dec.UseNumber()
	var d map[string]any
	if dec.Decode(&d) != nil || dec.More() {
		return "", nil // ValueError / TypeError: no sweep to show
	}
	mb, ok1 := d["mb"].(json.Number)
	dirs, ok2 := d["dirs"].(json.Number)
	if !ok1 || !ok2 {
		if _, has := d["mb"]; !has {
			return "", nil
		}
		if _, has := d["dirs"]; !has {
			return "", nil
		}
		return "", errDelegate // a non-number is printed by Python's repr
	}
	if _, e1 := strconv.ParseInt(mb.String(), 10, 64); e1 != nil {
		return "", errDelegate
	}
	if _, e2 := strconv.ParseInt(dirs.String(), 10, 64); e2 != nil {
		return "", errDelegate
	}
	return fmt.Sprintf("last %s freed %s MB in %s dirs", ts, mb.String(), dirs.String()), nil
}

type ownerBlocker struct {
	text   string
	ageMin int
}

// resOwnerBlockers is owner_blockers(con), read-only: when Python would write the throttle row the command goes to Python.
func resOwnerBlockers(c *ctx, clock time.Time, loc *time.Location, tmp *gate.Tmp, cfg gate.Config) ([]ownerBlocker, error) {
	type pending struct {
		since, text string
	}
	var out []pending
	rows, err := c.db.Query("SELECT id,created,subject FROM messages WHERE (recipient='boss' OR recipient IN " +
		"(SELECT name FROM agents WHERE role='boss')) AND kind='escalation' AND sender<>'tick' " +
		"AND lower(subject) LIKE '%owner action%' ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("res: owner blockers: %w", err)
	}
	type msg struct {
		id               int64
		created, subject string
	}
	var msgs []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.created, &m.subject); err != nil {
			_ = rows.Close() // read-only cursor already failed: its error is the one reported
			return nil, fmt.Errorf("res: owner blockers: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // read-only cursor already failed: its error is the one reported
		return nil, fmt.Errorf("res: owner blockers: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("res: owner blockers: %w", err)
	}
	for _, m := range msgs {
		id := strconv.FormatInt(m.id, 10)
		var one int
		err := c.db.QueryRow("SELECT 1 FROM messages WHERE (sender='boss' OR sender IN (SELECT name FROM agents WHERE role='boss')) "+
			"AND kind='decision' AND id>? AND (subject GLOB ? OR subject GLOB ? OR body GLOB ? OR body GLOB ?) LIMIT 1",
			m.id, "*M"+id+"[^0-9]*", "*M"+id, "*M"+id+"[^0-9]*", "*M"+id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			out = append(out, pending{m.created, "M" + id + " " + m.subject})
		} else if err != nil {
			return nil, fmt.Errorf("res: owner blockers: %w", err)
		}
	}
	var row sql.NullString
	err = c.db.QueryRow("SELECT ts FROM throttle WHERE key='owner-blocker:gate'").Scan(&row)
	hasRow := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("res: owner blockers: %w", err)
	}
	if tmp != nil && tmp.Pct >= cfg.TmpMaxPct {
		if !hasRow {
			return nil, errDelegate // Python records the first-seen time here
		}
		out = append(out, pending{row.String, fmt.Sprintf("spawn gate closed: /tmp %d/%d MB used (%.1f%%) ≥ %s%%",
			tmp.UsedMB, tmp.TotalMB, tmp.Pct, cfg.TmpMaxPctText)})
	} else if hasRow {
		return nil, errDelegate // Python deletes the stale row here
	}
	res := make([]ownerBlocker, 0, len(out))
	for _, p := range out {
		age := 0
		t, ok, err := sbParseTS(p.since, loc)
		if err != nil {
			return nil, err
		}
		if ok {
			if m := int(math.Floor(clock.Sub(t).Seconds() / 60)); m > 0 {
				age = m
			}
		}
		res = append(res, ownerBlocker{p.text, age})
	}
	return res, nil
}
