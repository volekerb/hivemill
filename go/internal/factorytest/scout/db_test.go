package scout

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	sc "factory/go/internal/scout"
)

// Port of tests/test_scout_db.py (ScoutLibTests 9 cases + ScoutCliTests 18 cases = 27; see ported_scout.txt).
// The lib cases call internal/scout on an in-memory database. The CLI cases run the factory CLI as a subprocess
// on the harness home; Go serves only scout list|show|runs itself (cmd_scout.go, not yet in cli_go_cmds) and
// hands the rest to bin/factory, so those cases call e.NeedPython() and skip where python3 is absent.
// not ported: the `bogus=1` fact of test_bad_key_and_fact - Facts is a struct, an unknown fact does not compile.

func countRows(t testing.TB, d *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := d.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func near(t testing.TB, what string, got *float64, want, delta float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > delta {
		t.Fatalf("%s = %v, want %v +- %v", what, got, want, delta)
	}
}

func isScoutErr(err error) bool { var se *sc.ScoutError; return errors.As(err, &se) }

func column(t testing.TB, d *sql.DB, q string) []int64 {
	t.Helper()
	rows, err := d.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func eqInts(t testing.TB, what string, got, want []int64) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func upsert(t testing.TB, d *sql.DB, key, src string) string {
	t.Helper()
	id, _, err := sc.UpsertCandidate(d, key, src, "", sc.Facts{})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestScoutLibDedupeTwoSources(t *testing.T) {
	d := mem(t)
	a, created, err := sc.UpsertCandidate(d, "gh:Owner/Repo", "ghsearch", "", sc.Facts{Stars: sc.I(5)})
	if err != nil {
		t.Fatal(err)
	}
	b, created2, err := sc.UpsertCandidate(d, "gh:owner/repo.git/", "awesome", "", sc.Facts{Stars: sc.I(7), Licence: sc.S("MIT")})
	if err != nil {
		t.Fatal(err)
	}
	if a != "C-0001" || !created || b != "C-0001" || created2 {
		t.Fatalf("got (%s %v %s %v)", a, created, b, created2)
	}
	if n := countRows(t, d, "scout_candidates"); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	var key, status, licence, sources string
	var seen, stars int
	if err := d.QueryRow("SELECT key, seen_count, status, stars, licence, sources FROM scout_candidates").Scan(&key, &seen, &status, &stars, &licence, &sources); err != nil {
		t.Fatal(err)
	}
	if key != "gh:owner/repo" || seen != 2 || status != "new" || stars != 7 || licence != "MIT" {
		t.Fatalf("row = %s %d %s %d %s", key, seen, status, stars, licence)
	}
	var srcs []string
	if err := json.Unmarshal([]byte(sources), &srcs); err != nil {
		t.Fatal(err)
	}
	sort.Strings(srcs)
	eqStrings(t, "sources", srcs, []string{"awesome", "ghsearch"})
	if c := upsert(t, d, "gh:a/b/Skills/X", "s"); c != "C-0002" {
		t.Fatalf("second id = %s", c)
	}
	var k2 string
	if err := d.QueryRow("SELECT key FROM scout_candidates WHERE id='C-0002'").Scan(&k2); err != nil || k2 != "gh:a/b/Skills/X" {
		t.Fatalf("key = %q %v", k2, err)
	}
}

func TestScoutLibBadKey(t *testing.T) {
	d := mem(t)
	for _, k := range []string{"", "owner/repo", "gh:onlyowner"} {
		if _, _, err := sc.UpsertCandidate(d, k, "s", "", sc.Facts{}); err == nil || !isScoutErr(err) {
			t.Fatalf("key %q: err = %v, want a ScoutError", k, err)
		}
	}
	if n := countRows(t, d, "scout_candidates"); n != 0 {
		t.Fatalf("a refused key wrote %d rows", n)
	}
}

func TestScoutLibStarTrend(t *testing.T) {
	d := mem(t)
	cid := upsert(t, d, "gh:a/b", "s")
	if r, err := sc.SnapshotStars(d, cid, 100, "2026-10-01T00:00:00+00:00"); err != nil || r != nil {
		t.Fatalf("first snapshot = %v %v, want nil", r, err)
	}
	rate, err := sc.SnapshotStars(d, cid, 130, "2026-10-04T00:00:00+00:00")
	if err != nil {
		t.Fatal(err)
	}
	near(t, "rate", rate, 10.0, 0.01)
	var stars int
	var spd float64
	if err := d.QueryRow("SELECT stars, stars_per_day FROM scout_candidates").Scan(&stars, &spd); err != nil {
		t.Fatal(err)
	}
	near(t, "stored stars_per_day", &spd, 10.0, 0.01)
	if stars != 130 {
		t.Fatalf("stars = %d, want 130", stars)
	}
}

func TestScoutLibStarTrendWindow14Days(t *testing.T) {
	d := mem(t)
	cid := upsert(t, d, "gh:a/b", "s")
	for _, s := range []struct {
		ts    string
		stars int64
	}{{"2026-09-01T00:00:00+00:00", 0}, {"2026-10-01T00:00:00+00:00", 100}} { // the first is outside the window
		if _, err := sc.SnapshotStars(d, cid, s.stars, s.ts); err != nil {
			t.Fatal(err)
		}
	}
	rate, err := sc.SnapshotStars(d, cid, 114, "2026-10-08T00:00:00+00:00")
	if err != nil {
		t.Fatal(err)
	}
	near(t, "rate", rate, 2.0, 0.01)
}

// 2026-10-25 02:30+02:00 is 00:30Z, 02:10+01:00 is 01:10Z: later in time, earlier as text.
func TestScoutLibStarTrendAcrossDSTSwitch(t *testing.T) {
	d := mem(t)
	cid := upsert(t, d, "gh:a/b", "s")
	if _, err := sc.SnapshotStars(d, cid, 100, "2026-10-25T02:30:00+02:00"); err != nil {
		t.Fatal(err)
	}
	rate, err := sc.SnapshotStars(d, cid, 110, "2026-10-25T02:10:00+01:00")
	if err != nil {
		t.Fatal(err)
	}
	near(t, "rate (+10 stars in 40 minutes)", rate, 10/(40.0/1440), 0.01)
	var stars int
	if err := d.QueryRow("SELECT stars FROM scout_candidates").Scan(&stars); err != nil || stars != 110 {
		t.Fatalf("stars = %d %v, want 110", stars, err)
	}
	eqInts(t, "by instant", column(t, d, "SELECT stars FROM scout_stars ORDER BY julianday(ts)"), []int64{100, 110})
	eqInts(t, "by text", column(t, d, "SELECT stars FROM scout_stars ORDER BY ts"), []int64{110, 100})
}

func TestScoutLibSnapshotStarsMixesNaiveAndAware(t *testing.T) {
	d := mem(t)
	cid := upsert(t, d, "gh:a/b", "s")
	if _, err := sc.SnapshotStars(d, cid, 100, "2026-10-01T00:00:00"); err != nil { // naive = local
		t.Fatal(err)
	}
	later, err := sc.ParseTS("2026-10-04T00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	rate, err := sc.SnapshotStars(d, cid, 130, later.Format("2006-01-02T15:04:05-07:00"))
	if err != nil {
		t.Fatal(err)
	}
	near(t, "rate", rate, 10.0, 0.01)
	rows, err := d.Query("SELECT ts FROM scout_stars")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	zone := regexp.MustCompile(`[+-]\d\d:\d\d$`)
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			t.Fatal(err)
		}
		if !zone.MatchString(ts) {
			t.Fatalf("stored ts %q has no UTC offset", ts)
		}
	}
	if _, err := sc.SnapshotStars(d, cid, 1, "not a time"); err == nil || !isScoutErr(err) {
		t.Fatalf("bad ts err = %v, want a ScoutError", err)
	}
}

func TestScoutLibPendingAndSinceUseInstants(t *testing.T) {
	d := mem(t)
	for _, r := range [][3]string{{"C-0001", "gh:a/late", "2026-10-25T02:30:00+02:00"}, {"C-0002", "gh:a/early", "2026-10-25T02:10:00+01:00"}} {
		exec(t, d, "INSERT INTO scout_candidates(id,key,sources,first_seen,last_seen) VALUES(?,?,'[]',?,?)", r[0], r[1], r[2], r[2])
	}
	ids := func(cs []sc.Candidate, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	eqStrings(t, "pending", ids(sc.Pending(d, 50)), []string{"C-0001", "C-0002"}) // 00:30Z before 01:10Z
	since := func(s string) []string { return ids(sc.ListCandidates(d, false, "", "", s)) }
	eqStrings(t, "since 01:00Z", since("2026-10-25T01:00:00+00:00"), []string{"C-0002"})
	eqStrings(t, "since 00:00Z", since("2026-10-25T00:00:00+00:00"), []string{"C-0001", "C-0002"})
	if _, err := sc.ListCandidates(d, false, "", "", "yesterday"); err == nil || !isScoutErr(err) {
		t.Fatalf("since yesterday err = %v", err)
	}
	if _, err := sc.Pending(d, 0); err == nil || !isScoutErr(err) {
		t.Fatalf("pending 0 err = %v", err)
	}
}

func TestScoutLibCursor(t *testing.T) {
	d := mem(t)
	if _, ok, err := sc.GetCursor(d, "gh"); ok || err != nil {
		t.Fatalf("cursor before set = %v %v", ok, err)
	}
	for _, c := range []string{"a", "b"} {
		if err := sc.SetCursor(d, "gh", c, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok, err := sc.GetCursor(d, "gh"); err != nil || !ok || got != "b" {
		t.Fatalf("cursor = %q %v %v, want b", got, ok, err)
	}
	if n := countRows(t, d, "scout_cursors"); n != 1 {
		t.Fatalf("cursor rows = %d, want 1", n)
	}
}

// Python greps scout/db.py for network imports; the Go counterpart reads the real internal/scout/db.go imports.
func TestScoutLibNoNetworkInScoutDB(t *testing.T) {
	root, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "internal", "scout", "db.go")
	src, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	imp := regexp.MustCompile(`(?m)^\s*(?:import\s+)?"([^"]+)"\s*$`)
	for _, m := range imp.FindAllStringSubmatch(string(src), -1) {
		for _, bad := range []string{"net", "net/http", "os/exec", "syscall"} {
			if m[1] == bad {
				t.Errorf("%s imports %q", p, bad)
			}
		}
	}
	notContains(t, "db.go", string(src), "gh api")
}

// ---- ScoutCliTests (subprocess; python3 needed for the words Go still hands to bin/factory) ---------------------

var (
	workers = []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm"}
	good    = []string{"--purpose", "p", "--roles", "coder,qa", "--overlap", "o", "--rec", "watch", "--why", "w"}
	// ROLES of tests/harness.py: the factory_cli.py ROLES literal.
	allRoles = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner",
		"staff-engineer", "researcher", "scout", "retro", "evaluator"}
)

var scoutTables = map[string][]string{
	"scout_candidates": {"id", "key", "kind", "repo", "path", "url", "licence", "last_commit", "default_branch",
		"head_sha", "stars", "stars_per_day", "install_risk", "risk_detail", "sources", "first_seen", "last_seen", "seen_count", "status"},
	"scout_triage": {"id", "candidate_id", "purpose", "target_roles", "overlap", "recommendation", "rationale",
		"eval_suggested", "run_id", "author", "created"},
	"scout_stars":   {"candidate_id", "ts", "stars"},
	"scout_cursors": {"source", "cursor", "updated"},
	"scout_runs": {"id", "slot", "agent", "status", "started", "ended", "tokens", "new_candidates", "triaged",
		"stop_reason", "reported_msg"},
	"skill_trials": {"id", "candidate_id", "vendor_sha", "task", "condition", "status", "agent", "sandbox", "queued",
		"started", "ended", "passed", "wall_s", "tokens_in", "tokens_out", "cache_read", "cache_write",
		"skill_invoked", "check_output", "author", "void_reason", "void_why"},
}

type cli struct{ e *ft.Env }

func newCLI(t *testing.T) cli {
	e := ft.New(t)
	e.NeedPython()
	for _, r := range append([]string{"director", "lead", "boss"}, workers...) {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r})
	}
	return cli{e}
}

func (c cli) ok(agent string, args ...string) ft.Result {
	c.e.T.Helper()
	return c.e.OKWith(ft.Opts{Agent: agent}, args...)
}

func (c cli) refused(code int, agent string, args ...string) ft.Result {
	c.e.T.Helper()
	return c.e.FailWith(ft.Opts{Agent: agent}, code, args...)
}

type candOpt struct {
	id, status, risk string
	stars            int
}

func (c cli) cand(key string, o candOpt) {
	c.e.T.Helper()
	if o.id == "" {
		o.id = "C-0001"
	}
	if o.status == "" {
		o.status = "new"
	}
	if o.risk == "" {
		o.risk = "prompt-only"
	}
	if o.stars == 0 {
		o.stars = 5
	}
	d := c.e.RawDB()
	defer d.Close()
	exec(c.e.T, d, "INSERT INTO scout_candidates(id,key,sources,first_seen,last_seen,status,stars,install_risk) VALUES(?,?,'[]','2026-10-01','2026-10-01',?,?,?)",
		o.id, key, o.status, o.stars, o.risk)
}

func (c cli) count(q string, args ...any) int { return c.e.Count(q, args...) }

func with(base map[string]string, k, v string) []string {
	m := map[string]string{}
	for a, b := range base {
		m[a] = b
	}
	m[k] = v
	var out []string
	for _, f := range []string{"--purpose", "--roles", "--overlap", "--rec", "--why"} {
		out = append(out, f, m[f])
	}
	return out
}

var baseTriage = map[string]string{"--purpose": "p", "--roles": "coder", "--overlap": "o", "--rec": "adopt", "--why": "w"}

func TestScoutCliHelpListsActions(t *testing.T) {
	c := newCLI(t)
	out := c.ok("", "scout", "--help").Stdout
	for _, w := range []string{"pending", "triage", "list", "show", "runs"} {
		contains(t, "scout --help", out, w)
	}
}

func TestScoutCliInitCreatesTables(t *testing.T) {
	c := newCLI(t)
	for tb, cols := range scoutTables {
		var got []string
		for _, r := range c.e.SQL("SELECT name FROM pragma_table_info('" + tb + "')") {
			got = append(got, r[0])
		}
		eqStrings(t, tb, got, cols)
	}
}

func TestScoutCliMigrateAddsTablesIdempotent(t *testing.T) {
	c := newCLI(t)
	d := c.e.RawDB()
	for tb := range scoutTables {
		exec(t, d, "DROP TABLE "+tb)
	}
	d.Close()
	// Python's db() migrates; `scout runs` is Go's now and never runs DDL, so the migrating word is `scout pending`.
	c.ok("owner", "scout", "pending")
	c.ok("owner", "scout", "pending")
	for tb, cols := range scoutTables {
		var got []string
		for _, r := range c.e.SQL("SELECT name FROM pragma_table_info('" + tb + "')") {
			got = append(got, r[0])
		}
		eqStrings(t, tb, got, cols)
	}
}

func TestScoutCliTriageValid(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	c.ok("zz-test-director", append([]string{"scout", "triage", "C-0001"}, append(good, "--suggest-eval")...)...)
	r := c.e.SQL("SELECT candidate_id, target_roles, recommendation, eval_suggested, author FROM scout_triage")[0]
	eqStrings(t, "triage row", r, []string{"C-0001", "coder,qa", "watch", "1", "zz-test-director"})
	if s := c.e.SQL("SELECT status FROM scout_candidates")[0][0]; s != "triaged" {
		t.Fatalf("status = %s, want triaged", s)
	}
}

func TestScoutCliTriageStoredVerbatim(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	evil := "x'); DROP TABLE scout_candidates;-- $(id) `id`"
	c.ok("owner", "scout", "triage", "C-0001", "--purpose", evil, "--roles", "none", "--overlap", "o", "--rec", "skip", "--why", "w")
	if got := c.e.SQL("SELECT purpose FROM scout_triage")[0][0]; got != evil {
		t.Fatalf("purpose = %q, want %q", got, evil)
	}
}

func TestScoutCliTriageValidationExit2(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	long := strings.Repeat("x", 601)
	for _, kv := range [][2]string{{"--rec", "maybe"}, {"--roles", "wizard"}, {"--roles", "coder,none"}, {"--roles", ""},
		{"--purpose", ""}, {"--overlap", ""}, {"--why", ""}, {"--purpose", long}, {"--overlap", long}, {"--why", long}} {
		c.refused(2, "owner", append([]string{"scout", "triage", "C-0001"}, with(baseTriage, kv[0], kv[1])...)...)
	}
	c.refused(2, "owner", append([]string{"scout", "triage", "C-0099"}, good...)...)
	if n := c.count("SELECT count(*) FROM scout_triage"); n != 0 {
		t.Fatalf("triage rows = %d, want 0", n)
	}
	if s := c.e.SQL("SELECT status FROM scout_candidates")[0][0]; s != "new" {
		t.Fatalf("status = %s, want new", s)
	}
	c.ok("owner", append([]string{"scout", "triage", "C-0001"}, with(baseTriage, "--purpose", strings.Repeat("x", 600))...)...)
}

func TestScoutCliRolesValidatedAgainstFactoryRoles(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	c.ok("owner", "scout", "triage", "C-0001", "--purpose", "p", "--roles", strings.Join(allRoles, ","), "--overlap", "o", "--rec", "adopt", "--why", "w")
}

func TestScoutCliTriagePermissions(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	for _, r := range []string{"coder", "qa", "reviewer", "pm", "lead", "boss", "judge", "cleaner", "nobody"} {
		c.refused(3, "zz-test-"+r, append([]string{"scout", "triage", "C-0001"}, good...)...)
	}
	if n := c.count("SELECT count(*) FROM scout_triage"); n != 0 {
		t.Fatalf("triage rows = %d, want 0", n)
	}
	c.ok("zz-test-director", append([]string{"scout", "triage", "C-0001"}, good...)...)
	c.ok("owner", append([]string{"scout", "triage", "C-0001"}, good...)...)
}

func TestScoutCliReadPermissions(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	for _, act := range [][]string{{"pending"}, {"list"}, {"show", "C-0001"}, {"runs"}} {
		args := append([]string{"scout"}, act...)
		for _, r := range workers {
			c.refused(3, "zz-test-"+r, args...)
		}
		for _, r := range []string{"director", "lead", "boss"} {
			c.ok("zz-test-"+r, args...)
		}
		c.ok("owner", args...)
		c.refused(3, "zz-test-nobody", args...)
	}
}

func TestScoutCliPendingOnlyUntriagedLimitAndInventory(t *testing.T) {
	c := newCLI(t)
	for i := 1; i <= 5; i++ {
		c.cand(fmt.Sprintf("gh:a/r%d", i), candOpt{id: fmt.Sprintf("C-%04d", i)})
	}
	c.ok("owner", append([]string{"scout", "triage", "C-0001"}, good...)...)
	out := c.ok("zz-test-lead", "scout", "pending", "--limit", "2").Stdout
	notContains(t, "pending", out, "C-0001")
	contains(t, "pending", out, "C-0002")
	contains(t, "pending", out, "C-0003")
	notContains(t, "pending", out, "C-0004")
	contains(t, "pending", out, "## our inventory")
	contains(t, "pending", out, "skill ")
	contains(t, "pending", out, "plugin ")
}

func (c cli) countPending(args ...string) int {
	out := c.ok("zz-test-lead", append([]string{"scout", "pending"}, args...)...).Stdout
	return len(regexp.MustCompile(`(?m)^C-\d{4}\b`).FindAllString(out, -1))
}

func TestScoutCliPendingCapComesFromConfig(t *testing.T) {
	c := newCLI(t)
	for i := 1; i <= 40; i++ {
		c.cand(fmt.Sprintf("gh:a/r%d", i), candOpt{id: fmt.Sprintf("C-%04d", i)})
	}
	if n := c.countPending(); n != 8 {
		t.Fatalf("default cap = %d, want 8", n)
	}
	c.ok("owner", "config", "scout_triage_per_run", "36")
	if n := c.countPending(); n != 36 {
		t.Fatalf("configured cap = %d, want 36", n)
	}
	if n := c.countPending("--limit", "3"); n != 3 {
		t.Fatalf("--limit 3 = %d, want 3", n)
	}
}

func TestScoutCliTriagePerRunInvalidRefused(t *testing.T) {
	c := newCLI(t)
	for _, bad := range []string{"0", "51", "-2", "abc", "1.5", ""} {
		r := c.refused(2, "owner", "config", "scout_triage_per_run", bad)
		contains(t, "stderr for "+bad, r.Stderr, "scout_triage_per_run")
	}
	contains(t, "config", c.ok("owner", "config").Stdout, "scout_triage_per_run = 8")
	c.ok("owner", "config", "scout_triage_per_run", "50")
	c.ok("owner", "config", "scout_triage_per_run", "1")
}

func TestScoutCliLimitBelowOneExit2(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	for _, bad := range []string{"0", "-1"} {
		r := c.refused(2, "owner", "scout", "pending", "--limit", bad)
		contains(t, "stderr", r.Stderr, "--limit")
	}
	c.ok("owner", "scout", "pending", "--limit", "1")
}

func TestScoutCliListBadSinceExit2(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	c.refused(2, "owner", "scout", "list", "--since", "yesterday")
}

func TestScoutCliShowSnapshotsInTimeOrder(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	d := c.e.RawDB()
	exec(t, d, "INSERT INTO scout_stars VALUES('C-0001','2026-10-25T02:30:00+02:00',100)")
	exec(t, d, "INSERT INTO scout_stars VALUES('C-0001','2026-10-25T02:10:00+01:00',110)")
	d.Close()
	out := c.ok("owner", "scout", "show", "C-0001").Stdout
	if a, b := strings.Index(out, "02:30:00+02:00"), strings.Index(out, "02:10:00+01:00"); a < 0 || b < 0 || a >= b {
		t.Fatalf("snapshots out of time order (%d, %d):\n%s", a, b, out)
	}
}

// The harness runs the CLI with cwd = the factory home; an @path why must stay inside it.
func TestScoutCliTriageAtPathRestrictedToCwd(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	// ft.Env runs the CLI with cwd = e.Root.
	inside := filepath.Join(c.e.Root, "why.txt")
	if err := os.WriteFile(inside, []byte("from a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	outside := filepath.Join(other, "outside-why.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(c.e.Root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	args := func(w string) []string {
		return []string{"scout", "triage", "C-0001", "--purpose", "p", "--roles", "coder", "--overlap", "o", "--rec", "watch", "--why", w}
	}
	rel, err := filepath.Rel(c.e.Root, outside)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"@" + outside, "@link.txt", "@" + rel} {
		r := c.refused(2, "owner", args(bad)...)
		contains(t, "stderr for "+bad, r.Stderr, "inside the current directory")
	}
	if n := c.count("SELECT count(*) FROM scout_triage"); n != 0 {
		t.Fatalf("triage rows = %d, want 0", n)
	}
	c.ok("owner", args("@why.txt")...)
	if got := c.e.SQL("SELECT rationale FROM scout_triage")[0][0]; got != "from a file" {
		t.Fatalf("rationale = %q, want %q", got, "from a file")
	}
}

// FACTORY_ROLE alone marks the caller a worker; only a stub multiplexer on PATH and a non-live home exempts it.
func TestScoutCliWorkerRoleRefusedWithoutStubBinary(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/b", candOpt{})
	bare := map[string]string{"PATH": "/usr/bin:/bin", "FACTORY_ROLE": "coder"}
	for _, act := range [][]string{{"list"}, {"pending"}, append([]string{"triage", "C-0001"}, good...), {"fetch"}} {
		r := c.e.FailWith(ft.Opts{Agent: "owner", Env: bare}, 3, append([]string{"scout"}, act...)...)
		contains(t, "stderr", r.Stderr, "may not run `factory scout")
	}
	if n := c.count("SELECT count(*) FROM scout_triage"); n != 0 {
		t.Fatalf("triage rows = %d, want 0", n)
	}
	c.e.OKWith(ft.Opts{Agent: "owner", Env: map[string]string{"PATH": "/usr/bin:/bin", "FACTORY_ROLE": "scout"}}, "scout", "list")
}

func TestScoutCliListFiltersShowRuns(t *testing.T) {
	c := newCLI(t)
	c.cand("gh:a/one", candOpt{id: "C-0001"})
	c.cand("gh:a/two", candOpt{id: "C-0002", risk: "hooks,scripts"})
	c.ok("owner", append([]string{"scout", "triage", "C-0002"}, good...)...)
	ls := func(a ...string) string { return c.ok("owner", append([]string{"scout", "list"}, a...)...).Stdout }
	contains(t, "list --new", ls("--new"), "C-0001")
	notContains(t, "list --new", ls("--new"), "C-0002")
	contains(t, "list --rec watch", ls("--rec", "watch"), "C-0002")
	notContains(t, "list --rec watch", ls("--rec", "watch"), "C-0001")
	contains(t, "list --risk hooks", ls("--risk", "hooks"), "C-0002")
	notContains(t, "list --risk hooks", ls("--risk", "hooks"), "C-0001")
	contains(t, "list --since", ls("--since", "2026-09-01"), "C-0001")
	if got := strings.TrimSpace(ls("--since", "2027-01-01")); got != "(none)" {
		t.Fatalf("list --since 2027 = %q, want (none)", got)
	}
	contains(t, "show", c.ok("owner", "scout", "show", "C-0002").Stdout, "rec=watch")
	c.refused(2, "owner", "scout", "show", "C-0099")
	if got := strings.TrimSpace(c.ok("owner", "scout", "runs").Stdout); got != "(none)" {
		t.Fatalf("runs = %q, want (none)", got)
	}
}
