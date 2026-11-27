package hm

import (
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/kit/clean/fsx"
	"factory/go/internal/shlex"
)

// `hivemill clean` (and the daemon, every clean_every_min) frees disk space without an agent: task worktrees of
// finished tasks (dirty or unmerged ones are saved to the attic first), Go build caches over go_cache_max_gb or idle on
// a full disk, temp leftovers, Xcode DerivedData and simulator clones of removed worktrees, old Maestro output, and old session transcripts
// (gzipped, never deleted). The other session machines get the same through the DB-free `hivemill local clean`.
// Every check fails closed: what cannot be read is kept, with the reason.

const (
	cleanWorktrees   = "worktrees"
	cleanGoCache     = "gocache"
	cleanTranscripts = "transcripts"
	cleanAttic       = "attic" // a worktree saved to the attic and removed, or an expired attic entry
	cleanTmp         = "tmp"
	cleanDerived     = "deriveddata"
	cleanSim         = "simulators"
	cleanMaestro     = "maestro"

	goCacheIdle   = 7 * 24 * time.Hour
	atticKeep     = 60 * 24 * time.Hour
	tmpIdle       = 3 * time.Hour
	userCacheIdle = 7 * 24 * time.Hour
	maestroKeep   = 7 * 24 * time.Hour
	diskWarnPct   = 85
	diskUsageMeta = "disk_usage" // what the last clean saw, for `hivemill health`
	// the spec for a session machine is one command argument (128 KiB at most), so its lists are bounded
	specTasks = 2000
	specAttic = 200
)

