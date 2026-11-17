// Package guard1 ports the worker-guard behaviour tests, part 1 (T-1689), to Go.
//
// Every Python test below is replayed against the real Go hook binary (go/cmd/worker-guard). The expected value of a
// call is what the Python guard (roles/hooks/worker-guard.py) answered for the same input, recorded once from the
// Python test run into testdata/<module>.json: the argv, the payload, the environment difference, the working
// directory, the temp-dir fixture tree (files, symlinks, modes, git repos) and the guard export the Go guard reads in
// place of the DB. The replay recreates that world in a fresh temp dir, runs the binary and requires the same exit
// code and the same stderr (the only normalised difference is the "worker-guard(go)" prefix, as in tests/guard_impl.py).
package guard1

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// entry is one node of the fixture tree under temp dir R: type d (dir), f (file, base64 data) or l (symlink target).
type entry struct {
	R int    `json:"r"`
	P string `json:"p"`
	T string `json:"t"`
	M uint32 `json:"m"`
	D string `json:"d"`
}

// call is one recorded hook run.
type call struct {
	Argv    []string          `json:"argv"`
	Payload string            `json:"payload"`
	Cwd     *string           `json:"cwd"`
	RC      int               `json:"rc"`
	Stderr  string            `json:"stderr"`
	Set     map[string]string `json:"set"`
	Unset   []string          `json:"unset"`
	Export  *string           `json:"export"`
	FS      *[]entry          `json:"fs"`
}

var (
	buildOnce sync.Once
	guardBin  string
	buildErr  error
	cacheMu   sync.Mutex
	cache     = map[string]map[string][]call{}
)

// goTool is the go command of the toolchain running this test.
func goTool() string {
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "go", "bin", "go")
}

// bin builds go/cmd/worker-guard once per test binary into a temp dir (removed by TestMain) and returns its path.
func bin(tb testing.TB) string {
	tb.Helper()
	buildOnce.Do(func() {
		root, err := ft.ModuleRoot()
		if err != nil {
			buildErr = fmt.Errorf("find go module root: %w", err)
			return
		}
		dir, err := os.MkdirTemp("", "guard1-bin-")
		if err != nil {
			buildErr = fmt.Errorf("make build dir: %w", err)
			return
		}
		builtDirs = append(builtDirs, dir)
		out := filepath.Join(dir, "worker-guard")
		cmd := exec.Command(goTool(), "build", "-o", out, "./cmd/worker-guard")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/worker-guard: %w\n%s", err, b)
			return
		}
		guardBin = out
	})
	if buildErr != nil {
		tb.Fatalf("%v; fix the Go build first (a guard test never skips on a build failure)", buildErr)
	}
	return guardBin
}

var builtDirs []string

var slots = make(chan struct{}, 4)

func TestMain(m *testing.M) {
	code := m.Run()
	for _, d := range builtDirs {
		os.RemoveAll(d)
	}
	os.Exit(code)
}

// load returns the recorded calls of one Python module, keyed by the Python test id.
func load(tb testing.TB, module string) map[string][]call {
	tb.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if m, ok := cache[module]; ok {
		return m
	}
	p := filepath.Join("testdata", module+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		tb.Fatalf("read %s: %v", p, err)
	}
	var m map[string][]call
	if err := json.Unmarshal(b, &m); err != nil {
		tb.Fatalf("parse %s: %v", p, err)
	}
	cache[module] = m
	return m
}

// world is the fixture state of one replayed test: its temp dirs and the tokens that name them.
type world struct {
	t      testing.TB
	dirs   map[int]string
	root   string // repo root, the Python ROOT
	home   string // the passwd home of the user, the Python pwd home
	locked []string
}

var tokRe = regexp.MustCompile(`@T(\d+)@`)

func newWorld(t testing.TB) *world {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	home := os.Getenv("HOME")
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		home = u.HomeDir
	}
	w := &world{t: t, dirs: map[int]string{}, root: root, home: home}
	t.Cleanup(func() { // a mode-0 fixture dir would stop TempDir's removal
		for _, d := range w.dirs {
			filepath.Walk(d, func(p string, fi os.FileInfo, err error) error {
				if fi != nil && fi.IsDir() {
					os.Chmod(p, 0o700)
				}
				return nil
			})
		}
	})
	return w
}

func (w *world) dir(i int) string {
	if d, ok := w.dirs[i]; ok {
		return d
	}
	d, err := shortTempDir(w.t)
	if err != nil {
		w.t.Fatalf("make temp dir: %v", err)
	}
	w.dirs[i] = d
	return d
}

