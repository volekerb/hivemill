package tick

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for units 9 tmp_clean and 10 tmp_harness. Python's results for the Unicode and edge cases come from one-off
// `python3 -I` runs (re.fullmatch(r"(G-\d+)-(T-\d+)", "G-٣-T-٣") matches; the same with a trailing "\n" does not).
// The constants below are quoted with their Python file:line in units_tmp.go.

func TestTmpConstantsMatchPython(t *testing.T) {
	// bin/factory_cli.py:9146-9147 and 9595-9597.
	if tmpCleanThrottleS != 3600 || tmpCleanMinAgeS != 3600 || tmpHarnessThrottleS != 3600 || tmpHarnessMinAgeS != 3600 || tmpHarnessCap != 500 {
		t.Fatal("tmp_clean / tmp_harness thresholds drifted from Python")
	}
}

// --- tmp_clean ---------------------------------------------------------------------------------------------------

type cleanTest struct {
	*alertTest
	base string
}

func newCleanTest(t *testing.T) *cleanTest {
	at := newAlertTest(t)
	base := filepath.Join(os.Getenv("FACTORY_TMP_DIR"), fmt.Sprintf("claude-%d", os.Getuid()))
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	at.exec("INSERT OR IGNORE INTO goals(id,title,status,created,updated) VALUES('G-1','g','active','2026-11-12T00:00:00+00:00','2026-11-12T00:00:00+00:00')")
	return &cleanTest{at, base}
}

func (c *cleanTest) name(goal, task string) string {
	return nonAlnum.ReplaceAllString(filepath.Join(c.Home, "worktrees"), "-") + "-" + goal + "-" + task
}

func (c *cleanTest) task(goal, id, status string) {
	c.exec("INSERT INTO tasks(id,goal_id,title,status,created,updated) VALUES(?,?,?,?,?,?)", id, goal, id, status,
		"2026-11-12T00:00:00+00:00", "2026-11-12T00:00:00+00:00")
}

// pad makes a scratchpad dir with one 2000-byte file, both `age` old.
func (c *cleanTest) pad(name string, age time.Duration) string {
	d := filepath.Join(c.base, name)
	m := c.Now.Add(-age)
	c.mkfile(filepath.Join(d, "f"), 2000, m)
	if err := os.Chtimes(d, m, m); err != nil {
		c.T.Fatal(err)
	}
	return d
}

func (c *cleanTest) run() {
	c.T.Helper()
	if err := tmpCleanUnit(c.unit()); err != nil {
		c.T.Fatal(err)
	}
}

func TestTmpCleanAllowList(t *testing.T) {
	c := newCleanTest(t)
	c.task("G-1", "T-1", "done")
	c.task("G-1", "T-2", "cancelled")
	c.task("G-1", "T-3", "in_progress")
	c.task("G-1", "T-4", "done")
	c.task("G-1", "T-5", "done")
	c.task("G-1", "T-6", "done")
	c.agent("a4", "coder", "live", "T-4", "", "")
	c.agent("a5", "coder", "done", "T-5", "", "")
	c.agent("a6", "coder", "exited", "T-6", "", "2026-11-12T01:00:00+00:00")
	gone1 := c.pad(c.name("G-1", "T-1"), 2*time.Hour)
	gone2 := c.pad(c.name("G-1", "T-2"), 2*time.Hour)
	keepActive := c.pad(c.name("G-1", "T-3"), 2*time.Hour)
	keepLive := c.pad(c.name("G-1", "T-4"), 2*time.Hour)
	keepUnreaped := c.pad(c.name("G-1", "T-5"), 2*time.Hour)
	gone6 := c.pad(c.name("G-1", "T-6"), 2*time.Hour) // an exited agent no longer holds it
	keepYoung := c.pad(c.name("G-1", "T-7"), 59*time.Minute)
	c.task("G-1", "T-7", "done")
	keepHQ := c.pad("-home-x-hq", 5*time.Hour)                // the HQ scratchpad has no goal-task suffix
	keepOtherGoal := c.pad(c.name("G-2", "T-1"), 5*time.Hour) // goal does not match the task row
	keepSuffix := c.pad(c.name("G-1", "T-1")+"-qa", 5*time.Hour)
	keepUnknown := c.pad(c.name("G-1", "T-99"), 5*time.Hour)
	c.run()
	for _, p := range []string{gone1, gone2, gone6} {
		if pathExists(p) {
			t.Errorf("%s must be removed", p)
		}
	}
	for _, p := range []string{keepActive, keepLive, keepUnreaped, keepYoung, keepHQ, keepOtherGoal, keepSuffix, keepUnknown} {
		if !pathExists(p) {
			t.Errorf("%s must stay", p)
		}
	}
	ev := c.events("tmp-cleaned")
	if len(ev) != 3 {
		t.Fatalf("tmp-cleaned events %+v", ev)
	}
	if ev[0].Actor != "tick" || ev[0].Entity != "task" || ev[0].EntityID != "T-1" ||
		ev[0].Detail != fmt.Sprintf(`{"path": %q, "mb": 0.0}`, gone1) {
		t.Errorf("first event %+v", ev[0])
	}
	// at most once an hour: a second run with a new candidate does nothing
	again := c.pad(c.name("G-1", "T-8"), 5*time.Hour)
	c.task("G-1", "T-8", "done")
	c.run()
	if !pathExists(again) {
		t.Error("throttled run deleted")
	}
	c.Now = c.Now.Add(time.Hour)
	c.run()
	if pathExists(again) {
		t.Error("run after the throttle window must delete")
	}
}

