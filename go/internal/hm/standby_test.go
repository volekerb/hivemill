package hm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStandbyCheckRestoresToTemp(t *testing.T) {
	primary, _ := testRunner(t, "")
	taken := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if _, err := primary.Backup(taken.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	snap, err := primary.Backup(taken)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}

	r, s := testRunner(t, "")
	own, _ := os.ReadFile(r.Env.DBPath())
	var remote, ran []string
	r.SSH = func(host, _ string, _ time.Duration, command string) (int, string, string) {
		remote = append(remote, host+" "+command)
		if strings.Contains(command, "ls -1 -- ") {
			return 0, "factory-20261120T090000Z.db\n" + filepath.Base(snap) + "\nzz-other.db\n", ""
		}
		return 0, string(data), ""
	}
	var tempHome string
	fail := ""
	r.Tools.Sh = func(_ string, _ time.Duration, env []string, argv ...string) (int, string) {
		if argv[0] != "zz-hivemill" {
			return 0, ""
		}
		for _, kv := range env { // the last value wins
			if v, ok := strings.CutPrefix(kv, "FACTORY_HOME="); ok {
				tempHome = v
			}
		}
		sum, err := CheckSnapshot((Env{Home: tempHome}).DBPath())
		if err != nil || !strings.Contains(sum, "1 tasks") {
			t.Errorf("restored DB: %q %v", sum, err)
		}
		cmd := strings.Join(argv[1:], " ")
		ran = append(ran, cmd)
		if cmd == fail {
			return 1, "zz broken"
		}
		return 0, ""
	}
	msg, err := r.StandbyCheck("zz-primary", "zz-hivemill", taken.Add(3*time.Hour))
	if err != nil || !strings.HasPrefix(msg, "OK: "+filepath.Base(snap)) || !strings.Contains(msg, "snapshot age 3h0m0s") {
		t.Fatalf("check: %q %v", msg, err)
	}
	if strings.Join(ran, ",") != "run status,goal list" {
		t.Errorf("commands: %v", ran)
	}
	if len(remote) != 2 || !strings.HasPrefix(remote[1], "zz-primary ") || !strings.Contains(remote[1], "cat -- ") ||
		!strings.Contains(remote[1], DefaultRemoteBackups+"/"+filepath.Base(snap)) {
		t.Errorf("ssh: %q", remote)
	}
	if tempHome == "" || tempHome == r.Env.Home {
		t.Errorf("temp home %q", tempHome)
	}
	if _, err := os.Stat(tempHome); !os.IsNotExist(err) {
		t.Errorf("the temp home stays: %v", err)
	}
	if now, _ := os.ReadFile(r.Env.DBPath()); string(now) != string(own) {
		t.Error("the check changed the home's own DB")
	}
	// a local directory (a mounted share) works too
	if _, err := r.StandbyCheck(filepath.Dir(snap), "zz-hivemill", taken); err != nil {
		t.Errorf("local dir: %v", err)
	}

	// the daemon step: off without standby_from, then once per standby_check_days, with an alert on failure
	if r.StandbyDue(taken) {
		t.Error("due without standby_from")
	}
	if _, err := s.db.Exec(FactorySchema); err != nil { // the alert feed table
		t.Fatal(err)
	}
	if err := s.SetCfg("standby_from", "zz-primary"); err != nil {
		t.Fatal(err)
	}
	pass := func() { // start the check, then collect its result as a later pass does
		t.Helper()
		r.StandbyPass("zz-hivemill", taken)
		for end := time.Now().Add(10 * time.Second); r.standby != nil; time.Sleep(time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("the standby check never ended")
			}
			r.StandbyPass("zz-hivemill", taken)
		}
	}
	open := func() (n int) {
		t.Helper()
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM owner_alert_feed WHERE kind='hivemill-standby' AND closed IS NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	fail = "goal list"
	pass()
	if open() != 1 {
		t.Error("no open alert row after a failed check")
	}
	if b, _ := os.ReadFile(r.Env.AlertLog()); !strings.Contains(string(b), "hivemill-standby") || !strings.Contains(string(b), "goal list") {
		t.Errorf("alert log: %q", b)
	}
	if r.StandbyDue(taken.Add(29 * 24 * time.Hour)) {
		t.Error("due after 29 days")
	}
	if !r.StandbyDue(taken.Add(30 * 24 * time.Hour)) {
		t.Error("not due after 30 days")
	}
	fail = ""
	r.StandbyPassed("zz-other", taken)
	if open() != 1 {
		t.Error("a check of another source closed the alert")
	}
	// a good check by hand closes it too and restarts the period
	later := taken.Add(30 * 24 * time.Hour)
	r.StandbyPassed("zz-primary", later)
	if open() != 0 {
		t.Error("a good check leaves the alert row open")
	}
	if r.StandbyDue(later) {
		t.Error("due right after a good check by hand")
	}
}

