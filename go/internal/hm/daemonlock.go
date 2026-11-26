package hm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The daemon lock (<state>/daemon.lock) names the machine whose daemon writes this home, and when it last said so. The
// flock in <state>/locks only stops a second daemon on one machine; this file also stops one on another machine that
// sees the same home (docs/standby.md). A daemon refuses to start on another host's fresh heartbeat unless TakeOver.

// DaemonLock is the content of the daemon lock file.
type DaemonLock struct {
	Host string `json:"host"`
	Time string `json:"time"`
}

const (
	daemonLockFresh = 15 * time.Minute // the watchdog's limit too; at least 3 passes
	daemonLockEvery = time.Minute      // refresh between passes: a pass can outlast daemonLockFresh
)

// DaemonLockPath is the daemon lock file of a home.
func (e Env) DaemonLockPath() string { return filepath.Join(e.StateDir(), "daemon.lock") }

// readDaemonLock returns the lock and the age of its heartbeat; nil without a file. An unreadable content has an empty
// Host and the age of the file, so a half-written file after a crash goes stale like any other.
func readDaemonLock(path string, now time.Time) (*DaemonLock, time.Duration, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var l DaemonLock
	if json.Unmarshal(b, &l) == nil && l.Host != "" {
		if t, err := time.Parse(time.RFC3339, l.Time); err == nil {
			return &l, now.Sub(t), nil
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	return &DaemonLock{}, now.Sub(fi.ModTime()), nil
}

func writeDaemonLock(path, host string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".daemon.lock-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op after the rename
	err = json.NewEncoder(f).Encode(DaemonLock{Host: host, Time: now.UTC().Format(time.RFC3339)})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// claimDaemonLock writes the first heartbeat of this daemon, or refuses.
func (r *Runner) claimDaemonLock(host string, now time.Time) error {
	path := r.Env.DaemonLockPath()
	l, age, err := readDaemonLock(path, now)
	if err != nil {
		return fmt.Errorf("daemon lock: %w", err)
	}
	fresh := max(daemonLockFresh, 3*time.Duration(cfgInt(r.Store, "pass_interval_s"))*time.Second)
	// the same host's record is a dead daemon: a live one would still hold the flock
	if l != nil && l.Host != host && age <= fresh && !r.TakeOver {
		who := l.Host
		if who == "" {
			who = "an unknown host (unreadable lock file)"
		}
		return fmt.Errorf("%w: a hivemill daemon on %s wrote %s %s ago; stop it, or start with --take-over when it is dead",
			ErrRefused, who, path, age.Round(time.Second))
	}
	return writeDaemonLock(path, host, now)
}

// refreshDaemonLock renews the heartbeat. It refuses when another host took the lock: this daemon must stop writing.
func (r *Runner) refreshDaemonLock(host string, now time.Time) error {
	path := r.Env.DaemonLockPath()
	l, _, err := readDaemonLock(path, now)
	if err != nil {
		return fmt.Errorf("daemon lock: %w", err)
	}
	if l != nil && l.Host != "" && l.Host != host {
		return fmt.Errorf("%w: the daemon on %s took over this home", ErrRefused, l.Host)
	}
	return writeDaemonLock(path, host, now)
}

// holdDaemonLock claims the lock and keeps it fresh. release stops the refresher and removes the file if it is still
// ours, so a clean stop hands the home over at once.
func (r *Runner) holdDaemonLock(ctx context.Context) (host string, release func(), err error) {
	if host, err = os.Hostname(); err != nil || host == "" {
		return "", nil, fmt.Errorf("daemon lock: no host name: %v", err)
	}
	if err = r.claimDaemonLock(host, time.Now()); err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(daemonLockEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				_ = r.refreshDaemonLock(host, now) // the pass loop reports a take-over
			}
		}
	}()
	return host, func() {
		cancel()
		<-done
		path := r.Env.DaemonLockPath()
		if l, _, err := readDaemonLock(path, time.Now()); err == nil && l != nil && l.Host == host {
			_ = os.Remove(path)
		}
	}, nil
}
