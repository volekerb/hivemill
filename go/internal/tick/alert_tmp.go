package tick

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"factory/go/internal/store"
)

// Units tmp_alert and the tmp_sweep half of remote_url_alert (tmp_usage, tmp_alert, tmp_sweep, _sweep_candidates,
// _sweep_leftovers, _proc_holds, _harness_newest_mtime, _tree_bytes in bin/factory_cli.py).

const (
	tmpSweepMinAgeS      = 1800  // TMP_SWEEP_MIN_AGE_S
	tmpLeftoverMinAgeS   = 86400 // TMP_LEFTOVER_MIN_AGE_S
	tmpLeftoverCap       = 500   // TMP_LEFTOVER_CAP
	tmpLeftoverThrottleS = 600   // TMP_LEFTOVER_THROTTLE_S
	tmpMB                = 1048576
	tmpGobuildMinAgeS    = 7200 // TMP_GOBUILD_MIN_AGE_S (T-1698)
	tmpFtcacheMinAgeS    = 7200 // TMP_FTCACHE_MIN_AGE_S (T-1698)
	tmpFtcacheKeep       = 1    // TMP_FTCACHE_KEEP: the newest entry always stays
)

var (
	tmpSweepDone = map[string]bool{"done": true, "stopped": true, "lost": true, "exited": true}
	// TMP_LEFTOVER_IGNORE_COMMS: same-uid daemons that are non-dumpable and never hold /tmp entries.
	tmpIgnoreComms = map[string]bool{"systemd": true, "(sd-pam)": true, "ssh-agent": true, "sshd-session": true}
	// _LEFTOVER_NAME, used with fullmatch. The classes are ASCII in Python too.
	// _GOBUILD_NAME (fullmatch, ASCII digits).
	tmpGobuildName  = regexp.MustCompile(`^go-build[0-9]+$`)
	tmpLeftoverName = regexp.MustCompile(`^(?:(?:guard-diff-(?:seed-)?|factory-screenshots-|freshbox-)[a-z0-9_]{8}` +
		`|zz-test-[a-z0-9-]+-[a-z0-9_]{8}|zz-rv-[a-z0-9_]{8}|tmp[a-z0-9_]{8}|tmp\.[A-Za-z0-9]{10})$`)
)

// tmpDir is tmp_dir(): $FACTORY_TMP_DIR, else /tmp.
func tmpDir() string {
	if d := os.Getenv("FACTORY_TMP_DIR"); d != "" {
		return d
	}
	return "/tmp"
}

// TmpUsageInfo is the dict tmp_usage returns. FSType "" is Python None.
type tmpUsageInfo struct {
	UsedMB, TotalMB int64
	Pct             float64
	FSType          string
}

// tmpUsage is tmp_usage(): the filesystem holding the tmp dir, nil when it cannot be read. $FACTORY_TMP_STATVFS
// ("<total_bytes>,<free_bytes>") replaces statvfs.
func tmpUsage() *tmpUsageInfo {
	var total, free int64
	if fake := os.Getenv("FACTORY_TMP_STATVFS"); fake != "" {
		parts := strings.Split(fake, ",")
		if len(parts) != 2 {
			return nil // unpacking two values from a list of another length: ValueError
		}
		var ok [2]bool
		var v [2]int64
		for i, p := range parts {
			n, good, err := pyIntArg(p)
			if err != nil || !good {
				return nil
			}
			v[i], ok[i] = n, good
		}
		total, free = v[0], v[1]
	} else {
		var err error
		if total, free, err = statvfs(tmpDir()); err != nil {
			return nil
		}
	}
	if total <= 0 {
		return nil
	}
	fstype := ""
	best := ""
	target := pyRealpath(tmpDir())
	if tmpGOOS == "darwin" {
		// statvfs does not name the fs type on macOS (T-1568 R5)
		used := total - free
		return &tmpUsageInfo{UsedMB: used / tmpMB, TotalMB: total / tmpMB, Pct: 100.0 * float64(used) / float64(total), FSType: darwinFsType(tmpDir())}
	}
	if raw, err := os.ReadFile("/proc/mounts"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") { // splitlines: /proc/mounts has only \n
			f := strings.FieldsFunc(line, store.PyIsSpace)
			if len(f) >= 3 && (target == f[1] || strings.HasPrefix(target, strings.TrimRight(f[1], "/")+"/")) && len(f[1]) >= len(best) {
				best, fstype = f[1], f[2]
			}
		}
	}
	used := total - free
	return &tmpUsageInfo{UsedMB: used / tmpMB, TotalMB: total / tmpMB, Pct: 100.0 * float64(used) / float64(total), FSType: fstype}
}

