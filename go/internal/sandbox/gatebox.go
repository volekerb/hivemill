package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/shlex"
)

// Gate sandbox profile (T-0632, GS1): run the merge gate's command under the allow-list view. $HOME,
// $XDG_RUNTIME_DIR, /tmp, /dev/shm and /var/tmp are empty tmpfs, FACTORY_MAIN and the repo's git common dir are
// read-only, FACTORY_MAIN's state dirs are masked, and only the gate tree and <wt_root>/{reports,factory-home,tmp}
// are writable. Own net namespace (lo only), own pid namespace, capabilities dropped. If user namespaces are
// unavailable it fails with a GateboxError: the caller must never fall back to running the command unsandboxed.

// Gate profile constants.
var (
	Masked      = []string{"db", "run", "logs", "worktrees", "exports", "state"}
	HideTmp     = []string{"/tmp", "/dev/shm", "/var/tmp"}
	GateEnvPass = []string{"PATH", "LANG", "TERM", "FACTORY_HOME", "FACTORY_MAIN", "FACTORY_TEST_SLOT_HELD", "GATE_SHARDS", "GATE_REPORT_DIR"} // plus LC_*
	GateTmpfs   = map[string]string{"/tmp": "64m", "/dev/shm": "16m", "/var/tmp": "16m"}
)

// GateboxError means the sandbox cannot be built or started; the command was not run.
type GateboxError struct{ Msg string }

func (e *GateboxError) Error() string { return e.Msg }

func gerr(format string, a ...any) error { return &GateboxError{fmt.Sprintf(format, a...)} }

func realDir(path, what string) (string, error) {
	p := Realpath(path)
	if !isDir(p) {
		return "", gerr("gatebox: %s %s is not a directory; fix the gate config, the command was not run", what, path)
	}
	return p, nil
}

func gitCommonDir(tree string) (string, error) {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") { // a stray GIT_DIR must not pick the dir
			env = append(env, kv)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", tree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Env = env
	var out, serr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &serr
	if err := cmd.Run(); err != nil {
		return "", gerr("gatebox: git rev-parse --git-common-dir failed in %s: %s", tree, strings.TrimSpace(serr.String()))
	}
	return Realpath(strings.TrimSpace(out.String())), nil
}

// GateEnvMap allow-lists the caller's env; nothing else (GH_TOKEN, SSH_AUTH_SOCK, FACTORY_AGENT, ...) reaches the gate.
func GateEnvMap(env map[string]string, factoryMain, wtRoot, home string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if contains(GateEnvPass, k) || strings.HasPrefix(k, "LC_") {
			out[k] = v
		}
	}
	setDefault := func(k, v string) {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	setDefault("FACTORY_MAIN", factoryMain)
	setDefault("FACTORY_HOME", wtRoot+"/factory-home")
	setDefault("GATE_REPORT_DIR", wtRoot+"/reports")
	for k, v := range map[string]string{"HOME": home, "TMPDIR": wtRoot + "/tmp", "PYTHONDONTWRITEBYTECODE": "1",
		"FACTORY_GATEBOX":     "1", // marker: tests that need a nested userns skip on it
		"PYTHONPYCACHEPREFIX": wtRoot + "/tmp/pycache"} {
		out[k] = v
	}
	return out
}

// GateArgs are the arguments of BuildGate.
type GateArgs struct {
	Cmd, Tree, FactoryMain, WtRoot string
	Env                            map[string]string
	Home, Runtime, Unshare         string
	Subuid                         bool
	Seams                          Opts // Self, Setpriv, UID, GID, ResolvConf; the box probe is Probe
	// Probe replaces the userns / subuid availability probes (test seam); nil runs the real ones.
	Probe func(subuid bool) (bool, string)
	// CommonDir and SizeOf replace `git rev-parse --git-common-dir` and TreeBytes (test seams); nil runs the real ones.
	CommonDir func(tree string) (string, error)
	SizeOf    func(tree string) (int64, error)
}