// Symlinks: never followed out of the root, never deleted themselves. Python: d.is_symlink() -> skipped.
func TestTmpCleanAllowListSymlinks(t *testing.T) {
	c := newCleanTest(t)
	outside := t.TempDir()
	c.mkfile(filepath.Join(outside, "precious"), 10, c.Now.Add(-9*time.Hour))
	for i, name := range []string{"T-1", "T-2", "T-3", "T-4", "T-5"} {
		c.task("G-1", name, "done")
		_ = i
	}
	links := map[string]string{
		"T-1": outside,                                     // points outside the root
		"T-2": filepath.Join(c.base, "nowhere"),            // dangling
		"T-3": filepath.Join(c.base, "gone", "deeper"),     // chain through a missing component
		"T-4": filepath.Join(c.base, c.name("G-1", "T-4")), // loop l -> l
		"T-5": filepath.Join(c.base, c.name("G-1", "T-1")), // another link
	}
	for task, target := range links {
		if err := os.Symlink(target, filepath.Join(c.base, c.name("G-1", task))); err != nil {
			t.Fatal(err)
		}
	}
	c.run()
	for task := range links {
		if fi, err := os.Lstat(filepath.Join(c.base, c.name("G-1", task))); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink for %s must stay (err %v)", task, err)
		}
	}
	if !pathExists(filepath.Join(outside, "precious")) {
		t.Fatal("the target outside the root was deleted")
	}
	if n := len(c.events("tmp-cleaned")) + len(c.events("tmp-clean-failed")); n != 0 {
		t.Errorf("symlinks must be skipped silently, %d events", n)
	}
	// the root itself a symlink: nothing happens (Python: base.is_symlink())
	real := c.base + "-real"
	if err := os.Rename(c.base, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, c.base); err != nil {
		t.Fatal(err)
	}
	c.task("G-1", "T-9", "done")
	d := filepath.Join(real, c.name("G-1", "T-9"))
	c.mkfile(filepath.Join(d, "f"), 1, c.Now.Add(-9*time.Hour))
	os.Chtimes(d, c.Now.Add(-9*time.Hour), c.Now.Add(-9*time.Hour))
	c.Now = c.Now.Add(2 * time.Hour)
	c.run()
	if !pathExists(d) {
		t.Error("a symlinked claude-<uid> root must be left alone")
	}
}

