package hm

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Backup writes a consistent snapshot of the factory DB with SQLite VACUUM INTO (no extra binary), keeps the newest
// backup_keep snapshots, and copies the snapshot to a file:// backup_target. It returns the snapshot path.
func (r *Runner) Backup(now time.Time) (string, error) {
	dir := r.backupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	snap := filepath.Join(dir, "factory-"+now.UTC().Format("20060102T150405Z")+".db")
	s, ok := r.Store.(*SQLite)
	if !ok {
		return "", fmt.Errorf("backup needs the SQLite store")
	}
	// under another name until complete: a standby check or a restore must never read a half-written snapshot
	part := snap + ".part"
	_ = os.Remove(part)
	defer os.Remove(part) // no-op after the rename
	if _, err := s.db.Exec(`VACUUM INTO ?`, part); err != nil {
		return "", fmt.Errorf("VACUUM INTO %s: %w", part, err)
	}
	_ = os.Chmod(part, 0o600)
	if err := os.Rename(part, snap); err != nil {
		return "", err
	}
	if err := pruneSnapshots(dir, cfgInt(r.Store, "backup_keep")); err != nil {
		return snap, err
	}
	if t := strings.TrimSpace(r.Store.Cfg("backup_target")); strings.HasPrefix(t, "file://") {
		u, err := url.Parse(t)
		if err != nil || u.Path == "" {
			return snap, fmt.Errorf("backup_target %q: want file:///absolute/path", t)
		}
		if err := os.MkdirAll(u.Path, 0o700); err != nil {
			return snap, err
		}
		if err := copyFile(snap, filepath.Join(u.Path, filepath.Base(snap))); err != nil {
			return snap, err
		}
		_ = pruneSnapshots(u.Path, cfgInt(r.Store, "backup_keep"))
	}
	_ = r.Store.SetMeta("last_backup", strconv.FormatInt(now.Unix(), 10))
	_ = r.Log.Emit("db.backup", nil, Event{Data: map[string]any{"snapshot": snap}})
	return snap, nil
}

func (r *Runner) backupDir() string {
	if d := strings.TrimSpace(r.Store.Cfg("backup_dir")); d != "" {
		return d
	}
	return filepath.Join(r.Env.StateDir(), "backups")
}

func pruneSnapshots(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	m, _ := filepath.Glob(filepath.Join(dir, "factory-*.db"))
	sort.Strings(m) // the UTC stamp sorts by time
	for i := 0; i < len(m)-keep; i++ {
		if err := os.Remove(m[i]); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// BackupDue reports whether the daemon should take a scheduled snapshot (backup_interval, e.g. 6h; off = never).
func (r *Runner) BackupDue(now time.Time) bool {
	iv := strings.TrimSpace(r.Store.Cfg("backup_interval"))
	if iv == "" || iv == "off" {
		return false
	}
	d, err := time.ParseDuration(iv)
	if err != nil || d <= 0 {
		return false
	}
	last, err := strconv.ParseInt(r.Store.Meta("last_backup"), 10, 64)
	return err != nil || now.Sub(time.Unix(last, 0)) >= d
}

// CheckSnapshot opens a snapshot read-only and runs integrity_check; it returns a one-line summary.
func CheckSnapshot(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String())
	if err != nil {
		return "", err
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil {
		return "", err
	}
	if ok != "ok" {
		return "", fmt.Errorf("integrity_check: %s", ok)
	}
	var tasks, runs int
	_ = db.QueryRow(`SELECT count(*) FROM tasks`).Scan(&tasks)
	_ = db.QueryRow(`SELECT count(*) FROM hm_runs`).Scan(&runs)
	return fmt.Sprintf("integrity ok, %d tasks, %d runs", tasks, runs), nil
}

// Restore replaces the factory DB with a snapshot after checking it. The current DB is kept as <db>.pre-restore-<ts>.
// It refuses while a hivemill daemon runs; stop the daemon (and the factory tick) first.
func Restore(e Env, snapshot string, dryRun bool, daemonRunning bool) (string, error) {
	sum, err := CheckSnapshot(snapshot)
	if err != nil {
		return "", fmt.Errorf("snapshot %s refused: %w", snapshot, err)
	}
	dbp := e.DBPath()
	if dryRun {
		return fmt.Sprintf("dry run: %s; would replace %s (kept as %s.pre-restore-<ts>)", sum, dbp, dbp), nil
	}
	if daemonRunning {
		return "", fmt.Errorf("%w: a hivemill daemon is running; stop it (systemctl --user stop hivemill.service) and the factory tick first", ErrRefused)
	}
	keep := dbp + ".pre-restore-" + time.Now().UTC().Format("20060102T150405Z")
	if err := copyFile(dbp, keep); err != nil {
		return "", err
	}
	for _, x := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbp + x)
	}
	if err := copyFile(snapshot, dbp); err != nil {
		return "", err
	}
	return fmt.Sprintf("restored %s from %s (%s); the old DB is %s", dbp, snapshot, sum, keep), nil
}

// ReplicationPlan says how backup_target is served: litestream (continuous) when it is installed and the target is
// s3:// or sftp://, else scheduled snapshots (file:// targets get a copy; s3/sftp without litestream are local only).
func (r *Runner) ReplicationPlan() string {
	t := strings.TrimSpace(r.Store.Cfg("backup_target"))
	switch {
	case t == "" || t == "off":
		return "snapshots only (" + r.backupDir() + ")"
	case strings.HasPrefix(t, "file://"):
		return "snapshots, copied to " + t
	case strings.HasPrefix(t, "s3://") || strings.HasPrefix(t, "sftp://"):
		if _, err := exec.LookPath("litestream"); err == nil {
			return "continuous with litestream to the configured target (systemd/hivemill-litestream.service)"
		}
		return "litestream not installed: snapshots stay local; install litestream for " + strings.SplitN(t, "://", 2)[0] + " replication"
	}
	return "backup_target must be file://, s3:// or sftp://"
}
