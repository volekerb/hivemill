package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// regenFiles maps every regular file under root (slash-separated relative path) to its bytes.
func regenFiles(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	return out, err
}

// regenDiff lists every file that differs between want (checked in) and got (regenerated), naming case dir and file.
func regenDiff(want, got map[string][]byte) []string {
	var names []string
	seen := map[string]bool{}
	for n := range want {
		seen[n] = true
		names = append(names, n)
	}
	for n := range got {
		if !seen[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var diffs []string
	for _, n := range names {
		w, inWant := want[n]
		g, inGot := got[n]
		dir, file := filepath.Split(n)
		dir = filepath.Clean(dir)
		switch {
		case !inGot:
			diffs = append(diffs, fmt.Sprintf("case %s: file %s is checked in but python3 no longer produces it", dir, file))
		case !inWant:
			diffs = append(diffs, fmt.Sprintf("case %s: file %s is produced by python3 but not checked in", dir, file))
		case !bytes.Equal(w, g):
			diffs = append(diffs, fmt.Sprintf("case %s: file %s differs from the python3 regeneration", dir, file))
		}
	}
	return diffs
}

// TestGoldenRegenDiff: the comparison names the case dir and file, and sees added and removed files.
func TestGoldenRegenDiff(t *testing.T) {
	want := map[string][]byte{"s1/a/stdout": []byte("x\n"), "s1/a/code": []byte("0\n"), "s1/b/calls": []byte("c")}
	got := map[string][]byte{"s1/a/stdout": []byte("y\n"), "s1/a/code": []byte("0\n"), "s1/b/extra": []byte("e")}
	d := regenDiff(want, got)
	if len(d) != 3 {
		t.Fatalf("want 3 diffs, got %q", d)
	}
	for i, sub := range []string{"case s1/a: file stdout differs", "case s1/b: file calls is checked in", "case s1/b: file extra is produced"} {
		if !bytes.Contains([]byte(d[i]), []byte(sub)) {
			t.Errorf("diff %d = %q, want it to contain %q", i, d[i], sub)
		}
	}
	if d := regenDiff(want, want); len(d) != 0 {
		t.Fatalf("identical trees must not differ, got %q", d)
	}
}
