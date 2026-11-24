package skilleval2

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_skilleval_tasks.py (30 cases; see ../ported_skilleval2.txt). The eval tasks are the real files
// under <repo>/skilleval/tasks; check.py runs as `python3 check.py` in a work dir, like the Python module does.

var tasksExpected = []string{"add-feature", "fix-bug", "review-diff"}

const tasksReviewGood = "# Review\n\nVerdict: request changes\n\n1. `app/store.py:39`: SQL built with an f-string.\n"

// tasksCheckTimeout is the Python subprocess timeout=60 and the "secs <= 60" bound.
const tasksCheckTimeout = 60 * time.Second

func tasksDir(t testing.TB) string {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "skilleval", "tasks")
}

func tasksNeedPython(t testing.TB) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH: check.py of an eval task is a Python script; this test runs where python3 exists")
	}
	return py
}

// tasksOverlay copies the tree src over dst (shutil.copytree dirs_exist_ok); a missing src is a no-op like the
// Python (src.is_dir() is false).
func tasksOverlay(t testing.TB, src, dst string) {
	t.Helper()
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("overlay %s: %v", src, err)
	}
}

type tasksRun struct {
	Exit int
	Out  string
	Secs time.Duration
}

// tasksTrial builds a work dir (repo [+ solution] + extra files + hidden) and runs check.py in it.
func tasksTrial(t testing.TB, task string, withSolution bool, extra map[string]string) tasksRun {
	t.Helper()
	py := tasksNeedPython(t)
	td := filepath.Join(tasksDir(t), task)
	work := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	tasksOverlay(t, filepath.Join(td, "repo"), work)
	if withSolution {
		tasksOverlay(t, filepath.Join(td, "solution"), work)
	}
	for name, text := range extra {
		if err := os.WriteFile(filepath.Join(work, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tasksOverlay(t, filepath.Join(td, "hidden"), work)
	ctx, cancel := context.WithTimeout(context.Background(), tasksCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, filepath.Join(td, "check.py"))
	cmd.Dir = work
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	cmd.Env = []string{"PATH=" + path, "HOME=/nonexistent", "PYTHONDONTWRITEBYTECODE=1", "LANG=C.UTF-8"}
	var out bytes.Buffer
	cmd.Stdout = &out
	var errb bytes.Buffer
	cmd.Stderr = &errb
	start := time.Now()
	err := cmd.Run()
	secs := time.Since(start)
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run check.py of %s: %v (%s)", task, err, errb.String())
		}
		code = ee.ExitCode()
	}
	return tasksRun{Exit: code, Out: out.String(), Secs: secs}
}

func tasksReview(t testing.TB, text string) tasksRun {
	t.Helper()
	return tasksTrial(t, "review-diff", false, map[string]string{"REVIEW.md": text})
}

func tasksRefs(from, to int, format string) string {
	var b strings.Builder
	for n := from; n < to; n++ {
		b.WriteString(strings.ReplaceAll(format, "%d", strconv.Itoa(n)))
	}
	return b.String()
}

func tasksWantPass(t *testing.T, r tasksRun) {
	t.Helper()
	if r.Exit != 0 {
		t.Fatalf("exit %d, want 0\n%s", r.Exit, r.Out)
	}
}

func tasksWantFail(t *testing.T, r tasksRun, in string) {
	t.Helper()
	if r.Exit == 0 {
		t.Fatalf("exit 0, want non-zero\n%s", r.Out)
	}
	if in != "" && !strings.Contains(r.Out, in) {
		t.Fatalf("stdout %q does not contain %q", r.Out, in)
	}
}

func TestTasksExactTaskSetAndLayout(t *testing.T) {
	dir := tasksDir(t)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(tasksExpected, ",") {
		t.Fatalf("tasks %v, want %v", names, tasksExpected)
	}
	for _, task := range tasksExpected {
		for _, n := range []string{"task.md", "check.py", "meta.json"} {
			if st, err := os.Stat(filepath.Join(dir, task, n)); err != nil || !st.Mode().IsRegular() {
				t.Errorf("%s/%s is not a file", task, n)
			}
		}
		for _, n := range []string{"repo", "hidden", "solution"} {
			sub, err := os.ReadDir(filepath.Join(dir, task, n))
			if err != nil || len(sub) == 0 {
				t.Errorf("%s/%s is empty or unreadable: %v", task, n, err)
			}
		}
	}
}

func TestTasksMetaJSON(t *testing.T) {
	for _, task := range tasksExpected {
		t.Run(task, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(tasksDir(t), task, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			var meta map[string]any
			if err := dec.Decode(&meta); err != nil {
				t.Fatalf("meta.json: %v", err)
			}
			var keys []string
			for k := range meta {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != "id,role,timeout_s,title" {
				t.Fatalf("keys %v", keys)
			}
			if meta["id"] != task {
				t.Errorf("id %v, want %s", meta["id"], task)
			}
			if r := meta["role"]; r != "coder" && r != "reviewer" {
				t.Errorf("role %v", r)
			}
			if s, ok := meta["title"].(string); !ok || s == "" {
				t.Errorf("title %v", meta["title"])
			}
			n, ok := meta["timeout_s"].(json.Number)
			if !ok || strings.ContainsAny(n.String(), ".eE") {
				t.Fatalf("timeout_s %v is not an int", meta["timeout_s"])
			}
			if v, err := n.Int64(); err != nil || v <= 0 {
				t.Errorf("timeout_s %v must be > 0", n)
			}
		})
	}
}

func TestTasksTaskMdShortAndNeutral(t *testing.T) {
	for _, task := range tasksExpected {
		t.Run(task, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(tasksDir(t), task, "task.md"))
			if err != nil {
				t.Fatal(err)
			}
			text := string(b)
			lines := strings.Count(text, "\n")
			if text != "" && !strings.HasSuffix(text, "\n") {
				lines++
			}
			if lines > 60 {
				t.Errorf("task.md has %d lines, max 60", lines)
			}
			for _, w := range []string{"skill", "claude", "eval"} {
				if strings.Contains(strings.ToLower(text), w) {
					t.Errorf("task.md contains %q", w)
				}
			}
		})
	}
}

