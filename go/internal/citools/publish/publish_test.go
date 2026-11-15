package publish

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/go/internal/citools/freshexport"
	_ "modernc.org/sqlite"
)

const termsEnv = "SCRUB_" + "TERMS_FILE"

// pyWrap runs a ci/*.py script with the passwd home faked, so the reference never reads the live home.
const pyWrap = `import sys, runpy, pwd
fake, script = sys.argv[1], sys.argv[2]
sys.argv = [script] + sys.argv[3:]
orig = pwd.getpwuid
def getpwuid(uid):
    e = orig(uid)
    return pwd.struct_passwd((e.pw_name, e.pw_passwd, e.pw_uid, e.pw_gid, e.pw_gecos, fake, e.pw_shell))
pwd.getpwuid = getpwuid
runpy.run_path(script, run_name="__main__")
`

type env struct{ root, ci, home string }

func newEnv(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	e := env{root, filepath.Join(root, "ci"), filepath.Join(root, "home")}
	src, err := filepath.Abs("../../../../ci")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.ci, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"publish_repo.py", "fresh_export.py", "scrub_scan.py", "scrub_allowlist.txt", "publish_metadata.json"} {
		data, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Skipf("ci/%s not readable (%v): parity not checked", f, err)
		}
		if err := os.WriteFile(filepath.Join(e.ci, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(e.home, 0o755); err != nil {
		t.Fatal(err)
	}
	freshexport.HomeOverride = e.home
	t.Cleanup(func() { freshexport.HomeOverride = "" })
	return e
}

type result struct {
	rc  int
	out string
	err string
}

func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, termsEnv+"=") {
			env = append(env, kv)
		}
	}
	return env
}

func (e env) python(t *testing.T, args ...string) result {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found: parity with ci/publish_repo.py not checked")
	}
	cmd := exec.Command(py, append([]string{"-I", "-c", pyWrap, e.home, filepath.Join(e.ci, "publish_repo.py")}, args...)...)
	cmd.Env = cleanEnv()
	var o, er bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &er
	rc := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		rc = ee.ExitCode()
	}
	return result{rc, o.String(), er.String()}
}

func (e env) goRun(t *testing.T, args ...string) result {
	t.Helper()
	t.Setenv(termsEnv, "")
	var o, er bytes.Buffer
	rc := Main(append([]string{"--ci-dir", e.ci}, args...), &o, &er)
	return result{rc, o.String(), er.String()}
}

// paths are the `gh api -X METHOD PATH` lines: what would be called.
func paths(out string) []string {
	var p []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "gh api ") {
			p = append(p, l)
		}
	}
	return p
}

// TestPublishDryRunParity: the Go dry run lists the same calls, with the same bodies, as publish_repo.py --dry-run.
func TestPublishDryRunParity(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"--repo", "example-org/hivemill"},
		{"--repo", "Example-Org/hive_mill.git"},
		{"--repo", "example-org/hivemill", "--private-repo", "someone/else"},
	} {
		want, got := e.python(t, args...), e.goRun(t, args...)
		if want.rc != 0 || want != got {
			t.Fatalf("args %v differ\npython: %+v\ngo:     %+v", args, want, got)
		}
		if n := len(paths(got.out)); n != 6 {
			t.Fatalf("want 6 calls, got %d:\n%s", n, got.out)
		}
	}
	got := e.goRun(t, "--repo", "example-org/hivemill")
	if !strings.Contains(got.out, "DRY RUN for example-org/hivemill; nothing is called") || !strings.Contains(got.out, "branches/main/protection") {
		t.Errorf("dry run text: %s", got.out)
	}
}

func TestRefusalsMatchPython(t *testing.T) {
	e := newEnv(t)
	for name, args := range map[string][]string{
		"not a slug":         {"--repo", "justaname"},
		"two slashes":        {"--repo", "a/b/c"},
		"private repo":       {"--repo", "acme/private", "--private-repo", "ACME/private"},
		"private repo .git":  {"--repo", "acme/private", "--private-repo", "acme/private.git"},
		"bad private repo":   {"--repo", "acme/ok", "--private-repo", "bad"},
		"metadata not found": {"--repo", "acme/ok", "--metadata", "/nonexistent/m.json"},
	} {
		t.Run(name, func(t *testing.T) {
			want, got := e.python(t, args...), e.goRun(t, args...)
			if got.rc != 2 || want.rc != 2 || strings.TrimSpace(want.err) != strings.TrimSpace(got.err) && !strings.Contains(name, "metadata") {
				t.Fatalf("python %+v go %+v", want, got)
			}
			if strings.Contains(got.out, "gh api") {
				t.Errorf("a refusal must not print calls: %s", got.out)
			}
		})
	}
}

