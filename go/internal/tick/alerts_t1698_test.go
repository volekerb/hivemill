package tick

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// T-1698 sweeps (go-build orphans, stale factorytest-cache) as in the Python oracle.
func TestTmpSweepGobuildAndFactorytestCache(t *testing.T) {
	at := newAlertTest(t)
	tmp := os.Getenv("FACTORY_TMP_DIR")
	t.Setenv("FACTORY_SERVE_PROC_DIR", t.TempDir())
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,100000000")
	three := at.Now.Add(-3 * time.Hour)
	one := at.Now.Add(-time.Hour)
	touch := func(dir string, m time.Time) {
		at.mkfile(filepath.Join(dir, "x"), 10, m)
		os.Chtimes(dir, m, m)
	}
	touch(filepath.Join(tmp, "go-build123"), three) // swept: 3 h old
	touch(filepath.Join(tmp, "go-build456"), one)   // kept: fresh
	touch(filepath.Join(tmp, "go-build12x"), three) // kept: not all digits
	touch(filepath.Join(tmp, "go-build٣"), three)   // kept: non-ASCII digit
	cache := filepath.Join(tmp, "factorytest-cache-"+strconv.Itoa(os.Getuid()))
	touch(filepath.Join(cache, "aaa"), three)                                // swept
	touch(filepath.Join(cache, "bbb"), at.Now.Add(-4*time.Hour))             // swept
	touch(filepath.Join(cache, "ccc"), at.Now.Add(-2*time.Hour-time.Minute)) // swept: not the newest, over 2 h
	touch(filepath.Join(cache, "newest"), one)                               // kept: newest and young

	if err := remoteURLAlertUnit(at.unit()); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"go-build123", "factorytest-cache-" + strconv.Itoa(os.Getuid()) + "/aaa", "factorytest-cache-" + strconv.Itoa(os.Getuid()) + "/bbb", "factorytest-cache-" + strconv.Itoa(os.Getuid()) + "/ccc"} {
		if pathExists(filepath.Join(tmp, n)) {
			t.Errorf("%s should be removed", n)
		}
	}
	for _, n := range []string{"go-build456", "go-build12x", "go-build٣", "factorytest-cache-" + strconv.Itoa(os.Getuid()) + "/newest"} {
		if !pathExists(filepath.Join(tmp, n)) {
			t.Errorf("%s must be kept", n)
		}
	}
	ev := at.events("tmp-swept")
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, `"leftovers": 4`) {
		t.Fatalf("events %+v", ev)
	}
}

// The newest cache entry stays even when it is old (TMP_FTCACHE_KEEP).
func TestFactorytestCacheKeepsNewestWhenOld(t *testing.T) {
	at := newAlertTest(t)
	tmp := os.Getenv("FACTORY_TMP_DIR")
	t.Setenv("FACTORY_SERVE_PROC_DIR", t.TempDir())
	cache := filepath.Join(tmp, "factorytest-cache-"+strconv.Itoa(os.Getuid()))
	old := at.Now.Add(-10 * time.Hour)
	at.mkfile(filepath.Join(cache, "only", "x"), 1, old)
	os.Chtimes(filepath.Join(cache, "only"), old, old)
	a, closeDB, err := openAlert(at.unit())
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if got := sweepFactorytestCache(a, alertNow()); len(got) != 0 || !pathExists(filepath.Join(cache, "only")) {
		t.Fatalf("newest entry removed: %v", got)
	}
}

// macOS: tmp_usage names the fs type with `stat -f %T`, and /tmp is probed as /private/tmp.
func TestTmpUsageDarwinFsType(t *testing.T) {
	newAlertTest(t)
	stub := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'apfs %s\\n' \"$3\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldStat, oldOS := darwinStat, tmpGOOS
	darwinStat, tmpGOOS = stub, "darwin"
	t.Cleanup(func() { darwinStat, tmpGOOS = oldStat, oldOS })
	t.Setenv("FACTORY_TMP_STATVFS", "1000000000,100000000")
	u := tmpUsage()
	if u == nil || u.Pct != 90.0 || !strings.HasPrefix(u.FSType, "apfs ") {
		t.Fatalf("usage %+v", u)
	}
	if got := darwinFsType("/tmp"); got != "apfs /private/tmp" {
		t.Errorf("/tmp probed as %q", got)
	}
	darwinStat = filepath.Join(t.TempDir(), "missing")
	if got := darwinFsType("/x"); got != "" {
		t.Errorf("failed probe gave %q, want empty (None)", got)
	}
}

// Python fullmatch semantics of _GOBUILD_NAME (ASCII [0-9]+, no trailing newline allowed by Go's $ either).
func TestUnicodeGobuildName(t *testing.T) {
	for name, want := range map[string]bool{
		"go-build1": true, "go-build0123456789": true, "go-build": false, "go-build٣": false,
		"go-build1\n": false, "go-build1 ": false, "go-build1\v": false, " go-build1": false,
	} {
		if got := tmpGobuildName.MatchString(name); got != want {
			t.Errorf("%q: got %v want %v", name, got, want)
		}
	}
}

func FuzzTmpNames(f *testing.F) {
	for _, s := range []string{"go-build123", "go-build٣", "tmp.AbCdEfGhIj", "guard-diff-seed-abcd1234", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if tmpGobuildName.MatchString(s) && tmpLeftoverName.MatchString(s) {
			t.Fatalf("%q matches both name rules", s)
		}
	})
}
