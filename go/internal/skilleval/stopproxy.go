package skilleval

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/sandbox"
)

// StopProxy kills the pid in <trial>/proxy.pid if it is still our egress proxy (nsbox.stop_proxy). Idempotent:
// SIGTERM, up to 50 x 50 ms of grace, then SIGKILL; the pid file is removed. Nothing here can fail a collect.
func StopProxy(trialDir string) {
	pidfile := filepath.Join(trialDir, "proxy.pid")
	raw, err := os.ReadFile(pidfile)
	if err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && pid > 0 {
			if cmd, cerr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline"); cerr == nil &&
				bytes.Contains(cmd, []byte("egress.py")) && sandbox.PidAlive(pid) {
				signalAndWait(pid)
			}
		}
	}
	if rerr := os.Remove(pidfile); rerr != nil {
		return // a missing pid file is the normal case (missing_ok=True)
	}
}

func signalAndWait(pid int) {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return // already gone
	}
	for i := 0; i < 50; i++ {
		if !sandbox.PidAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return // died in between
	}
}
