package tick

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/store"
)

// Tests for units 5-8 (digest, tmp_alert, tmp_sweep + remote_url_alert, main_protection). The expected texts and values
// come from one-off `python3 -I` runs (re.sub, repr, float(), re.search, the OSError and TimeoutExpired texts) and
// from the Python tests of the same units (tests/test_main_protection_check.py, test_remote_url_check.py,
// test_tmp_sweep.py, test_tmp_leftover_sweep.py), see the PR body.

var alertT0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// alertTest is one throw-away factory home with the golden Python schema and the digestall_send seed.
type alertTest struct {
	T    *testing.T
	A    *alertEnv
	DB   *sql.DB
	Home string
	PW   string
	Now  time.Time
}

func newAlertTest(t *testing.T) *alertTest {
	t.Helper()
	home, pw := t.TempDir(), t.TempDir()
	path := filepath.Join(home, "db", "factory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db := dbtest.Raw(t, path)
	t.Cleanup(func() { db.Close() })
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join("..", "cli", "testdata", "golden", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, q := range []string{"PRAGMA journal_mode=WAL", read("base.sql"), read("digestall_send/seed.sql"),
		fmt.Sprintf("PRAGMA user_version = %d", factorydb.SchemaVersion)} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	at := &alertTest{T: t, DB: db, Home: home, PW: pw, Now: alertT0}
	env := pruneTestEnv(pw, home, "2026-11-12T12:00:00+00:00", func() time.Time { return at.Now })
	withPruneIdentity(t, env)
	prev := alertWall
	alertWall = func() time.Time { return at.Now }
	t.Cleanup(func() { alertWall = prev })
	at.A = &alertEnv{S: &store.Store{DB: db, Clock: func() time.Time { return at.Now }, Zone: time.UTC}, Env: env, Home: home}
	t.Setenv("TZ", "UTC")
	t.Setenv("FACTORY_NOW", "")
	t.Setenv("FACTORY_TMP_DIR", t.TempDir())
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,900000000") // 10% used: nothing to do
	return at
}

func (at *alertTest) exec(q string, args ...any) {
	at.T.Helper()
	if _, err := at.DB.Exec(q, args...); err != nil {
		at.T.Fatalf("%s: %v", q, err)
	}
}

func (at *alertTest) cfg(k, v string) {
	at.T.Helper()
	at.exec("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", k, v)
}

func (at *alertTest) unit() *UnitCtx { return &UnitCtx{Home: at.Home} }

type msgRow struct{ Sender, Recipient, Kind, Subject, Body string }

func (at *alertTest) messages(where string) []msgRow {
	at.T.Helper()
	rs, err := at.DB.Query("SELECT sender,recipient,kind,subject,COALESCE(body,'') FROM messages WHERE " + where + " ORDER BY id")
	if err != nil {
		at.T.Fatal(err)
	}
	defer rs.Close()
	var out []msgRow
	for rs.Next() {
		var m msgRow
		if err := rs.Scan(&m.Sender, &m.Recipient, &m.Kind, &m.Subject, &m.Body); err != nil {
			at.T.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

type evRow struct{ Actor, Entity, EntityID, Action, Detail string }

func (at *alertTest) events(action string) []evRow {
	at.T.Helper()
	rs, err := at.DB.Query("SELECT COALESCE(actor,''),COALESCE(entity,''),COALESCE(entity_id,''),action,COALESCE(detail,'') FROM events WHERE action=? ORDER BY id", action)
	if err != nil {
		at.T.Fatal(err)
	}
	defer rs.Close()
	var out []evRow
	for rs.Next() {
		var e evRow
		if err := rs.Scan(&e.Actor, &e.Entity, &e.EntityID, &e.Action, &e.Detail); err != nil {
			at.T.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func (at *alertTest) cfgVal(k string) string {
	at.T.Helper()
	v, _, err := configRow(at.DB, k)
	if err != nil {
		at.T.Fatal(err)
	}
	return v
}

// --- unit table --------------------------------------------------------------------------------------------------

// TestAlertUnitTable: the Go-capable units are exactly the ones in testdata/go_units.txt; units 5-8 are in, every other
// unit is unchanged against origin/main (judge_ci_prune and hold_expiry stay the only other Go units).
func TestAlertUnitTable(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "go_units.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(b))
	got := sortedKeys(goUnits)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go units %v, testdata/go_units.txt %v", got, want)
	}
	for _, n := range []string{"digest", "tmp_alert", "remote_url_alert", "main_protection"} {
		if _, ok := goUnits[n]; !ok {
			t.Errorf("unit %s is not Go-capable", n)
		}
	}
	for _, n := range []string{"tmp_clean", "tmp_harness", "red_main", "issue_intake", "reap_serves", "reap_orphans", "test_orphans"} {
		if _, ok := goUnits[n]; ok {
			t.Errorf("unit %s must stay Python in this slice", n)
		}
	}
}

// --- digest ------------------------------------------------------------------------------------------------------

// pyDigestBody is the digest body Python sent in the golden case digestall_send/send_director (db.diff), the oracle
// for the body of the tick's digest message: same database, same FACTORY_NOW.
func pyDigestBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "cli", "testdata", "golden", "digestall_send", "send_director", "db.diff"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`body=("(?:[^"\\]|\\.)*")`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no body in the golden db.diff")
	}
	var body string
	if err := json.Unmarshal(m[1], &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDigestUnit(t *testing.T) {
	at := newAlertTest(t)
	t.Setenv("FACTORY_NOW", "2026-11-12T12:00:00+00:00")
	subject := "daily digest 2026-11-12"

	// before digest_hour (8): nothing, no marker
	at.Now = time.Date(2026, 11, 12, 7, 59, 0, 0, time.UTC)
	if err := digestUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if got := at.messages("subject LIKE 'daily digest%'"); len(got) != 0 || at.cfgVal("last_digest_date") != "" {
		t.Fatalf("before digest_hour: %v marker %q", got, at.cfgVal("last_digest_date"))
	}

	// from digest_hour on: one message per enabled org, from that org's director, body = the Python digest
	at.Now = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	if err := digestUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	got := at.messages("subject LIKE 'daily digest%'")
	if len(got) != 2 {
		t.Fatalf("want 2 digests (claude, cursor), got %+v", got)
	}
	body := pyDigestBody(t)
	for i, m := range got {
		if m.Subject != subject || m.Kind != "report" || m.Body != body {
			t.Errorf("message %d: subject %q kind %q body equal to Python=%v", i, m.Subject, m.Kind, m.Body == body)
		}
	}
	if got[0].Sender != "director" || got[1].Sender != "cursor-director" {
		t.Errorf("senders %q %q, want director and cursor-director (oname of each org)", got[0].Sender, got[1].Sender)
	}
	if got[0].Recipient != "claude-boss" {
		t.Errorf("recipient %q, want claude-boss", got[0].Recipient)
	}
	if at.cfgVal("last_digest_date") != "2026-11-12" {
		t.Errorf("last_digest_date %q", at.cfgVal("last_digest_date"))
	}

	// already sent for the day: no second send
	if err := digestUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if n := len(at.messages("subject LIKE 'daily digest%'")); n != 2 {
		t.Fatalf("second run sent again: %d messages", n)
	}

	// the next day sends again
	at.Now = at.Now.Add(24 * time.Hour)
	t.Setenv("FACTORY_NOW", "2026-11-13T12:00:00+00:00")
	if err := digestUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if n := len(at.messages("subject = 'daily digest 2026-11-13'")); n != 2 {
		t.Fatalf("next day: %d messages", n)
	}
}

// TestDigestFailureIsOneEventPerDay: a failing build never stops the tick; it writes digest-failed once and the
// marker, as morning_digest does.
func TestDigestFailureIsOneEventPerDay(t *testing.T) {
	at := newAlertTest(t)
	at.exec("DROP TABLE goals") // build_digest raises
	for i := 0; i < 2; i++ {
		if err := digestUnit(at.unit()); err != nil {
			t.Fatalf("run %d: the unit must not fail the tick: %v", i, err)
		}
	}
	ev := at.events("digest-failed")
	if len(ev) != 1 || ev[0].Entity != "digest" || ev[0].EntityID != "-" || ev[0].Actor != "owner" {
		t.Fatalf("digest-failed events: %+v", ev)
	}
	if at.cfgVal("last_digest_fail_date") != "2026-11-12" || at.cfgVal("last_digest_date") != "" {
		t.Errorf("markers: fail %q sent %q", at.cfgVal("last_digest_fail_date"), at.cfgVal("last_digest_date"))
	}
	at.Now = at.Now.Add(24 * time.Hour)
	if err := digestUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if n := len(at.events("digest-failed")); n != 2 {
		t.Fatalf("next day: %d events, want 2", n)
	}
}

// TestDigestBadHourIsAFailureEvent: int("x") raises inside the try, so it is the digest-failed event with Python's text.
func TestDigestBadHourIsAFailureEvent(t *testing.T) {
	at := newAlertTest(t)
	at.cfg("digest_hour", "eight")
	if err := digestUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	ev := at.events("digest-failed")
	if len(ev) != 1 || ev[0].Detail != "invalid literal for int() with base 10: 'eight'" {
		t.Fatalf("%+v", ev)
	}
}

// --- tmp_alert ---------------------------------------------------------------------------------------------------

func TestTmpAlert(t *testing.T) {
	at := newAlertTest(t)
	run := func() {
		t.Helper()
		if err := tmpAlertUnit(at.unit()); err != nil {
			t.Fatal(err)
		}
	}
	alerts := func() []msgRow { return at.messages("subject LIKE '/tmp %'") }

	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,50000000") // 95% used, over tmp_max_pct 90
	run()
	m := alerts()
	if len(m) != 1 {
		t.Fatalf("want one alert, got %+v", m)
	}
	if m[0].Sender != "tick" || m[0].Kind != "alert" || m[0].Subject != "/tmp 95.0% full" {
		t.Errorf("alert %+v", m[0])
	}
	// the file system type comes from the machine's /proc/mounts, so only its shape is pinned
	if !regexp.MustCompile(`^/tmp \(\S+( \S+)?\) uses 905/953 MB, over tmp_max_pct 90%: new spawns are held\. Free finished tasks' scratchpads and QA temp homes \(/tmp/qa-\*\) via the cleaner\.$`).MatchString(m[0].Body) {
		t.Errorf("body %q", m[0].Body)
	}
	if at.cfgVal("tmp_alerted") != "1" {
		t.Errorf("tmp_alerted %q", at.cfgVal("tmp_alerted"))
	}
	run() // still over: armed, no second alert
	if n := len(alerts()); n != 1 {
		t.Fatalf("armed: %d alerts", n)
	}
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,900000000")
	run() // re-armed
	if at.cfgVal("tmp_alerted") != "0" {
		t.Errorf("not re-armed: %q", at.cfgVal("tmp_alerted"))
	}
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,50000000")
	run()
	if n := len(alerts()); n != 2 {
		t.Fatalf("after re-arm: %d alerts, want 2", n)
	}
	// unreadable usage (garbage, one field, zero total): nothing happens, no error
	for _, v := range []string{"x,y", "1", "0,0", "1,2,3"} {
		t.Setenv("FACTORY_TMP_STATVFS", v)
		run()
	}
	if n := len(alerts()); n != 2 {
		t.Fatalf("unreadable usage changed the alerts: %d", n)
	}
}

// --- tmp_sweep ---------------------------------------------------------------------------------------------------

func (at *alertTest) agent(name, role, status, task, cwd, ended string) {
	at.T.Helper()
	at.exec("INSERT INTO agents(name,role,status,task_id,cwd,started,ended,flagless) VALUES(?,?,?,?,?,?,?,0)",
		name, role, status, nullIf(task), nullIf(cwd), "2026-11-12T01:00:00+00:00", nullIf(ended))
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (at *alertTest) mkfile(path string, size int, mtime time.Time) {
	at.T.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		at.T.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		at.T.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		at.T.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestTmpSweepScratchOfFinishedAgents(t *testing.T) {
	at := newAlertTest(t)
	tmp := os.Getenv("FACTORY_TMP_DIR")
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,100000000") // 90% used: over tmp_sweep_pct (80)
	slug := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(filepath.Join(at.Home, "worktrees")) + "-"
	base := filepath.Join(tmp, fmt.Sprintf("claude-%d", os.Getuid()))
	done, live, noAgents := slug+"G-1-T-10", slug+"G-1-T-11", slug+"G-1-T-12"
	for _, d := range []string{done, live, noAgents, slug + "G-1-T-13-qa", "HQ-scratch"} {
		at.mkfile(filepath.Join(base, d, "f"), 2048, at.Now)
	}
	qa := filepath.Join(tmp, "qa-home-T-10-abc")
	at.mkfile(filepath.Join(qa, "f"), 100, at.Now)
	ended := at.Now.Add(-31 * time.Minute).Format("2006-01-02T15:04:05-07:00")
	at.agent("a-done", "coder", "done", "T-10", "", ended)
	at.agent("a-live", "coder", "live", "T-11", "", "")
	at.agent("a-qa13", "qa", "stopped", "T-13", "", ended)
	at.agent("a-fresh", "qa", "stopped", "T-14", "", at.Now.Add(-10*time.Minute).Format("2006-01-02T15:04:05-07:00"))
	at.mkfile(filepath.Join(tmp, "qa-x-T-14-y", "f"), 1, at.Now)

	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{filepath.Join(base, done), filepath.Join(base, slug+"G-1-T-13-qa"), qa} {
		if exists(gone) {
			t.Errorf("%s should be swept", gone)
		}
	}
	for _, kept := range []string{filepath.Join(base, live), filepath.Join(base, noAgents), filepath.Join(base, "HQ-scratch"), filepath.Join(tmp, "qa-x-T-14-y")} {
		if !exists(kept) {
			t.Errorf("%s must be kept", kept)
		}
	}
	ev := at.events("tmp-swept")
	if len(ev) != 1 || ev[0].Entity != "tick" || ev[0].EntityID != "tmp-sweep" || ev[0].Actor != "tick" {
		t.Fatalf("tmp-swept events: %+v", ev)
	}
	// 3 dirs swept; their 2048+2048+100 bytes plus the dir entries themselves count in the MB figure
	var d map[string]any
	if err := json.Unmarshal([]byte(ev[0].Detail), &d); err != nil || d["dirs"] != float64(3) || d["leftovers"] != float64(0) || d["locked"] != float64(0) {
		t.Fatalf("detail %q: %v", ev[0].Detail, err)
	}
	if !strings.HasPrefix(ev[0].Detail, `{"mb": `) {
		t.Errorf("detail %q is not json.dumps order and spacing", ev[0].Detail)
	}
	// still over tmp_max_pct (90): one alert to the director, once
	al := at.messages("subject LIKE 'tmp sweep:%'")
	if len(al) != 1 || al[0].Subject != "tmp sweep: /tmp still 90.0% full" || al[0].Sender != "tick" || al[0].Kind != "alert" ||
		!strings.Contains(al[0].Body, "uses 858/953 MB, over tmp_max_pct 90%: spawns stay held and nothing more is safe to delete automatically.") {
		t.Fatalf("alert %+v", al)
	}
	if at.cfgVal("tmp_sweep_alerted") != "1" {
		t.Errorf("tmp_sweep_alerted %q", at.cfgVal("tmp_sweep_alerted"))
	}
	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if n := len(at.messages("subject LIKE 'tmp sweep:%'")); n != 1 {
		t.Fatalf("second run alerted again: %d", n)
	}
}

// TestTmpSweepLeftovers: the closed name list, the 24 h age, the agent-cwd and open-fd holds, the 500 cap, the throttle.
func TestTmpSweepLeftovers(t *testing.T) {
	at := newAlertTest(t)
	tmp := os.Getenv("FACTORY_TMP_DIR")
	proc := t.TempDir() // FACTORY_SERVE_PROC_DIR: a fake /proc with no process of ours
	t.Setenv("FACTORY_SERVE_PROC_DIR", proc)
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,100000000")
	old := at.Now.Add(-25 * time.Hour)
	for _, n := range []string{"guard-diff-abcd1234", "freshbox-abcd1234", "zz-test-foo-abcd1234", "tmp12345678", "zz-rv-abcd1234"} {
		at.mkfile(filepath.Join(tmp, n, "x"), 10, old)
		os.Chtimes(filepath.Join(tmp, n), old, old)
	}
	at.mkfile(filepath.Join(tmp, "tmp.AbCdEfGhIj"), 10, old) // a plain file of the listed shape
	// kept: too young, a name outside the list, held by an agent cwd
	at.mkfile(filepath.Join(tmp, "guard-diff-young001", "x"), 1, at.Now.Add(-time.Hour))
	at.mkfile(filepath.Join(tmp, "go-buildX", "x"), 1, old)
	at.mkfile(filepath.Join(tmp, "freshbox-held0001", "x"), 1, old)
	os.Chtimes(filepath.Join(tmp, "freshbox-held0001"), old, old)
	at.agent("a-cwd", "coder", "live", "T-5", filepath.Join(tmp, "freshbox-held0001"), "")

	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"guard-diff-abcd1234", "freshbox-abcd1234", "zz-test-foo-abcd1234", "tmp12345678", "zz-rv-abcd1234", "tmp.AbCdEfGhIj"} {
		if exists(filepath.Join(tmp, n)) {
			t.Errorf("%s should be removed", n)
		}
	}
	for _, n := range []string{"guard-diff-young001", "go-buildX", "freshbox-held0001"} {
		if !exists(filepath.Join(tmp, n)) {
			t.Errorf("%s must be kept", n)
		}
	}
	ev := at.events("tmp-swept")
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, `"leftovers": 6`) {
		t.Fatalf("events %+v", ev)
	}
	if v := at.throttle("tmpleftover"); v == "" || !strings.Contains(v, ".") {
		t.Errorf("throttle row %q is not a Python float repr", v)
	}
	// throttled for 10 minutes: a new leftover stays
	at.mkfile(filepath.Join(tmp, "guard-diff-late0001", "x"), 1, old)
	os.Chtimes(filepath.Join(tmp, "guard-diff-late0001"), old, old)
	at.Now = at.Now.Add(5 * time.Minute)
	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(tmp, "guard-diff-late0001")) {
		t.Error("the throttle (600 s) did not hold the leftover walk back")
	}
	at.Now = at.Now.Add(6 * time.Minute)
	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(tmp, "guard-diff-late0001")) {
		t.Error("after the throttle the leftover must go")
	}
}

func (at *alertTest) throttle(key string) string {
	at.T.Helper()
	v, _, err := throttleTS(at.DB, key)
	if err != nil {
		at.T.Fatal(err)
	}
	return v
}

// TestTmpSweepBlockedByUnreadablePid: a pid of ours that cannot be read and is not an ignored daemon blocks the
// leftover removal: nothing removed, one tmp-sweep-blocked event with pid, comm, kept and mb.
func TestTmpSweepBlockedByUnreadablePid(t *testing.T) {
	at := newAlertTest(t)
	tmp := os.Getenv("FACTORY_TMP_DIR")
	proc := t.TempDir()
	t.Setenv("FACTORY_SERVE_PROC_DIR", proc)
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,100000000")
	old := at.Now.Add(-25 * time.Hour)
	at.mkfile(filepath.Join(tmp, "freshbox-blocked1", "x"), 1000, old)
	os.Chtimes(filepath.Join(tmp, "freshbox-blocked1"), old, old)
	if os.Getuid() == 0 {
		t.Skip("root can read every fd directory")
	}
	// pid 4242 of our uid whose fd directory cannot be read (a non-dumpable process) and whose comm is "weird"
	unreadable := func(pid, comm string) {
		fd := filepath.Join(proc, pid, "fd")
		if err := os.MkdirAll(fd, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Symlink("/", filepath.Join(proc, pid, "cwd"))
		os.WriteFile(filepath.Join(proc, pid, "comm"), []byte(comm+"\n"), 0o644)
		if err := os.Chmod(fd, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(fd, 0o755) })
	}
	unreadable("4242", "weird")
	unreadable("99", "systemd") // an ignored daemon with the same problem does not block

	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(tmp, "freshbox-blocked1")) {
		t.Error("a blocked run must remove nothing")
	}
	ev := at.events("tmp-sweep-blocked")
	if len(ev) != 1 || ev[0].Detail != `{"pid": "4242", "comm": "weird", "kept": 1, "mb": 0.0}` {
		t.Fatalf("blocked events %+v", ev)
	}
	if n := len(at.events("tmp-swept")); n != 0 {
		t.Errorf("a blocked run writes no tmp-swept (locked and blocked): %d", n)
	}
}

// TestTmpSweepHeldByOpenFd: a process of ours with a cwd or fd inside a leftover keeps it.
func TestTmpSweepHeldByOpenFd(t *testing.T) {
	at := newAlertTest(t)
	tmp := os.Getenv("FACTORY_TMP_DIR")
	proc := t.TempDir()
	t.Setenv("FACTORY_SERVE_PROC_DIR", proc)
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,100000000")
	old := at.Now.Add(-25 * time.Hour)
	for _, n := range []string{"freshbox-heldfd01", "freshbox-free0001"} {
		at.mkfile(filepath.Join(tmp, n, "x"), 1, old)
		os.Chtimes(filepath.Join(tmp, n), old, old)
	}
	pid := filepath.Join(proc, "77")
	os.MkdirAll(filepath.Join(pid, "fd"), 0o755)
	os.Symlink("/", filepath.Join(pid, "cwd"))
	os.Symlink(filepath.Join(tmp, "freshbox-heldfd01", "x"), filepath.Join(pid, "fd", "3"))
	os.Symlink("pipe:[123]", filepath.Join(pid, "fd", "4"))
	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(tmp, "freshbox-heldfd01")) || exists(filepath.Join(tmp, "freshbox-free0001")) {
		t.Fatalf("held kept=%v free removed=%v", exists(filepath.Join(tmp, "freshbox-heldfd01")), !exists(filepath.Join(tmp, "freshbox-free0001")))
	}
}

// TestTmpSweepFailureIsAnEvent: any failure is tmp-sweep-failed, never a stopped tick.
func TestTmpSweepFailureIsAnEvent(t *testing.T) {
	at := newAlertTest(t)
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,100000000")
	at.cfg("tmp_sweep_pct", "lots")
	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatalf("tmp_sweep must not stop the tick: %v", err)
	}
	ev := at.events("tmp-sweep-failed")
	if len(ev) != 1 || ev[0].Detail != "could not convert string to float: 'lots'" || ev[0].Actor != "tick" {
		t.Fatalf("%+v", ev)
	}
}

// --- remote_url_alert ---------------------------------------------------------------------------------------------

// liveCheckout makes the live checkout (<pw>/factory) a git repo whose origin is url ("" = no origin).
func (at *alertTest) liveCheckout(url string) {
	at.T.Helper()
	repo := filepath.Join(at.PW, "factory")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		at.T.Fatal(err)
	}
	run := func(args ...string) {
		at.T.Helper()
		out, err := execGit(repo, args...)
		if err != nil {
			at.T.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	if url != "" {
		run("remote", "add", "origin", url)
	}
}

func TestRemoteURLAlert(t *testing.T) {
	const old, now = "https://github.com/example-org/old.git", "https://github.com/example-org/new.git"
	at := newAlertTest(t)
	run := func() {
		t.Helper()
		if err := remoteURLAlertUnit(at.unit()); err != nil {
			t.Fatal(err)
		}
	}
	alerts := func() []msgRow { return at.messages("subject = 'factory_remote_url does not match origin'") }

	at.liveCheckout("https://tok3n" + atGitHub + "/example-org/old.git")
	run() // factory_remote_url empty: nothing
	if len(alerts()) != 0 || at.cfgVal("remote_url_alerted") != "" {
		t.Fatalf("empty config alerted: %+v", alerts())
	}
	at.cfg("factory_remote_url", old)
	run() // the origin has credentials in its URL: it differs from the stored value
	m := alerts()
	if len(m) != 1 || m[0].Recipient != "director" && m[0].Recipient != "claude-director" || m[0].Sender != "tick" || m[0].Kind != "alert" {
		t.Fatalf("%+v", m)
	}
	want := "config factory_remote_url is '" + old + "' but `git remote get-url origin` of the factory checkout is '" + old +
		"': the git guard will refuse pushes. After a remote switch run `factory config factory_remote_url <new origin url>`; otherwise restore the origin."
	if m[0].Body != want {
		t.Fatalf("body\n got %q\nwant %q", m[0].Body, want)
	}
	if strings.Contains(m[0].Body, "tok3n") {
		t.Error("a credential leaked into the alert")
	}
	run() // same mismatch: no second alert
	if n := len(alerts()); n != 1 {
		t.Fatalf("repeated alert: %d", n)
	}
	// matching again re-arms
	at.cfg("factory_remote_url", "https://tok3n"+atGitHub+"/example-org/old.git")
	run()
	if at.cfgVal("remote_url_alerted") != "" || len(alerts()) != 1 {
		t.Fatalf("match must clear the marker, got %q", at.cfgVal("remote_url_alerted"))
	}
	at.cfg("factory_remote_url", now)
	run()
	if n := len(alerts()); n != 2 {
		t.Fatalf("after re-arm: %d alerts", n)
	}
}

func TestRemoteURLAlertNoOrigin(t *testing.T) {
	at := newAlertTest(t)
	at.liveCheckout("")
	at.cfg("factory_remote_url", "https://github.com/example-org/old.git")
	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	m := at.messages("subject = 'factory_remote_url does not match origin'")
	if len(m) != 1 || !strings.Contains(m[0].Body, "of the factory checkout is '(none)': the git guard will refuse pushes.") {
		t.Fatalf("%+v", m)
	}
}

// --- main_protection ---------------------------------------------------------------------------------------------

const protSlug = "acme/hive"

// fakeGH puts a `gh` on PATH that prints out, errs err and exits code for every call, and logs its argv.
func fakeGH(t *testing.T, out string, code int, errText string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\ncat <<'OUT'\n" + out + "\nOUT\ncat 1>&2 <<'ERR'\n" + errText + "\nERR\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestMainProtection(t *testing.T) {
	const (
		good    = `{"enforce_admins": {"enabled": true}, "required_pull_request_reviews": {"required_approving_review_count": 0}}`
		noAdmin = `{"enforce_admins": {"enabled": false}, "required_pull_request_reviews": {"required_approving_review_count": 0}}`
		noPR    = `{"enforce_admins": {"enabled": true}}`
	)
	cases := []struct {
		name, out, err string
		code           int
		wantAlert      string // substring of the body, "" = no alert
		wantEvent      string
	}{
		{"compliant", good, "", 0, "", ""},
		{"no_enforce_admins", noAdmin, "", 0, "enforce_admins is off (admins, including the owner's login, can push to main)", ""},
		{"no_pr_requirement", noPR, "", 0, "pull requests are not required", ""},
		{"both_missing", `{}`, "", 0, "enforce_admins is off (admins, including the owner's login, can push to main); pull requests are not required", ""},
		{"unprotected", "", "gh: Branch not protected (HTTP 404)", 1, "main has no branch protection at all", ""},
		{"gh_failure", "", "gh: network down", 1, "", "acme/hive: gh api protection exited 1: gh: network down"},
		{"bad_json", "not json", "", 0, "", "acme/hive: Expecting value: line 1 column 1 (char 0)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at := newAlertTest(t)
			at.cfg("public_text_repos", protSlug)
			log := fakeGH(t, c.out, c.code, c.err)
			if err := mainProtectionUnit(at.unit()); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(log); !strings.Contains(string(b), "api repos/"+protSlug+"/branches/main/protection") {
				t.Errorf("gh calls %q", b)
			}
			m := at.messages("subject LIKE 'public repo%'")
			if c.wantAlert == "" {
				if len(m) != 0 {
					t.Fatalf("unexpected alert %+v", m)
				}
			} else {
				if len(m) != 1 || m[0].Recipient != "claude-boss" || m[0].Sender != "tick" || m[0].Kind != "alert" ||
					m[0].Subject != "public repo acme/hive: main protection is too weak" {
					t.Fatalf("alert %+v", m)
				}
				body := "acme/hive main: " + c.wantAlert + ". Owner rule R-a: PR required (0 approvals), enforce_admins on, no force push, no deletion. " +
					"The owner fixes it with `python3 ci/publish_repo.py --repo acme/hive --apply`; agents may not change protection. Next reminder in one hour while it stays wrong."
				if m[0].Body != body {
					t.Fatalf("body\n got %q\nwant %q", m[0].Body, body)
				}
			}
			ev := at.events("main-protection-check-failed")
			if c.wantEvent == "" && len(ev) != 0 || c.wantEvent != "" && (len(ev) != 1 || ev[0].Detail != c.wantEvent || ev[0].Entity != "tick" || ev[0].EntityID != "main-protection") {
				t.Fatalf("events %+v, want %q", ev, c.wantEvent)
			}
		})
	}
}

func TestMainProtectionHourlyAndFrozen(t *testing.T) {
	at := newAlertTest(t)
	at.cfg("public_text_repos", protSlug+",Other/Repo")
	at.cfg("frozen_repos", "https://github.com/other/repo.git") // a frozen repo is not checked
	log := fakeGH(t, `{"enforce_admins": {"enabled": false}}`, 0, "")
	run := func(min int) { // the inner function: the unit's own clock is the pinned FACTORY_TEST_NOW
		t.Helper()
		at.Now = alertT0.Add(time.Duration(min) * time.Minute)
		if err := mainProtectionAlert(at.A); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []int{0, 1, 30, 59} {
		run(m)
	}
	if n := len(at.messages("subject LIKE 'public repo%'")); n != 1 {
		t.Fatalf("within the hour: %d alerts, want 1", n)
	}
	run(61)
	if n := len(at.messages("subject LIKE 'public repo%'")); n != 2 {
		t.Fatalf("after the hour: %d alerts, want 2", n)
	}
	if b, _ := os.ReadFile(log); strings.Contains(strings.ToLower(string(b)), "other/repo") {
		t.Errorf("the frozen repo was checked: %q", b)
	}
	// no repo configured: no gh call at all
	at.cfg("public_text_repos", "")
	os.Remove(log)
	run(200)
	if exists(log) {
		t.Error("gh was called with no public repo configured")
	}
	// an unreadable stamp is overwritten and the check runs
	at.cfg("public_text_repos", protSlug)
	at.cfg("main_protection_checked_at", "garbage")
	run(300)
	if at.cfgVal("main_protection_checked_at") != at.A.S.Now() {
		t.Errorf("stamp %q", at.cfgVal("main_protection_checked_at"))
	}
}

// --- tools down --------------------------------------------------------------------------------------------------

// TestAlertsToolsDown: gh, git and herdr unreachable. The units use no herdr (the herdr list is the driver's state), so
// units 5-8 are unaffected by it; with gh and git missing from PATH the texts are Python's, quoted from
// `python3 -I -c "subprocess.run(['gh_nope'])"`:  [Errno 2] No such file or directory: 'gh_nope'  and
// `subprocess.run(['sleep','2'], timeout=0.5)`: Command '['sleep', '2']' timed out after 0.5 seconds.
func TestAlertsToolsDown(t *testing.T) {
	at := newAlertTest(t)
	empty := t.TempDir()
	t.Setenv("PATH", empty) // no gh, no git, no herdr
	at.cfg("public_text_repos", protSlug)
	at.cfg("factory_remote_url", "https://github.com/example-org/old.git")
	at.liveCheckoutDirOnly()

	for _, fn := range []func(*UnitCtx) error{digestUnit, tmpAlertUnit, remoteURLAlertUnit, mainProtectionUnit} {
		if err := fn(at.unit()); err != nil {
			t.Fatalf("a missing tool must not stop the tick: %v", err)
		}
	}
	if ev := at.events("main-protection-check-failed"); len(ev) != 1 || ev[0].Detail != "acme/hive: [Errno 2] No such file or directory: 'gh'" {
		t.Errorf("gh down: %+v", ev)
	}
	if ev := at.events("remote-url-check-failed"); len(ev) != 1 || ev[0].Detail != "[Errno 2] No such file or directory: 'git'" ||
		ev[0].Entity != "tick" || ev[0].EntityID != "remote-url" {
		t.Errorf("git down: %+v", ev)
	}
	if m := at.messages("kind='alert'"); len(m) != 0 {
		t.Errorf("a tool that is down is an event, never an alert: %+v", m)
	}
	if at.cfgVal("remote_url_alerted") != "" {
		t.Errorf("remote_url_alerted written although git could not answer: %q", at.cfgVal("remote_url_alerted"))
	}
}

func (at *alertTest) liveCheckoutDirOnly() {
	at.T.Helper()
	if err := os.MkdirAll(filepath.Join(at.PW, "factory"), 0o755); err != nil {
		at.T.Fatal(err)
	}
}

// fakeHang puts a tool of that name on PATH that sleeps far longer than the shortened timeouts.
func fakeHang(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestAlertsHangingToolsTimeOut: a hanging gh or git ends in the timeout event with Python's TimeoutExpired text, not a
// hang (the 30 s of Python are shortened to 1 s here; the text carries the value).
func TestAlertsHangingToolsTimeOut(t *testing.T) {
	at := newAlertTest(t)
	at.cfg("public_text_repos", protSlug)
	at.cfg("factory_remote_url", "https://github.com/example-org/old.git")
	at.liveCheckout("https://github.com/example-org/old.git")
	fakeHang(t, "gh")
	fakeHang(t, "git")
	remoteGitTimeout, protectionGHTimeout = time.Second, time.Second
	t.Cleanup(func() { remoteGitTimeout, protectionGHTimeout = 30*time.Second, 30*time.Second })
	start := time.Now()
	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if err := mainProtectionUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("the hang was not cut: %v", time.Since(start))
	}
	ev := at.events("remote-url-check-failed")
	if len(ev) != 1 || !strings.HasPrefix(ev[0].Detail, "Command '['git', '-C', '") || len([]rune(ev[0].Detail)) != 300 { // str(e)[:300]
		t.Errorf("git: %+v", ev)
	}
	ev = at.events("main-protection-check-failed")
	if len(ev) != 1 || ev[0].Detail != "acme/hive: Command '['gh', 'api', 'repos/acme/hive/branches/main/protection']' timed out after 1 seconds" {
		t.Errorf("gh: %+v", ev)
	}
}

func execGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestDigestDelegateInputIsAFailureEvent: an input the Go builder hands to Python (an invalid FACTORY_NOW here) cannot be
// handed over inside a tick unit: it is the digest-failed event naming the rollback, never a different digest.
func TestDigestDelegateInputIsAFailureEvent(t *testing.T) {
	at := newAlertTest(t)
	t.Setenv("FACTORY_NOW", "not-a-time")
	if err := digestUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	ev := at.events("digest-failed")
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "set tick_impl to python") {
		t.Fatalf("%+v", ev)
	}
	if n := len(at.messages("subject LIKE 'daily digest%'")); n != 0 {
		t.Fatalf("a digest was sent: %d", n)
	}
}
