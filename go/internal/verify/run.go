package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var errTimeout = errors.New("timeout")

// couldNotRun marks a setup problem; its reason starts "could not run: ".
func couldNotRun(format string, a ...any) error {
	return fmt.Errorf("could not run: "+format, a...)
}

type runState struct {
	cfg     Config
	environ []string // inherited environment
	tr      *trace
	ctx     context.Context
	start   time.Time
	tree    string
	out     string
	tmp     string
	home    string
	pgids   []int
	reason  string
}

// Run executes one verify invocation and returns the exit code: 0 PASS, 1 FAIL.
func Run(args []string, environ []string, stdout io.Writer) int {
	start := time.Now()
	tr := &trace{w: stdout}
	cfg, err := ParseArgs(args)
	if err != nil {
		tr.result(cfg.Mode, err.Error(), time.Since(start))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	r := &runState{cfg: cfg, environ: environ, tr: tr, ctx: ctx, start: start}
	r.steps()
	r.step("cleanup", r.cleanup, true)
	tr.result(cfg.Mode, r.reason, time.Since(start))
	if r.out != "" {
		if err := os.WriteFile(filepath.Join(r.out, "trace.txt"), tr.buf.Bytes(), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "verify: cannot save trace.txt:", err)
			return 1
		}
	}
	if r.reason != "" {
		return 1
	}
	return 0
}

func (r *runState) steps() {
	if !r.step("tempdir", r.tempdir, false) || !r.step("tree", r.resolveTree, false) {
		return
	}
	switch r.cfg.Mode {
	case "cli":
		r.cliSteps()
	case "mod":
		r.modSteps()
	}
}

// step runs f unless an earlier step failed (always overrides that). It records the trace line and the first failure.
func (r *runState) step(name string, f func() (string, error), always bool) bool {
	if r.reason != "" && !always {
		return false
	}
	t0 := time.Now()
	var detail string
	var err error
	if r.ctx.Err() != nil && !always {
		err = errTimeout
	} else {
		detail, err = f()
	}
	if errors.Is(err, errTimeout) {
		err = fmt.Errorf("timeout after %s in step %s", r.cfg.Timeout, name)
	}
	if err != nil {
		if detail == "" {
			detail = err.Error()
		}
		r.tr.step(name, false, time.Since(t0), detail)
		if r.reason == "" {
			r.reason = err.Error()
		}
		return false
	}
	r.tr.step(name, true, time.Since(t0), detail)
	return true
}

// killGroup SIGKILLs a process group; ESRCH (already gone) is the goal, and any other error shows up as a
// group that stays alive, which the cleanup step reports.
func killGroup(pgid int) {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintln(os.Stderr, "verify: kill group:", err)
	}
}

func envValue(environ []string, key string) (string, bool) {
	v, ok := "", false
	for _, e := range environ {
		if k, val, found := strings.Cut(e, "="); found && k == key {
			v, ok = val, true
		}
	}
	return v, ok
}

func real(p string) string {
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		p = rp
	}
	if ap, err := filepath.Abs(p); err == nil {
		p = ap
	}
	return p
}

func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// liveHomes lists the real homes the harness must never use: $HOME/factory and the inherited FACTORY_HOME.
func (r *runState) liveHomes() []string {
	var live []string
	if h, ok := envValue(r.environ, "HOME"); ok && h != "" {
		live = append(live, real(filepath.Join(h, "factory")))
	}
	if f, ok := envValue(r.environ, "FACTORY_HOME"); ok && f != "" {
		live = append(live, real(f))
	}
	return live
}

func (r *runState) tempdir() (string, error) {
	base, ok := envValue(r.environ, "TMPDIR")
	if !ok || base == "" {
		base = os.TempDir()
	}
	// Check the base before creating anything: a temp dir inside the live home would already touch it.
	rb := real(base)
	for _, live := range r.liveHomes() {
		if inside(rb, live) {
			return "", couldNotRun("live home")
		}
	}
	var err error
	if r.tmp, err = os.MkdirTemp(base, "verify-home-*"); err != nil {
		return "", couldNotRun("temp home: %w", err)
	}
	r.home = filepath.Join(r.tmp, "home")
	if err = os.Mkdir(r.home, 0o755); err != nil {
		return "", couldNotRun("temp home: %w", err)
	}
	if r.cfg.Out != "" {
		for _, live := range r.liveHomes() {
			if inside(real(r.cfg.Out), live) {
				return "", couldNotRun("live home")
			}
		}
		r.out = r.cfg.Out
		err = os.MkdirAll(r.out, 0o755)
	} else {
		r.out, err = os.MkdirTemp(base, "verify-out-*")
	}
	if err != nil {
		r.out = ""
		return "", couldNotRun("out dir: %w", err)
	}
	return fmt.Sprintf("home=%s out=%s", r.tmp, r.out), nil
}

