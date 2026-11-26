package guard

// The guard1 replay (go/internal/factorytest/guard1/replay_test.go), run in-process. Every recorded call of a Python
// worker-guard test (testdata/replay/<module>.json: argv, payload, environment difference, cwd, the temp-dir fixture
// tree and the guard export) is fed to HookMain, and the exit code and stderr must equal what the Python guard
// answered (the only normalised difference is the "worker-guard(go)" prefix, as in tests/guard_impl.py).
//
// The environment and the working directory are process-wide, so a replayed test never calls t.Parallel: each call
// replaces the environment and cwd for the call and restores both afterwards, and resetHookState drops what HookMain
// cached, so every call starts as a fresh hook process would.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// rpEntry is one node of the fixture tree under temp dir R: type d (dir), f (file, base64 data) or l (symlink target).
type rpEntry struct {
	R int    `json:"r"`
	P string `json:"p"`
	T string `json:"t"`
	M uint32 `json:"m"`
	D string `json:"d"`
}

// rpCall is one recorded hook run.
type rpCall struct {
	Argv    []string          `json:"argv"`
	Payload string            `json:"payload"`
	Cwd     *string           `json:"cwd"`
	RC      int               `json:"rc"`
	Stderr  string            `json:"stderr"`
	Set     map[string]string `json:"set"`
	Unset   []string          `json:"unset"`
	Export  *string           `json:"export"`
	FS      *[]rpEntry        `json:"fs"`
}

var (
	rpCacheMu sync.Mutex
	rpCache   = map[string]map[string][]rpCall{}
)

// rpLoad returns the recorded calls of one Python module, keyed by the Python test id.
func rpLoad(tb testing.TB, module string) map[string][]rpCall {
	tb.Helper()
	rpCacheMu.Lock()
	defer rpCacheMu.Unlock()
	if m, ok := rpCache[module]; ok {
		return m
	}
	p := filepath.Join("testdata", "replay", module+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		tb.Fatalf("read %s: %v", p, err)
	}
	var m map[string][]rpCall
	if err := json.Unmarshal(b, &m); err != nil {
		tb.Fatalf("parse %s: %v", p, err)
	}
	rpCache[module] = m
	return m
}

// rpRepoRoot is the checkout root: this package is go/internal/guard.
func rpRepoRoot(tb testing.TB) string {
	tb.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		tb.Fatalf("find repo root: %v", err)
	}
	return root
}

// rpWorld is the fixture state of one replayed test: its temp dirs and the tokens that name them.
type rpWorld struct {
	t      testing.TB
	dirs   map[int]string
	root   string // repo root, the Python ROOT
	home   string // the passwd home of the user, the Python pwd home
	locked []string
}

var rpTokRe = regexp.MustCompile(`@T(\d+)@`)

func newRPWorld(t testing.TB) *rpWorld {
	t.Helper()
	home := os.Getenv("HOME")
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		home = u.HomeDir
	}
	return &rpWorld{t: t, dirs: map[int]string{}, root: rpRepoRoot(t), home: home}
}

func (w *rpWorld) dir(i int) string {
	if d, ok := w.dirs[i]; ok {
		return d
	}
	d, err := rpShortTempDir(w.t)
	if err != nil {
		w.t.Fatalf("make temp dir: %v", err)
	}
	w.dirs[i] = d
	return d
}

func (w *rpWorld) subst(s string) string {
	s = strings.ReplaceAll(s, "@ROOT@", w.root)
	s = strings.ReplaceAll(s, "@PWHOME@", w.home)
	return rpTokRe.ReplaceAllStringFunc(s, func(m string) string {
		var i int
		fmt.Sscanf(m, "@T%d@", &i)
		return w.dir(i)
	})
}

// apply makes the recorded fixture tree exist (it only adds or rewrites; Python tests never delete fixtures).
func (w *rpWorld) apply(es []rpEntry) {
	t := w.t
	t.Helper()
	for _, d := range w.locked { // a dir the previous apply locked must take new children
		os.Chmod(d, 0o755)
	}
	w.locked = nil
	var dirs []rpEntry
	for _, e := range es {
		p := filepath.Join(w.dir(e.R), e.P)
		switch e.T {
		case "d":
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatalf("make fixture dir %s: %v", p, err)
			}
			dirs = append(dirs, e)
		case "f":
			raw, err := base64.StdEncoding.DecodeString(e.D)
			if err != nil {
				t.Fatalf("decode fixture %s: %v", e.P, err)
			}
			os.Chmod(p, 0o644)
			if err := os.WriteFile(p, []byte(w.subst(string(raw))), 0o644); err != nil {
				t.Fatalf("write fixture %s: %v", p, err)
			}
			if err := os.Chmod(p, os.FileMode(e.M)); err != nil {
				t.Fatalf("chmod fixture %s: %v", p, err)
			}
		case "l":
			os.Remove(p)
			if err := os.Symlink(w.subst(e.D), p); err != nil {
				t.Fatalf("symlink fixture %s: %v", p, err)
			}
		}
	}
	sort.SliceStable(dirs, func(i, j int) bool { return len(dirs[i].P) > len(dirs[j].P) })
	for _, e := range dirs {
		p := filepath.Join(w.dir(e.R), e.P)
		if err := os.Chmod(p, os.FileMode(e.M)); err != nil {
			t.Fatalf("chmod fixture dir %s: %v", p, err)
		}
		if e.M&0o700 != 0o700 {
			w.locked = append(w.locked, p)
		}
	}
}

