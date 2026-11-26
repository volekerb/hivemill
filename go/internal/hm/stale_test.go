package hm

import "testing"

func TestBehindSafe(t *testing.T) {
	cases := []struct {
		name     string
		main, pr []string
		want     bool
	}{
		{"disjoint files", []string{"a.go", "docs/x.md"}, []string{"b.go"}, true},
		{"nothing on main", nil, []string{"b.go"}, true},
		{"shared file", []string{"a.go"}, []string{"a.go", "b.go"}, false},
		{"only CHANGELOG.md shared", []string{"CHANGELOG.md", "a.go"}, []string{"CHANGELOG.md", "b.go"}, true},
		{"CHANGELOG.md and a code file shared", []string{"CHANGELOG.md", "a.go"}, []string{"CHANGELOG.md", "a.go"}, false},
		{"workflow changed on main", []string{".github/workflows/tests.yml"}, []string{"b.go"}, false},
		{"other workflow changed on main", []string{".github/workflows/install.yml"}, []string{"b.go"}, false},
	}
	for _, c := range cases {
		if got := behindSafe(c.main, c.pr); got != c.want {
			t.Errorf("%s: behindSafe = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRefreshSlotIsSerial(t *testing.T) {
	r, s := testRunner(t, "")
	a := &Run{Task: "T-0001", Repo: "zz-test/Repo", State: Merging}
	b := &Run{Task: "T-0002", Repo: "zz-test/repo", State: Merging}
	for _, x := range []*Run{a, b} {
		if err := s.PutRun(x); err != nil {
			t.Fatal(err)
		}
	}
	if h := r.refreshHolder(a); h != "" {
		t.Fatalf("free slot: a got holder %q", h)
	}
	if h := r.refreshHolder(b); h != "T-0001" {
		t.Fatalf("b while a merges: holder %q, want T-0001", h)
	}
	if h := r.refreshHolder(a); h != "" {
		t.Fatalf("the holder refreshes again: got %q", h)
	}
	a.State = Merged
	if err := s.PutRun(a); err != nil {
		t.Fatal(err)
	}
	if h := r.refreshHolder(b); h != "" {
		t.Fatalf("a merged: b got holder %q", h)
	}
	if h := r.refreshHolder(&Run{Task: "T-0003", Repo: "zz-test/other", State: Merging}); h != "" {
		t.Fatalf("another repo has its own slot: %q", h)
	}
}
