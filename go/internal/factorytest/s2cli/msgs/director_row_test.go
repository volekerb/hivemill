package s2msgs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_director_row.py (13 cases).

const (
	dirNow  = "2026-10-27T09:00:00+00:00"
	dirLine = "director (24h, daytime 08-20): avg workers 9.0 · share 45% · target 10 · median workers with queue 6 · " +
		"P1 starved 30 min (longest 30 min G-1) · cap breaches 1 · boss scores 3, 2"
	dirINS = "INSERT INTO util_samples(ts,live,workers,leads,judges,hq,other,queued,queued_workers,cap,target,demand,p1_starved,p2_starved) " +
		"VALUES(?,?,?,0,0,0,0,?,0,6,?,0,?,'[]')"
)

func directorEnv(t *testing.T) *ft.Env {
	t.Helper()
	t.Setenv("TZ", "UTC")
	e := ft.New(t)
	e.OK("config", "day_start_hour", "8")
	e.OK("config", "day_end_hour", "20")
	return e
}

func localDir(t *testing.T, day, h, m int) string {
	t.Helper()
	return time.Date(2026, 10, day, h, m, 0, 0, time.UTC).Format("2006-01-02T15:04:05Z07:00")
}

func (d *dirBase) sample(t *testing.T, ts string, workers, live, queued, target int, p1 []string) {
	t.Helper()
	sqlExec(t, d.e, dirINS, ts, live, workers, queued, target, jsonString(t, p1))
}

func jsonString(t *testing.T, p1 []string) string {
	t.Helper()
	if p1 == nil {
		p1 = []string{}
	}
	b, err := json.Marshal(p1)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type dirBase struct {
	e *ft.Env
}

func newDirBase(t *testing.T) *dirBase {
	return &dirBase{e: directorEnv(t)}
}

func (d *dirBase) fixture(t *testing.T) {
	t.Helper()
	for m := 50; m < 60; m++ {
		d.sample(t, localDir(t, 27, 7, m), 0, 20, 0, 10, []string{"G-9"})
	}
	for m := 0; m < 60; m++ {
		if m < 30 {
			d.sample(t, localDir(t, 27, 8, m), 6, 20, 2, 10, []string{"G-1"})
		} else {
			d.sample(t, localDir(t, 27, 8, m), 12, 20, 0, 10, nil)
		}
	}
	d.e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	sqlExec(t, d.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		localDir(t, 27, 8, 10), "zz-test-director", "config", "max_live_agents", "set", "8")
	d.msg(t, "director score 2026-10-26: 3/5")
	d.msg(t, "director score 2026-10-27: 2/5")
}

func (d *dirBase) msg(t *testing.T, subject string) {
	t.Helper()
	sqlExec(t, d.e, "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('boss','director','msg',?,'',?)",
		subject, localDir(t, 27, 8, 30))
}

func (d *dirBase) card(t *testing.T, args ...string) string {
	t.Helper()
	r := d.e.OKWith(ft.Opts{Env: map[string]string{"FACTORY_NOW": dirNow}}, append([]string{"scorecard"}, args...)...)
	lines := strings.Split(strings.TrimRight(r.Stdout, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestDirectorRowExactLine(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.fixture(t)
	if got := d.card(t); got != dirLine {
		t.Fatalf("card = %q", got)
	}
}

func TestDirectorRowBlankLineBeforeDirector(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	out := d.e.OKWith(ft.Opts{Env: map[string]string{"FACTORY_NOW": dirNow}}, "scorecard").Stdout
	contains(t, "out", out, "\n\ndirector (24h")
}

func TestDirectorRowTwoLowScores(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.fixture(t)
	d.msg(t, "director score 2026-10-26: 2/5")
	d.msg(t, "director score 2026-10-27: 1/5")
	if !strings.HasSuffix(d.card(t), "boss scores 2, 1 · TWO LOW SCORES") {
		t.Fatalf("card = %q", d.card(t))
	}
}

func TestDirectorRowNoScores(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.fixture(t)
	sqlExec(t, d.e, "DELETE FROM messages")
	if !strings.HasSuffix(d.card(t), "cap breaches 1 · boss scores -") {
		t.Fatalf("card = %q", d.card(t))
	}
}

func TestDirectorRowOneScore(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	sqlExec(t, d.e, "DELETE FROM messages")
	d.msg(t, "director score 2026-10-27: 4/5")
	if !strings.HasSuffix(d.card(t), "boss scores 4") {
		t.Fatalf("card = %q", d.card(t))
	}
}

func TestDirectorRowNoDaytimeSamples(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.sample(t, localDir(t, 27, 7, 55), 3, 20, 0, 10, nil)
	contains(t, "card", d.card(t), "no utilization samples · cap breaches 0")
}

func TestDirectorRowGapEndsEpisode(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	for _, m := range []int{0, 1, 2} {
		d.sample(t, localDir(t, 27, 8, m), 1, 20, 0, 10, []string{"G-1"})
	}
	for _, m := range []int{8, 9, 10, 11, 12, 13} {
		d.sample(t, localDir(t, 27, 8, m), 1, 20, 0, 10, []string{"G-1"})
	}
	line := d.card(t)
	contains(t, "line", line, "(longest 7 min G-1)")
	contains(t, "line", line, "P1 starved 13 min")
}

func TestDirectorRowOwnerConfigSetNotABreach(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	sqlExec(t, d.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		localDir(t, 27, 8, 10), "owner", "config", "max_live_agents", "set", "8")
	contains(t, "card", d.card(t), "cap breaches 0")
}

func TestDirectorRowDays2CountsPreviousDay(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.sample(t, localDir(t, 26, 8, 30), 4, 20, 0, 10, nil)
	contains(t, "card", d.card(t), "no utilization samples")
	line := d.card(t, "--days", "2")
	if !strings.HasPrefix(line, "director (48h, daytime 08-20): avg workers 4.0") {
		t.Fatalf("line = %q", line)
	}
}

func TestDirectorRowDaysInvalidExit2(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.e.Fail(2, "scorecard", "--days", "0")
	d.e.Fail(2, "scorecard", "--days", "x")
}

func TestDirectorRowBadConfigDefaults(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.e.OK("config", "day_start_hour", "abc")
	contains(t, "card", d.card(t), "daytime 08-20")
	d.e.OK("config", "day_start_hour", "20")
	d.e.OK("config", "day_end_hour", "8")
	contains(t, "card", d.card(t), "daytime 08-20")
}

func TestDirectorRowDigestSection(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	d.fixture(t)
	d.e.SeedGoal("G-001", "", "zz-test goal")
	out := d.e.OKWith(ft.Opts{Env: map[string]string{"FACTORY_NOW": dirNow}}, "digest").Stdout
	contains(t, "digest", out, "## Director\n\n"+dirLine+"\n")
	if strings.Index(out, "## Director") >= strings.Index(out, "## G-001") {
		t.Fatal("Director section should precede G-001")
	}
}

func TestDirectorRowPerformance35Days(t *testing.T) {
	needPy(t)
	d := newDirBase(t)
	base := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	const n = 35 * 1440
	db := d.e.RawDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05Z07:00")
		if _, err := tx.Exec(dirINS, ts, 20, 5, 0, 10, "[]"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	start := time.Now()
	d.e.OKWith(ft.Opts{Env: map[string]string{"FACTORY_NOW": dirNow}}, "scorecard", "--days", "2")
	if time.Since(start) >= 2*time.Second {
		t.Fatalf("scorecard too slow: %v", time.Since(start))
	}
}