// rpStripEnv is the ambient environment a guard test must not inherit (the Python tests drop the same names).
var rpStripEnv = regexp.MustCompile(`^(WORKER_GUARD_.*|FACTORY_.*|GIT_[A-Z_0-9]*|SCRUB_[A-Z_]+|TAR_OPTIONS|POSIXLY_CORRECT|UNZIP.*|TAR_READER_OPTIONS)=`)

// environ is the environment of one call: the ambient one without the stripped names, a hermetic git config and an
// empty FACTORY_HOME, then the recorded unset and set (a recorded set wins).
func (w *rpWorld) environ(c rpCall) map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if rpStripEnv.MatchString(kv) {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	// tests/_gitenv.py: no user or system git config leaks into a fixture repo
	m["GIT_CONFIG_GLOBAL"], m["GIT_CONFIG_SYSTEM"], m["GIT_CONFIG_NOSYSTEM"] = os.DevNull, os.DevNull, "1"
	// hermetic: an empty FACTORY_HOME, so no live DB (and no public-text config) changes an answer
	m["FACTORY_HOME"] = w.dir(-1)
	for _, k := range c.Unset {
		delete(m, k)
	}
	for k, v := range c.Set {
		m[k] = w.subst(v)
	}
	return m
}

// seedExport writes <FACTORY_HOME>/run/guard-export.json with the recorded content and a fresh timestamp.
func (w *rpWorld) seedExport(c rpCall, env map[string]string) {
	t := w.t
	t.Helper()
	if c.Export == nil {
		return
	}
	home := env["FACTORY_HOME"]
	if home == "" {
		t.Fatalf("recorded export but no FACTORY_HOME in the recorded env")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(w.subst(*c.Export)), &doc); err != nil {
		t.Fatalf("parse recorded guard export: %v", err)
	}
	doc["generated_at"] = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal guard export: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o755); err != nil {
		t.Fatalf("make run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "guard-export.json"), b, 0o600); err != nil {
		t.Fatalf("write guard export: %v", err)
	}
}

type rpResult struct {
	rc     int
	stderr string
}

// rpRun calls HookMain with argv tail args (plus --py-hook, as the shim passes it), the environment env and the
// working directory dir ("" = the test's own), and restores the environment, the cwd and the hook state afterwards.
func rpRun(t testing.TB, root string, args []string, payload string, env map[string]string, dir string) rpResult {
	t.Helper()
	saved := os.Environ()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		os.Clearenv()
		for _, kv := range saved {
			k, v, _ := strings.Cut(kv, "=")
			os.Setenv(k, v)
		}
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd %s: %v", wd, err)
		}
		resetHookState()
	}()
	resetHookState()
	os.Clearenv()
	for k, v := range env {
		os.Setenv(k, v)
	}
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("chdir %s: %v", dir, err)
		}
	}
	hook := filepath.Join(root, "roles", "hooks", "worker-guard.py")
	argv := append(append([]string{}, args...), "--py-hook", hook)
	var so, se bytes.Buffer
	rc := HookMain(argv, strings.NewReader(payload), &so, &se)
	return rpResult{rc: rc, stderr: strings.ReplaceAll(se.String(), "worker-guard(go)", "worker-guard")}
}

// rpPythonOnPath reports whether python3 resolves on PATH: the Go guard hands an archive member list to the Python
// hook.
func rpPythonOnPath() bool {
	_, err := exec.LookPath("python3")
	return err == nil
}

// replay runs every recorded call of Python test id and requires the in-process Go guard to answer as the Python
// guard did. needPython marks a test whose calls include a tar/unzip extraction: the Go guard delegates the archive
// member list to the Python hook, so without python3 on PATH the test skips and says why.
func replay(t *testing.T, module, id string, needPython bool) {
	t.Helper()
	if needPython && !rpPythonOnPath() {
		t.Skip("python3 is not on PATH: the Go guard delegates an archive member list to the Python hook (T-1001); runs where python3 exists")
	}
	calls := rpLoad(t, module)[id]
	if len(calls) == 0 {
		t.Fatalf("testdata/replay/%s.json has no calls for %s; copy the recording again", module, id)
	}
	w := newRPWorld(t)
	bad := 0
	for i, c := range calls {
		if c.FS != nil {
			w.apply(*c.FS)
		}
		env := w.environ(c)
		w.seedExport(c, env)
		dir := ""
		if c.Cwd != nil {
			dir = w.subst(*c.Cwd)
		}
		got := rpRun(t, w.root, c.Argv, w.subst(c.Payload), env, dir)
		want := rpResult{rc: c.RC, stderr: w.subst(c.Stderr)}
		if got != want && bad < 10 {
			bad++
			t.Errorf("call %d of %s: go guard differs from python guard\n  argv: %v\n  payload: %s\n  python: exit %d stderr %q\n  go:     exit %d stderr %q",
				i, id, c.Argv, w.subst(c.Payload), want.rc, want.stderr, got.rc, got.stderr)
		}
	}
}

// rpShortTempDir makes a fixture dir with a short path, as the Python tests' tempfile.mkdtemp under /tmp does: the
// guard truncates a long command in its message, so a long temp path would change stderr, not just the fixture
// location.
func rpShortTempDir(t testing.TB) (string, error) {
	base := ""
	if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
		base = "/tmp"
	}
	d, err := os.MkdirTemp(base, "g1-")
	if err != nil {
		return "", fmt.Errorf("make fixture dir under %q: %w", base, err)
	}
	t.Cleanup(func() { // a mode-0 fixture dir would stop RemoveAll
		filepath.Walk(d, func(p string, fi os.FileInfo, err error) error {
			if fi != nil && fi.IsDir() {
				os.Chmod(p, 0o700)
			}
			return nil
		})
		os.RemoveAll(d)
	})
	return filepath.EvalSymlinks(d)
}
