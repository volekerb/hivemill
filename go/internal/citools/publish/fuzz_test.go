package publish

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseSlug: no input panics and an accepted slug has a non-empty owner and name.
func FuzzParseSlug(f *testing.F) {
	for _, s := range []string{"a/b", "a/b.git", "a/.git", "a/b/c", "", "é/b", "a/b\n", "a b/c", "/", "a/.."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if o, n, err := parseSlug(s, "--repo"); err == nil && (o == "" || n == "") {
			t.Fatalf("parseSlug(%q) accepted an empty part: %q/%q", s, o, n)
		}
	})
}

// FuzzLoadMetadata: arbitrary file content never panics; an accepted file has non-empty topics and checks.
func FuzzLoadMetadata(f *testing.F) {
	f.Add(`{"description":"d","homepage":"h","topics":["t"],"required_checks":["c"]}`)
	f.Add(`{"description":1}`)
	f.Add(`[]`)
	f.Add("")
	f.Add("null")
	f.Fuzz(func(t *testing.T, data string) {
		p := filepath.Join(t.TempDir(), "m.json")
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if m, err := LoadMetadata(p); err == nil && (len(m.Topics) == 0 || len(m.RequiredChecks) == 0) {
			t.Fatalf("LoadMetadata accepted %q with empty lists", data)
		}
	})
}
