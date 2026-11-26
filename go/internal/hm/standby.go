package hm

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The warm standby check (docs/standby.md): on the standby machine, pull the primary's newest snapshot, restore it into
// a temporary home and run two read commands against it.

// DefaultRemoteBackups is the snapshot directory when --from names only a host: relative to the remote home.
const DefaultRemoteBackups = "factory/state/hivemill/backups"

var snapshotRe = regexp.MustCompile(`^factory-(\d{8}T\d{6}Z)\.db$`)

// newestSnapshot picks the newest snapshot name (the UTC stamp sorts by time).
func newestSnapshot(names []string) (string, time.Time, bool) {
	var best string
	for _, n := range names {
		if n = strings.TrimSpace(n); snapshotRe.MatchString(n) && n > best {
			best = n
		}
	}
	if best == "" {
		return "", time.Time{}, false
	}
	t, err := time.Parse("20060102T150405Z", snapshotRe.FindStringSubmatch(best)[1])
	return best, t, err == nil
}

// fetchSnapshot copies the newest snapshot of from to dst. from is an ssh host, HOST:DIR, or a local directory that
// starts with / . or ~/ or contains a slash.
func (r *Runner) fetchSnapshot(from, dst string) (string, time.Time, error) {
	host, dir, err := ParsePullFrom(from)
	if err != nil {
		return "", time.Time{}, err
	}
	if host == "" && !strings.ContainsAny(dir[:1], "/.~") && !strings.Contains(dir, "/") {
		host, dir = dir, DefaultRemoteBackups // a bare HOST
	} else if host != "" && dir == DefaultRemoteLog {
		dir = DefaultRemoteBackups // "HOST:"
	}
	var names []string
	if rest, ok := strings.CutPrefix(dir, "~/"); host == "" && ok {
		dir = filepath.Join(r.Env.UserHome, rest)
	}
	if host == "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", time.Time{}, err
		}
		for _, e := range entries {
			names = append(names, e.Name())
		}
	} else {
		dir = strings.TrimPrefix(dir, "~/") // ssh runs the command in the remote home
		out, err := r.rshOK(host, "", 2*time.Minute, "ls -1 -- "+shellQuote(dir))
		if err != nil {
			return "", time.Time{}, err
		}
		names = strings.Split(out, "\n")
	}
	name, taken, ok := newestSnapshot(names)
	if !ok {
		return "", time.Time{}, fmt.Errorf("no factory-*.db snapshot in %s (is backup_interval set on the primary?)", from)
	}
	if host == "" {
		return name, taken, copyFile(filepath.Join(dir, name), dst)
	}
	data, err := r.rshOK(host, "", 10*time.Minute, "cat -- "+shellQuote(path.Join(dir, name)))
	if err != nil {
		return "", time.Time{}, err
	}
	return name, taken, os.WriteFile(dst, []byte(data), 0o600)
}

