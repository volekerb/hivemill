package hm

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/kit/clean/fsx"
)

// `hivemill clean` (and the daemon, every clean_every_min) frees disk space without an agent: task worktrees of
// finished tasks, idle Go build caches when the disk is full, and old session transcripts (gzipped, never deleted).
// Every check fails closed: what cannot be read is kept, with the reason.

const (
	cleanWorktrees   = "worktrees"
	cleanGoCache     = "gocache"
	cleanTranscripts = "transcripts"

	goCacheIdle = 7 * 24 * time.Hour
)

// CleanItem is one thing clean removed (or would remove), or kept for Reason.
type CleanItem struct {
	Kind, Path, Reason string
	Bytes              int64 // freed; for a transcript in a dry run, its whole size
}

// CleanReport is the outcome of one clean.
type CleanReport struct {
	DryRun  bool
	Removed []CleanItem
	Kept    []CleanItem
	Errors  []string
	Disks   []MachineDisk
}

// MachineDisk is the disk use of one machine: of the factory home on this one, of the user home on a session machine.
type MachineDisk struct {
	Machine string `json:"machine"`
	UsedPct int    `json:"disk_used_pct"` // -1 = unreadable
}

// MB is the megabytes freed in one category.
func (c *CleanReport) MB(kind string) int64 {
	var n int64
	for _, x := range c.Removed {
		if x.Kind == kind {
			n += x.Bytes
		}
	}
	return n >> 20
}

func (c *CleanReport) count(kind string) (n int) {
	for _, x := range c.Removed {
		if x.Kind == kind {
			n++
		}
	}
	return n
}

// Print writes the report for the terminal.
func (c *CleanReport) Print(w io.Writer) {
	verb := "removed"
	if c.DryRun {
		verb = "would remove"
	}
	for _, x := range c.Removed {
		v := verb
		if x.Kind == cleanTranscripts {
			v = "gzipped"
			if c.DryRun {
				v = "would gzip"
			}
		}
		fmt.Fprintf(w, "%s %s %s (%d MB)\n", v, x.Kind, x.Path, x.Bytes>>20)
	}
	for _, x := range c.Kept {
		fmt.Fprintf(w, "kept %s %s: %s\n", x.Kind, x.Path, x.Reason)
	}
	for _, e := range c.Errors {
		fmt.Fprintf(w, "error: %s\n", e)
	}
	for _, d := range c.Disks {
		if d.UsedPct < 0 {
			fmt.Fprintf(w, "disk used on %s: unknown\n", d.Machine)
		} else {
			fmt.Fprintf(w, "disk used on %s: %d%%\n", d.Machine, d.UsedPct)
		}
	}
	freed := "freed"
	if c.DryRun {
		freed = "to free"
	}
	fmt.Fprintf(w, "MB %s: worktrees %d, gocache %d, transcripts %d\n", freed,
		c.MB(cleanWorktrees), c.MB(cleanGoCache), c.MB(cleanTranscripts))
}

// Clean runs every category once. A dry run changes nothing and writes no event. Only one clean runs at a time.
func (r *Runner) Clean(ctx context.Context, dryRun bool, now time.Time) (*CleanReport, error) {
	l := r.lock("clean", false)
	if l == nil {
		return nil, fmt.Errorf("%w: another clean is running", ErrRefused)
	}
	defer l.Close()
	rep := &CleanReport{DryRun: dryRun}
	if err := r.cleanWorktreeDirs(ctx, rep, now); err != nil {
		rep.Errors = append(rep.Errors, "worktrees: "+err.Error())
	}
	r.cleanGoCaches(ctx, rep, now)
	if err := r.cleanOldTranscripts(ctx, rep, now); err != nil {
		rep.Errors = append(rep.Errors, "transcripts: "+err.Error())
	}
	if !dryRun && ctx.Err() != nil { // a clean cut short is not done: the next one is due at once
		return rep, ctx.Err()
	}
	rep.Disks = r.diskPerMachine() // after the removals
	if dryRun {
		return rep, nil
	}
	_ = r.Store.SetMeta("cleaned", strconv.FormatInt(now.Unix(), 10))
	err := r.Log.Emit("clean.done", nil, Event{Data: map[string]any{
		"worktrees_mb": rep.MB(cleanWorktrees), "gocache_mb": rep.MB(cleanGoCache), "transcripts_mb": rep.MB(cleanTranscripts),
		"worktrees": rep.count(cleanWorktrees), "gocaches": rep.count(cleanGoCache), "transcripts": rep.count(cleanTranscripts),
		"kept": len(rep.Kept), "errors": rep.Errors, "disks": rep.Disks}})
	return rep, err
}