// BuildGate is gatebox.build: the (argv, env) to run the shell command in tree under the gate profile.
func BuildGate(a GateArgs) ([]string, map[string]string, error) {
	unshare := a.Unshare
	if unshare == "" {
		unshare = "unshare"
	}
	probe := a.Probe
	if probe == nil {
		probe = func(subuid bool) (bool, string) {
			if subuid {
				self := a.Seams.Self
				if self == "" {
					self, _ = os.Executable()
				}
				return SubuidAvailable(unshare, self)
			}
			return UsernsAvailable(unshare)
		}
	}
	if ok, why := probe(false); !ok {
		return nil, nil, gerr("gatebox: user namespaces unavailable (%s); the gate must not run unsandboxed", why)
	}
	if a.Subuid {
		if ok, why := probe(true); !ok {
			return nil, nil, gerr("gatebox: subordinate uid mapping unavailable (%s); the command must not run as the owner uid", why)
		}
	}
	tree, err := realDir(a.Tree, "gate tree")
	if err != nil {
		return nil, nil, err
	}
	main, err := realDir(a.FactoryMain, "FACTORY_MAIN")
	if err != nil {
		return nil, nil, err
	}
	wt := Realpath(a.WtRoot)
	home := a.Home
	if home == "" {
		home = RealHome()
	}
	runtime := a.Runtime
	if runtime == "" {
		runtime = os.Getenv("XDG_RUNTIME_DIR")
	}
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	var rw []string
	for _, d := range []string{"reports", "factory-home", "tmp"} {
		rw = append(rw, wt+"/"+d)
	}
	for _, d := range rw {
		if err := os.MkdirAll(d, 0o755); err != nil {
			var pe *os.PathError
			name := d
			if errors.As(err, &pe) {
				name = pe.Path
			}
			return nil, nil, gerr("gatebox: cannot create %s: %v; fix wt_root, the command was not run", name, errors.Unwrap(err))
		}
	}
	real := []string{tree}
	for _, d := range rw {
		real = append(real, Realpath(d)) // the gate tree is a throwaway detached checkout: tests write into it
	}
	rw = real
	box := tree + "/.factory-box" // subuid mode: scratch inside the in-box copy of the tree
	var copySpec *CopySpec
	var roTree []string
	if a.Subuid { // no host path is writable: the tree is bound read-only and copied into a tmpfs the box's uid owns
		sizeOf := a.SizeOf
		if sizeOf == nil {
			sizeOf = TreeBytes
		}
		size, err := sizeOf(tree)
		if err != nil {
			return nil, nil, gerr("gatebox: cannot size the tree %s: %v; the command was not run", tree, err)
		}
		var mk []string
		for _, d := range []string{"reports", "factory-home", "tmp", "tmp/pycache"} {
			mk = append(mk, box+"/"+d)
		}
		copySpec = &CopySpec{Tree: tree, Mkdirs: mk, Size: fmt.Sprintf("%dm", (size*2>>20)+128)}
		roTree, rw = []string{tree}, nil
	}
	ro := append([]string{main}, roTree...)
	commonDir := a.CommonDir
	if commonDir == nil {
		commonDir = gitCommonDir
	}
	common, err := commonDir(tree)
	if err != nil {
		return nil, nil, err
	}
	if !under(common, main) {
		ro = append(ro, common) // hooks, config, refs, objects: read-only even when it sits inside the rw tree
	}
	if ptr := tree + "/.git"; isFile(ptr) && !isLink(ptr) {
		ro = append(ro, ptr) // worktree pointer file: a rw one lets the run redirect gitdir to code git then runs outside the box
	}
	var masks []string
	for _, m := range Masked {
		if isDir(filepath.Join(main, m)) {
			masks = append(masks, filepath.Join(main, m))
		}
	}
	hide := append([]string{home, runtime}, HideTmp...)
	sizes := map[string]string{}
	for d, s := range GateTmpfs {
		if isDir(d) {
			sizes[d] = s
		}
	}
	o := a.Seams
	o.TrialDir, o.Home, o.Mode, o.Net, o.NoPid, o.Unshare = wt+"/reports", home, "allow", false, false, unshare
	o.HideDirs, o.HideFiles, o.Rw, o.Ro, o.Masks, o.Cwd = hide, nil, rw, ro, masks, tree
	o.Subuid, o.Copy, o.TmpfsSizes, o.TmpfsSize, o.RealClaude, o.Forward = a.Subuid, copySpec, sizes, TmpfsSize, "", false
	cfg, err := MakeCfg(o)
	if err != nil {
		return nil, nil, gerr("gatebox: %v", err)
	}
	argv, err := ComposeStage1(cfg, []string{"bash", "-c", a.Cmd})
	if err != nil {
		return nil, nil, err
	}
	envRoot := wt
	if a.Subuid {
		envRoot = box
	}
	return argv, GateEnvMap(a.Env, main, envRoot, home), nil
}

// RunGate runs cmd under the gate profile; the tree dies on timeout.
func RunGate(a GateArgs, timeout time.Duration) (Contained, error) {
	argv, benv, err := BuildGate(a)
	if err != nil {
		return Contained{}, err
	}
	cwd := Realpath(a.Tree)
	cfgEnv := EnvList(benv)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = cwd, cfgEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	res := Contained{Output: out.String(), TimedOut: ctx.Err() != nil}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ReturnCode = ee.ExitCode()
	} else if err != nil && !res.TimedOut {
		return res, err
	}
	if res.TimedOut {
		res.ReturnCode = -1
	}
	return res, nil
}

// --- Test-run sandbox profile (T-0674): `factory test <cmd>` for coders and qa: the gate profile with a rw tree ---

// TestboxDropEnv is dropped from a test run: the live state dir and the gate knobs never reach it.
var TestboxDropEnv = []string{"FACTORY_HOME", "GATE_SHARDS", "GATE_REPORT_DIR"}

// TestboxError means the test sandbox cannot be built; the command was not run.
type TestboxError struct{ Msg string }

func (e *TestboxError) Error() string { return e.Msg }

// NewScratch is a fresh disk-backed per-run dir under tree; the caller removes it with os.RemoveAll.
func NewScratch(tree string) (string, error) {
	d, err := os.MkdirTemp(Realpath(tree), ".factory-test-")
	if err != nil {
		return "", &TestboxError{fmt.Sprintf("testbox: cannot create the scratch dir under %s: %v; the command was not run", tree, err)}
	}
	return d, nil
}

// BuildTest is testbox.build: the (argv, env) to run the command argv in tree under the test profile.
func BuildTest(argv []string, tree, factoryMain, scratch string, env map[string]string, a GateArgs) ([]string, map[string]string, error) {
	benv := map[string]string{}
	for k, v := range env {
		if !contains(TestboxDropEnv, k) {
			benv[k] = v
		}
	}
	a.Cmd, a.Tree, a.FactoryMain, a.WtRoot, a.Env = joinQuoted(argv), tree, factoryMain, scratch, benv
	out, oenv, err := BuildGate(a)
	if err != nil {
		var ge *GateboxError
		if errors.As(err, &ge) {
			return nil, nil, &TestboxError{strings.Replace(ge.Msg, "gatebox:", "testbox:", 1)}
		}
		return nil, nil, err
	}
	return out, oenv, nil
}

// joinQuoted is shlex.join: each word quoted, joined by one space.
func joinQuoted(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shlex.Quote(a)
	}
	return strings.Join(q, " ")
}