func (w *world) subst(s string) string {
	s = strings.ReplaceAll(s, "@ROOT@", w.root)
	s = strings.ReplaceAll(s, "@PWHOME@", w.home)
	return tokRe.ReplaceAllStringFunc(s, func(m string) string {
		var i int
		fmt.Sscanf(m, "@T%d@", &i)
		return w.dir(i)
	})
}

func (w *world) substB(b []byte) []byte { return []byte(w.subst(string(b))) }

// apply makes the recorded fixture tree exist (it only adds or rewrites; Python tests never delete fixtures).
func (w *world) apply(es []entry) {
	t := w.t
	t.Helper()
	for _, d := range w.locked { // a dir the previous apply locked must take new children
		os.Chmod(d, 0o755)
	}
	w.locked = nil
	var dirs []entry
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
			if err := os.WriteFile(p, w.substB(raw), 0o644); err != nil {
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

// stripEnv is the ambient environment a guard test must not inherit (the Python tests drop the same names).
var stripEnv = regexp.MustCompile(`^(WORKER_GUARD_.*|FACTORY_.*|GIT_[A-Z_0-9]*|SCRUB_[A-Z_]+|TAR_OPTIONS|POSIXLY_CORRECT|UNZIP.*|TAR_READER_OPTIONS)=`)

func (w *world) environ(c call) []string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if stripEnv.MatchString(kv) {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	// tests/_gitenv.py: no user or system git config leaks into a fixture repo
	m["GIT_CONFIG_GLOBAL"], m["GIT_CONFIG_SYSTEM"], m["GIT_CONFIG_NOSYSTEM"] = os.DevNull, os.DevNull, "1"
	for _, k := range c.Unset {
		delete(m, k)
	}
	for k, v := range c.Set {
		m[k] = w.subst(v)
	}
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// seedExport writes <FACTORY_HOME>/run/guard-export.json with the recorded content and a fresh timestamp.
func (w *world) seedExport(c call, env []string) {
	t := w.t
	t.Helper()
	if c.Export == nil {
		return
	}
	home := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "FACTORY_HOME="); ok {
			home = v
		}
	}
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

type result struct {
	rc     int
	stderr string
}

// run feeds payload to the hook binary with argv tail args, the way tests/guard_impl.py does in GUARD_IMPL=go mode.
func run(t testing.TB, args []string, payload string, env []string, dir string) result {
	t.Helper()
	hook := ""
	if root, err := ft.RepoRoot(); err == nil {
		hook = filepath.Join(root, "roles", "hooks", "worker-guard.py")
	}
	slots <- struct{}{} // at most 4 guard processes at once: the box is shared (factory rules: no heavy parallel jobs)
	defer func() { <-slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin(t), append(append([]string{}, args...), "--py-hook", hook)...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(payload)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	rc := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run worker-guard %v: %v", args, err)
		}
		rc = ee.ExitCode()
	}
	return result{rc: rc, stderr: strings.ReplaceAll(se.String(), "worker-guard(go)", "worker-guard")}
}

// pythonOnPath reports whether python3 resolves on PATH: the Go guard hands an archive member list to the Python hook.
func pythonOnPath() bool {
	_, err := exec.LookPath("python3")
	return err == nil
}

// replay runs every recorded call of Python test id and requires the Go guard to answer as the Python guard did.
// needPython marks a test whose calls include a tar/unzip extraction: the Go binary delegates the archive member list
// to the Python hook, so without python3 on PATH the test skips (like Env.NeedPython) and says why.
func replay(t *testing.T, module, id string, needPython bool) {
	t.Helper()
	if needPython && !pythonOnPath() {
		t.Skip("python3 is not on PATH: the Go guard delegates an archive member list to the Python hook (T-1001); runs where python3 exists")
	}
	t.Parallel()
	calls := load(t, module)[id]
	if len(calls) == 0 {
		t.Fatalf("testdata/%s.json has no calls for %s; regenerate the data", module, id)
	}
	w := newWorld(t)
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
		got := run(t, c.Argv, w.subst(c.Payload), env, dir)
		want := result{rc: c.RC, stderr: w.subst(c.Stderr)}
		if got != want && bad < 10 {
			bad++
			t.Errorf("call %d of %s: go guard differs from python guard\n  argv: %v\n  payload: %s\n  python: exit %d stderr %q\n  go:     exit %d stderr %q",
				i, id, c.Argv, w.subst(c.Payload), want.rc, want.stderr, got.rc, got.stderr)
		}
	}
}

// shortTempDir makes a fixture dir with a short path, as the Python tests' tempfile.mkdtemp under /tmp does: the guard
// truncates a long command in its message, so a long temp path would change stderr, not just the fixture location.
func shortTempDir(t testing.TB) (string, error) {
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
