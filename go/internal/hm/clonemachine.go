package hm

// Clone mode (machine_<name>_mode=clone, docs/machines.md): a session machine that does not share the coordinator's
// home. The task session works in its own clone under <remote home>/hivemill/worktrees, and the runner reaches it only
// over ssh. The coordinator keeps its own clone of the task (Run.Mirror) for the checks that must run here (public
// text, risk tier) and for the merge. Run.Mirror != "" marks such a run; Run.Worktree is then a path on Run.Machine.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/shlex"
)

const (
	modeShared = "shared"
	modeClone  = "clone"
)

// remoteExitAbsent is the exit code of a remote read whose file does not exist (any other failure is an error).
const remoteExitAbsent = 44

// remotePATH finds hivemill, claude, git, gh and go on a session machine; a non-login ssh shell may not.
const remotePATH = `$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/local/go/bin:$HOME/go/bin:$PATH`

// modeKey reports whether key is machine_<name>_mode of a valid machine name.
func modeKey(key string) bool {
	name, ok := strings.CutPrefix(key, "machine_")
	if !ok {
		return false
	}
	name, ok = strings.CutSuffix(name, "_mode")
	return ok && validMachine(name)
}

func (r *Runner) cloneMode(machine string) bool {
	return r.Store.Cfg("machine_"+machine+"_mode") == modeClone
}

// cloned reports whether the run's worktree is on a clone-mode machine.
func (run *Run) cloned() bool { return run != nil && run.Mirror != "" }

// remoteBase is the factory home of a clone-mode machine.
func remoteBase(remoteHome string) string { return path.Join(remoteHome, "hivemill") }

func remoteBin(remoteHome string) string { return path.Join(remoteHome, ".local", "bin", "hivemill") }

func quoteAll(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shlex.Quote(a)
	}
	return strings.Join(q, " ")
}

// sshCommand wraps a POSIX script for the remote login shell (which may not be sh).
func sshCommand(script string) string {
	return "sh -c " + shlex.Quote(`PATH="`+remotePATH+`"; export PATH; `+script)
}

