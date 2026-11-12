package cli

import (
	"reflect"
	"testing"
)

// Expected values below are the output of Python's SPEC_TOKEN.findall / spec_files (bin/factory_cli.py 3377-3412) on
// the same input, quoted from python3.
func TestSpecTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"edit ./a/b.go now", []string{"edit", "./a/b.go", "now"}},
		{"see go/x/y.go.", []string{"see", "go/x/y.go."}},
		{"https://example.com/a/b.go and c.go", []string{"https", "and", "c.go"}},
		{"a.b.c", []string{"a.b.c"}},
		{"foo/ bar/baz.py", []string{"foo", "bar/baz.py"}},
		{"(go/a.go), [docs/x.md]", []string{"go/a.go", "docs/x.md"}},
		{"a/./b.go ../c.go", []string{"a/./b.go", "../c.go"}},
		{"x/y-z_w.txt:12", []string{"x/y-z_w.txt"}},
		{"naïve/ünï.go über.txt", []string{"naïve/ünï.go", "über.txt"}},
		{"--flag=a/b.c", []string{"--flag", "a/b.c"}},
		{"a//b.go", []string{"a"}},
		{"T-0001 a/b", []string{"T-0001", "a/b"}},
		{"key:val/a.go", []string{"key"}},
		{"/abs/path.go ~/x.go", nil},
		{"end/", []string{"end"}},
		{"snake_case.py, kebab-case.md;", []string{"snake_case.py", "kebab-case.md"}},
		{"a/b/c/d.e.f", []string{"a/b/c/d.e.f"}},
	} {
		if got := specTokens(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("specTokens(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSpecFiles(t *testing.T) {
	tracked := newTrackedSet([]string{"go/a.go", "go/sub/b.go", "bin/x.py", "README.md", "docs/y.md", "go/.hidden"})
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"edit ./go/a.go.", []string{"go/a.go"}},
		{"new go/sub/c.go and go/sub/d", []string{"go/sub/c.go"}},
		{"README.md, docs/y.md", []string{"README.md", "docs/y.md"}},
		{"go/sub/z_ and go/sub/q-", nil},
		{"go/a.go/..", nil},
		{"https://h/go/a.go", nil},
		{"bin/new.py bin/.rc go/.x", []string{"bin/new.py"}},
		{"go/sub/ok.go-", []string{"go/sub/ok.go"}},
		{"x/../go/a.go", nil},
		{"go//a.go", nil},
		{"docs/", nil},
		{"v1.2.3 go/sub/v1.2", []string{"go/sub/v1.2"}},
		{"go/sub/.git", nil},
		{"go/sub/ü.go", []string{"go/sub/ü.go"}},
	} {
		got := map[string]bool{}
		for f := range specFiles(tc.in, tracked) {
			got[f] = true
		}
		want := map[string]bool{}
		for _, f := range tc.want {
			want[f] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("specFiles(%q) = %v, want %v", tc.in, got, want)
		}
	}
	if got := specFiles("go/a.go", newTrackedSet(nil)); len(got) != 0 {
		t.Errorf("no tracked files must name nothing, got %v", got)
	}
}

func TestOverlayEnv(t *testing.T) {
	got := overlayEnv([]string{"A=1", "GIT_TERMINAL_PROMPT=1", "B=2"}, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	want := []string{"A=1", "B=2", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overlayEnv = %v, want %v", got, want)
	}
}
