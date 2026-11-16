package tick

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"

	_ "modernc.org/sqlite"
)

// Tests for unit judge_ci_prune. The rule cases (testdata/prune/<case>/in.json + want.json) port
// tests/test_judge_ci_prune.py plus the edge cases of the T-1385 spec. Python names:
//
//	done_task                         test_row_of_done_task_is_deleted
//	cancelled_task                    test_row_of_cancelled_task_is_deleted
//	no_task                           test_row_without_a_task_is_deleted
//	older_than_seven_days             test_row_older_than_seven_days_is_deleted_even_for_an_open_task
//	open_younger_kept                 test_row_of_open_in_review_task_younger_than_seven_days_is_kept
//	unparsable_values                 test_unparsable_value_is_deleted_without_a_crash
//	non_finite_or_huge_at             test_non_finite_or_huge_at_is_deleted_and_does_not_stop_the_other_rows
//	key_case_sensitive                test_key_match_is_case_sensitive
//	shared_done_and_open_kept         test_a_pr_shared_by_a_done_and_an_open_task_is_kept
//	only_judge_ci_rows_touched        test_only_judge_ci_rows_are_touched
//	logged_once                       test_pruned_rows_are_logged_once
//	raising_step_is_one_event         test_a_raising_prune_step_is_one_event_and_the_tick_goes_on
//
// test_pruned_open_task_row_is_cached_again_by_the_next_judge_pr_ready and test_split_equals_whole_db_and_calls stay in
// Python (they call gh / the whole tick). floats.json and clock.json are the output of one-off `python3 -I` runs over
// the same inputs (stdlib json/float/datetime.fromisoformat only), see the PR body.

const pruneTestSchema = `
CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE tasks (id TEXT PRIMARY KEY, status TEXT NOT NULL, pr_url TEXT);
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT,
  entity_id TEXT, action TEXT NOT NULL, detail TEXT);
`

func pruneTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // as factorydb.Open: a second statement while rows are open would deadlock
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(pruneTestSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

type pruneIn struct {
	FactoryNow string `json:"factory_now"`
	Tasks      []struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		PRURL  *string `json:"pr_url"`
	} `json:"tasks"`
	Config map[string]string `json:"config"`
}

type pruneEv struct {
	Actor    string `json:"actor"`
	Entity   string `json:"entity"`
	EntityID string `json:"entity_id"`
	Action   string `json:"action"`
	Detail   string `json:"detail"`
}

type pruneWant struct {
	ConfigKeys []string  `json:"config_keys"`
	Events     []pruneEv `json:"events"`
}

func pruneSetup(t *testing.T, db *sql.DB, factoryNow string) {
	t.Helper()
	pruneNow = func() (float64, error) {
		return pyClockNow(func(k string) string {
			if k == "FACTORY_NOW" {
				return factoryNow
			}
			return ""
		}, time.Now, time.FixedZone("local", 3600)) // naive FACTORY_NOW = UTC+1 here
	}
	t.Cleanup(func() {
		pruneNow = func() (float64, error) { return pyClockNow(os.Getenv, time.Now, nil) }
	})
	// Events use identity.Env.Now. The stamp is FACTORY_TEST_NOW on a non-live home. The map stays off the process
	// environment so a tick child started by a later test does not inherit it.
	pw, home := t.TempDir(), t.TempDir()
	withPruneIdentity(t, pruneTestEnv(pw, home, "2026-10-31T12:00:00+00:00", time.Now))
}

func withPruneIdentity(t *testing.T, env identity.Env) {
	t.Helper()
	prev := pruneIdentity
	pruneIdentity = func() identity.Env { return env }
	t.Cleanup(func() { pruneIdentity = prev })
}

// pruneTestEnv is a non-live home: pw is the passwd directory, home is FACTORY_HOME, and they differ.
func pruneTestEnv(pw, home, stamp string, real func() time.Time) identity.Env {
	vars := map[string]string{"FACTORY_HOME": home, "FACTORY_TEST_NOW": stamp}
	return identity.Env{
		Getenv: func(k string) string { return vars[k] },
		LookupEnv: func(k string) (string, bool) {
			v, ok := vars[k]
			return v, ok
		},
		PwDir: func(int) (string, error) { return pw, nil },
		Now:   real,
	}
}