var cleanKinds = []string{cleanWorktrees, cleanGoCache, cleanTranscripts, cleanAttic, cleanTmp, cleanDerived, cleanSim, cleanMaestro}

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
	Skipped []string // session machines that could not be cleaned, and why
	Absent  []string // attic call: worktree names that are not on the machine
	Disks   []MachineDisk
	// Sizes is the size in MB per category of each machine whose disk is above diskWarnPct.
	Sizes map[string]map[string]int64
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
		why := ""
		if x.Reason != "" {
			why = ": " + x.Reason
		}
		fmt.Fprintf(w, "%s %s %s (%d MB)%s\n", v, x.Kind, x.Path, x.Bytes>>20, why)
	}
	for _, x := range c.Kept {
		fmt.Fprintf(w, "kept %s %s: %s\n", x.Kind, x.Path, x.Reason)
	}
	for _, e := range c.Errors {
		fmt.Fprintf(w, "error: %s\n", e)
	}
	for _, s := range c.Skipped {
		fmt.Fprintf(w, "not cleaned: %s\n", s)
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
	totals := make([]string, len(cleanKinds))
	for i, k := range cleanKinds {
		totals[i] = fmt.Sprintf("%s %d", k, c.MB(k))
	}
	fmt.Fprintf(w, "MB %s: %s\n", freed, strings.Join(totals, ", "))
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
	r.expireAttic(rep, now)
	ended, err := r.endedTasks(now)
	if err != nil {
		rep.Errors = append(rep.Errors, "ended tasks: "+err.Error())
	}
	spec := r.machineSpec(dryRun, ended)
	if dryRun { // the worktrees a clean would remove count as gone
		for _, x := range rep.Removed {
			if x.Kind == cleanWorktrees || x.Kind == cleanAttic {
				spec.Gone = append(spec.Gone, filepath.Base(x.Path))
			}
		}
	}
	r.cleanHere(ctx, spec, r.gateHere, rep, now)
	if err := r.cleanOldTranscripts(ctx, rep, now); err != nil {
		rep.Errors = append(rep.Errors, "transcripts: "+err.Error())
	}
	if !dryRun && ctx.Err() != nil { // a clean cut short is not done: the next one is due at once
		return rep, ctx.Err()
	}
	r.cleanMachines(ctx, dryRun, ended, rep, now)
	if !dryRun && ctx.Err() != nil {
		return rep, ctx.Err()
	}
	rep.Disks = r.diskPerMachine() // after the removals
	if dryRun {
		return rep, nil
	}
	r.recordDiskUsage(rep)
	_ = r.Store.SetMeta("cleaned", strconv.FormatInt(now.Unix(), 10))
	data := map[string]any{"gocaches": rep.count(cleanGoCache), "kept": len(rep.Kept), "errors": rep.Errors,
		"not_cleaned": rep.Skipped, "disks": rep.Disks}
	for _, k := range cleanKinds {
		data[k+"_mb"] = rep.MB(k)
		if k != cleanGoCache {
			data[k] = rep.count(k)
		}
	}
	return rep, r.Log.Emit("clean.done", nil, Event{Data: data})
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
		if run, err := r.Store.Run(id); err != nil || !failedTask(status, run) {
			keep("task is " + status)
			return
		}
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
	run, err := r.Store.Run(id)
	if err != nil {
		keep("run: " + err.Error())
		return
	}
	failed := failedTask(status, run)
	if status != "done" && status != "cancelled" && !failed {
		keep("task is " + status)
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
	aged := r.atticAged(run, updated, now)
	if failed && !aged {
		keep("the run failed less than clean_attic_after_d days ago")
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
	mergedHead, unsaved := "", "uncommitted changes"
	if strings.TrimSpace(out) == "" {
		mergedHead, unsaved = r.worktreeSaved(wt, t, run, linked)
	}
	if unsaved != "" && !aged {
		keep(unsaved)
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
	if unsaved != "" {
		item.Kind, item.Reason = cleanAttic, "saved to the attic first: "+unsaved
	}
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
	if unsaved != "" {
		if err := r.atticSave(wt, id, t.Base, linked); err != nil {
			keep("attic: " + err.Error())
			return
		}
		branch = ""
	}
	makeWritable(wt) // a read-only directory (a module cache) would stop the removal half way
	if linked {
		args := []string{"worktree", "remove", wt}
		if unsaved != "" {
			args = []string{"worktree", "remove", "--force", wt}
		}
		if rc, _, errs := r.Tools.Git(t.RepoPath, args...); rc != 0 {
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

// otherMachines are the session machines that are not this one.
func (r *Runner) otherMachines() (out []string) {
	entries := csv(r.Store.Cfg("session_machines"))
	if len(entries) == 0 {
		return nil
	}
	me, core := r.Machine(), r.coreName()
	for _, m := range entries {
		if m != localMachine && m != me && m != core {
			out = append(out, m)
		}
	}
	return out
}

// cwdBusy says why a process may be working in wt ("" = none does): this machine and every session machine that
// shares the home are scanned. It scans for each worktree, right before the removal.
func (r *Runner) cwdBusy(wt string) string {
	machines := []string{""}
	for _, m := range r.otherMachines() {
		if !r.cloneMode(m) {
			machines = append(machines, m)
		}
	}
	for _, m := range machines {
		cwds, why := r.processCwds(m)
		if why != "" {
			return why
		}
		if inside(cwds, wt) {
			return "a process on " + machineLabel(m) + " has its cwd inside"
		}
	}
	return ""
}

// inside reports whether one of cwds is dir or below it.
func inside(cwds []string, dir string) bool {
	return slices.ContainsFunc(cwds, func(v string) bool { return v == dir || strings.HasPrefix(v, dir+"/") })
}

// processCwds lists the cwd of every process of machine ("" = this one); why is set when the list is not complete.
func (r *Runner) processCwds(m string) (cwds []string, why string) {
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
		return nil, "cannot check the processes on " + label
	}
	for _, line := range lines {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "cwd":
			cwds = append(cwds, strings.TrimSuffix(v, " (deleted)"))
		case "unread":
			if !hiddenCwd[v] {
				return nil, "cannot read the cwd of process " + v + " on " + label
			}
		}
	}
	return cwds, ""
}

// openPaths lists the executables and open files of this machine's processes that this user may see.
func (r *Runner) openPaths() (paths []string, why string) {
	if GOOS == "darwin" {
		rc, out := r.Tools.Sh("/", 2*time.Minute, nil, "lsof", "-w", "-Fn", "-u", strconv.Itoa(os.Getuid()))
		for _, line := range strings.Split(out, "\n") {
			if p, ok := strings.CutPrefix(line, "n/"); ok {
				paths = append(paths, "/"+p)
			}
		}
		if rc != 0 && len(paths) == 0 { // lsof also exits 1 after a complete list
			return nil, "cannot check the open files"
		}
		return paths, ""
	}
	for _, pattern := range []string{"/proc/[0-9]*/exe", "/proc/[0-9]*/fd/*"} {
		links, _ := filepath.Glob(pattern)
		for _, l := range links {
			if p, err := os.Readlink(l); err == nil {
				paths = append(paths, strings.TrimSuffix(p, " (deleted)"))
			}
		}
	}
	return paths, ""
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
	for _, m := range r.otherMachines() {
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
		filepath.Join(h, ".cache", "go", "worker-build"), filepath.Join(h, ".cache", "hivemill", "go", "build"),
		filepath.Join(h, ".cache", "factory-gate", "go-build")}
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

// cleanGoCaches empties each build cache over spec.GoCacheMaxGB while no CI job or gate runs (gate says which one
// does), and the ones idle for goCacheIdle while the disk of the home is above spec.DiskTrimPct.
func (r *Runner) cleanGoCaches(ctx context.Context, spec MachineSpec, gate func() string, rep *CleanReport, now time.Time) {
	home := r.Env.Home
	if _, err := os.Stat(home); errors.Is(err, fs.ErrNotExist) {
		home = r.Env.UserHome
	}
	full := false
	if total, free, err := r.diskUse(home); err != nil {
		rep.Errors = append(rep.Errors, "disk use of "+home+": "+err.Error())
	} else {
		full = usedPct(total, free) > spec.DiskTrimPct
	}
	for _, dir := range r.goCacheDirs() {
		used, ok := goCacheUsed(dir)
		if !ok || ctx.Err() != nil {
			continue
		}
		idle := full && now.Sub(used) >= goCacheIdle
		var before int64
		over := false
		if spec.GoCacheMaxGB > 0 {
			before = dirSize(dir)
			over = before > int64(spec.GoCacheMaxGB)<<30
		}
		if !idle && over {
			if why := gate(); why != "" {
				rep.Kept = append(rep.Kept, CleanItem{Kind: cleanGoCache, Path: dir,
					Reason: fmt.Sprintf("over %d GB, but %s", spec.GoCacheMaxGB, why)})
				continue
			}
		}
		if !idle && !over {
			if full {
				rep.Kept = append(rep.Kept, CleanItem{Kind: cleanGoCache, Path: dir, Reason: "used in the last 7 days"})
			}
			continue
		}
		if before == 0 {
			before = dirSize(dir)
		}
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
			gzipTranscripts(ctx, projects, dirs, cutoff, rep)
		}
		l.Close()
	}
	return nil
}

// gzipTranscripts gzips the transcripts in dirs (of projects) that were not written since cutoff.
func gzipTranscripts(ctx context.Context, projects string, dirs []string, cutoff time.Time, rep *CleanReport) {
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

// failedTask: the task's run failed and nobody queued it again (it waits for the owner as blocked).
func failedTask(status string, run *Run) bool {
	return status == "blocked" && run != nil && run.State == Failed
}

// atticAged reports whether a finished task ended more than clean_attic_after_d days ago ("off" = never). A task
// without a run ended at its last change.
func (r *Runner) atticAged(run *Run, updated string, now time.Time) bool {
	if strings.TrimSpace(r.Store.Cfg("clean_attic_after_d")) == "off" {
		return false
	}
	ended := parseTS(updated)
	if run != nil && run.Ended != "" {
		ended = parseTS(run.Ended)
	}
	return !ended.IsZero() && now.Sub(ended) > time.Duration(cfgInt(r.Store, "clean_attic_after_d"))*24*time.Hour
}

func (r *Runner) atticDir() string { return filepath.Join(r.Env.StateDir(), "attic") }

// atticSave writes what removing wt would lose: <task>.patch (`git diff HEAD` with the untracked files), <task>.bundle
// (the local commits not on origin/<base>; of a clone also its other branches and stashes) and, when the index differs
// from both HEAD and the files, <task>.staged.patch. Ignored files are not saved. Any error means the worktree stays,
// with its index as it was. An earlier entry of the task is never overwritten.
func (r *Runner) atticSave(wt, task, base string, linked bool) (err error) {
	git := func(args ...string) (string, error) {
		rc, out, errs := r.Tools.Git(wt, args...)
		if rc != 0 {
			return "", fmt.Errorf("git %s: %s", args[0], firstLine(errs))
		}
		return strings.TrimSpace(out), nil
	}
	dir := r.atticDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := task
	for i := 2; exists1(filepath.Join(dir, name+".patch")) || exists1(filepath.Join(dir, name+".bundle")); i++ {
		name = fmt.Sprintf("%s-%d", task, i)
	}
	patch, bundle := filepath.Join(dir, name+".patch"), filepath.Join(dir, name+".bundle")
	staged := filepath.Join(dir, name+".staged.patch")
	defer func() {
		if err != nil {
			_ = os.Remove(patch)
			_ = os.Remove(bundle)
			_ = os.Remove(staged)
		}
	}()
	// a submodule's own changes and commits are in neither the diff nor the bundle
	status, err := git("status", "--porcelain=v2", "-z", "--untracked-files=no", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(status, "\x00") {
		if f := strings.Fields(line); len(f) > 2 && (f[0] == "1" || f[0] == "2" || f[0] == "u") && strings.HasPrefix(f[2], "S") {
			return errors.New("a changed submodule inside")
		}
	}
	diff := func(file string, args ...string) error {
		args = append([]string{"diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--src-prefix=a/",
			"--dst-prefix=b/", "--output=" + file}, args...)
		_, err := git(args...)
		return err
	}
	unstaged, _, _ := r.Tools.Git(wt, "diff", "--quiet")
	cached, _, _ := r.Tools.Git(wt, "diff", "--cached", "--quiet", "HEAD")
	if unstaged != 0 && cached != 0 { // the index may hold content that no file has
		if err := diff(staged, "--cached", "HEAD"); err != nil {
			return err
		}
	}
	untracked, err := git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(untracked, "\x00") {
		if strings.HasSuffix(p, "/") {
			return errors.New("an untracked git repository inside: " + p)
		}
	}
	if untracked != "" { // the diff then holds their content
		index, err := git("rev-parse", "--git-path", "index")
		if err != nil {
			return err
		}
		if !filepath.IsAbs(index) {
			index = filepath.Join(wt, index)
		}
		before, err := os.ReadFile(index)
		if err != nil {
			return err
		}
		if _, err := git("add", "--intent-to-add", "--", "."); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_ = os.WriteFile(index, before, 0o600)
			}
		}()
	}
	if err := diff(patch, "HEAD"); err != nil {
		return err
	}
	if fi, err := os.Stat(patch); err != nil {
		return err
	} else if fi.Size() == 0 {
		if err := os.Remove(patch); err != nil {
			return err
		}
	}
	refs := []string{"HEAD"}
	if linked { // its branch by name, so `git fetch <bundle> <branch>` finds it
		if rc, branch, _ := r.Tools.Git(wt, "symbolic-ref", "-q", "HEAD"); rc == 0 {
			refs = append(refs, strings.TrimSpace(branch))
		}
	} else { // the branches and stashes of a linked worktree stay in the repository
		if rc, _, _ := r.Tools.Git(wt, "rev-parse", "--verify", "-q", "refs/stash"); rc == 0 {
			stashes, err := git("stash", "list", "--format=%H")
			if err != nil {
				return err
			}
			for i, sha := range strings.Fields(stashes) {
				if _, err := git("update-ref", fmt.Sprintf("refs/attic/stash-%d", i), sha); err != nil {
					return err
				}
			}
		}
		refs = append(refs, "--branches", "--glob=refs/attic/*")
	}
	if rc, _, _ := r.Tools.Git(wt, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+base); rc == 0 {
		refs = append(refs, "--not", "refs/remotes/origin/"+base)
	}
	n, err := git(append([]string{"rev-list", "--count"}, refs...)...)
	if err != nil {
		return err
	}
	if n == "0" {
		return nil
	}
	if _, err := git(append([]string{"bundle", "create", bundle}, refs...)...); err != nil {
		return err
	}
	_, err = git("bundle", "verify", bundle)
	return err
}

func exists1(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// expireAttic deletes attic entries older than atticKeep.
func (r *Runner) expireAttic(rep *CleanReport, now time.Time) {
	ents, err := os.ReadDir(r.atticDir())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			rep.Errors = append(rep.Errors, "attic: "+err.Error())
		}
		return
	}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < atticKeep {
			continue
		}
		path := filepath.Join(r.atticDir(), e.Name())
		if !rep.DryRun {
			if err := os.Remove(path); err != nil {
				rep.Errors = append(rep.Errors, "attic: "+err.Error())
				continue
			}
		}
		rep.Removed = append(rep.Removed, CleanItem{Kind: cleanAttic, Path: path, Bytes: fi.Size(), Reason: "older than 60 days"})
	}
}

// MachineSpec is what the coordinator tells a machine to clean: everything the factory DB knows, so the machine never
// opens it.
type MachineSpec struct {
	Home         string // the factory home on that machine
	DryRun       bool
	GoCacheMaxGB int // 0 = no cap
	DiskTrimPct  int
	Gate         string          // the CI job or gate that runs there ("" = none)
	Ended        []string        // task ids that ended: done, cancelled, or failed long ago
	Gone         []string        // worktree names a dry run counts as removed
	Attic        []AtticWorktree // clone mode: ended worktrees to save and remove
	Transcripts  []string        // worktree names whose old transcripts are gzipped
	TranscriptD  int
}

// AtticWorktree is one ended worktree of a clone-mode machine.
type AtticWorktree struct{ Name, Base string }

func (r *Runner) machineSpec(dryRun bool, ended []string) MachineSpec {
	maxGB := cfgInt(r.Store, "go_cache_max_gb")
	if strings.TrimSpace(r.Store.Cfg("go_cache_max_gb")) == "off" {
		maxGB = 0
	}
	return MachineSpec{Home: r.Env.Home, DryRun: dryRun, GoCacheMaxGB: maxGB, DiskTrimPct: cfgInt(r.Store, "disk_trim_pct"),
		Ended: ended, TranscriptD: cfgInt(r.Store, "transcript_keep_d")}
}

// endedTasks lists the ids of the tasks that are done or cancelled, or whose run failed long enough ago.
func (r *Runner) endedTasks(now time.Time) ([]string, error) {
	s, err := r.sq()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, status, updated FROM tasks WHERE status IN ('done','cancelled','blocked')`)
	if err != nil {
		return nil, err
	}
	var all [][3]string
	for rows.Next() {
		var x [3]string
		if err := rows.Scan(&x[0], &x[1], &x[2]); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, x)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var out []string
	for _, x := range all {
		run, err := r.Store.Run(x[0])
		if err != nil || (run != nil && Active(run.State)) {
			continue
		}
		if x[1] != "blocked" || (failedTask(x[1], run) && r.atticAged(run, x[2], now)) {
			out = append(out, x[0])
		}
	}
	return out, nil
}

// sameMachine maps every name of this machine to "".
func (r *Runner) sameMachine(m string) string {
	if m == "" || m == localMachine || m == r.Machine() || m == r.coreName() {
		return ""
	}
	return m
}

// gateMark is the lock file that every running gate and proof rerun of machine ("" = this one) holds shared.
func (r *Runner) gateMark(machine string) string {
	return filepath.Join(r.Env.StateDir(), "locks", "gate-"+cmp.Or(machine, "here")+".lock")
}

// holdGate marks a gate or proof rerun of run as running until the returned func is called.
func (r *Runner) holdGate(run *Run) (release func()) {
	on := ""
	if run.cloned() {
		on = r.sameMachine(run.Machine)
	}
	if on != "" && !validMachine(on) {
		return func() {}
	}
	_ = os.MkdirAll(filepath.Dir(r.gateMark(on)), 0o700)
	f, err := os.OpenFile(r.gateMark(on), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		r.printf("%s: gate mark: %v\n", run.Task, err)
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		r.printf("%s: gate mark: %v\n", run.Task, err)
	}
	return func() { _ = f.Close() }
}

// gateRunning says which CI job or gate runs on machine ("" = this one) right now ("" = none).
func (r *Runner) gateRunning(machine string) string {
	if (machine == "" || validMachine(machine)) && lockHeld(r.gateMark(machine)) {
		return "a gate or a proof rerun runs"
	}
	for _, j := range r.ciJobs() {
		if r.sameMachine(j.Machine) != machine || j.Started.IsZero() || j.Failed != "" {
			continue
		}
		if state, _, err := r.ciJobState(j); err != nil || state == jobRunning {
			return "the CI job of " + j.Task + " may be running"
		}
	}
	runs, err := r.Store.Runs()
	if err != nil {
		return "the runs cannot be read"
	}
	for _, x := range runs {
		on := ""
		if x.cloned() {
			on = r.sameMachine(x.Machine)
		}
		// HookStarted outlives a killed hook; the task lock does not
		if on == machine && Active(x.State) && x.HookStarted != "" &&
			lockHeld(filepath.Join(r.Env.StateDir(), "locks", x.Task+".lock")) {
			return "the gate of " + x.Task + " runs"
		}
	}
	return ""
}

// gateHere says which CI job or gate uses the caches of this machine: its own, or one of a machine sharing the home.
func (r *Runner) gateHere() string {
	why := r.gateRunning("")
	for _, m := range r.otherMachines() {
		if why == "" && !r.cloneMode(m) {
			why = r.gateRunning(m)
		}
	}
	return why
}

// cleanHere runs the categories that belong to a machine rather than to the factory DB.
func (r *Runner) cleanHere(ctx context.Context, spec MachineSpec, gate func() string, rep *CleanReport, now time.Time) {
	r.cleanGoCaches(ctx, spec, gate, rep, now)
	r.cleanTmpLeftovers(ctx, rep, now)
	if GOOS == "darwin" {
		r.cleanDerivedData(ctx, spec, rep)
		r.cleanSimulators(ctx, spec, rep)
		r.cleanMaestroOutput(ctx, rep, now)
	}
}

var tmpPatterns = []string{"wg-bin-*", "ci-tool-test-*", "docker-prune-bin-*", "zz-test-factory-*", "lg-*", "go-build*", "hm-*"}

func (r *Runner) tmpRoots() []string {
	if r.TmpRoots != nil {
		return r.TmpRoots
	}
	return []string{"/tmp", "/var/tmp"}
}

// cleanTmpLeftovers removes this user's build and test leftovers in the temp directories after tmpIdle, and entries of
// the factory's cache directories after userCacheIdle. Go build caches are left to cleanGoCaches.
func (r *Runner) cleanTmpLeftovers(ctx context.Context, rep *CleanReport, now time.Time) {
	var cwds []string
	why, scanned := "", false
	busy := func(path string) string {
		if !scanned {
			scanned = true
			cwds, why = r.processCwds("")
			if why == "" { // a running binary and an open file count too
				var open []string
				open, why = r.openPaths()
				cwds = append(cwds, open...)
			}
		}
		if why != "" {
			return why
		}
		if inside(cwds, path) {
			return "a process uses it"
		}
		return ""
	}
	for _, dir := range r.tmpRoots() {
		removeStale(ctx, rep, cleanTmp, now, dir, tmpIdle, busy, func(name string) bool {
			return slices.ContainsFunc(tmpPatterns, func(p string) bool { ok, _ := filepath.Match(p, name); return ok })
		})
	}
	// the containers themselves stay: sessions point TMPDIR and GOTMPDIR into them
	keep := []string{"tmp", "go", "build", "go-build", "mod"}
	other := func(name string) bool { return !slices.Contains(keep, name) }
	cache := filepath.Join(r.Env.UserHome, ".cache")
	for _, dir := range []string{"hivemill", "hivemill/tmp", "hivemill/go", "hivemill/go/tmp", "factory-gate"} {
		removeStale(ctx, rep, cleanTmp, now, filepath.Join(cache, dir), userCacheIdle, busy, other)
	}
}

// removeStale removes the matching files and directories of dir that this user owns and nothing wrote for idle.
func removeStale(ctx context.Context, rep *CleanReport, kind string, now time.Time, dir string, idle time.Duration,
	busy func(string) string, match func(string) bool) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return
	}
	root, err := fsx.OpenRoot(real)
	if err != nil {
		rep.Errors = append(rep.Errors, kind+": "+err.Error())
		return
	}
	defer root.Close()
	ents, err := os.ReadDir(real)
	if err != nil {
		rep.Errors = append(rep.Errors, kind+": "+err.Error())
		return
	}
	for _, e := range ents {
		if ctx.Err() != nil {
			return
		}
		if !match(e.Name()) || !(e.IsDir() || e.Type().IsRegular()) {
			continue
		}
		path := filepath.Join(real, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || now.Sub(fi.ModTime()) < idle {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
			continue
		}
		size, err := fsx.Size(root, e.Name())
		if err != nil {
			rep.Kept = append(rep.Kept, CleanItem{Kind: kind, Path: path, Reason: "cannot be measured: " + err.Error()})
			continue
		}
		if !size.Newest.IsZero() && now.Sub(size.Newest) < idle {
			continue
		}
		item := CleanItem{Kind: kind, Path: path, Bytes: size.Bytes}
		if why := busy(path); why != "" {
			rep.Kept = append(rep.Kept, CleanItem{Kind: kind, Path: path, Reason: why})
			continue
		}
		if !rep.DryRun {
			if e.IsDir() {
				makeWritable(path)
			}
			ident, err := root.Identity(e.Name())
			if err == nil {
				_, err = fsx.Remove(root, e.Name(), ident)
			}
			if err != nil {
				rep.Errors = append(rep.Errors, "remove "+path+": "+err.Error())
				continue
			}
		}
		rep.Removed = append(rep.Removed, item)
	}
}

// maestroDirs hold the output of Maestro UI test runs.
func (r *Runner) maestroDirs() []string {
	m := filepath.Join(r.Env.UserHome, ".maestro")
	return []string{filepath.Join(m, "tests"), filepath.Join(m, "screenshots"), filepath.Join(m, "recordings")}
}

// cleanMaestroOutput removes the Maestro output that nothing wrote for maestroKeep.
func (r *Runner) cleanMaestroOutput(ctx context.Context, rep *CleanReport, now time.Time) {
	home, err := filepath.EvalSymlinks(r.Env.UserHome)
	if err != nil {
		return
	}
	for _, dir := range r.maestroDirs() {
		real, err := filepath.EvalSymlinks(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		// every entry goes here, so a link must not lead the clean into some other folder of the user's
		if rel, relErr := filepath.Rel(r.Env.UserHome, dir); err != nil || relErr != nil || real != filepath.Join(home, rel) {
			rep.Kept = append(rep.Kept, CleanItem{Kind: cleanMaestro, Path: dir, Reason: "reached through a link"})
			continue
		}
		removeStale(ctx, rep, cleanMaestro, now, dir, maestroKeep,
			func(string) string { return "" }, func(string) bool { return true })
	}
}

var (
	workspacePath = regexp.MustCompile(`<key>WorkspacePath</key>\s*<string>([^<]*)</string>`)
	worktreePath  = regexp.MustCompile(`^(.*/worktrees)/(G-\d+-T-\d+)(/|$)`)
	taskInName    = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(T-\d+)(?:$|[^0-9])`)
)

// cleanDerivedData removes the Xcode DerivedData of a task worktree that no longer exists (info.plist WorkspacePath).
func (r *Runner) cleanDerivedData(ctx context.Context, spec MachineSpec, rep *CleanReport) {
	dir, err := filepath.EvalSymlinks(filepath.Join(r.Env.UserHome, "Library", "Developer", "Xcode", "DerivedData"))
	if err != nil {
		return
	}
	root, err := fsx.OpenRoot(dir)
	if err != nil {
		rep.Errors = append(rep.Errors, "deriveddata: "+err.Error())
		return
	}
	defer root.Close()
	ents, err := os.ReadDir(dir)
	if err != nil {
		rep.Errors = append(rep.Errors, "deriveddata: "+err.Error())
		return
	}
	for _, e := range ents {
		if ctx.Err() != nil || !e.IsDir() {
			continue
		}
		plist, err := os.ReadFile(filepath.Join(dir, e.Name(), "info.plist"))
		if err != nil {
			continue
		}
		m := workspacePath.FindSubmatch(plist)
		if m == nil {
			continue
		}
		w := worktreePath.FindStringSubmatch(html.UnescapeString(string(m[1])))
		if w == nil {
			continue
		}
		// the worktrees directory must be there: an unmounted volume says nothing about a worktree
		if _, err := os.Stat(w[1]); err != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(w[1], w[2])); !errors.Is(err, fs.ErrNotExist) && !slices.Contains(spec.Gone, w[2]) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		size, err := fsx.Size(root, e.Name())
		if err != nil {
			continue
		}
		if !rep.DryRun {
			makeWritable(path)
			ident, err := root.Identity(e.Name())
			if err == nil {
				_, err = fsx.Remove(root, e.Name(), ident)
			}
			if err != nil {
				rep.Errors = append(rep.Errors, "remove "+path+": "+err.Error())
				continue
			}
		}
		rep.Removed = append(rep.Removed, CleanItem{Kind: cleanDerived, Path: path, Bytes: size.Bytes})
	}
}