// tasksStdlib is the set of top-level stdlib modules a check.py may import (Python: sys.stdlib_module_names). The Go
// test has no interpreter to ask, so the allow-list is fixed; a module outside it fails the test with its name.
var tasksStdlib = map[string]bool{
	"__future__": true, "argparse": true, "ast": true, "base64": true, "collections": true, "contextlib": true,
	"copy": true, "csv": true, "dataclasses": true, "datetime": true, "difflib": true, "enum": true, "functools": true,
	"hashlib": true, "importlib": true, "inspect": true, "io": true, "itertools": true, "json": true, "math": true,
	"os": true, "pathlib": true, "re": true, "shlex": true, "shutil": true, "sqlite3": true, "string": true,
	"subprocess": true, "sys": true, "tempfile": true, "textwrap": true, "time": true, "types": true, "typing": true,
	"unittest": true, "uuid": true, "zlib": true,
}

var tasksNetwork = []string{"urllib", "socket", "http", "requests", "ftplib", "smtplib", "telnetlib", "ssl"}

// Python str patterns are Unicode: \s also matches \v, NBSP and the other Unicode spaces, \w is Unicode.
const tasksSpace = `[\s\v\p{Z}\x1c-\x1f\x85]`

var tasksImportRE = regexp.MustCompile(`(?m)^` + tasksSpace + `*(?:import|from)` + tasksSpace + `+([\p{L}\p{N}\p{M}_.]+)`)

func TestTasksCheckPyStdlibAndOffline(t *testing.T) {
	for _, task := range tasksExpected {
		t.Run(task, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(tasksDir(t), task, "check.py"))
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			for _, m := range tasksImportRE.FindAllStringSubmatch(src, -1) {
				top := strings.SplitN(m[1], ".", 2)[0]
				if !tasksStdlib[top] {
					t.Errorf("import %s is not in the stdlib allow-list", m[1])
				}
				for _, n := range tasksNetwork {
					if top == n {
						t.Errorf("import %s is a network module", m[1])
					}
				}
			}
			if regexp.MustCompile(`urllib|socket|http|requests`).MatchString(src) {
				t.Errorf("check.py mentions a network word")
			}
		})
	}
}