// A source that never answers (a hung mount) ends as a failed check.
func TestStandbyPassGivesUp(t *testing.T) {
	r, s := testRunner(t, "")
	old := standbyLimit
	standbyLimit = 10 * time.Millisecond
	t.Cleanup(func() { standbyLimit = old })
	hang, left := make(chan struct{}), make(chan struct{})
	r.SSH = func(string, string, time.Duration, string) (int, string, string) {
		defer close(left)
		<-hang
		return 1, "", "zz gone"
	}
	t.Cleanup(func() { close(hang); <-left })
	if err := s.SetCfg("standby_from", "zz-primary"); err != nil {
		t.Fatal(err)
	}
	r.StandbyPass("zz-hivemill", time.Now())
	for end := time.Now().Add(10 * time.Second); r.standby != nil; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the standby check never ended")
		}
		r.StandbyPass("zz-hivemill", time.Now())
	}
	if b, _ := os.ReadFile(r.Env.AlertLog()); !strings.Contains(string(b), "not finished after") {
		t.Errorf("alert log: %q", b)
	}
}

// writeForeignLock is the daemon lock of another machine with a heartbeat age ago.
func writeForeignLock(t *testing.T, r *Runner, age time.Duration) {
	t.Helper()
	if err := writeDaemonLock(r.Env.DaemonLockPath(), "zz-primary", time.Now().Add(-age)); err != nil {
		t.Fatal(err)
	}
}

func lockHost(r *Runner) string {
	l, _, _ := readDaemonLock(r.Env.DaemonLockPath(), time.Now())
	if l == nil {
		return ""
	}
	return l.Host
}

func TestDaemonRefusesSecondWriter(t *testing.T) {
	r, _ := testRunner(t, "")
	writeForeignLock(t, r, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Daemon(ctx); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "zz-primary") {
		t.Fatalf("fresh foreign lock: %v", err)
	}
	if lockHost(r) != "zz-primary" {
		t.Error("a refused daemon changed the lock")
	}
	// an unreadable lock refuses while its file is fresh
	_ = os.WriteFile(r.Env.DaemonLockPath(), nil, 0o600)
	if err := r.claimDaemonLock("zz-standby", time.Now()); !errors.Is(err, ErrRefused) {
		t.Errorf("empty lock: %v", err)
	}
	if err := r.claimDaemonLock("zz-standby", time.Now().Add(time.Hour)); err != nil {
		t.Errorf("old empty lock: %v", err)
	}
	// a stale heartbeat and this machine's own (a crashed daemon) do not block
	writeForeignLock(t, r, time.Hour)
	if err := r.claimDaemonLock("zz-standby", time.Now()); err != nil {
		t.Errorf("stale lock: %v", err)
	}
	if err := r.claimDaemonLock("zz-standby", time.Now()); err != nil {
		t.Errorf("own lock: %v", err)
	}
}

func TestDaemonTakeOver(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetCfg("pass_interval_s", "3600")
	writeForeignLock(t, r, time.Minute)
	r.TakeOver = true
	host, release, err := r.holdDaemonLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lockHost(r) != host {
		t.Errorf("lock names %q, want %q", lockHost(r), host)
	}
	// the old daemon sees the take-over at its next pass and stops writing
	if err := r.refreshDaemonLock("zz-primary", time.Now()); !errors.Is(err, ErrRefused) {
		t.Errorf("old daemon refresh: %v", err)
	}
	if lockHost(r) != host {
		t.Error("the old daemon overwrote the lock")
	}
	release()
	if _, err := os.Stat(r.Env.DaemonLockPath()); !os.IsNotExist(err) {
		t.Errorf("a clean stop leaves the lock: %v", err)
	}
	// the new owner is gone again: the old daemon still may not claim the home back
	if err := r.refreshDaemonLock("zz-primary", time.Now()); !errors.Is(err, ErrRefused) || lockHost(r) != "" {
		t.Errorf("old daemon after the new one stopped: %v, lock %q", err, lockHost(r))
	}

	writeForeignLock(t, r, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Daemon(ctx); err != nil {
		t.Fatalf("daemon --take-over: %v", err)
	}
	if lockHost(r) != "" {
		t.Errorf("lock after stop: %q", lockHost(r))
	}
}
