package tick

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// G10 symlink cases for the /tmp walkers. Expected values come from one-off `python3 -I` runs on the same tree
// (os.walk(followlinks=False), os.lstat, os.path.realpath):
//   tree: dangling -> missing, chain -> nodir/x, l -> l, realink -> real, real/f (3 bytes)
//   lstat sizes: dangling 7, chain 7, l 1, realink 4, f 3 (the length of the link text)
//   realpath: D/dangling -> D/missing, D/chain -> D/nodir/x, D/l -> D/l, D/realink -> D/real, D/real/f -> D/real/f
//   _tree_bytes(l) = 0, _tree_bytes(dangling) = 0; _harness_newest_mtime(l | dangling) raises OSError (ELOOP / ENOENT)

func symlinkTree(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "real", "f"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"dangling": "missing", "chain": "nodir/x", "l": "l", "realink": "real"} {
		if err := os.Symlink(target, filepath.Join(d, name)); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestTmpTreeBytesSymlinks(t *testing.T) {
	d := symlinkTree(t)
	ri, err := os.Lstat(filepath.Join(d, "real"))
	if err != nil {
		t.Fatal(err)
	}
	// links are lstat'd, never followed: 7 + 7 + 1 + 4 + size(real) + 3
	if got, want := tmpTreeBytes(d), 7+7+1+4+ri.Size()+3; got != want {
		t.Errorf("tmpTreeBytes = %d, want %d", got, want)
	}
	for _, n := range []string{"l", "dangling", "chain"} {
		if got := tmpTreeBytes(filepath.Join(d, n)); got != 0 {
			t.Errorf("tmpTreeBytes(%s) = %d, Python's os.walk gives 0", n, got)
		}
	}
}

func TestHarnessNewestMTimeSymlinks(t *testing.T) {
	d := symlinkTree(t)
	old := time.Unix(1_700_000_000, 0)
	for _, p := range []string{"real/f", "real"} {
		if err := os.Chtimes(filepath.Join(d, p), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// the links are not followed: the walk of d succeeds and sees only lstat times
	if _, err := harnessNewestMTime(d); err != nil {
		t.Errorf("walk of a tree holding dangling, chained and looping links: %v", err)
	}
	for _, n := range []string{"l", "dangling", "chain"} {
		if _, err := harnessNewestMTime(filepath.Join(d, n)); err == nil {
			t.Errorf("harnessNewestMTime(%s): Python raises OSError, got nil", n)
		}
	}
}

func TestPyRealpathSymlinks(t *testing.T) {
	d, err := filepath.EvalSymlinks(symlinkTree(t))
	if err != nil {
		t.Fatal(err)
	}
	// re-create the tree under the resolved path (t.TempDir may itself sit behind a link)
	d = filepath.Clean(d)
	for _, c := range [][2]string{
		{"dangling", "missing"}, {"chain", "nodir/x"}, {"l", "l"}, {"realink", "real"}, {"real/f", "real/f"},
	} {
		if got, want := pyRealpath(filepath.Join(d, c[0])), filepath.Join(d, c[1]); got != want {
			t.Errorf("pyRealpath(%s) = %s, want %s", c[0], got, want)
		}
	}
}

// A symlinked FACTORY_TMP_DIR is refused by both sweeps as in Python (is_symlink, or not is_dir): no candidates, no error.
func TestTmpSweepRefusesSymlinkedRoot(t *testing.T) {
	at := newAlertTest(t)
	d := symlinkTree(t)
	for _, n := range []string{"realink", "dangling", "chain", "l"} {
		t.Run(n, func(t *testing.T) {
			t.Setenv("FACTORY_TMP_DIR", filepath.Join(d, n))
			c, err := sweepCandidates(at.A.S, at.Home, alertNow())
			if err != nil || len(c) != 0 {
				t.Errorf("sweepCandidates = %v, %v", c, err)
			}
			r, locked, blocked, err := sweepLeftovers(at.A, alertNow())
			if err != nil || len(r) != 0 || locked != 0 || blocked {
				t.Errorf("sweepLeftovers = %v %d %v %v", r, locked, blocked, err)
			}
		})
	}
	if !exists(filepath.Join(d, "real", "f")) {
		t.Error("a sweep followed a link and removed the target")
	}
}

// A leftover-shaped entry that is a link (dangling, looping, or to a directory) is neither dir nor file: kept, and the
// target survives.
func TestTmpSweepKeepsLinkedLeftovers(t *testing.T) {
	at := newAlertTest(t)
	tmp := os.Getenv("FACTORY_TMP_DIR")
	t.Setenv("FACTORY_SERVE_PROC_DIR", t.TempDir())
	target := t.TempDir()
	at.mkfile(filepath.Join(target, "keep"), 5, at.Now.Add(-48*time.Hour))
	for name, tgt := range map[string]string{
		"guard-diff-dangl001": filepath.Join(tmp, "missing"),
		"freshbox-chain001":   filepath.Join(tmp, "nodir", "x"),
		"guard-diff-loop0001": filepath.Join(tmp, "guard-diff-loop0001"),
		"freshbox-dirlk001":   target,
	} {
		if err := os.Symlink(tgt, filepath.Join(tmp, name)); err != nil {
			t.Fatal(err)
		}
	}
	r, locked, blocked, err := sweepLeftovers(at.A, alertNow())
	if err != nil || len(r) != 0 || blocked {
		t.Fatalf("sweepLeftovers = %v %d %v %v", r, locked, blocked, err)
	}
	for _, n := range []string{"guard-diff-dangl001", "freshbox-chain001", "guard-diff-loop0001", "freshbox-dirlk001"} {
		if !exists(filepath.Join(tmp, n)) {
			t.Errorf("link %s was removed", n)
		}
	}
	if !exists(filepath.Join(target, "keep")) {
		t.Error("the link target was removed")
	}
}
