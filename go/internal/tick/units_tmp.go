package tick

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"factory/go/internal/store"
)

// Units 9 tmp_clean (_tick_u_tmp_clean -> tmp_clean) and 10 tmp_harness (_tick_u_tmp_harness -> tmp_sweep_harness) of
// bin/factory_cli.py. Both delete files: the allow-lists, prefixes and age thresholds equal Python's.

const (
	tmpCleanThrottleS   = 3600 // TMP_CLEAN_THROTTLE_S (bin/factory_cli.py:9146)
	tmpCleanMinAgeS     = 3600 // TMP_CLEAN_MIN_AGE_S (bin/factory_cli.py:9147)
	tmpHarnessThrottleS = 3600 // TMP_HARNESS_THROTTLE_S (bin/factory_cli.py:9595)
	tmpHarnessMinAgeS   = 3600 // TMP_HARNESS_MIN_AGE_S (bin/factory_cli.py:9596)
	tmpHarnessCap       = 500  // TMP_HARNESS_CAP (bin/factory_cli.py:9597)
)

// _HARNESS_NAME (bin/factory_cli.py:9598), used with fullmatch; the class is ASCII in Python too.
var tmpHarnessName = regexp.MustCompile(`^zz-test-factory-[A-Za-z0-9_]{8}$`)

func init() {
	registerGo("tmp_clean", tmpCleanUnit)
	registerGo("tmp_harness", tmpHarnessUnit)
}

func tmpCleanUnit(u *UnitCtx) error {
	a, closeDB, err := openAlert(u)
	if err != nil {
		return err
	}
	defer closeDB()
	tmpClean(a)
	return nil
}

func tmpHarnessUnit(u *UnitCtx) error {
	a, closeDB, err := openAlert(u)
	if err != nil {
		return err
	}
	defer closeDB()
	tmpSweepHarness(a)
	return nil
}

// throttled is the shared head of both units: true when the unit ran less than `every` seconds ago; otherwise the
// throttle row is set to now (str(time.time())).
func throttled(a *alertEnv, key string, every, t float64) (bool, error) {
	last, has, err := throttleTS(a.S.DB, key)
	if err != nil {
		return false, err
	}
	if has {
		lf, ok := pyFloat(last)
		if !ok {
			return false, fmt.Errorf("could not convert string to float: %s", pyRepr(last))
		}
		if t-lf < every {
			return true, nil
		}
	}
	if _, err := a.S.DB.Exec("INSERT INTO throttle(key,ts) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts",
		key, store.PyFloatRepr(t)); err != nil {
		return false, fmt.Errorf("write throttle %s: %w", key, err)
	}
	return false, nil
}

// failed is the `except Exception` tail of both units: one *-failed event, never a stopped tick.
func failed(a *alertEnv, entityID, action string, err error) {
	// a failing event write is Python's inner try/except pass: housekeeping must never stop the tick
	_ = a.S.EventAs("tick", "tick", entityID, action, pyErrText(err), "")
}

// tmpClean is tmp_clean: delete <tmp>/claude-<uid>/<worktrees slug>-<GOAL>-<TASK> scratchpads of done or cancelled tasks
// that no live/starting/queued/done-unreaped agent holds and that are older than 1 h. Symlinks and anything resolving
// outside <tmp>/claude-<uid>/ stay. One tmp-cleaned event per deletion.
func tmpClean(a *alertEnv) {
	if err := tmpCleanRun(a); err != nil {
		failed(a, "tmp-clean", "tmp-clean-failed", err)
	}
}

func tmpCleanRun(a *alertEnv) error {
	s := a.S
	t := alertNow()
	skip, err := throttled(a, "tmpclean", tmpCleanThrottleS, t)
	if err != nil || skip {
		return err
	}
	base := filepath.Join(tmpDir(), fmt.Sprintf("claude-%d", os.Getuid()))
	if li, err := os.Lstat(base); err != nil || li.Mode()&os.ModeSymlink != 0 {
		return nil // not a dir, or a symlink: left alone
	}
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return nil
	}
	root := pyRealpath(base)
	// re.escape(re.sub(r"[^A-Za-z0-9]", "-", str(WORKTREES)) + "-") + r"(G-\d+)-(T-\d+)", fullmatch; \d is Unicode in Python.
	slug := regexp.MustCompile(`^` + regexp.QuoteMeta(nonAlnum.ReplaceAllString(filepath.Join(a.Home, "worktrees"), "-")+"-") +
		`(G-\p{Nd}+)-(T-\p{Nd}+)$`)
	ents, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("list %s: %w", base, err)
	}
	for _, e := range ents {
		m := slug.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		d := filepath.Join(base, e.Name())
		li, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if li.Mode()&os.ModeSymlink != 0 || !li.IsDir() || filepath.Dir(pyRealpath(d)) != root {
			continue
		}
		var status string
		switch err := s.DB.QueryRow("SELECT status FROM tasks WHERE id=? AND goal_id=?", m[2], m[1]).Scan(&status); {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return fmt.Errorf("read task %s: %w", m[2], err)
		}
		if status != "done" && status != "cancelled" {
			continue
		}
		var one int
		switch err := s.DB.QueryRow("SELECT 1 FROM agents WHERE task_id=? AND status IN ('live','starting','queued','done')", m[2]).Scan(&one); {
		case err == nil:
			continue // a live agent of the task holds it
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read agents of %s: %w", m[2], err)
		}
		if t-epochSeconds(li.ModTime()) < tmpCleanMinAgeS {
			continue
		}
		size := tmpTreeBytes(d)
		if err := os.RemoveAll(d); err != nil {
			return err // shutil.rmtree raises: the whole run ends in tmp-clean-failed
		}
		detail := store.Ordered{{K: "path", V: d}, {K: "mb", V: pyRound1(float64(size) / tmpMB)}}
		if err := s.EventAs("tick", "task", m[2], "tmp-cleaned", detail, ""); err != nil {
			return err
		}
	}
	return nil
}