// pyIntArg is int(s) for the FACTORY_TMP_STATVFS fields (ASCII digits, sign and surrounding whitespace).
func pyIntArg(s string) (int64, bool, error) {
	t := store.PyStrip(s)
	if t == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(t, "_", ""), 10, 64)
	if err != nil || strings.HasPrefix(t, "_") || strings.HasSuffix(t, "_") || strings.Contains(t, "__") {
		return 0, false, nil
	}
	return n, true, nil
}

func tmpAlertUnit(u *UnitCtx) error {
	a, closeDB, err := openAlert(u)
	if err != nil {
		return err
	}
	defer closeDB()
	return tmpAlert(a.S)
}

// tmpAlert is tmp_alert: one alert to the director each time the tmp filesystem crosses tmp_max_pct; re-armed once it
// drops below.
func tmpAlert(s *store.Store) error {
	tmp := tmpUsage()
	if tmp == nil {
		return nil
	}
	limit, err := cfgFloat(s, "tmp_max_pct")
	if err != nil {
		return err
	}
	over := tmp.Pct >= limit
	cur, _, err := configRow(s.DB, "tmp_alerted")
	if err != nil {
		return err
	}
	armed := cur == "1"
	if over && !armed {
		pct, _ := s.Cfg("tmp_max_pct")
		fs := tmp.FSType
		if fs == "" {
			fs = "fs unknown"
		}
		body := fmt.Sprintf("/tmp (%s) uses %d/%d MB, over tmp_max_pct %s%%: new spawns are held. Free finished tasks' scratchpads "+
			"and QA temp homes (/tmp/qa-*) via the cleaner.", fs, tmp.UsedMB, tmp.TotalMB, pct)
		if _, _, err := s.Send(store.Msg{To: "director", Subject: fmt.Sprintf("/tmp %.1f%% full", tmp.Pct), Body: &body,
			Kind: "alert", Sender: alertTickActor}); err != nil {
			return err
		}
	}
	if over != armed {
		v := "0"
		if over {
			v = "1"
		}
		return setConfig(s.DB, "tmp_alerted", v)
	}
	return nil
}

// tmpTreeBytes is _tree_bytes: the sum of lstat sizes of every entry below path (symlinks not followed, unreadable
// entries skipped).
func tmpTreeBytes(path string) int64 {
	var total int64
	var walk func(dir string)
	walk = func(dir string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return // os.walk with no onerror skips an unreadable dir
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if fi, err := os.Lstat(p); err == nil {
				total += fi.Size()
			}
			if e.Type().IsDir() {
				walk(p)
			}
		}
	}
	walk(path)
	return total
}