func TestFrozenReposAndMetadataSlugRefused(t *testing.T) {
	e := newEnv(t)
	dbDir := filepath.Join(e.home, "factory", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dbDir, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)", "INSERT INTO config VALUES ('frozen_repos', 'acme/frozen, acme/frozen2')"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	want, got := e.python(t, "--repo", "ACME/Frozen2"), e.goRun(t, "--repo", "ACME/Frozen2")
	if got.rc != 2 || want != got || !strings.Contains(got.err, "is the private repository") {
		t.Fatalf("python %+v go %+v", want, got)
	}
	// metadata that still names the private slug is refused
	meta := filepath.Join(e.root, "meta.json")
	body := `{"description": "mirror of acme/frozen here", "homepage": "h", "topics": ["a"], "required_checks": ["c"]}`
	if err := os.WriteFile(meta, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want, got = e.python(t, "--repo", "example-org/new", "--metadata", meta), e.goRun(t, "--repo", "example-org/new", "--metadata", meta)
	if got.rc != 2 || want != got || !strings.Contains(got.err, "still contains the private repository slug") {
		t.Fatalf("python %+v go %+v", want, got)
	}
	// a longer name is not the slug
	body = strings.ReplaceAll(body, "acme/frozen here", "acme/frozen-other here")
	if err := os.WriteFile(meta, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want, got = e.python(t, "--repo", "example-org/new", "--metadata", meta), e.goRun(t, "--repo", "example-org/new", "--metadata", meta)
	if got.rc != 0 || want != got {
		t.Fatalf("python %+v go %+v", want, got)
	}
}

func TestScrubHitInMetadataRefused(t *testing.T) {
	e := newEnv(t)
	meta := filepath.Join(e.root, "meta.json")
	body := `{"description": "run on 192.168.` + `1.5", "homepage": "h", "topics": ["a"], "required_checks": ["c"]}`
	if err := os.WriteFile(meta, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want, got := e.python(t, "--repo", "example-org/new", "--metadata", meta), e.goRun(t, "--repo", "example-org/new", "--metadata", meta)
	if got.rc != 2 || !strings.Contains(got.err, "scrub-scan hits") || strings.Contains(got.out, "gh api") {
		t.Fatalf("python %+v go %+v", want, got)
	}
}

// stub is a fake gh: it logs each call and its stdin; $STUB_FAIL_ON makes the call with that path fail.
func stub(t *testing.T, e env, getAnswer string) (string, string) {
	t.Helper()
	log := filepath.Join(e.root, "calls.log")
	script := "#!/bin/sh\nbody=$(cat)\necho \"$@ :: $body\" >> '" + log + "'\n" +
		"case \"$*\" in *STUBFAIL*) echo boom >&2; exit 4;; esac\n" +
		"if [ \"$3\" = GET ]; then echo '" + getAnswer + "'; fi\n"
	p := filepath.Join(e.root, "gh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p, log
}

func TestApplyRunsCallsInOrderAndStopsAtFailure(t *testing.T) {
	e := newEnv(t)
	gh, log := stub(t, e, `{"private": false, "fork": false}`)
	got := e.goRun(t, "--repo", "example-org/hivemill", "--apply", "--gh", gh)
	if got.rc != 0 {
		t.Fatalf("%+v", got)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "api -X GET repos/example-org/hivemill ::") ||
		!strings.Contains(lines[5], "branches/main/protection --input - :: {\"required_status_checks\"") {
		t.Fatalf("calls: %q", lines)
	}
	if !strings.Contains(got.out, "ok: GET repos/example-org/hivemill\nok: PATCH") || !strings.HasSuffix(got.out, "Social preview.\n") {
		t.Errorf("stdout: %s", got.out)
	}
	// the Go calls equal the Python ones, stdin included
	pyLog := filepath.Join(e.root, "py.log")
	os.Remove(log)
	if r := e.python(t, "--repo", "example-org/hivemill", "--apply", "--gh", gh); r.rc != 0 {
		t.Fatalf("python: %+v", r)
	}
	pyCalls, _ := os.ReadFile(log)
	os.WriteFile(pyLog, pyCalls, 0o644)
	if string(pyCalls) != string(calls) {
		t.Fatalf("calls differ\npython:\n%s\ngo:\n%s", pyCalls, calls)
	}
}

func TestApplyRefusesPrivateOrForkAndReportsFailure(t *testing.T) {
	e := newEnv(t)
	for _, answer := range []string{`{"private": true, "fork": false}`, `{"private": false, "fork": true}`} {
		gh, log := stub(t, e, answer)
		got := e.goRun(t, "--repo", "example-org/hivemill", "--apply", "--gh", gh)
		if got.rc != 2 || !strings.Contains(got.err, "private or a fork") {
			t.Fatalf("%s: %+v", answer, got)
		}
		if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 1 {
			t.Fatalf("only the GET may run: %s", calls)
		}
		os.Remove(log)
	}
	gh, _ := stub(t, e, `not json`)
	if got := e.goRun(t, "--repo", "example-org/hivemill", "--apply", "--gh", gh); got.rc != 1 || !strings.Contains(got.err, "cannot read private/fork") {
		t.Fatalf("%+v", got)
	}
	gh, log := stub(t, e, `{"private": false, "fork": false}`)
	got := e.goRun(t, "--repo", "STUBFAIL/x", "--apply", "--gh", gh)
	if got.rc != 1 || !strings.Contains(got.err, "FAILED GET repos/STUBFAIL/x (exit 4): boom") || !strings.Contains(got.err, "calls already made: none") {
		t.Fatalf("%+v", got)
	}
	_ = log
	if got := e.goRun(t, "--repo", "example-org/hivemill", "--apply", "--gh", filepath.Join(e.root, "no-such-gh")); got.rc != 1 || !strings.Contains(got.err, "exit 127") {
		t.Fatalf("%+v", got)
	}
}