func (r *runState) lookPath(name string) (string, error) {
	path, _ := envValue(r.environ, "PATH")
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", couldNotRun("%s not found on PATH", name)
}

func (r *runState) resolveTree() (string, error) {
	tree := r.cfg.Tree
	if tree == "" {
		git, err := r.lookPath("git")
		if err != nil {
			return "", err
		}
		b, err := exec.Command(git, "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", couldNotRun("no --tree and cwd is not in a git checkout")
		}
		tree = strings.TrimSpace(string(b))
	}
	st, err := os.Stat(tree)
	if err != nil || !st.IsDir() {
		return "", couldNotRun("tree %s is not a directory", tree)
	}
	r.tree = real(tree)
	return "tree=" + filepath.Base(r.tree), nil
}

// childEnv is the environment for the code under test: temp home, no inherited FACTORY_* variable.
func (r *runState) childEnv(extra ...string) []string {
	var env []string
	for _, e := range r.environ {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "FACTORY_") || k == "HOME" {
			continue
		}
		env = append(env, e)
	}
	// FACTORY_AGENT=owner names the caller inside the throwaway home, where nothing else can; without it the CLI refuses in an agent pane.
	env = append(env, "FACTORY_HOME="+r.tmp, "HOME="+r.home, "FACTORY_AGENT=owner")
	return append(env, extra...)
}

// exec runs one child in its own process group, output to the given files. The group is killed afterwards.
func (r *runState) exec(bin string, args []string, dir string, env []string, stdout, stderr *os.File) (int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return -1, couldNotRun("start %s: %w", filepath.Base(bin), err)
	}
	pgid := cmd.Process.Pid
	r.pgids = append(r.pgids, pgid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-r.ctx.Done():
		killGroup(pgid)
		<-done
		return -1, errTimeout
	}
	// Stray background children of the finished command die with its group.
	killGroup(pgid)
	return cmd.ProcessState.ExitCode(), nil
}

// execLog runs a child with stdout and stderr merged into OUT/NAME.log and returns the exit code and the log's last line.
func (r *runState) execLog(name, bin string, args []string, dir string, env []string) (int, string, error) {
	p := filepath.Join(r.out, name+".log")
	f, err := os.Create(p)
	if err != nil {
		return -1, "", couldNotRun("create %s: %w", p, err)
	}
	code, err := r.exec(bin, args, dir, env, f, f)
	if cerr := f.Close(); cerr != nil && err == nil {
		err = couldNotRun("close %s: %w", p, cerr)
	}
	b, rerr := os.ReadFile(p)
	if rerr != nil && err == nil {
		err = couldNotRun("read %s: %w", p, rerr)
	}
	return code, trimTail(string(b)), err
}

func (r *runState) cleanup() (string, error) {
	var errs []string
	if r.tmp != "" {
		if err := os.RemoveAll(r.tmp); err != nil {
			errs = append(errs, "remove temp home: "+err.Error())
		}
	}
	// Wait on the condition "process group gone" with a deadline.
	deadline := time.Now().Add(2 * time.Second)
	alive := 0
	for _, pg := range r.pgids {
		for syscall.Kill(-pg, 0) == nil {
			killGroup(pg)
			if time.Now().After(deadline) {
				alive++
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if alive > 0 {
		errs = append(errs, fmt.Sprintf("%d child process group(s) still alive", alive))
	}
	if len(errs) > 0 {
		return "", errors.New("cleanup: " + strings.Join(errs, "; "))
	}
	return fmt.Sprintf("removed=%s children=%d", r.tmp, len(r.pgids)), nil
}