// harnessNewestMTime is _harness_newest_mtime: the newest mtime of d and of every entry below it (symlinks not
// followed). An unreadable or unsearchable subdir is an error: the caller must keep the tree.
func harnessNewestMTime(d string) (float64, error) {
	fi, err := os.Lstat(d)
	if err != nil {
		return 0, err
	}
	newest := epochSeconds(fi.ModTime())
	var walk func(dir string) error
	walk = func(dir string) error {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if fi, err := os.Lstat(p); err == nil {
				if m := epochSeconds(fi.ModTime()); m > newest {
					newest = m
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if e.Type().IsDir() {
				if err := walk(p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return newest, walk(d)
}

type sweepCand struct{ Dir, Task string }

// qaTaskID is re.search(r"(?<![A-Za-z0-9])[Tt]-(\d+)(?![0-9])", name): the first T-<digits> that does not follow an ASCII
// letter or digit. Returns "T-<digits>" or "". (\d is any Unicode decimal digit; a maximal run is never followed by [0-9].)
func qaTaskID(name string) string {
	rs := []rune(name)
	for i := 0; i+2 < len(rs); i++ {
		if (rs[i] != 'T' && rs[i] != 't') || rs[i+1] != '-' {
			continue
		}
		if i > 0 && isASCIIAlnum(rs[i-1]) {
			continue
		}
		j := i + 2
		for j < len(rs) && unicode.Is(unicode.Nd, rs[j]) {
			j++
		}
		if j > i+2 {
			return "T-" + string(rs[i+2:j])
		}
	}
	return ""
}

func isASCIIAlnum(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// sweepCandidates is _sweep_candidates: per-session scratch dirs and QA homes whose task has agent rows, every one
// finished for >= 30 min.
func sweepCandidates(s *store.Store, home string, t float64) ([]sweepCand, error) {
	root := tmpDir()
	if fi, err := os.Lstat(root); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return nil, nil
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, nil
	}
	root = pyRealpath(root)
	uid := os.Getuid()
	type found struct{ dir, task, parent string }
	var all []found
	base := filepath.Join(root, fmt.Sprintf("claude-%d", uid))
	// the worktrees slug: the worktrees path with every non-alphanumeric turned into "-", then "-G-<n>-<T-n>[-qa]"
	slugPrefix := regexp.QuoteMeta(nonAlnum.ReplaceAllString(filepath.Join(home, "worktrees"), "-") + "-")
	slug := regexp.MustCompile(`^` + slugPrefix + `G-\p{Nd}+-(T-\p{Nd}+)(?:-qa)?\n?$`)
	if fi, err := os.Lstat(base); err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
		ents, err := os.ReadDir(base)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", base, err)
		}
		for _, e := range ents {
			if m := slug.FindStringSubmatch(e.Name()); m != nil {
				all = append(all, found{filepath.Join(base, e.Name()), m[1], base})
			}
		}
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", root, err)
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "qa-") {
			continue
		}
		if task := qaTaskID(e.Name()); task != "" {
			all = append(all, found{filepath.Join(root, e.Name()), task, root})
		}
	}
	var out []sweepCand
	for _, f := range all {
		li, err := os.Lstat(f.dir)
		if err != nil || li.Mode()&os.ModeSymlink != 0 || !li.IsDir() {
			continue
		}
		if filepath.Dir(pyRealpath(f.dir)) != pyRealpath(f.parent) {
			continue
		}
		st, err := os.Stat(f.dir)
		if err != nil {
			return nil, err
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != uid {
			continue
		}
		okAll, any, err := sweepAgentsDone(s.DB, f.task, t)
		if err != nil {
			return nil, err
		}
		if !any || !okAll {
			continue
		}
		out = append(out, sweepCand{f.dir, f.task})
	}
	return out, nil
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// sweepAgentsDone reads the agent rows of a task: any=false when there are none; ok is all() of "finished for >= 30
// min" with Python's short-circuit order (an unreadable ended stamp makes ok false, never an error).
func sweepAgentsDone(db *sql.DB, task string, t float64) (ok, any bool, err error) {
	rs, err := db.Query("SELECT status,ended FROM agents WHERE task_id=?", task)
	if err != nil {
		return false, false, fmt.Errorf("read agents of %s: %w", task, err)
	}
	defer rs.Close()
	ok = true
	for rs.Next() {
		var status string
		var ended sql.NullString
		if err := rs.Scan(&status, &ended); err != nil {
			return false, false, fmt.Errorf("read agents of %s: %w", task, err)
		}
		any = true
		if !ok {
			continue // all() has stopped; Python already fetched every row
		}
		if !tmpSweepDone[status] || ended.String == "" {
			ok = false
			continue
		}
		tm, good := pyFromISO(ended.String, nil)
		if !good {
			ok = false // an ended stamp we cannot read: ownership unclear, keep
			continue
		}
		if t-epochSeconds(tm) < tmpSweepMinAgeS {
			ok = false
		}
	}
	if err := rs.Err(); err != nil {
		return false, false, fmt.Errorf("read agents of %s: %w", task, err)
	}
	return ok, any, nil
}

// procRoot is _serve_proc_root: /proc, or $FACTORY_SERVE_PROC_DIR off the live home only (on a live home the variable is
// ignored).
func procRoot(a *alertEnv) string {
	fake := os.Getenv("FACTORY_SERVE_PROC_DIR")
	if fake == "" {
		return "/proc"
	}
	live, err := a.Env.HomeIsLive()
	if err == nil && !live {
		return fake
	}
	return "/proc"
}

// procHolds is _proc_holds: the roots that a process of our uid has as cwd or an open fd (or holds something inside).
// ok=false means a pid of ours could not be read completely: keep everything. An unreadable pid outside ignore is
// appended to blocker as "pid:comm" first.
func procHolds(proc string, roots []string, ignore map[string]bool, blocker *[]string) (held map[string]bool, ok bool, err error) {
	if tmpGOOS == "darwin" {
		return nil, false, nil // no /proc: keep everything (never delete what may be in use)
	}
	held = map[string]bool{}
	uid := os.Getuid()
	ents, err := os.ReadDir(proc)
	if err != nil {
		return nil, false, fmt.Errorf("list %s: %w", proc, err)
	}
	for _, e := range ents {
		if !isUnicodeDigits(e.Name()) {
			continue
		}
		pdir := filepath.Join(proc, e.Name())
		targets, perr := pidTargets(pdir, uid)
		if perr != nil {
			if errors.Is(perr, fs.ErrNotExist) {
				continue // the process exited while we looked
			}
			if len(ignore) == 0 && blocker == nil {
				return nil, false, nil
			}
			comm, cerr := pidComm(pdir)
			if cerr != nil && errors.Is(cerr, fs.ErrNotExist) {
				continue
			}
			if comm != nil && ignore[*comm] {
				continue
			}
			if blocker != nil {
				c := "None"
				if comm != nil {
					c = *comm
				}
				*blocker = append(*blocker, e.Name()+":"+c)
			}
			return nil, false, nil
		}
		for _, tg := range targets {
			for _, r := range roots {
				if tg == r || strings.HasPrefix(tg, r+string(os.PathSeparator)) {
					held[r] = true
				}
			}
		}
	}
	return held, true, nil
}

func isUnicodeDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// pidTargets is the cwd and fd targets (real paths) of a pid of ours; nil for another uid.
func pidTargets(pdir string, uid int) ([]string, error) {
	st, err := os.Stat(pdir)
	if err != nil {
		return nil, err
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != uid {
		return nil, nil
	}
	cwd, err := os.Readlink(filepath.Join(pdir, "cwd"))
	if err != nil {
		return nil, err
	}
	out := []string{pyRealpath(cwd)}
	fds, err := os.ReadDir(filepath.Join(pdir, "fd"))
	if err != nil {
		return nil, err
	}
	for _, f := range fds {
		out = append(out, pyRealpath(filepath.Join(pdir, "fd", f.Name())))
	}
	if exe, eerr := os.Readlink(filepath.Join(pdir, "exe")); eerr == nil {
		out = append(out, pyRealpath(exe)) // a running built tool (T-1698); no exe link adds nothing
	}
	return out, nil
}

// pidComm is _pid_comm: /proc/<pid>/comm, nil when unreadable; a missing file is returned as an error (the pid exited).
func pidComm(pdir string) (*string, error) {
	b, err := os.ReadFile(filepath.Join(pdir, "comm"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, nil
	}
	c := store.PyStrip(string(b))
	return &c, nil
}

type leftover struct {
	Dir  string
	Size int64
}

// sweepLeftovers is _sweep_leftovers: (removed, locked, blocked). See the Python docstring for the rules.
func sweepLeftovers(a *alertEnv, t float64) (removed []leftover, locked int, blocked bool, err error) {
	s := a.S
	base := tmpDir()
	if li, lerr := os.Lstat(base); lerr != nil || li.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, nil
	}
	if fi, serr := os.Stat(base); serr != nil || !fi.IsDir() {
		return nil, 0, false, nil
	}
	rs, qerr := s.DB.Query("SELECT cwd FROM agents WHERE cwd IS NOT NULL AND cwd != '' AND status IN ('live','starting','queued','done')")
	if qerr != nil {
		return nil, 0, false, fmt.Errorf("read agent cwds: %w", qerr)
	}
	var cwds []string
	for rs.Next() {
		var c string
		if err := rs.Scan(&c); err != nil {
			rs.Close()
			return nil, 0, false, fmt.Errorf("read agent cwds: %w", err)
		}
		cwds = append(cwds, pyRealpath(c))
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, 0, false, fmt.Errorf("read agent cwds: %w", err)
	}
	type oldEnt struct {
		dir   string
		isdir bool
		real  string
	}
	var old []oldEnt
	ents, rerr := os.ReadDir(base)
	if rerr != nil {
		return nil, 0, false, fmt.Errorf("list %s: %w", base, rerr)
	}
	uid := os.Getuid()
	for _, e := range ents {
		gobuild := tmpGobuildName.MatchString(e.Name())
		if !gobuild && !tmpLeftoverName.MatchString(e.Name()) {
			continue
		}
		d := filepath.Join(base, e.Name())
		fi, lerr := os.Lstat(d)
		if lerr != nil {
			if errors.Is(lerr, fs.ErrNotExist) {
				continue
			}
			locked++
			continue
		}
		isdir := fi.IsDir()
		sys, _ := fi.Sys().(*syscall.Stat_t)
		if !(isdir || fi.Mode().IsRegular()) || sys == nil || int(sys.Uid) != uid {
			continue
		}
		newest := epochSeconds(fi.ModTime())
		if isdir {
			n, nerr := harnessNewestMTime(d)
			if nerr != nil {
				if errors.Is(nerr, fs.ErrNotExist) {
					continue // vanished while we looked
				}
				locked++ // unreadable subdir: cannot be measured, keep it
				continue
			}
			newest = n
		}
		minAge := float64(tmpLeftoverMinAgeS)
		if gobuild {
			minAge = tmpGobuildMinAgeS
		}
		if t-newest < minAge {
			continue
		}
		real := pyRealpath(d)
		inCwd := false
		for _, c := range cwds {
			if c == real || strings.HasPrefix(c, real+string(os.PathSeparator)) {
				inCwd = true
			}
		}
		if inCwd {
			continue
		}
		old = append(old, oldEnt{d, isdir, real})
	}
	if len(old) == 0 {
		return nil, locked, false, nil
	}
	roots := make([]string, len(old))
	for i, o := range old {
		roots[i] = o.real
	}
	var blocker []string
	held, ok, herr := procHolds(procRoot(a), roots, tmpIgnoreComms, &blocker)
	if herr != nil {
		return nil, locked, false, herr
	}
	if !ok {
		pid, comm, _ := strings.Cut(firstOr(blocker, "?:?"), ":")
		var keptBytes int64
		for _, o := range old {
			if o.isdir {
				keptBytes += tmpTreeBytes(o.dir)
			} else if fi, lerr := os.Lstat(o.dir); lerr == nil {
				keptBytes += fi.Size()
			}
		}
		detail := store.Ordered{{K: "pid", V: pid}, {K: "comm", V: comm}, {K: "kept", V: len(old)},
			{K: "mb", V: pyRound1(float64(keptBytes) / tmpMB)}}
		if eerr := s.EventAs("tick", "tick", "tmp-sweep", "tmp-sweep-blocked", detail, ""); eerr != nil {
			return nil, locked, true, eerr
		}
		return nil, locked, true, nil
	}
	for _, o := range old {
		if len(removed) >= tmpLeftoverCap {
			break
		}
		if held[o.real] {
			continue
		}
		var size int64
		if o.isdir {
			size = tmpTreeBytes(o.dir)
			if _, lerr := os.Lstat(o.dir); lerr != nil && errors.Is(lerr, fs.ErrNotExist) {
				continue
			}
			if rerr := os.RemoveAll(o.dir); rerr != nil {
				locked++ // partly removed or not removable: counted, not an error
				continue
			}
		} else {
			fi, lerr := os.Lstat(o.dir)
			if lerr != nil {
				if errors.Is(lerr, fs.ErrNotExist) {
					continue
				}
				locked++
				continue
			}
			size = fi.Size()
			if rerr := os.Remove(o.dir); rerr != nil {
				if errors.Is(rerr, fs.ErrNotExist) {
					continue
				}
				locked++
				continue
			}
		}
		removed = append(removed, leftover{o.dir, size})
	}
	return removed, locked, false, nil
}

// sweepFactorytestCache is the T-1698 factorytest-cache sweep: entries of <tmp>/factorytest-cache-<uid>/ untouched
// for 2 h, not held by a process of ours, never the newest tmpFtcacheKeep. Any error keeps everything.
func sweepFactorytestCache(a *alertEnv, t float64) []leftover {
	uid := os.Getuid()
	base := filepath.Join(tmpDir(), fmt.Sprintf("factorytest-cache-%d", uid))
	li, err := os.Lstat(base)
	if err != nil || li.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	fi, err := os.Stat(base)
	if err != nil || !fi.IsDir() {
		return nil
	}
	if sys, ok := fi.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != uid {
		return nil
	}
	dirents, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	type ent struct {
		m float64
		d string
	}
	var ents []ent
	for _, e := range dirents {
		d := filepath.Join(base, e.Name())
		st, err := os.Lstat(d)
		if err != nil {
			return nil // Python: any OSError in the scan returns []
		}
		sys, _ := st.Sys().(*syscall.Stat_t)
		if st.IsDir() && sys != nil && int(sys.Uid) == uid {
			m, err := harnessNewestMTime(d)
			if err != nil {
				return nil
			}
			ents = append(ents, ent{m, d})
		}
	}
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].m > ents[j].m })
	type oldEnt struct{ d, real string }
	var old []oldEnt
	if len(ents) > tmpFtcacheKeep {
		for _, e := range ents[tmpFtcacheKeep:] {
			if t-e.m >= tmpFtcacheMinAgeS {
				old = append(old, oldEnt{e.d, pyRealpath(e.d)})
			}
		}
	}
	if len(old) == 0 {
		return nil
	}
	roots := make([]string, len(old))
	for i, o := range old {
		roots[i] = o.real
	}
	held, ok, herr := procHolds(procRoot(a), roots, tmpIgnoreComms, new([]string))
	if herr != nil || !ok {
		return nil
	}
	var out []leftover
	for _, o := range old {
		if held[o.real] {
			continue
		}
		size := tmpTreeBytes(o.d)
		if os.RemoveAll(o.d) != nil {
			continue // Python: OSError keeps going
		}
		out = append(out, leftover{o.d, size})
	}
	return out
}

func firstOr(l []string, d string) string {
	if len(l) > 0 {
		return l[0]
	}
	return d
}

// tmpSweep is tmp_sweep: while the tmp filesystem is over tmp_sweep_pct, delete the scratch of finished agents and
// stale leftovers, write one tmp-swept event, and alert the director once if it is still over tmp_max_pct. Any failure
// is a tmp-sweep-failed event, never a stopped tick.
func tmpSweep(a *alertEnv) error {
	err := tmpSweepRun(a)
	if err == nil {
		return nil
	}
	_ = a.S.EventAs("tick", "tick", "tmp-sweep", "tmp-sweep-failed", err.Error(), "") // inner try/except pass
	return nil
}

func tmpSweepRun(a *alertEnv) error {
	s := a.S
	tmp := tmpUsage()
	if tmp == nil {
		return nil
	}
	sweepPct, err := cfgFloat(s, "tmp_sweep_pct")
	if err != nil {
		return err
	}
	if tmp.Pct > sweepPct {
		t := alertNow()
		var freed int64
		n := 0
		cands, err := sweepCandidates(s, a.Home, t)
		if err != nil {
			return err
		}
		for _, c := range cands {
			size := tmpTreeBytes(c.Dir)
			if rerr := os.RemoveAll(c.Dir); rerr != nil {
				if eerr := s.EventAs("tick", "task", c.Task, "tmp-sweep-failed", c.Dir+": "+pyErrText(rerr), ""); eerr != nil {
					return eerr
				}
				continue
			}
			freed += size
			n++
		}
		var left []leftover
		locked, blocked := 0, false
		lastTS, has, err := throttleTS(s.DB, "tmpleftover")
		if err != nil {
			return err
		}
		skip := false
		if has {
			lf, ok := pyFloat(lastTS)
			if !ok {
				return fmt.Errorf("could not convert string to float: %s", pyRepr(lastTS))
			}
			skip = t-lf < tmpLeftoverThrottleS
		}
		if !skip {
			if _, err := s.DB.Exec("INSERT INTO throttle(key,ts) VALUES('tmpleftover',?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts",
				store.PyFloatRepr(t)); err != nil {
				return fmt.Errorf("write throttle tmpleftover: %w", err)
			}
			if left, locked, blocked, err = sweepLeftovers(a, t); err != nil {
				return err
			}
			left = append(left, sweepFactorytestCache(a, t)...)
		}
		for _, l := range left {
			freed += l.Size
		}
		if n > 0 || len(left) > 0 || (locked > 0 && !blocked) {
			detail := store.Ordered{{K: "mb", V: pyRound1(float64(freed) / tmpMB)}, {K: "dirs", V: n},
				{K: "leftovers", V: len(left)}, {K: "locked", V: locked}}
			if err := s.EventAs("tick", "tick", "tmp-sweep", "tmp-swept", detail, ""); err != nil {
				return err
			}
		}
		if again := tmpUsage(); again != nil {
			tmp = again
		}
	}
	maxPct, err := cfgFloat(s, "tmp_max_pct")
	if err != nil {
		return err
	}
	over := tmp.Pct >= maxPct
	cur, _, err := configRow(s.DB, "tmp_sweep_alerted")
	if err != nil {
		return err
	}
	armed := cur == "1"
	if over && !armed {
		pct, _ := s.Cfg("tmp_max_pct")
		body := fmt.Sprintf("The tick swept the scratch of finished agents but /tmp uses %d/%d MB, over tmp_max_pct %s%%: spawns stay "+
			"held and nothing more is safe to delete automatically. Find what holds the space (`du -sh /tmp/* | sort -h`) and ask "+
			"the owner or boss.", tmp.UsedMB, tmp.TotalMB, pct)
		if _, _, err := s.Send(store.Msg{To: "director", Subject: fmt.Sprintf("tmp sweep: /tmp still %.1f%% full", tmp.Pct),
			Body: &body, Kind: "alert", Sender: alertTickActor}); err != nil {
			return err
		}
	}
	if over != armed {
		v := "0"
		if over {
			v = "1"
		}
		return setConfig(s.DB, "tmp_sweep_alerted", v)
	}
	return nil
}

// throttleTS is the ts of a throttle row.
func throttleTS(db *sql.DB, key string) (string, bool, error) {
	var v sql.NullString
	err := db.QueryRow("SELECT ts FROM throttle WHERE key=?", key).Scan(&v)
	switch {
	case err == nil:
		return v.String, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	}
	return "", false, fmt.Errorf("read throttle %s: %w", key, err)
}