// A symlink inside the swept scratchpad pointing outside is unlinked, its target stays (rmtree does not follow).
func TestTmpCleanDoesNotFollowInnerSymlink(t *testing.T) {
	c := newCleanTest(t)
	outside := t.TempDir()
	c.mkfile(filepath.Join(outside, "precious"), 10, c.Now)
	c.task("G-1", "T-1", "done")
	d := c.pad(c.name("G-1", "T-1"), 3*time.Hour)
	if err := os.Symlink(outside, filepath.Join(d, "l")); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(d, c.Now.Add(-3*time.Hour), c.Now.Add(-3*time.Hour))
	c.run()
	if pathExists(d) || !pathExists(filepath.Join(outside, "precious")) {
		t.Fatalf("scratchpad exists=%v, outside target exists=%v", pathExists(d), pathExists(filepath.Join(outside, "precious")))
	}
}

// Python's \d is Unicode: a scratchpad of G-<U+0663>-T-<U+0663> is deleted when the task row with that id is done.
func TestTmpCleanUnicodeDigits(t *testing.T) {
	c := newCleanTest(t)
	c.exec("INSERT INTO goals(id,title,status,created,updated) VALUES('G-٣','g','active','2026-11-12T00:00:00+00:00','2026-11-12T00:00:00+00:00')")
	c.task("G-٣", "T-٣", "done")
	d := c.pad(c.name("G-٣", "T-٣"), 3*time.Hour)
	// NBSP, \v and a trailing newline are not digits: never matched (Python fullmatch refuses "G-1-T-2\n")
	c.task("G-1", "T-2", "done")
	nl := c.pad(c.name("G-1", "T-2")+"\n", 3*time.Hour)
	nbsp := c.pad(c.name("G-1", "T-2 "), 3*time.Hour)
	vt := c.pad(c.name("G-1", "T-2\v"), 3*time.Hour)
	c.run()
	if pathExists(d) {
		t.Error("U+0663 digits match Python's \\d")
	}
	for _, p := range []string{nl, nbsp, vt} {
		if !pathExists(p) {
			t.Errorf("%q must stay", p)
		}
	}
}

// shutil.rmtree raises on a permission error: the run ends with tmp-clean-failed (str(e)), later candidates stay.
func TestTmpCleanFailureIsAnEvent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	c := newCleanTest(t)
	c.task("G-1", "T-1", "done")
	d := c.pad(c.name("G-1", "T-1"), 3*time.Hour)
	sub := filepath.Join(d, "sub")
	c.mkfile(filepath.Join(sub, "x"), 1, c.Now)
	os.Chtimes(d, c.Now.Add(-3*time.Hour), c.Now.Add(-3*time.Hour)) // creating sub touched it
	os.Chmod(sub, 0o500)
	t.Cleanup(func() { os.Chmod(sub, 0o755) })
	c.run()
	ev := c.events("tmp-clean-failed")
	if len(ev) != 1 || ev[0].Actor != "tick" || ev[0].Entity != "tick" || ev[0].EntityID != "tmp-clean" ||
		!strings.HasPrefix(ev[0].Detail, "[Errno 13] Permission denied: ") {
		t.Fatalf("failed events %+v", ev)
	}
	if !pathExists(d) {
		t.Error("a failed removal keeps the dir")
	}
}

// --- tmp_harness -------------------------------------------------------------------------------------------------

type harnessTest struct {
	*alertTest
	tmp, proc string
}

func newHarnessTest(t *testing.T) *harnessTest {
	at := newAlertTest(t)
	proc := t.TempDir()
	t.Setenv("FACTORY_SERVE_PROC_DIR", proc)
	return &harnessTest{at, os.Getenv("FACTORY_TMP_DIR"), proc}
}

func (h *harnessTest) home(name string, age time.Duration) string {
	d := filepath.Join(h.tmp, name)
	m := h.Now.Add(-age)
	h.mkfile(filepath.Join(d, "sub", "f"), 3000, m)
	os.Chtimes(filepath.Join(d, "sub"), m, m)
	os.Chtimes(d, m, m)
	return d
}

func (h *harnessTest) run() {
	h.T.Helper()
	if err := tmpHarnessUnit(h.unit()); err != nil {
		h.T.Fatal(err)
	}
}