// cleanSimulators deletes the simulator devices cloned for an ended task whose worktree is gone (the device name holds
// the task id) and then the unavailable ones. A booted device stays.
func (r *Runner) cleanSimulators(ctx context.Context, spec MachineSpec, rep *CleanReport) {
	if _, err := os.Stat(filepath.Join(r.Env.UserHome, "Library", "Developer", "CoreSimulator")); err != nil {
		return
	}
	simctl := func(args ...string) (int, string) {
		return r.Tools.Sh("/", 5*time.Minute, nil, append([]string{"xcrun", "simctl"}, args...)...)
	}
	rc, out := simctl("list", "-j")
	var list struct {
		Devices map[string][]struct{ Name, UDID, State, DataPath string }
	}
	if i := strings.Index(out, "{"); rc != 0 || i < 0 || json.NewDecoder(strings.NewReader(out[i:])).Decode(&list) != nil {
		rep.Errors = append(rep.Errors, "simctl list: "+firstLine(out))
		return
	}
	worktrees := filepath.Join(r.Env.Home, "worktrees")
	ents, err := os.ReadDir(worktrees)
	if err != nil {
		return // without the worktrees directory nothing says which worktrees are gone
	}
	present := map[string]bool{}
	for _, e := range ents {
		if m := worktreeName.FindStringSubmatch(e.Name()); m != nil && !slices.Contains(spec.Gone, e.Name()) {
			present[m[2]] = true
		}
	}
	for _, devices := range list.Devices {
		for _, d := range devices {
			m := taskInName.FindStringSubmatch(d.Name)
			if ctx.Err() != nil || m == nil || !slices.Contains(spec.Ended, m[1]) || present[m[1]] || d.UDID == "" {
				continue
			}
			path := d.Name + " " + d.UDID
			if d.State == "Booted" {
				rep.Kept = append(rep.Kept, CleanItem{Kind: cleanSim, Path: path, Reason: "booted"})
				continue
			}
			var size int64
			if d.DataPath != "" {
				size = dirSize(d.DataPath)
			}
			if !rep.DryRun {
				if rc, out := simctl("delete", d.UDID); rc != 0 {
					rep.Errors = append(rep.Errors, "simctl delete "+path+": "+firstLine(out))
					continue
				}
			}
			rep.Removed = append(rep.Removed, CleanItem{Kind: cleanSim, Path: path, Bytes: size})
		}
	}
	if !rep.DryRun {
		if rc, out := simctl("delete", "unavailable"); rc != 0 {
			rep.Errors = append(rep.Errors, "simctl delete unavailable: "+firstLine(out))
		}
	}
}

