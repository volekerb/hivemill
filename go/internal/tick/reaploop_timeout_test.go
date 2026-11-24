package tick

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Timeouts and caps carried over from Python (go checklist d):
//
//	python bin/factory_cli.py                          go
//	HERDR_TIMEOUT_S = 90 (herdr(), hc ls :1581)        herdr.DefaultTimeout; hcTimeout units_reaploop.go; herdrCallTimeout reapstate.go
//	NUDGE_PENDING_TTL_S = 600 (:14628)                 nudgePendingTTLS units_nudges.go
//	SETTINGS_ESC_COOLDOWN_S = 300 (:17354)             settingsEscCooldownS dialog_act.go
//	SETTINGS_ESC_MAX_PER_DAY = 3 (:17355)              settingsEscMaxPerDay dialog_act.go
//	STARTUP_GRACE_S = 180, CURSOR_START_LOG_MAX_S=600  units_reaploop.go (TestPythonConstants quotes the Python lines)

const hangSh = "#!/bin/sh\nexec sleep 30\n"

// TestHangingHcTimesOut: a hanging "hc ls" ends at the timeout and reads as "cannot be read", it never blocks the tick.
func TestHangingHcTimesOut(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "hc"), []byte(hangSh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := hcTimeout
	hcTimeout = 300 * time.Millisecond
	t.Cleanup(func() { hcTimeout = prev })
	start := time.Now()
	out, failed := hcRemoteTerminals(&UnitCtx{})
	if !failed || out != nil {
		t.Errorf("out=%v failed=%v, want nil,true", out, failed)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("hc ls took %v, the timeout did not fire", d)
	}
}

// TestHangingHostLeavesRowAlone: a hanging host call times out as an unreadable host: the done row stays 'done' and the
// unit returns nil.
func TestHangingHostLeavesRowAlone(t *testing.T) {
	u := newUnitFix(t)
	u.reapRow("coder-1", "coder", "done", "p-1")
	if err := os.WriteFile(filepath.Join(u.stub.dir, "bin", "herdr"), []byte(hangSh), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := herdrCallTimeout
	herdrCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { herdrCallTimeout = prev })
	start := time.Now()
	if err := u.run("reap_loop", loopState(t, u.now, hostAgent("coder-1", "p-1", "done", "s-coder-1"))); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("reap_loop took %v, the host timeout did not fire", d)
	}
	if got := u.col("SELECT status FROM agents WHERE name='coder-1'"); got != "done" {
		t.Errorf("status = %s, want done", got)
	}
}