func TestTmpHarnessAllowList(t *testing.T) {
	h := newHarnessTest(t)
	gone := h.home("zz-test-factory-abcd1234", 2*time.Hour)
	young := h.home("zz-test-factory-young001", 2*time.Hour)
	h.mkfile(filepath.Join(young, "sub", "new"), 1, h.Now.Add(-59*time.Minute)) // anything inside under 1 h keeps it
	other := h.home("zz-test-factory-abc", 2*time.Hour)                         // 3 chars, not 8
	other2 := h.home("zz-test-factory-abcd12345", 2*time.Hour)                  // 9 chars
	other3 := h.home("zz-test-other-abcd1234", 2*time.Hour)
	other4 := h.home("zz-test-factory-abcd-234", 2*time.Hour) // '-' is not in [A-Za-z0-9_]
	file := filepath.Join(h.tmp, "zz-test-factory-file0001")  // a plain file of the right name
	h.mkfile(file, 1, h.Now.Add(-5*time.Hour))
	h.run()
	if pathExists(gone) {
		t.Error("stale harness home must be removed")
	}
	for _, p := range []string{young, other, other2, other3, other4, file} {
		if !pathExists(p) {
			t.Errorf("%s must stay", p)
		}
	}
	ev := h.events("tmp-harness-swept")
	if len(ev) != 1 || ev[0].Actor != "tick" || ev[0].Entity != "tick" || ev[0].EntityID != "tmp-harness" ||
		ev[0].Detail != `{"count": 1, "mb": 0.0, "kept_busy": 0}` {
		t.Fatalf("swept events %+v", ev)
	}
	// throttled to once an hour
	again := h.home("zz-test-factory-again001", 3*time.Hour)
	h.run()
	if !pathExists(again) {
		t.Error("throttled run deleted")
	}
	h.Now = h.Now.Add(time.Hour)
	h.run()
	if pathExists(again) {
		t.Error("run after the window must delete")
	}
}

func TestTmpHarnessAllowListSymlinks(t *testing.T) {
	h := newHarnessTest(t)
	outside := t.TempDir()
	h.mkfile(filepath.Join(outside, "precious"), 10, h.Now.Add(-9*time.Hour))
	os.Chtimes(outside, h.Now.Add(-9*time.Hour), h.Now.Add(-9*time.Hour))
	names := []string{"zz-test-factory-link0001", "zz-test-factory-link0002", "zz-test-factory-link0003", "zz-test-factory-link0004"}
	targets := []string{outside, filepath.Join(h.tmp, "nowhere"), filepath.Join(h.tmp, "missing", "deeper"), filepath.Join(h.tmp, names[3])}
	for i, n := range names {
		if err := os.Symlink(targets[i], filepath.Join(h.tmp, n)); err != nil {
			t.Fatal(err)
		}
	}
	// a real stale home holding a link to the outside dir: the link goes, the target stays (not followed)
	d := h.home("zz-test-factory-real0001", 3*time.Hour)
	if err := os.Symlink(outside, filepath.Join(d, "sub", "out")); err != nil {
		t.Fatal(err)
	}
	m := h.Now.Add(-3 * time.Hour)
	// the link's own mtime counts too (lstat): age it with touch -h
	if out, err := exec.Command("touch", "-h", "-d", m.Format(time.RFC3339), filepath.Join(d, "sub", "out")).CombinedOutput(); err != nil {
		t.Fatalf("touch -h: %v %s", err, out)
	}
	os.Chtimes(filepath.Join(d, "sub"), m, m) // adding the link touched the dir
	h.run()
	for _, n := range names {
		if fi, err := os.Lstat(filepath.Join(h.tmp, n)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s must stay (err %v)", n, err)
		}
	}
	if !pathExists(filepath.Join(outside, "precious")) {
		t.Fatal("target outside the root was touched")
	}
	if pathExists(d) {
		t.Error("the real stale home must go")
	}
	if n := len(h.events("tmp-harness-failed")) + len(h.events("tmp-harness-unmeasured")); n != 0 {
		t.Errorf("symlinks must not raise events: %d", n)
	}
}

