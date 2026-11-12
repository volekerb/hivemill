package cli

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// pyLiteral returns the quoted strings of the first `NAME = [ ... ]` or `NAME = ( ... )` literal in factory_cli.py.
func pyLiteral(t *testing.T, name string) []string {
	t.Helper()
	src, err := os.ReadFile(repoFile(t, "bin/factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^` + name + ` = [\[(]([^\])]*)[\])]`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("no %s literal in bin/factory_cli.py", name)
	}
	var out []string
	for _, q := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(m[1], -1) {
		out = append(out, string(q[1]))
	}
	return out
}

func TestLessonRolesMatchPython(t *testing.T) {
	if got := pyLiteral(t, "ROLES"); !reflect.DeepEqual(got, lessonRoles) || len(got) < 10 {
		t.Fatalf("lessonRoles %q drifted from ROLES %q", lessonRoles, got)
	}
}

func TestLessonStatusesMatchPython(t *testing.T) {
	if got := pyLiteral(t, "LESSON_STATUSES"); !reflect.DeepEqual(got, lessonStatuses) || len(got) != 3 {
		t.Fatalf("lessonStatuses %q drifted from LESSON_STATUSES %q", lessonStatuses, got)
	}
}

func TestParseLesson(t *testing.T) {
	for _, tc := range []struct {
		rest []string
		ok   bool
	}{
		{nil, true}, {[]string{"--role", "coder"}, true}, {[]string{"--status", "open", "--role", "all"}, true},
		{[]string{"--role=coder"}, false}, {[]string{"-h"}, false}, {[]string{"--role"}, false},
		{[]string{"--role", ""}, false}, {[]string{"--role", "-x"}, false}, {[]string{"L-001"}, false},
		{[]string{"--role", "a", "--role", "b"}, false}, {[]string{"--rol", "coder"}, false},
	} {
		if _, ok := parseLessonList(tc.rest); ok != tc.ok {
			t.Errorf("parseLessonList(%q) ok=%v, want %v", tc.rest, ok, tc.ok)
		}
	}
	for _, tc := range []struct {
		rest []string
		ok   bool
	}{
		{[]string{"L-001"}, true}, {nil, false}, {[]string{"L-1", "L-2"}, false}, {[]string{""}, false}, {[]string{"-h"}, false},
	} {
		if _, ok := parseLessonShow(tc.rest); ok != tc.ok {
			t.Errorf("parseLessonShow(%q) ok=%v, want %v", tc.rest, ok, tc.ok)
		}
	}
	if !strings.Contains(strings.Join(lessonRoles, ","), "retro") {
		t.Fatal("lessonRoles lost retro")
	}
}