// categorySizes is the size in MB of what each category covers on this machine, empty ones left out.
func (r *Runner) categorySizes() map[string]int64 {
	h := r.Env.UserHome
	dirs := map[string][]string{
		cleanWorktrees:   {filepath.Join(r.Env.Home, "worktrees")},
		cleanGoCache:     r.goCacheDirs(),
		cleanTranscripts: {filepath.Join(h, ".claude", "projects")},
		cleanAttic:       {r.atticDir()},
		cleanTmp:         {filepath.Join(h, ".cache", "hivemill", "tmp"), filepath.Join(h, ".cache", "hivemill", "go", "tmp")},
		cleanDerived:     {filepath.Join(h, "Library", "Developer", "Xcode", "DerivedData")},
		cleanSim:         {filepath.Join(h, "Library", "Developer", "CoreSimulator")},
	}
	if GOOS == "darwin" { // only cleaned there
		dirs[cleanMaestro] = r.maestroDirs()
	}
	for _, root := range r.tmpRoots() {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			dirs[cleanTmp] = append(dirs[cleanTmp], real)
		}
	}
	out := map[string]int64{}
	for kind, list := range dirs {
		var n int64
		for _, d := range list {
			n += dirSize(d)
		}
		if n >= 1<<20 {
			out[kind] = n >> 20
		}
	}
	return out
}