func TestJudgeCiPrune(t *testing.T) {
	dirs, err := os.ReadDir("testdata/prune")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		n++
		t.Run(d.Name(), func(t *testing.T) {
			var in pruneIn
			var want pruneWant
			readJSON(t, filepath.Join("testdata/prune", d.Name(), "in.json"), &in)
			readJSON(t, filepath.Join("testdata/prune", d.Name(), "want.json"), &want)
			db := pruneTestDB(t)
			pruneSetup(t, db, in.FactoryNow)
			for _, tk := range in.Tasks {
				if _, err := db.Exec("INSERT INTO tasks(id,status,pr_url) VALUES(?,?,?)", tk.ID, tk.Status, tk.PRURL); err != nil {
					t.Fatal(err)
				}
			}
			for k, v := range in.Config {
				if _, err := db.Exec("INSERT INTO config(key,value) VALUES(?,?)", k, v); err != nil {
					t.Fatal(err)
				}
			}
			if err := pruneUnitFn(&UnitCtx{DB: db}); err != nil {
				t.Fatalf("unit error (it must never stop the tick): %v", err)
			}
			var keys []string
			rs, err := db.Query("SELECT key FROM config ORDER BY key")
			if err != nil {
				t.Fatal(err)
			}
			for rs.Next() {
				var k string
				if err := rs.Scan(&k); err != nil {
					rs.Close()
					t.Fatal(err)
				}
				keys = append(keys, k)
			}
			if err := rs.Err(); err != nil {
				rs.Close()
				t.Fatal(err)
			}
			rs.Close()
			sort.Strings(want.ConfigKeys)
			if !reflect.DeepEqual(append([]string(nil), keys...), append([]string(nil), want.ConfigKeys...)) && !(len(keys) == 0 && len(want.ConfigKeys) == 0) {
				t.Errorf("config keys %q, want %q", keys, want.ConfigKeys)
			}
			var evs []pruneEv
			re, err := db.Query("SELECT actor,entity,entity_id,action,detail FROM events ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			for re.Next() {
				var e pruneEv
				if err := re.Scan(&e.Actor, &e.Entity, &e.EntityID, &e.Action, &e.Detail); err != nil {
					re.Close()
					t.Fatal(err)
				}
				evs = append(evs, e)
			}
			if err := re.Err(); err != nil {
				re.Close()
				t.Fatal(err)
			}
			re.Close()
			if len(evs) != len(want.Events) || (len(evs) > 0 && !reflect.DeepEqual(evs, want.Events)) {
				t.Errorf("events %+v, want %+v", evs, want.Events)
			}
			var ts string
			if len(evs) > 0 {
				if err := db.QueryRow("SELECT ts FROM events LIMIT 1").Scan(&ts); err != nil {
					t.Fatal(err)
				}
				if ts != "2026-10-31T12:00:00+00:00" {
					t.Errorf("event ts %q: want the FACTORY_TEST_NOW layout", ts)
				}
			}
		})
	}
	if n < 20 {
		t.Fatalf("only %d cases in testdata/prune", n)
	}
}

func readJSON(t *testing.T, p string, v any) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
}

// TestJudgeCiPruneLongError: the error detail is cut at 300 characters (runes), like str(e)[:300].
func TestJudgeCiPruneLongError(t *testing.T) {
	db := pruneTestDB(t)
	bad := strings.Repeat("é", 400)
	pruneSetup(t, db, bad)
	if err := pruneUnitFn(&UnitCtx{DB: db}); err != nil {
		t.Fatal(err)
	}
	var d string
	if err := db.QueryRow("SELECT detail FROM events WHERE action='judge-ci-prune-error'").Scan(&d); err != nil {
		t.Fatal(err)
	}
	if len([]rune(d)) != 300 || !strings.HasPrefix(d, "FACTORY_NOW must be an ISO timestamp, got ééé") {
		t.Fatalf("detail %q (%d runes)", d, len([]rune(d)))
	}
}

// TestJudgeCiPruneDBError: a failing statement (no tasks table) is an error event, never a returned error.
func TestJudgeCiPruneDBError(t *testing.T) {
	db := pruneTestDB(t)
	pruneSetup(t, db, "2026-10-31T12:00:00+00:00")
	if _, err := db.Exec("DROP TABLE tasks"); err != nil {
		t.Fatal(err)
	}
	if err := pruneUnitFn(&UnitCtx{DB: db}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM events WHERE action='judge-ci-prune-error' AND entity_id='judge-ci-prune'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want one error event, got %d", n)
	}
}