func TestTasksTemplateFailsAndSolutionPasses(t *testing.T) {
	for _, task := range tasksExpected {
		t.Run(task, func(t *testing.T) {
			r := tasksTrial(t, task, false, nil)
			if r.Exit == 0 {
				t.Errorf("the template passed:\n%s", r.Out)
			}
			if r.Secs > tasksCheckTimeout {
				t.Errorf("template check took %v", r.Secs)
			}
			r = tasksTrial(t, task, true, nil)
			if r.Exit != 0 {
				t.Errorf("the solution failed (exit %d):\n%s", r.Exit, r.Out)
			}
			if r.Secs > tasksCheckTimeout {
				t.Errorf("solution check took %v", r.Secs)
			}
		})
	}
}

func tasksFileNames(t testing.TB, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			out[d.Name()] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

func TestTasksHiddenNotVisibleInRepo(t *testing.T) {
	for _, task := range tasksExpected {
		t.Run(task, func(t *testing.T) {
			repo := tasksFileNames(t, filepath.Join(tasksDir(t), task, "repo"))
			for n := range tasksFileNames(t, filepath.Join(tasksDir(t), task, "hidden")) {
				if repo[n] {
					t.Errorf("hidden file %s is visible in repo/", n)
				}
			}
		})
	}
}

// Python: unittest discovery over tests/ never collects anything under a skilleval dir or test_taskdeps*/test_afterlist*.
// Go has no discovery to run, so the test walks tests/ for the files discovery would load (test_*.py) and checks the same two
// properties on their paths.
func TestTasksNotCollectedByTestsDiscovery(t *testing.T) {
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	err = filepath.WalkDir(filepath.Join(root, "tests"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "test_") || !strings.HasSuffix(d.Name(), ".py") {
			return nil
		}
		found++
		rel, _ := filepath.Rel(root, p)
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "skilleval" {
				t.Errorf("%s is under a skilleval dir", rel)
			}
		}
		if strings.HasPrefix(d.Name(), "test_taskdeps") || strings.HasPrefix(d.Name(), "test_afterlist") {
			t.Errorf("%s must not be collected", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no test_*.py found under tests/")
	}
}

func TestTasksReviewRightLocationPasses(t *testing.T) {
	tasksWantPass(t, tasksReview(t, tasksReviewGood))
}

func TestTasksReviewWithinTwoLinesPasses(t *testing.T) {
	tasksWantPass(t, tasksReview(t, strings.Replace(tasksReviewGood, ":39", ":41", 1)))
}

func TestTasksReviewWrongLineFails(t *testing.T) {
	tasksWantFail(t, tasksReview(t, strings.Replace(tasksReviewGood, ":39", ":42", 1)), "")
}

func TestTasksReviewWrongFileFails(t *testing.T) {
	tasksWantFail(t, tasksReview(t, strings.Replace(tasksReviewGood, "store.py", "handlers.py", 1)), "")
}

func TestTasksReviewMissingVerdictFails(t *testing.T) {
	tasksWantFail(t, tasksReview(t, strings.Replace(tasksReviewGood, "request changes", "approve", 1)), "")
}

func TestTasksReviewSixFindingsFails(t *testing.T) {
	tasksWantFail(t, tasksReview(t, tasksReviewGood+tasksRefs(2, 7, "%d. `app/util.py:%d`: nit.\n")), "6 findings")
}

func TestTasksReviewFiveFindingsPass(t *testing.T) {
	tasksWantPass(t, tasksReview(t, tasksReviewGood+tasksRefs(2, 6, "%d. `app/util.py:%d`: nit.\n")))
}

func TestTasksReviewApproveVerdictFailsDespiteProse(t *testing.T) {
	text := strings.Replace(tasksReviewGood, "request changes", "approve", 1) + "\nI would otherwise request changes.\n"
	tasksWantFail(t, tasksReview(t, text), "verdict")
}

func TestTasksReviewSixFindingsInAnyFormatFail(t *testing.T) {
	extra := "### app/util.py:1\napp/util.py:2 nit\n- app/util.py:3 nit\n* app/util.py:4\n2) app/util.py:5\n"
	tasksWantFail(t, tasksReview(t, tasksReviewGood+extra), "6 findings")
}

func TestTasksReviewSixRefsOnTwoLinesFail(t *testing.T) {
	extra := "app/util.py:1 app/util.py:2 app/util.py:3\napp/util.py:4 app/util.py:5\n"
	tasksWantFail(t, tasksReview(t, tasksReviewGood+extra), "6 findings")
}

func TestTasksReviewFiveRefsOnTwoLinesPass(t *testing.T) {
	tasksWantPass(t, tasksReview(t, tasksReviewGood+"app/util.py:1 app/util.py:2 app/util.py:3\napp/util.py:4\n"))
}

func TestTasksReviewRepeatedRefCountsOnce(t *testing.T) {
	tasksWantPass(t, tasksReview(t, tasksReviewGood+strings.Repeat("app/util.py:1 again app/util.py:1\n", 3)))
}

func TestTasksReviewNarrowRangePasses(t *testing.T) {
	tasksWantPass(t, tasksReview(t, strings.Replace(tasksReviewGood, ":39", ":37-40", 1)))
}

func TestTasksReviewWideRangeFails(t *testing.T) {
	tasksWantFail(t, tasksReview(t, strings.Replace(tasksReviewGood, ":39", ":1-100", 1)), "too wide")
}

func TestTasksReviewSixSpacedFindingsFail(t *testing.T) {
	tasksWantFail(t, tasksReview(t, tasksReviewGood+tasksRefs(2, 7, "%d. `app/util.py: %d`: nit.\n")), "6 findings")
}

func TestTasksReviewFiveMixedFormFindingsPass(t *testing.T) {
	tasksWantPass(t, tasksReview(t, tasksReviewGood+"app/util.py: 2, app/util.py:3, app/util.py: 4-5, app/util.py:6\n"))
}

func TestTasksReviewEquivalentRefFormsCountOnce(t *testing.T) {
	extra := "app/util.py:1 app/util.py: 1 app/util.py:1-1\n" + tasksRefs(2, 5, "app/util.py:%d\n")
	tasksWantPass(t, tasksReview(t, tasksReviewGood+extra))
}

func TestTasksReviewVersionProseNotARef(t *testing.T) {
	extra := tasksRefs(2, 6, "app/util.py:%d\n") + "Tested on Python 3.11: 2 runs, v1.2: 5 times, 3.11:2.\n"
	tasksWantPass(t, tasksReview(t, tasksReviewGood+extra))
}

func TestTasksReviewTwoSpaceRefNotARef(t *testing.T) {
	tasksWantPass(t, tasksReview(t, tasksReviewGood+tasksRefs(2, 6, "app/util.py:%d\n")+"app/util.py:  9\n"))
}

func TestTasksReviewTabOkNewlineNotARef(t *testing.T) {
	base := tasksRefs(2, 6, "app/util.py:%d\n")
	tasksWantFail(t, tasksReview(t, tasksReviewGood+base+"app/util.py:\t6\n"), "6 findings")
	tasksWantPass(t, tasksReview(t, tasksReviewGood+base+"app/util.py:\n6\napp/util.py:\n7\n"))
}

func TestTasksReviewSpacedDefectRefCredited(t *testing.T) {
	r := tasksReview(t, strings.Replace(tasksReviewGood, ":39", ": 39", 1))
	tasksWantPass(t, r)
	if !strings.Contains(r.Out, "PASS: defect at app/store.py:39 found") {
		t.Fatalf("stdout %q", r.Out)
	}
}

func TestTasksReviewSpacedWideRangeFails(t *testing.T) {
	tasksWantFail(t, tasksReview(t, strings.Replace(tasksReviewGood, ":39", ": 1-100", 1)), "too wide")
}

func TestTasksReviewNoReviewFails(t *testing.T) {
	tasksWantFail(t, tasksTrial(t, "review-diff", false, nil), "")
}
