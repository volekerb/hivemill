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
		{"workflow changed on main", []string{".github/workflows/tests.yml"}, []string{"b.go"}, false},
		{"other workflow changed on main", []string{".github/workflows/install.yml"}, []string{"b.go"}, false},
	}
	for _, c := range cases {
		if got := behindSafe(c.main, c.pr); got != c.want {
			t.Errorf("%s: behindSafe = %v, want %v", c.name, got, c.want)
		}
	}
}