// CleanDue reports whether clean_every_min has passed since the last clean (0 or off: never).
func (r *Runner) CleanDue(now time.Time) bool {
	if v := strings.TrimSpace(r.Store.Cfg("clean_every_min")); v == "off" || v == "0" {
		return false
	}
	last, err := strconv.ParseInt(r.Store.Meta("cleaned"), 10, 64)
	return err != nil || now.Sub(time.Unix(last, 0)) >= time.Duration(cfgInt(r.Store, "clean_every_min"))*time.Minute
}

// keepClean runs the daemon's clean beside the passes: removing many worktrees must not stall the pass loop.
func (r *Runner) keepClean(ctx context.Context) {
	for {
		if now := time.Now(); r.CleanDue(now) {
			if _, err := r.Clean(ctx, false, now); err != nil && !errors.Is(err, ErrRefused) && ctx.Err() == nil {
				r.printf("clean: %v\n", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

var worktreeName = regexp.MustCompile(`^(G-\d+)-(T-\d+)$`)

func (r *Runner) cleanWorktreeDirs(ctx context.Context, rep *CleanReport, now time.Time) error {
	dir, err := filepath.EvalSymlinks(filepath.Join(r.Env.Home, "worktrees"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	root, err := fsx.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m := worktreeName.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		r.cleanWorktree(root, filepath.Join(dir, e.Name()), m[1], m[2], rep, now)
	}
	return nil
}

// cleanWorktree decides about one worktree with the task lock held, so no run of the task can start meanwhile.
func (r *Runner) cleanWorktree(root *fsx.Root, wt, goal, id string, rep *CleanReport, now time.Time) {
	keep := func(why string) { r.keepWorktree(rep, wt, why) }
	// the lock of an unfinished task is never taken: its passes must not find it busy
	if status, _, err := r.Store.TaskState(id); err == nil && status != "done" && status != "cancelled" {
		keep("task is " + status)
		return
	}
	l := r.lock(id, false)
	if l == nil { // passing, so no event
		rep.Kept = append(rep.Kept, CleanItem{Kind: cleanWorktrees, Path: wt, Reason: "the task is locked by another hivemill process"})
		return
	}
	defer l.Close()
	t, err := r.Store.Task(id)
	if err != nil || t.Goal != goal {
		keep("no task " + id + " of " + goal + " in the factory DB")
		return
	}
	status, updated, err := r.Store.TaskState(id)
	if err != nil {
		keep("task state: " + err.Error())
		return
	}
	if status != "done" && status != "cancelled" {
		keep("task is " + status)
		return
	}
	run, err := r.Store.Run(id)
	if err != nil {
		keep("run: " + err.Error())
		return
	}
	if run != nil && Active(run.State) {
		keep("a run of the task is " + run.State)
		return
	}
	hours := cfgInt(r.Store, "worktree_keep_h")
	since := parseTS(updated)
	if since.IsZero() {
		keep("unknown time of the last task change: " + updated)
		return
	}
	if now.Sub(since) < time.Duration(hours)*time.Hour {
		keep(fmt.Sprintf("%s less than %d hours ago", status, hours))
		return
	}
	fi, err := os.Lstat(filepath.Join(wt, ".git"))
	if err != nil {
		keep("not a git worktree: no .git")
		return
	}
	linked := !fi.IsDir() // a `git worktree add` tree; the runner's own are clones with a .git directory
	rc, out, errs := r.Tools.Git(wt, "--no-optional-locks", "status", "--porcelain", "--untracked-files=normal")
	if rc != 0 {
		keep("git status failed: " + firstLine(errs))
		return
	}
	if strings.TrimSpace(out) != "" {
		keep("uncommitted changes")
		return
	}
	mergedHead, why := r.worktreeSaved(wt, t, run, linked)
	if why != "" {
		keep(why)
		return
	}
	if why := r.cwdBusy(wt); why != "" {
		keep(why)
		return
	}
	rel := filepath.Base(wt)
	size, err := fsx.Size(root, rel)
	if err != nil {
		keep("cannot measure: " + err.Error())
		return
	}
	item := CleanItem{Kind: cleanWorktrees, Path: wt, Bytes: size.Bytes}
	if rep.DryRun {
		rep.Removed = append(rep.Removed, item)
		return
	}
	// asked while the clone is still there: it sees the objects of both
	branch := ""
	if mergedHead != "" && strings.HasPrefix(t.Branch, "factory/") && t.Branch != t.Base {
		if rc, tip, _ := r.Tools.Git(t.RepoPath, "rev-parse", "--verify", "-q", "refs/heads/"+t.Branch); rc == 0 {
			if r.contains(wt, mergedHead, strings.TrimSpace(tip)) {
				branch = t.Branch
			}
		}
	}
	makeWritable(wt) // a read-only directory (a module cache) would stop the removal half way
	if linked {
		if rc, _, errs := r.Tools.Git(t.RepoPath, "worktree", "remove", wt); rc != 0 {
			keep("git worktree remove: " + firstLine(errs))
			return
		}
	} else {
		ident, err := root.Identity(rel)
		if err == nil {
			_, err = fsx.Remove(root, rel, ident)
		}
		if err != nil {
			keep("remove: " + err.Error())
			return
		}
	}
	rep.Removed = append(rep.Removed, item)
	_ = r.Store.SetMeta("kept:"+rel, "")
	if t.RepoPath != "" {
		if rc, _, errs := r.Tools.Git(t.RepoPath, "worktree", "prune"); rc != 0 {
			rep.Errors = append(rep.Errors, "git worktree prune in "+t.RepoPath+": "+firstLine(errs))
		}
		if branch != "" { // a checked-out branch is refused by git and stays
			if rc, _, errs := r.Tools.Git(t.RepoPath, "branch", "-D", branch); rc != 0 {
				rep.Errors = append(rep.Errors, "delete branch "+branch+": "+firstLine(errs))
			}
		}
	}
}

// makeWritable gives the owner write access to every directory under dir; symlinks are not followed.
func makeWritable(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			if fi, err := d.Info(); err == nil && fi.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(path, fi.Mode().Perm()|0o700)
			}
		}
		return nil
	})
}

// keepWorktree records why a worktree stays; the event is written when the reason changes, not every hour.
func (r *Runner) keepWorktree(rep *CleanReport, wt, why string) {
	rep.Kept = append(rep.Kept, CleanItem{Kind: cleanWorktrees, Path: wt, Reason: why})
	key := "kept:" + filepath.Base(wt)
	if rep.DryRun || r.Store.Meta(key) == why {
		return
	}
	if err := r.Log.Emit("clean.kept", nil, Event{Data: map[string]any{"path": wt, "reason": why}}); err == nil {
		_ = r.Store.SetMeta(key, why)
	}
}

// contains reports whether commit tip is head or one of its ancestors.
func (r *Runner) contains(dir, head, tip string) bool {
	if head == "" || tip == "" {
		return false
	}
	if head == tip {
		return true
	}
	rc, _, _ := r.Tools.Git(dir, "merge-base", "--is-ancestor", tip, head)
	return rc == 0
}

// worktreeSaved says why removing wt would lose work ("" = it would not): HEAD must be merged, part of a closed PR
// or on origin, and a clone must hold no stash and no other local commit. mergedHead is the head of the merged PR
// ("" = no merged PR is known). The local record and git are asked first; the GitHub API only when they do not answer.
// Remote-tracking refs other than the base are not trusted: the branch may be gone on origin, so origin is asked.
func (r *Runner) worktreeSaved(wt string, t *Task, run *Run, linked bool) (mergedHead, why string) {
	rc, head, errs := r.Tools.Git(wt, "rev-parse", "HEAD")
	if rc != 0 {
		return "", "cannot read HEAD: " + firstLine(errs)
	}
	head = strings.TrimSpace(head)
	_, branch, _ := r.Tools.Git(wt, "symbolic-ref", "-q", "--short", "HEAD")
	branch = strings.TrimSpace(branch)
	url := t.PRURL
	if run != nil {
		if run.PRURL != "" {
			url = run.PRURL
		}
		if run.State == Merged {
			mergedHead = run.Head
		}
	}
	saved := r.contains(wt, mergedHead, head) || r.contains(wt, "refs/remotes/origin/"+t.Base, head)
	if !saved {
		for _, b := range []string{branch, t.Branch} {
			if b == "" {
				continue
			}
			rc, out, _ := r.Tools.Git(wt, "ls-remote", "origin", "refs/heads/"+b)
			if f := strings.Fields(out); rc == 0 && len(f) > 0 && r.contains(wt, f[0], head) {
				saved = true
				break
			}
		}
	}
	closedHead := "" // GitHub keeps the commits of a closed PR
	if url != "" && mergedHead == "" {
		if pr, err := PRView(r.Tools, url); err == nil {
			switch pr.State {
			case "MERGED":
				mergedHead = pr.HeadRefOid
				saved = saved || r.contains(wt, mergedHead, head)
			case "CLOSED":
				closedHead = pr.HeadRefOid
				saved = saved || r.contains(wt, closedHead, head)
			}
		}
	}
	if !saved {
		return "", "HEAD is not merged, not part of a closed PR and not on origin"
	}
	if linked { // the branches and stashes of a linked worktree stay in the repository
		return mergedHead, ""
	}
	if rc, _, _ := r.Tools.Git(wt, "rev-parse", "--verify", "-q", "refs/stash"); rc == 0 {
		return "", "the clone holds a stash"
	}
	rc, out, errs := r.Tools.Git(wt, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads")
	if rc != 0 {
		return "", "cannot list branches: " + firstLine(errs)
	}
	var onOrigin []string // the heads origin has now, asked once and only when needed
	asked := false
	for _, line := range strings.Split(out, "\n") {
		name, tip, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || tip == head || r.contains(wt, mergedHead, tip) || r.contains(wt, closedHead, tip) ||
			r.contains(wt, "refs/remotes/origin/"+t.Base, tip) {
			continue
		}
		if !asked {
			asked = true
			rc, heads, errs := r.Tools.Git(wt, "ls-remote", "--heads", "origin")
			if rc != 0 {
				return "", "cannot list the branches of origin: " + firstLine(errs)
			}
			for _, h := range strings.Split(heads, "\n") {
				if f := strings.Fields(h); len(f) > 0 {
					onOrigin = append(onOrigin, f[0])
				}
			}
		}
		if !slices.ContainsFunc(onOrigin, func(h string) bool { return r.contains(wt, h, tip) }) {
			return "", "branch " + name + " has commits that are not on origin"
		}
	}
	return mergedHead, ""
}

// cwdScript prints the cwd of every process of a machine, "unread <name>" for an own process whose cwd cannot be read,
// and "done" at the end. A scan without "done" counts as failed.
const cwdScript = `if [ -e /proc/self/cwd ]; then
  for p in /proc/[0-9]*; do
    if c=$(readlink "$p/cwd" 2>/dev/null); then printf 'cwd %s\n' "$c"; continue; fi
    [ -O "$p" ] || continue
    s=$(sed -n 's/^State:[[:space:]]*\(.\).*/\1/p' "$p/status" 2>/dev/null)
    case "$s" in ''|Z|X) continue ;; esac
    printf 'unread %s\n' "$(cat "$p/comm" 2>/dev/null)"
  done
  echo done
else
  out=$(lsof -w -a -d cwd -u "$(id -u)" -Fn 2>/dev/null)
  if [ -n "$out" ]; then printf '%s\n' "$out" | sed -n 's/^n/cwd /p'; echo done; fi
fi`

// hiddenCwd lists the system daemons that hide their cwd from their own user (they are not dumpable). None of them
// works in a task worktree; any other unreadable process keeps the worktree.
var hiddenCwd = map[string]bool{"systemd": true, "(sd-pam)": true, "sshd": true, "sshd-session": true,
	"ssh-agent": true, "gpg-agent": true, "fusermount3": true}

// cwdBusy says why a process may be working in wt ("" = none does): this machine and every session machine that
// shares the home are scanned. It scans for each worktree, right before the removal.
func (r *Runner) cwdBusy(wt string) string {
	machines := []string{""}
	if entries := csv(r.Store.Cfg("session_machines")); len(entries) > 0 {
		me, core := r.Machine(), r.coreName()
		for _, m := range entries {
			if m != localMachine && m != me && m != core && !r.cloneMode(m) {
				machines = append(machines, m)
			}
		}
	}
	for _, m := range machines {
		var rc int
		var out string
		if m == "" {
			rc, out = r.Tools.Sh("/", 2*time.Minute, nil, "sh", "-c", cwdScript)
		} else if validMachine(m) {
			rc, out, _ = r.Tools.HC("exec", "--on", m, "sh", "-c", cwdScript)
		} else {
			rc = 127
		}
		label := machineLabel(m)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if rc != 0 || lines[len(lines)-1] != "done" {
			return "cannot check the processes on " + label
		}
		for _, line := range lines {
			k, v, _ := strings.Cut(line, " ")
			switch k {
			case "cwd":
				v = strings.TrimSuffix(v, " (deleted)")
				if v == wt || strings.HasPrefix(v, wt+"/") {
					return "a process on " + label + " has its cwd inside"
				}
			case "unread":
				if !hiddenCwd[v] {
					return "cannot read the cwd of process " + v + " on " + label
				}
			}
		}
	}
	return ""
}

// diskUse is (total, available) bytes of the filesystem of path, as df counts them: total leaves out the blocks only
// root may use.
func (r *Runner) diskUse(path string) (uint64, uint64, error) {
	if r.DiskFree != nil {
		return r.DiskFree(path)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize) //nolint:unconvert // width differs per OS
	avail := uint64(st.Bavail)
	return (uint64(st.Blocks) - uint64(st.Bfree) + avail) * bs, avail * bs, nil //nolint:unconvert
}

// diskPerMachine is the disk use of this machine and of every other session machine, from placement's probe with a
// marker that never matches, so it writes nothing there.
func (r *Runner) diskPerMachine() []MachineDisk {
	me := r.Machine()
	local := MachineDisk{Machine: me, UsedPct: -1}
	if total, free, err := r.diskUse(r.Env.Home); err == nil && total > 0 {
		local.UsedPct = usedPct(total, free)
	}
	disks := []MachineDisk{local}
	entries := csv(r.Store.Cfg("session_machines"))
	if len(entries) == 0 {
		return disks
	}
	core := r.coreName()
	for _, m := range entries {
		if m == localMachine || m == me || m == core {
			continue
		}
		d := MachineDisk{Machine: m, UsedPct: -1}
		if validMachine(m) {
			if rc, out, _ := r.Tools.HC("exec", "--on", m, "sh", "-c", probeScript, "sh", "/nonexistent", "-"); rc == 0 {
				d.UsedPct = ParseProbe(out).DiskPct
			}
		}
		disks = append(disks, d)
	}
	return disks
}

// usedPct is the used share of a disk in percent, rounded up like df.
func usedPct(total, free uint64) int {
	if total == 0 || free > total {
		return 0
	}
	return int(((total-free)*100 + total - 1) / total)
}

// goCacheDirs are the build caches of this user: Go's default and the ones the factory sets for its sessions and
// workers. The module cache is never one of them.
func (r *Runner) goCacheDirs() []string {
	h := r.Env.UserHome
	dirs := []string{filepath.Join(h, ".cache", "go-build"), filepath.Join(h, ".cache", "go", "build"),
		filepath.Join(h, ".cache", "go", "worker-build")}
	if GOOS == "darwin" {
		dirs = append(dirs, filepath.Join(h, "Library", "Caches", "go-build"))
	}
	return dirs
}

// goCacheUsed is when a build cache was last used (false: dir is no Go build cache). Go writes trim.txt at most a day
// after a use and adds entries to the subdirectories.
func goCacheUsed(dir string) (time.Time, bool) {
	var last time.Time
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return last, false
	}
	trim, trimErr := os.Stat(filepath.Join(dir, "trim.txt"))
	readme, _ := os.ReadFile(filepath.Join(dir, "README"))
	if trimErr != nil && !strings.Contains(string(readme), "Go build system") {
		return last, false
	}
	if trimErr == nil {
		last = trim.ModTime()
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return last, false
	}
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
	}
	return last, true
}

func dirSize(dir string) (n int64) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// cleanGoCaches empties the build caches idle for goCacheIdle while the disk of the home is above disk_trim_pct.
func (r *Runner) cleanGoCaches(ctx context.Context, rep *CleanReport, now time.Time) {
	total, free, err := r.diskUse(r.Env.Home)
	if err != nil {
		rep.Errors = append(rep.Errors, "disk use of "+r.Env.Home+": "+err.Error())
		return
	}
	if usedPct(total, free) <= cfgInt(r.Store, "disk_trim_pct") {
		return
	}
	for _, dir := range r.goCacheDirs() {
		used, ok := goCacheUsed(dir)
		if !ok || ctx.Err() != nil {
			continue
		}
		if now.Sub(used) < goCacheIdle {
			rep.Kept = append(rep.Kept, CleanItem{Kind: cleanGoCache, Path: dir, Reason: "used in the last 7 days"})
			continue
		}
		before := dirSize(dir)
		if rep.DryRun {
			rep.Removed = append(rep.Removed, CleanItem{Kind: cleanGoCache, Path: dir, Bytes: before})
			continue
		}
		goBin := "go"
		if _, err := os.Stat("/usr/local/go/bin/go"); err == nil {
			goBin = "/usr/local/go/bin/go" // a service PATH may not have it
		}
		if rc, out := r.Tools.Sh("/", 10*time.Minute, append(os.Environ(), "GOCACHE="+dir), goBin, "clean", "-cache"); rc != 0 {
			rep.Errors = append(rep.Errors, "go clean -cache of "+dir+": "+firstLine(out))
			continue
		}
		rep.Removed = append(rep.Removed, CleanItem{Kind: cleanGoCache, Path: dir, Bytes: max(0, before-dirSize(dir))})
	}
}

var projectChar = regexp.MustCompile(`[^A-Za-z0-9]`)

// transcriptDirs are the Claude project directories of a worktree of this home (and of directories inside it). Claude
// names them after the whole path, so the same task id of another factory home does not match.
func (r *Runner) transcriptDirs(names []string, goal, task string) (out []string) {
	homes := []string{r.Env.Home}
	if real, err := filepath.EvalSymlinks(r.Env.Home); err == nil && real != r.Env.Home {
		homes = append(homes, real)
	}
	for _, n := range names {
		for _, h := range homes {
			key := projectChar.ReplaceAllString(filepath.Join(h, "worktrees", goal+"-"+task), "-")
			if n == key || strings.HasPrefix(n, key+"-") {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// cleanOldTranscripts gzips the transcripts of runs that ended more than transcript_keep_d days ago. A transcript
// written since then stays plain, and nothing is deleted.
func (r *Runner) cleanOldTranscripts(ctx context.Context, rep *CleanReport, now time.Time) error {
	projects := filepath.Join(r.Env.UserHome, ".claude", "projects")
	ents, err := os.ReadDir(projects)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	runs, err := r.Store.Runs()
	if err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(cfgInt(r.Store, "transcript_keep_d")) * 24 * time.Hour)
	for _, x := range runs {
		ended := parseTS(x.Ended)
		if Active(x.State) || ended.IsZero() || !ended.Before(cutoff) {
			continue
		}
		dirs := r.transcriptDirs(names, x.Goal, x.Task)
		if len(dirs) == 0 {
			continue
		}
		l := r.lock(x.Task, false)
		if l == nil {
			continue
		}
		if run, err := r.Store.Run(x.Task); err == nil && run != nil && !Active(run.State) {
			for _, d := range dirs {
				_ = filepath.WalkDir(filepath.Join(projects, d), func(path string, e fs.DirEntry, err error) error {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if err != nil || !e.Type().IsRegular() || !strings.HasSuffix(path, ".jsonl") {
						return nil
					}
					fi, err := e.Info()
					if err != nil || !fi.ModTime().Before(cutoff) {
						return nil
					}
					freed := fi.Size()
					if _, err := os.Lstat(path + ".gz"); err == nil {
						// never overwrite an earlier archive; a clean cut short left the plain copy of the same content
						if !sameAsArchive(path, path+".gz") {
							return nil
						}
						if !rep.DryRun {
							if err := os.Remove(path); err != nil {
								rep.Errors = append(rep.Errors, "remove "+path+": "+err.Error())
								return nil
							}
						}
						rep.Removed = append(rep.Removed, CleanItem{Kind: cleanTranscripts, Path: path, Bytes: freed})
						return nil
					}
					if !rep.DryRun {
						if freed, err = gzipFile(path, fi); err != nil {
							rep.Errors = append(rep.Errors, "gzip "+path+": "+err.Error())
							return nil
						}
					}
					rep.Removed = append(rep.Removed, CleanItem{Kind: cleanTranscripts, Path: path, Bytes: freed})
					return nil
				})
			}
		}
		l.Close()
	}
	return nil
}

// sameAsArchive reports whether the gzip file gz holds exactly the content of path.
func sameAsArchive(path, gz string) bool {
	sum := func(name string, zipped bool) (string, error) {
		f, err := os.Open(name)
		if err != nil {
			return "", err
		}
		defer f.Close()
		var in io.Reader = f
		if zipped {
			if in, err = gzip.NewReader(f); err != nil {
				return "", err
			}
		}
		h := sha256.New()
		_, err = io.Copy(h, in)
		return string(h.Sum(nil)), err
	}
	plain, err1 := sum(path, false)
	packed, err2 := sum(gz, true)
	return err1 == nil && err2 == nil && plain == packed
}

// gzipFile replaces path with path.gz (same mode and mtime) and returns the bytes saved. The plain file is removed
// only after the archive is complete and if the file did not change meanwhile.
func gzipFile(path string, fi fs.FileInfo) (int64, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gzip-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name()) // gone after the rename
	defer tmp.Close()
	zw := gzip.NewWriter(tmp)
	if _, err := io.Copy(zw, in); err != nil {
		return 0, err
	}
	if err := errors.Join(zw.Close(), tmp.Chmod(fi.Mode().Perm()), tmp.Sync()); err != nil {
		return 0, err
	}
	st, err := tmp.Stat()
	if err != nil {
		return 0, err
	}
	if err := os.Chtimes(tmp.Name(), fi.ModTime(), fi.ModTime()); err != nil { // reports skip files by mtime
		return 0, err
	}
	now, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if now.Size() != fi.Size() || !now.ModTime().Equal(fi.ModTime()) {
		return 0, errors.New("the file changed while it was compressed")
	}
	if err := os.Rename(tmp.Name(), path+".gz"); err != nil {
		return 0, err
	}
	if err := os.Remove(path); err != nil {
		return 0, err
	}
	return max(0, fi.Size()-st.Size()), nil
}