// sshTransport runs command on a clone-mode session machine, with stdin.
func sshTransport(machine, stdin string, timeout time.Duration, command string) (int, string, string) {
	return run(timeout, "", stdin, nil, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", "--", machine, command)
}

// rsh runs a POSIX script on machine. Nothing of the coordinator's environment goes along.
func (r *Runner) rsh(machine, stdin string, timeout time.Duration, script string) (int, string, string) {
	if !validMachine(machine) {
		return 127, "", "not a machine name: " + machine
	}
	if r.SSH == nil {
		return 127, "", "no ssh transport"
	}
	return r.SSH(machine, stdin, timeout, sshCommand(script))
}

// rshOK is rsh with the output of a success and an error for anything else.
func (r *Runner) rshOK(machine, stdin string, timeout time.Duration, script string) (string, error) {
	rc, out, errs := r.rsh(machine, stdin, timeout, script)
	if rc != 0 {
		return out, fmt.Errorf("ssh %s: exit %d: %s", machine, rc, firstLine(errs+"\n"+out))
	}
	return out, nil
}

// remoteHomeOf is $HOME on machine.
func (r *Runner) remoteHomeOf(machine string) (string, error) {
	out, err := r.rshOK(machine, "", 30*time.Second, `echo "$HOME"`)
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(out)
	if !path.IsAbs(home) || home == "/" || strings.ContainsAny(home, "\n\r") || path.Clean(home) != home {
		return "", fmt.Errorf("ssh %s: unusable $HOME %q", machine, home)
	}
	return home, nil
}

// place is where a run's worktree is: this machine (machine "") or a clone-mode machine reached over ssh.
type place struct {
	r       *Runner
	machine string
	lost    *bool // set when ssh itself failed, so a caller does not read that as an answer about the worktree
}

// sshFailed is ssh's own exit code: no connection, or one that dropped.
const sshFailed = 255

func (p place) rsh(stdin string, timeout time.Duration, script string) (int, string, string) {
	rc, out, errs := p.r.rsh(p.machine, stdin, timeout, script)
	if rc == sshFailed && p.lost != nil {
		*p.lost = true
	}
	return rc, out, errs
}

func (r *Runner) at(run *Run) place {
	if run.cloned() {
		return place{r: r, machine: run.Machine}
	}
	return place{r: r}
}

// git runs git in dir with hooks off.
func (p place) git(dir string, args ...string) (int, string, string) {
	if p.machine == "" {
		return p.r.Tools.Git(dir, args...)
	}
	rc, out, errs := p.rsh("", 180*time.Second,
		"GIT_TERMINAL_PROMPT=0 exec git -C "+shlex.Quote(dir)+" -c core.hooksPath=/dev/null "+quoteAll(args))
	return rc, strings.TrimSpace(out), strings.TrimSpace(errs)
}

// sh runs argv in dir and returns its combined output. env applies here only: the other machine has its own.
func (p place) sh(dir string, timeout time.Duration, env []string, argv ...string) (int, string) {
	if p.machine == "" {
		return p.r.Tools.Sh(dir, timeout, env, argv...)
	}
	rc, out, errs := p.rsh("", timeout, "unset GH_TOKEN GITHUB_TOKEN; cd "+shlex.Quote(dir)+" || exit 127; exec "+quoteAll(argv)+" 2>&1")
	return rc, out + errs
}

// read returns a file; a missing one is fs.ErrNotExist.
func (p place) read(file string) ([]byte, error) {
	if p.machine == "" {
		return os.ReadFile(file)
	}
	q := shlex.Quote(file)
	rc, out, errs := p.rsh("", 60*time.Second, fmt.Sprintf("[ -e %s ] || exit %d; exec cat %s", q, remoteExitAbsent, q))
	switch rc {
	case 0:
		return []byte(out), nil
	case remoteExitAbsent:
		return nil, &fs.PathError{Op: "open", Path: file, Err: fs.ErrNotExist}
	}
	return nil, fmt.Errorf("read %s on %s: exit %d: %s", file, p.machine, rc, firstLine(errs))
}

// write replaces a file (tmp + rename), creating its directory.
func (p place) write(file string, data []byte, mode os.FileMode) error {
	if p.machine == "" {
		return writeFile(file, string(data), mode)
	}
	q := shlex.Quote(file)
	_, err := p.r.rshOK(p.machine, string(data), 120*time.Second, fmt.Sprintf(
		`set -e; umask 077; mkdir -p %s; t=%s.tmp$$; cat > "$t"; chmod %o "$t"; mv -f "$t" %s`,
		shlex.Quote(path.Dir(file)), q, mode.Perm(), q))
	return err
}

// isDir: the zero place is this machine.
func (p place) isDir(dir string) bool {
	if p.machine == "" {
		fi, err := os.Stat(dir)
		return err == nil && fi.IsDir()
	}
	rc, _, _ := p.rsh("", 30*time.Second, "[ -d "+shlex.Quote(dir)+" ]")
	return rc == 0
}

// rename moves a file if it exists.
func (p place) rename(from, to string) error {
	if p.machine == "" {
		if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	q := shlex.Quote(from)
	_, err := p.r.rshOK(p.machine, "", 30*time.Second, "[ ! -e "+q+" ] || mv -f "+q+" "+shlex.Quote(to))
	return err
}

// remoteWorktree creates (or reuses) the task's clone on machine and returns its path there. The objects come from a
// per-repo bare cache on that machine, so only the first task of a repo pays for a full clone.
func (r *Runner) remoteWorktree(machine, remoteHome string, t *Task, branch string) (string, error) {
	rc, url, errs := r.Tools.Git(t.RepoPath, "remote", "get-url", "origin")
	if rc != 0 || url == "" {
		return "", fmt.Errorf("%s has no origin URL: %s", t.RepoPath, errs)
	}
	base := remoteBase(remoteHome)
	name := t.GHRepo
	if name == "" {
		name = filepath.Base(t.RepoPath)
	}
	wt := path.Join(base, "worktrees", t.Goal+"-"+t.ID)
	hook := "#!/bin/sh\n# factory pre-push public-text guard: hivemill check public-text\nexec " + shlex.Quote(remoteBin(remoteHome)) +
		" check public-text --home " + shlex.Quote(base) + " \"$@\"\n"
	script := fmt.Sprintf(`set -e
export GIT_TERMINAL_PROMPT=0
url=%s; cache=%s; wt=%s; branch=%s; base=%s
mkdir -p "$(dirname "$cache")" "$(dirname "$wt")"
if [ ! -d "$cache" ]; then git clone -q --bare "$url" "$cache"; git -C "$cache" config gc.auto 0; fi
git -C "$cache" fetch -q "$url" '+refs/heads/*:refs/heads/*'
if [ -d "$wt/.git" ]; then
  cd "$wt"
  git fetch -q --prune origin
  start="origin/$base"
  if git rev-parse --verify -q "refs/remotes/origin/$branch" >/dev/null; then start="origin/$branch"; fi
  if git rev-parse --verify -q "refs/heads/$branch" >/dev/null; then
    git checkout -q "$branch"
    # the pushed branch may have moved (another machine, a refresh): follow it, and refuse a diverged one
    if [ "$start" = "origin/$branch" ]; then git merge -q --ff-only "$start"; fi
  else
    git checkout -q -B "$branch" "$start"
  fi
else
  if [ -e "$wt" ]; then echo "$wt exists but is not a clone; remove it" >&2; exit 1; fi
  git clone -q --reference "$cache" --no-checkout "$url" "$wt"
  cd "$wt"
  git fetch -q origin
  start="origin/$base"
  if git rev-parse --verify -q "refs/remotes/origin/$branch" >/dev/null; then start="origin/$branch"; fi
  git checkout -q -f -B "$branch" "$start"
fi
mkdir -p .hivemill .git/info .git/hooks
grep -qxF '/.hivemill/' .git/info/exclude 2>/dev/null || printf '\n/.hivemill/\n' >> .git/info/exclude
rm -f .hivemill/findings.json .hivemill/evidence.json
cat > .git/hooks/pre-push
chmod 755 .git/hooks/pre-push`, shlex.Quote(url), shlex.Quote(path.Join(base, "cache", strings.ReplaceAll(name, "/", "-")+".git")),
		shlex.Quote(wt), shlex.Quote(branch), shlex.Quote(t.Base))
	if _, err := r.rshOK(machine, hook, 15*time.Minute, script); err != nil {
		return "", err
	}
	if _, err := r.rshOK(machine, "", 30*time.Second, "env FACTORY_HOME="+shlex.Quote(base)+" "+
		shlex.Quote(remoteBin(remoteHome))+" local trust "+shlex.Quote(wt)); err != nil {
		fmt.Fprintf(os.Stderr, "hivemill: trust %s on %s: %v (the trust dialog may show)\n", wt, machine, err)
	}
	return wt, nil
}

// removeRemoteWorktrees removes the worktree of every ended clone-mode run whose work is pushed. A worktree with
// uncommitted or unpushed work stays; an unreachable machine is retried on the next pass.
func (r *Runner) removeRemoteWorktrees() {
	runs, err := r.Store.Runs()
	if err != nil {
		return
	}
	for _, x := range runs {
		if !x.cloned() || x.RemoteDone || Active(x.State) {
			continue
		}
		// Start takes the same lock, so a new run of the task cannot take the worktree while it is removed
		l := r.lock(x.Task, false)
		if l == nil {
			continue
		}
		if run, err := r.Store.Run(x.Task); err == nil && run.cloned() && !run.RemoteDone && !Active(run.State) {
			r.removeRemoteWorktree(run)
		}
		l.Close()
	}
}

// removeRemoteWorktree is called with the task lock held.
func (r *Runner) removeRemoteWorktree(run *Run) {
	under := path.Join(remoteBase(run.RemoteHome), "worktrees") + "/"
	if !strings.HasPrefix(run.Worktree, under) || path.Clean(run.Worktree) != run.Worktree {
		return
	}
	merged := ""
	if run.State == Merged {
		merged = run.Head
	}
	const kept = 3
	// a merged run's HEAD may stay behind the merged head (a refresh the worktree did not follow): all of it is merged
	rc, _, errs := r.rsh(run.Machine, "", 120*time.Second, fmt.Sprintf(`wt=%s; branch=%s; merged=%s
export GIT_TERMINAL_PROMPT=0
[ -d "$wt" ] || exit 0
cd "$wt" || exit 1
[ -z "$(git status --porcelain --untracked-files=normal)" ] || exit %d
head=$(git rev-parse HEAD) || exit 1
done=
if [ -n "$merged" ]; then
  git cat-file -e "$merged^{commit}" 2>/dev/null || git fetch -q origin "$merged" 2>/dev/null || true
  if git merge-base --is-ancestor "$head" "$merged" 2>/dev/null; then done=1; fi
fi
if [ -z "$done" ]; then
  pushed=$(git ls-remote origin "refs/heads/$branch") || exit 1
  [ "${pushed%%%%[!0-9a-f]*}" = "$head" ] || exit %d
fi
cd / && rm -rf "$wt"`, shlex.Quote(run.Worktree), shlex.Quote(run.Branch), shlex.Quote(merged), kept, kept))
	switch rc {
	case 0:
	case kept:
		r.printf("%s: kept %s on %s: it has work that is not pushed\n", run.Task, run.Worktree, run.Machine)
	default:
		r.printf("%s: cannot remove %s on %s (retried next pass): %s\n", run.Task, run.Worktree, run.Machine, firstLine(errs))
		return
	}
	run.RemoteDone = true
	if err := r.Store.PutRun(run); err != nil {
		r.printf("%s: %v\n", run.Task, err)
	}
}

// worktreeLost: the machine answers and the run's worktree is not on it.
func (r *Runner) worktreeLost(run *Run) bool {
	rc, _, _ := r.rsh(run.Machine, "", 30*time.Second, "[ -d "+shlex.Quote(path.Join(run.Worktree, ".git"))+" ] || exit "+
		strconv.Itoa(remoteExitAbsent))
	return rc == remoteExitAbsent
}

// mapHome rewrites a path under the coordinator's user home to the same path under the remote one.
func (r *Runner) mapHome(p, remoteHome string) string {
	if rest, ok := strings.CutPrefix(p, r.Env.UserHome+"/"); ok {
		return path.Join(remoteHome, rest)
	}
	return p
}

// cloneEligible probes a clone-mode machine and says why it may not take a session ("" = it may): `machine add` set it
// up for this coordinator, and it has the memory.
func (r *Runner) cloneEligible(m string, floor int) (Load, string) {
	if !validMachine(m) {
		return Load{}, "not a machine name"
	}
	rc, out, errs := r.rsh(m, "", 30*time.Second, `[ -x "$HOME/.local/bin/hivemill" ] || { echo "no hivemill" >&2; exit 1; }
printf 'coordinator='; sed -n 3p "$HOME/.config/hivemill/coordinator" 2>/dev/null; echo
sh -c `+shlex.Quote(probeScript)+` sh /nonexistent -`)
	if rc != 0 {
		return Load{}, fmt.Sprintf("ssh exit %d: %s", rc, firstLine(errs+out))
	}
	first, rest, _ := strings.Cut(out, "\n")
	p := ParseProbe(rest)
	switch {
	case strings.TrimSpace(strings.TrimPrefix(first, "coordinator=")) != r.Env.Home:
		return Load{}, "not set up for this coordinator (hivemill machine add " + m + " --mode clone)"
	case p.MemMB < floor:
		return Load{}, fmt.Sprintf("%s MemAvailable < %d MB", mbText(p.MemMB), floor)
	}
	return r.cachedLoad(m, p.Load), ""
}