// TestJudgeCiPruneHonoursTestNow: on a non-live home the events row is stamped with FACTORY_TEST_NOW
// (identity.Env.Now / EventAs), not the real clock.
func TestJudgeCiPruneHonoursTestNow(t *testing.T) {
	const stamp = "2026-04-01T05:06:07+02:00"
	db := pruneTestDB(t)
	if _, err := db.Exec("INSERT INTO config(key,value) VALUES(?,?)",
		"judge_ci:https://example.test/pull/1", `{"at": 1}`); err != nil {
		t.Fatal(err)
	}
	pruneNow = func() (float64, error) { return 1_700_000_000, nil }
	t.Cleanup(func() {
		pruneNow = func() (float64, error) { return pyClockNow(os.Getenv, time.Now, nil) }
	})
	pw := t.TempDir()
	t.Setenv("FACTORY_HOME", t.TempDir())
	t.Setenv("FACTORY_TEST_NOW", stamp)
	withPruneIdentity(t, identity.Env{
		Getenv:    os.Getenv,
		LookupEnv: os.LookupEnv,
		PwDir:     func(int) (string, error) { return pw, nil },
		Now:       func() time.Time { return time.Date(1999, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	if err := pruneUnitFn(&UnitCtx{DB: db}); err != nil {
		t.Fatal(err)
	}
	var ts string
	if err := db.QueryRow("SELECT ts FROM events WHERE action='judge-ci-pruned'").Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts != stamp {
		t.Fatalf("ts %q, want FACTORY_TEST_NOW %q", ts, stamp)
	}
}

// TestJudgeCiPruneTestNowIgnoredOnLiveHome: a live home stamps identity.Env.Now and ignores FACTORY_TEST_NOW.
func TestJudgeCiPruneTestNowIgnoredOnLiveHome(t *testing.T) {
	const stamp = "2026-04-01T05:06:07+02:00"
	fixed := time.Date(1999, 1, 2, 3, 4, 5, 0, time.FixedZone("CET", 3600))
	db := pruneTestDB(t)
	if _, err := db.Exec("INSERT INTO config(key,value) VALUES(?,?)",
		"judge_ci:https://example.test/pull/1", `{"at": 1}`); err != nil {
		t.Fatal(err)
	}
	pruneNow = func() (float64, error) { return 1_700_000_000, nil }
	t.Cleanup(func() {
		pruneNow = func() (float64, error) { return pyClockNow(os.Getenv, time.Now, nil) }
	})
	pw := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pw, "factory"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_HOME", filepath.Join(pw, "factory"))
	t.Setenv("FACTORY_TEST_NOW", stamp)
	withPruneIdentity(t, identity.Env{
		Getenv:    os.Getenv,
		LookupEnv: os.LookupEnv,
		PwDir:     func(int) (string, error) { return pw, nil },
		Now:       func() time.Time { return fixed },
	})
	if err := pruneUnitFn(&UnitCtx{DB: db}); err != nil {
		t.Fatal(err)
	}
	var ts string
	if err := db.QueryRow("SELECT ts FROM events WHERE action='judge-ci-pruned'").Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts != "1999-01-02T03:04:05+01:00" {
		t.Fatalf("ts %q, want the live-home clock, not %s", ts, stamp)
	}
}

// TestTickChildrenRealPrune: with the real unit registered the walk is two Python children around it, and
// judge_ci_prune is in neither list (T-1385 acceptance 5).
func TestTickChildrenRealPrune(t *testing.T) {
	if _, ok := goUnits[pruneUnit]; !ok {
		t.Fatal("judge_ci_prune is not registered from init()")
	}
	for _, timers := range []bool{false, true} {
		r := newRig(t, []step{{State: map[string]any{"agents": []any{}}}, {}})
		r.timers = timers
		r.goFns = goUnits
		db := pruneTestDB(t)
		pruneSetup(t, db, "2026-10-31T12:00:00+00:00")
		self, _ := os.Executable()
		tm := 0.0
		code := Run(Config{Home: r.home, DB: db, Stdout: &r.out, Stderr: &r.errb, Environ: os.Environ(), Python: self,
			Entry: "bin/factory", Clock: func() float64 { tm++; return tm }, Getpid: os.Getpid,
			TimersOn: func() bool { return timers }, Go: goUnits})
		if code != 0 {
			t.Fatalf("exit %d: %s", code, r.errb.String())
		}
		cs := r.calls()
		all := allUnits()
		i := indexOf(all, pruneUnit)
		if len(cs) != 2 {
			t.Fatalf("timers=%v: want 2 children, got %+v", timers, cs)
		}
		if cs[0].Units != phaseSpec(all[:i]) || !strings.HasSuffix(cs[0].Units, "test_orphans") || strings.Contains(cs[0].Units, pruneUnit) {
			t.Fatalf("first child %q", cs[0].Units)
		}
		wantTail := ",util"
		if timers {
			wantTail = ",tail" // the emit unit completes the tail block, so phaseSpec names the block
		}
		if !strings.HasPrefix(cs[1].Units, "side,") || !strings.HasSuffix(cs[1].Units, wantTail) || strings.Contains(cs[1].Units, pruneUnit) {
			t.Fatalf("second child %q", cs[1].Units)
		}
	}
}