// StandbyCheck restores the newest snapshot of from into a temporary home, runs `run status` and `goal list` of the
// binary bin against it and returns the OK line. The home of this runner is not touched.
func (r *Runner) StandbyCheck(from, bin string, now time.Time) (string, error) {
	// a check cut off by a stop leaves its copy of the DB behind; no check runs for an hour
	old, _ := filepath.Glob(filepath.Join(os.TempDir(), "hivemill-standby-*"))
	for _, d := range old {
		if st, err := os.Stat(d); err == nil && time.Since(st.ModTime()) > time.Hour {
			_ = os.RemoveAll(d)
		}
	}
	tmp, err := os.MkdirTemp("", "hivemill-standby-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, "snapshot.db")
	name, taken, err := r.fetchSnapshot(from, snap)
	if err != nil {
		return "", err
	}
	e := Env{Home: filepath.Join(tmp, "home"), UserHome: r.Env.UserHome}
	dbp := filepath.Join(e.Home, "db", "factory.db")
	if err := os.MkdirAll(filepath.Dir(dbp), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(dbp, nil, 0o600); err != nil { // Restore keeps the DB it replaces
		return "", err
	}
	if _, err := Restore(e, snap, false, false); err != nil {
		return "", err
	}
	// the caller's identity stays; a worker machine must not forward these reads to its coordinator's DB
	env := append(os.Environ(), "FACTORY_HOME="+e.Home, "HIVEMILL_FORWARDED=1")
	for _, args := range [][]string{{"run", "status"}, {"goal", "list"}} {
		if rc, out := r.Tools.Sh(tmp, 2*time.Minute, env, append([]string{bin}, args...)...); rc != 0 {
			return "", fmt.Errorf("`hivemill %s` on the restored %s: exit %d: %s", strings.Join(args, " "), name, rc,
				firstLine(lastLines(out, 2)))
		}
	}
	return fmt.Sprintf("OK: %s restored and read, snapshot age %s", name, now.Sub(taken).Round(time.Minute)), nil
}

// StandbyDue reports whether the daemon should run the standby check: standby_from set, and the last one at least
// standby_check_days ago (0 = off).
func (r *Runner) StandbyDue(now time.Time) bool {
	days := cfgInt(r.Store, "standby_check_days")
	if strings.TrimSpace(r.Store.Cfg("standby_from")) == "" || days <= 0 {
		return false
	}
	last, err := strconv.ParseInt(r.Store.Meta("last_standby_check"), 10, 64)
	return err != nil || now.Sub(time.Unix(last, 0)) >= time.Duration(days)*24*time.Hour
}

// StandbyPassed records a good check of the configured source: the alert closes and the next one is a full period away.
// A check of another source says nothing about standby_from.
func (r *Runner) StandbyPassed(from string, now time.Time) {
	if strings.TrimSpace(from) != strings.TrimSpace(r.Store.Cfg("standby_from")) {
		return
	}
	_ = r.Store.SetMeta("last_standby_check", strconv.FormatInt(now.Unix(), 10))
	_ = r.Store.CloseAlert("hivemill-standby", "standby")
}

// standbyLimit is how long the daemon waits for a check, above the sum of its command timeouts (tests replace it).
var standbyLimit = 30 * time.Minute

// standbyResult is the outcome of a check that ran beside the pass loop.
type standbyResult struct {
	from, msg string
	err       error
}

// StandbyPass is the daemon's standby step. A due check runs beside the pass loop (a slow link must not hold up the
// passes, the watchdog or SIGTERM); a later pass records its result and alerts the owner when it failed. A check cut off
// by a stop is not recorded, so it runs again. A failed check waits standby_check_days too; `hivemill standby check`
// runs it again at once.
func (r *Runner) StandbyPass(bin string, now time.Time) {
	if r.standby == nil {
		if !r.StandbyDue(now) {
			return
		}
		ch := make(chan standbyResult, 1)
		r.standby = ch
		from := strings.TrimSpace(r.Store.Cfg("standby_from"))
		limit := standbyLimit
		go func() { // StandbyCheck does not use the store
			done := make(chan standbyResult, 1)
			go func() {
				msg, err := r.StandbyCheck(from, bin, now)
				done <- standbyResult{from, msg, err}
			}()
			select {
			case res := <-done:
				ch <- res
			case <-time.After(limit): // a hung mount has no timeout of its own, and would silence the check for good
				ch <- standbyResult{from: from, err: fmt.Errorf("not finished after %s", limit)}
			}
		}()
		return
	}
	var res standbyResult
	select {
	case res = <-r.standby:
		r.standby = nil
	default:
		return // still running
	}
	_ = r.Store.SetMeta("last_standby_check", strconv.FormatInt(now.Unix(), 10))
	if res.err != nil {
		res.msg = res.err.Error()
	}
	_ = r.Log.Emit("standby.check", nil, Event{Data: map[string]any{"ok": res.err == nil, "result": res.msg}})
	r.printf("standby check: %s\n", res.msg)
	if res.err == nil {
		r.StandbyPassed(res.from, now)
		return
	}
	_ = r.Store.AlertFeed("hivemill-standby", "standby", "hivemill standby check")
	r.ownerAlert("hivemill-standby", fmt.Sprintf("standby check from %s failed: %v", res.from, res.err))
}