// tmpSweepHarness is tmp_sweep_harness: delete stale <tmp>/zz-test-factory-<8 chars> test-harness homes (real dirs of our
// uid directly under <tmp>) that nothing touched for 1 h and no process of ours holds. At most 500 per pass.
func tmpSweepHarness(a *alertEnv) {
	if err := tmpSweepHarnessRun(a); err != nil {
		failed(a, "tmp-harness", "tmp-harness-failed", err)
	}
}

func tmpSweepHarnessRun(a *alertEnv) error {
	s := a.S
	t := alertNow()
	skip, err := throttled(a, "tmpharness", tmpHarnessThrottleS, t)
	if err != nil || skip {
		return err
	}
	base := tmpDir()
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return nil
	}
	ents, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("list %s: %w", base, err)
	}
	var old, unmeasured []string
	uid := os.Getuid()
	for _, e := range ents {
		if !tmpHarnessName.MatchString(e.Name()) {
			continue
		}
		d := filepath.Join(base, e.Name())
		li, err := os.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			unmeasured = append(unmeasured, e.Name()+": "+pyErrText(err))
			continue
		}
		if sys, ok := li.Sys().(*syscall.Stat_t); !li.IsDir() || !ok || int(sys.Uid) != uid {
			continue
		}
		newest, err := harnessNewestMTime(d)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue // vanished while we looked
		case err != nil:
			unmeasured = append(unmeasured, e.Name()+": "+pyErrText(err)) // keep it, go on with the others
			continue
		}
		if t-newest >= tmpHarnessMinAgeS {
			old = append(old, d)
		}
	}
	if len(unmeasured) > 0 {
		kept := make([]any, 0, 20)
		for i, u := range unmeasured {
			if i == 20 {
				break
			}
			kept = append(kept, u)
		}
		if err := s.EventAs("tick", "tick", "tmp-harness", "tmp-harness-unmeasured",
			store.Ordered{{K: "count", V: len(unmeasured)}, {K: "kept", V: kept}}, ""); err != nil {
			return err
		}
	}
	if len(old) == 0 {
		return nil
	}
	roots := make([]string, len(old))
	for i, d := range old {
		roots[i] = pyRealpath(d)
	}
	var blocker []string
	held, ok, err := procHolds(procRoot(a), roots, tmpIgnoreComms, &blocker)
	if err != nil {
		return err
	}
	if !ok {
		first := "?:?"
		if len(blocker) > 0 {
			first = blocker[0]
		}
		pid, comm, _ := strings.Cut(first, ":")
		var kept int64
		for _, d := range old {
			kept += tmpTreeBytes(d)
		}
		return s.EventAs("tick", "tick", "tmp-harness", "tmp-harness-blocked",
			store.Ordered{{K: "pid", V: pid}, {K: "comm", V: comm}, {K: "kept", V: len(old)},
				{K: "mb", V: pyRound1(float64(kept) / tmpMB)}}, "")
	}
	n, busy := 0, 0
	var freed int64
	for _, d := range old {
		switch {
		case held[pyRealpath(d)]:
			busy++
		case n < tmpHarnessCap:
			size := tmpTreeBytes(d)
			if err := os.RemoveAll(d); err != nil {
				if eerr := s.EventAs("tick", "tick", "tmp-harness", "tmp-harness-failed", d+": "+pyErrText(err), ""); eerr != nil {
					return eerr
				}
				continue
			}
			freed += size
			n++
		}
	}
	if n > 0 {
		return s.EventAs("tick", "tick", "tmp-harness", "tmp-harness-swept",
			store.Ordered{{K: "count", V: n}, {K: "mb", V: pyRound1(float64(freed) / tmpMB)}, {K: "kept_busy", V: busy}}, "")
	}
	return nil
}