// One unreadable subdir keeps that home (tmp-harness-unmeasured) and the rest is still swept.
func TestTmpHarnessUnreadableEntry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	h := newHarnessTest(t)
	bad := h.home("zz-test-factory-badd0001", 3*time.Hour)
	locked := filepath.Join(bad, "sub", "locked")
	h.mkfile(filepath.Join(locked, "x"), 1, h.Now.Add(-3*time.Hour))
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	good := h.home("zz-test-factory-good0001", 3*time.Hour)
	h.run()
	if !pathExists(bad) || pathExists(good) {
		t.Fatalf("bad kept=%v good removed=%v", pathExists(bad), !pathExists(good))
	}
	ev := h.events("tmp-harness-unmeasured")
	want := fmt.Sprintf(`{"count": 1, "kept": ["zz-test-factory-badd0001: [Errno 13] Permission denied: '%s'"]}`, locked)
	if len(ev) != 1 || ev[0].Detail != want {
		t.Fatalf("unmeasured %+v want %s", ev, want)
	}
	if n := len(h.events("tmp-harness-swept")); n != 1 {
		t.Errorf("swept events %d", n)
	}
}

func TestTmpHarnessHeldByProcess(t *testing.T) {
	h := newHarnessTest(t)
	busy := h.home("zz-test-factory-busy0001", 3*time.Hour)
	free := h.home("zz-test-factory-free0001", 3*time.Hour)
	pid := filepath.Join(h.proc, "77")
	os.MkdirAll(filepath.Join(pid, "fd"), 0o755)
	os.Symlink(filepath.Join(busy, "sub"), filepath.Join(pid, "cwd")) // a cwd inside holds the home
	h.run()
	if !pathExists(busy) || pathExists(free) {
		t.Fatalf("busy kept=%v free removed=%v", pathExists(busy), !pathExists(free))
	}
	ev := h.events("tmp-harness-swept")
	if len(ev) != 1 || ev[0].Detail != `{"count": 1, "mb": 0.0, "kept_busy": 1}` {
		t.Fatalf("swept %+v", ev)
	}
}

func TestTmpHarnessBlockedByUnreadablePid(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read every fd directory")
	}
	h := newHarnessTest(t)
	d := h.home("zz-test-factory-blck0001", 3*time.Hour)
	fd := filepath.Join(h.proc, "4242", "fd")
	os.MkdirAll(fd, 0o755)
	os.Symlink("/", filepath.Join(h.proc, "4242", "cwd"))
	os.WriteFile(filepath.Join(h.proc, "4242", "comm"), []byte("weird\n"), 0o644)
	os.Chmod(fd, 0)
	t.Cleanup(func() { os.Chmod(fd, 0o755) })
	h.run()
	if !pathExists(d) {
		t.Fatal("a blocked run removes nothing")
	}
	ev := h.events("tmp-harness-blocked")
	if len(ev) != 1 || ev[0].Detail != `{"pid": "4242", "comm": "weird", "kept": 1, "mb": 0.0}` {
		t.Fatalf("blocked %+v", ev)
	}
}

// A failing proc listing is tmp-harness-failed with Python's str(OSError) text, never a stopped tick.
func TestTmpHarnessFailureIsAnEvent(t *testing.T) {
	h := newHarnessTest(t)
	h.home("zz-test-factory-fail0001", 3*time.Hour)
	t.Setenv("FACTORY_SERVE_PROC_DIR", filepath.Join(h.proc, "missing"))
	h.run()
	ev := h.events("tmp-harness-failed")
	if len(ev) != 1 || ev[0].Actor != "tick" {
		t.Fatalf("failed %+v", ev)
	}
}

// --- fuzz --------------------------------------------------------------------------------------------------------

func FuzzTmpUnitNames(f *testing.F) {
	for _, s := range []string{"zz-test-factory-abcd1234", "zz-test-factory-٣bcd123", "-home-x-worktrees-G-1-T-2", "G-٣-T-٣\n", "T-2\v", " "} {
		f.Add(s)
	}
	slug := tmpHarnessName
	f.Fuzz(func(t *testing.T, s string) {
		_ = slug.MatchString(s)
		_ = qaTaskID(s)
		_ = nonAlnum.ReplaceAllString(s, "-")
	})
}