func (r *Runner) fullDisk(path string) bool {
	total, free, err := r.diskUse(path)
	return err == nil && total > 0 && usedPct(total, free) > diskWarnPct
}

// LocalClean is `hivemill local clean`: it cleans this machine as the coordinator's spec says and opens no DB.
func (r *Runner) LocalClean(ctx context.Context, spec MachineSpec, now time.Time) (*CleanReport, error) {
	if !filepath.IsAbs(spec.Home) || filepath.Clean(spec.Home) == "/" {
		return nil, fmt.Errorf("%w: not a factory home: %q", ErrRefused, spec.Home)
	}
	r.Env.Home = filepath.Clean(spec.Home)
	rep := &CleanReport{DryRun: spec.DryRun}
	if len(spec.Attic) > 0 { // a call of its own: the coordinator holds the task locks for it
		for _, a := range spec.Attic {
			r.atticRemote(a, rep)
		}
		return rep, nil
	}
	r.expireAttic(rep, now)
	r.cleanHere(ctx, spec, func() string { return spec.Gate }, rep, now)
	projects := filepath.Join(r.Env.UserHome, ".claude", "projects")
	if ents, err := os.ReadDir(projects); err == nil {
		var names []string
		for _, e := range ents {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		cutoff := now.Add(-time.Duration(spec.TranscriptD) * 24 * time.Hour)
		for _, wt := range spec.Transcripts {
			if m := worktreeName.FindStringSubmatch(wt); m != nil && spec.TranscriptD > 0 {
				gzipTranscripts(ctx, projects, r.transcriptDirs(names, m[1], m[2]), cutoff, rep)
			}
		}
	}
	disk := r.Env.Home
	if _, err := os.Stat(disk); err != nil {
		disk = r.Env.UserHome
	}
	if r.fullDisk(disk) {
		rep.Sizes = map[string]map[string]int64{"": r.categorySizes()}
	}
	return rep, ctx.Err()
}

// atticRemote saves and removes one ended worktree of a clone-mode machine. Its Path in the report is the name.
func (r *Runner) atticRemote(a AtticWorktree, rep *CleanReport) {
	m := worktreeName.FindStringSubmatch(a.Name)
	if m == nil {
		return
	}
	wt := filepath.Join(r.Env.Home, "worktrees", a.Name)
	keep := func(why string) {
		rep.Kept = append(rep.Kept, CleanItem{Kind: cleanWorktrees, Path: a.Name, Reason: why})
	}
	if _, err := os.Lstat(wt); errors.Is(err, fs.ErrNotExist) {
		rep.Absent = append(rep.Absent, a.Name)
		return
	}
	if fi, err := os.Lstat(filepath.Join(wt, ".git")); err != nil || !fi.IsDir() {
		keep("not a task clone")
		return
	}
	cwds, why := r.processCwds("")
	if why == "" && inside(cwds, wt) {
		why = "a process has its cwd inside"
	}
	if why != "" {
		keep(why)
		return
	}
	root, err := fsx.OpenRoot(filepath.Dir(wt))
	if err != nil {
		keep(err.Error())
		return
	}
	defer root.Close()
	size, err := fsx.Size(root, a.Name)
	if err != nil {
		keep("cannot measure: " + err.Error())
		return
	}
	if !rep.DryRun {
		if err := r.atticSave(wt, m[2], a.Base, false); err != nil {
			keep("attic: " + err.Error())
			return
		}
		makeWritable(wt)
		ident, err := root.Identity(a.Name)
		if err == nil {
			_, err = fsx.Remove(root, a.Name, ident)
		}
		if err != nil {
			keep("remove: " + err.Error())
			return
		}
	}
	rep.Removed = append(rep.Removed, CleanItem{Kind: cleanAttic, Path: a.Name, Bytes: size.Bytes, Reason: "saved to the attic first"})
}

// askMachine runs `hivemill local clean` on a session machine over its runner command.
// A stopped daemon does not wait for it: the call itself ends with its own timeout.
func (r *Runner) askMachine(ctx context.Context, machine string, spec MachineSpec) (*CleanReport, error) {
	if !validMachine(machine) {
		return nil, errors.New("not a machine name")
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	clone := r.cloneMode(machine)
	type answer struct {
		rc  int
		out string
	}
	done := make(chan answer, 1)
	go func() {
		if clone {
			home := path.Dir(spec.Home)
			// hc refuses a timeout over 600 s (2026-11-27: every remote clean failed with invalid_params)
			rc, out, errs := r.rsh(machine, "", 10*time.Minute, "exec "+shlex.Quote(remoteBin(home))+" local clean "+shlex.Quote(string(b)))
			if rc != 0 {
				out += errs
			}
			done <- answer{rc, out}
			return
		}
		// hc exec accepts at most 600 s (2026-11-27: every shared-home machine clean failed with invalid_params)
		rc, out := r.Tools.Sh("/", 11*time.Minute, nil, "hc", "exec", "--on", machine, "--timeout", "600",
			"sh", "-c", `PATH="`+remotePATH+`"; export PATH; exec hivemill local clean "$1"`, "sh", string(b))
		done <- answer{rc, out}
	}()
	var rc int
	var out string
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case a := <-done:
		rc, out = a.rc, a.out
	}
	for _, line := range strings.Split(out, "\n") {
		var rep CleanReport
		if rest, ok := strings.CutPrefix(line, cleanReportLine); ok && rc == 0 && json.Unmarshal([]byte(rest), &rep) == nil {
			return &rep, nil
		}
	}
	return nil, fmt.Errorf("exit %d: %s", rc, firstLine(out))
}

// cleanReportLine marks the report in the output of `hivemill local clean`.
const cleanReportLine = "clean-report "

// WriteLine writes the report for the coordinator that asked.
func (c *CleanReport) WriteLine(w io.Writer) error {
	b, err := json.Marshal(c)
	if err == nil {
		_, err = fmt.Fprintf(w, "%s%s\n", cleanReportLine, b)
	}
	return err
}

// cleanMachines has every other session machine clean itself.
func (r *Runner) cleanMachines(ctx context.Context, dryRun bool, ended []string, rep *CleanReport, now time.Time) {
	machines := r.otherMachines()
	if len(machines) == 0 {
		return
	}
	runs, err := r.Store.Runs()
	if err != nil {
		rep.Errors = append(rep.Errors, "machines: "+err.Error())
		return
	}
	cutoff := now.Add(-time.Duration(cfgInt(r.Store, "transcript_keep_d")) * 24 * time.Hour)
	for _, m := range machines {
		if ctx.Err() != nil {
			return
		}
		on := map[string]bool{} // the tasks that ran there: the machine hears of no others
		for _, x := range runs {
			if x.Machine == m {
				on[x.Task] = true
			}
		}
		spec := r.machineSpec(dryRun, slices.DeleteFunc(slices.Clone(ended), func(t string) bool { return !on[t] }))
		clone := r.cloneMode(m)
		if clone {
			home, err := r.remoteHomeOf(m)
			if err != nil {
				rep.Skipped = append(rep.Skipped, m+": "+err.Error())
				continue
			}
			spec.Home = remoteBase(home)
			r.atticMachine(ctx, m, spec, runs, rep, now)
		}
		spec.Gate = r.gateRunning(m)
		if !clone && spec.Gate == "" { // it shares the caches with this machine
			spec.Gate = r.gateHere()
		}
		if clone { // a shared home has its transcripts gzipped from here
			for _, x := range runs {
				if t := parseTS(x.Ended); x.Machine == m && !Active(x.State) && !t.IsZero() && t.Before(cutoff) {
					spec.Transcripts = append(spec.Transcripts, x.Goal+"-"+x.Task)
				}
			}
		}
		spec.Ended, spec.Transcripts = newest(spec.Ended), newest(spec.Transcripts)
		got, err := r.askMachine(ctx, m, spec)
		if err != nil {
			rep.Skipped = append(rep.Skipped, m+": "+err.Error())
			continue
		}
		rep.merge(m, got)
	}
}

// newest keeps the last specTasks entries.
func newest(s []string) []string { return s[max(0, len(s)-specTasks):] }

// merge adds the report of a session machine, with its name in front of the paths.
func (c *CleanReport) merge(machine string, got *CleanReport) {
	for _, x := range got.Removed {
		x.Path = machine + ":" + x.Path
		c.Removed = append(c.Removed, x)
	}
	for _, x := range got.Kept {
		x.Path = machine + ":" + x.Path
		c.Kept = append(c.Kept, x)
	}
	for _, e := range got.Errors {
		c.Errors = append(c.Errors, machine+": "+e)
	}
	if s := got.Sizes[""]; s != nil {
		c.sizes(machine, s)
	}
}

func (c *CleanReport) sizes(machine string, s map[string]int64) {
	if c.Sizes == nil {
		c.Sizes = map[string]map[string]int64{}
	}
	c.Sizes[machine] = s
}

// atticMachine saves and removes the ended worktrees a clone-mode machine still holds (removeRemoteWorktrees keeps the
// dirty and unpushed ones). Their task locks are held meanwhile, so no run can start on them.
func (r *Runner) atticMachine(ctx context.Context, machine string, spec MachineSpec, runs []Run, rep *CleanReport, now time.Time) {
	var locks []*os.File
	defer func() {
		for _, l := range locks {
			l.Close()
		}
	}()
	byName := map[string]string{} // worktree name -> task
	for _, x := range runs {
		name := x.Goal + "-" + x.Task
		under := path.Join(spec.Home, "worktrees", name)
		if !x.cloned() || x.RemoteGone || x.Machine != machine || x.Worktree != under || !slices.Contains(spec.Ended, x.Task) {
			continue
		}
		l := r.lock(x.Task, false)
		if l == nil {
			continue
		}
		locks = append(locks, l)
		run, err := r.Store.Run(x.Task)
		status, updated, serr := r.Store.TaskState(x.Task)
		if err != nil || serr != nil || run == nil || Active(run.State) || run.RemoteGone || run.Worktree != under ||
			(status != "done" && status != "cancelled" && !failedTask(status, run)) || !r.atticAged(run, updated, now) {
			continue
		}
		spec.Attic = append(spec.Attic, AtticWorktree{Name: name, Base: run.Base})
		byName[name] = x.Task
		if len(spec.Attic) == specAttic {
			break // the rest on the next pass
		}
	}
	if len(spec.Attic) == 0 {
		return
	}
	spec.Ended, spec.Transcripts = nil, nil
	got, err := r.askMachine(ctx, machine, spec)
	if err != nil {
		rep.Skipped = append(rep.Skipped, machine+": attic: "+err.Error())
		return
	}
	if !spec.DryRun {
		gone := slices.Clone(got.Absent)
		for _, x := range got.Removed {
			gone = append(gone, x.Path)
		}
		for _, name := range gone {
			if run, err := r.Store.Run(byName[name]); err == nil && run != nil {
				run.RemoteDone, run.RemoteGone = true, true
				if err := r.Store.PutRun(run); err != nil {
					rep.Errors = append(rep.Errors, run.Task+": "+err.Error())
				}
			}
		}
	}
	rep.merge(machine, got)
}

// diskUsage is what the last clean saw of the machines' disks.
type diskUsage struct {
	Disks []MachineDisk               `json:"disks"`
	Sizes map[string]map[string]int64 `json:"sizes_mb"`
}

func (r *Runner) recordDiskUsage(rep *CleanReport) {
	if r.fullDisk(r.Env.Home) {
		rep.sizes(r.Machine(), r.categorySizes())
	}
	if b, err := json.Marshal(diskUsage{Disks: rep.Disks, Sizes: rep.Sizes}); err == nil {
		_ = r.Store.SetMeta(diskUsageMeta, string(b))
	}
}

// diskCheck is the health warning for machines whose disk the last clean saw above diskWarnPct.
func (r *Runner) diskCheck() *OwnerAlert {
	var u diskUsage
	if json.Unmarshal([]byte(r.Store.Meta(diskUsageMeta)), &u) != nil {
		return nil
	}
	var names, lines []string
	for _, d := range u.Disks {
		if d.UsedPct <= diskWarnPct {
			continue
		}
		sizes := u.Sizes[d.Machine]
		kinds := slices.Clone(cleanKinds)
		slices.SortStableFunc(kinds, func(a, b string) int { return cmp.Compare(sizes[b], sizes[a]) })
		var top []string
		for _, k := range kinds[:3] {
			if sizes[k] > 0 {
				top = append(top, fmt.Sprintf("%s %.1f GB", k, float64(sizes[k])/1024))
			}
		}
		biggest := "sizes unknown"
		if len(top) > 0 {
			biggest = "biggest: " + strings.Join(top, ", ")
		}
		names = append(names, d.Machine)
		lines = append(lines, fmt.Sprintf("  %s: %d%% used; %s", d.Machine, d.UsedPct, biggest))
	}
	if len(names) == 0 {
		return nil
	}
	return &OwnerAlert{Kind: "hivemill-disk", Ref: fmt.Sprintf("disk above %d%% on %s", diskWarnPct, strings.Join(names, ", ")),
		Text: fmt.Sprintf("disk above %d%% (see `hivemill clean --dry-run`):\n%s", diskWarnPct, strings.Join(lines, "\n"))}
}
