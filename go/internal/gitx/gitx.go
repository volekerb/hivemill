// Package gitx is the Go port of git_run (bin/factory_cli.py, git_overrides, git_argv, live_checkout_head_refusal):
// every git call runs through argv exec (never a shell) with hooks, fsmonitor, non-https transports and the system
// config off, no prompts, and no GIT_DIR / GIT_WORK_TREE / GIT_COMMON_DIR from the parent env.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// scrubbed is the list of parent-env variables that would redirect git to another repo. Python git_run
// itself passes them through; only its config-refusal caller drops exactly these (factory_cli.py "base_env", GIT_DIR / GIT_WORK_TREE / GIT_COMMON_DIR).
var scrubbed = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"}

// Result mirrors subprocess.CompletedProcess: stdout, stderr and the exit code.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// TimeoutError is subprocess.TimeoutExpired: its text equals Python's str(e).
type TimeoutError struct {
	Argv    []string
	Seconds float64
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("Command '%s' timed out after %s seconds", pyList(e.Argv), pyFloat(e.Seconds))
}

func pyFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

func pyList(a []string) string {
	q := make([]string, len(a))
	for i, s := range a {
		if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
			q[i] = `"` + s + `"`
		} else {
			q[i] = "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
		}
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// Options tunes one call. Zero values are Python's defaults: parent env, no timeout, no stdin.
type Options struct {
	Env     []string      // full env "K=V" list; nil = os.Environ()
	Timeout time.Duration // 0 = none
	Input   string
	// Home and LiveHomes name the shared main checkout(s): a checkout/switch/reset there is refused and
	// protocol.file.allow is "never" when Home is a live home (git_file_protocol).
	Home      string
	LiveHomes []string
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func (o Options) homeIsLive() bool {
	if o.Home == "" {
		return false
	}
	h := realpath(o.Home)
	for _, l := range o.LiveHomes {
		if realpath(l) == h {
			return true
		}
	}
	return false
}

func (o Options) isLiveToplevel(repo string) bool {
	p := realpath(repo)
	if o.Home != "" && p == realpath(o.Home) {
		return true
	}
	for _, l := range o.LiveHomes {
		if p == realpath(l) {
			return true
		}
	}
	return false
}

// Overrides is git_overrides(): the key/value pairs forced on every git call.
func (o Options) Overrides() [][2]string {
	fp := "always"
	if o.homeIsLive() {
		fp = "never"
	}
	return [][2]string{{"core.hooksPath", "/dev/null"}, {"core.fsmonitor", "false"}, {"protocol.allow", "never"},
		{"protocol.https.allow", "always"}, {"protocol.file.allow", fp},
		{"commit.gpgSign", "false"}, {"tag.gpgSign", "false"}}
}

// Argv is git_argv(repo, args...).
func (o Options) Argv(repo string, args ...string) []string {
	a := []string{"git", "-C", repo}
	for _, kv := range o.Overrides() {
		a = append(a, "-c", kv[0]+"="+kv[1])
	}
	return append(a, args...)
}

// LiveHeadRefusal is live_checkout_head_refusal minus the `reset --keep` on main exception, which needs a second git
// call: it is decided by the caller via Run. "" means allowed.
func (o Options) liveHeadRefusal(repo string, args []string) (why string, needKeepCheck bool) {
	if len(args) == 0 || !o.isLiveToplevel(repo) {
		return "", false
	}
	switch args[0] {
	case "checkout", "switch":
		return fmt.Sprintf("refusing `git %s` on the live factory checkout (%s); only the tick may move main (factory sync_pull)", args[0], repo), false
	case "reset":
		for _, a := range args[1:] {
			if a == "--keep" {
				return "", true
			}
		}
		return fmt.Sprintf("refusing `git reset` on the live factory checkout (%s); only `git reset --keep` on branch main is allowed for the tick", repo), false
	}
	return "", false
}

// scrubEnv drops the redirecting GIT_* variables and forces no prompts and no system config.
func scrubEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, s := range scrubbed {
			if k == s {
				drop = true
			}
		}
		// the two forced keys are appended last so they win
		if k == "GIT_TERMINAL_PROMPT" || k == "GIT_CONFIG_NOSYSTEM" {
			drop = true
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
}

func exec1(argv []string, env []string, input string, timeout time.Duration) (Result, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = time.Second // a grandchild holding the pipes must not outlive the kill
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	res := Result{Stdout: so.String(), Stderr: se.String()}
	if ctx.Err() == context.DeadlineExceeded {
		return res, &TimeoutError{Argv: argv, Seconds: timeout.Seconds()}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err // start failure (git missing): OSError in Python
}

// Run is git_run: a refusal is exit 1 with the reason on stderr; a timeout is a *TimeoutError.
func Run(repo string, o Options, args ...string) (Result, error) {
	why, keep := o.liveHeadRefusal(repo, args)
	if keep {
		sym, err := exec1(o.Argv(repo, "symbolic-ref", "-q", "HEAD"),
			append(append([]string{}, osEnv(o.Env)...), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"), "", 0)
		if err == nil && sym.ExitCode == 0 && strings.TrimSpace(sym.Stdout) == "refs/heads/main" {
			why = ""
		} else {
			why = fmt.Sprintf("refusing `git reset` on the live factory checkout (%s); only `git reset --keep` on branch main is allowed for the tick", repo)
		}
	}
	if why != "" {
		return Result{ExitCode: 1, Stderr: why + "\n"}, nil
	}
	return exec1(o.Argv(repo, args...), scrubEnv(o.Env), o.Input, o.Timeout)
}

func osEnv(e []string) []string {
	if e == nil {
		return os.Environ()
	}
	return e
}

// RC is _git_rc: (rc, trimmed stdout, trimmed stderr); a timeout is rc 124.
func RC(repo string, o Options, args ...string) (int, string, string) {
	r, err := Run(repo, o, args...)
	var te *TimeoutError
	if errors.As(err, &te) {
		return 124, "", "git " + args[0] + " timed out"
	}
	if err != nil {
		return 127, "", err.Error()
	}
	return r.ExitCode, strings.TrimSpace(r.Stdout), strings.TrimSpace(r.Stderr)
}

// RevParse runs `git rev-parse args...` and returns the trimmed stdout.
func RevParse(repo string, o Options, args ...string) (string, error) {
	return one(repo, o, append([]string{"rev-parse"}, args...))
}

// LsRemote runs `git ls-remote args...` and returns the trimmed stdout.
func LsRemote(repo string, o Options, args ...string) (string, error) {
	return one(repo, o, append([]string{"ls-remote"}, args...))
}

// MergeBase runs `git merge-base a b` and returns the trimmed stdout.
func MergeBase(repo string, o Options, a, b string) (string, error) {
	return one(repo, o, []string{"merge-base", a, b})
}

func one(repo string, o Options, args []string) (string, error) {
	r, err := Run(repo, o, args...)
	if err != nil {
		return "", err
	}
	if r.ExitCode != 0 {
		return "", fmt.Errorf("git %s exit %d: %s", args[0], r.ExitCode, strings.TrimSpace(r.Stderr))
	}
	return strings.TrimSpace(r.Stdout), nil
}
