package cli

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

// pyList reads a top-level `NAME = [...]` string list literal out of bin/factory_cli.py.
func pyList(t *testing.T, name string) []string {
	t.Helper()
	src, err := os.ReadFile(repoFile(t, "bin/factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^` + name + ` = \[([^\]]*)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("%s literal not found in bin/factory_cli.py", name)
	}
	var out []string
	for _, w := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		out = append(out, string(w[1]))
	}
	return out
}

func TestRolesMatchPython(t *testing.T) {
	if py := pyList(t, "ROLES"); !reflect.DeepEqual(py, factoryRoles) {
		t.Fatalf("factoryRoles %v differ from Python ROLES %v", factoryRoles, py)
	}
}

func TestTaskStatusesMatchPython(t *testing.T) {
	if py := pyList(t, "TASK_STATUSES"); !reflect.DeepEqual(py, taskStatuses) {
		t.Fatalf("taskStatuses %v differ from Python %v", taskStatuses, py)
	}
}

func TestGoalStatusesMatchPython(t *testing.T) {
	if py := pyList(t, "GOAL_STATUSES"); !reflect.DeepEqual(py, goalStatuses) {
		t.Fatalf("goalStatuses %v differ from Python %v", goalStatuses, py)
	}
}

func TestTaskWaitsMatchPython(t *testing.T) {
	if py := pyList(t, "TASK_WAITS"); !reflect.DeepEqual(py, taskWaits) {
		t.Fatalf("taskWaits %v differ from Python %v", taskWaits, py)
	}
}

func TestTaskIDRe(t *testing.T) {
	src, _ := os.ReadFile(repoFile(t, "bin/factory_cli.py"))
	if !regexp.MustCompile(`(?m)^TASK_ID_RE = re\.compile\(r"\^T-\\d\+\$", re\.I\)`).Match(src) {
		t.Fatal("TASK_ID_RE changed in bin/factory_cli.py; update taskIDRe")
	}
	for s, want := range map[string]bool{"T-1": true, "t-0042": true, "T-1\n": true, "T-": false, "T-1x": false, "X-1": false, " T-1": false, "T-1\n\n": false} {
		if taskIDRe.MatchString(s) != want {
			t.Errorf("taskIDRe(%q) = %v, want %v", s, !want, want)
		}
	}
}
