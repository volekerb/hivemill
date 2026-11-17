package gate

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "darwin", name))
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "#") { // the "# synthetic" marker line
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func TestDarwinLoad(t *testing.T) {
	if v, err := ParseDarwinLoad(fixture(t, "loadavg")); err != nil || v != 3.71 {
		t.Fatalf("load = %v, %v", v, err)
	}
	if v, err := ParseDarwinLoad("{ 0.5 0.4 0.3 }\n"); err != nil || v != 0.5 {
		t.Fatalf("plain = %v, %v", v, err)
	}
}

func TestDarwinVMStat(t *testing.T) {
	// (11361+205358+980) pages x 16384 B = 3401 MB (MacBook M0 2026-11-15)
	if v, err := ParseDarwinVMStat(fixture(t, "vm_stat")); err != nil || v != 3401 {
		t.Fatalf("vm_stat = %v, %v; want 3401", v, err)
	}
}

func TestDarwinSwap(t *testing.T) {
	if v, err := ParseDarwinSwap(fixture(t, "swapusage")); err != nil || v != 5070 {
		t.Fatalf("swap = %v, %v", v, err)
	}
	if v, err := ParseDarwinSwap("used = 1.50G"); err != nil || v != 1536 {
		t.Fatalf("swap G = %v, %v", v, err)
	}
}

func TestDarwinPressure(t *testing.T) {
	for f, want := range map[string]int{"pressure_normal": 1, "pressure_warn": 2, "pressure_critical": 4} {
		if v, err := ParseDarwinPressure(fixture(t, f)); err != nil || v != want {
			t.Fatalf("%s = %v, %v", f, v, err)
		}
	}
}

func TestDarwinStatfs(t *testing.T) {
	tm := TmpFromStatfs(100<<30, 40<<30, "apfs", "/System/Volumes/Data")
	if tm == nil || tm.FSType != "apfs" || tm.Mount != "/System/Volumes/Data" || tm.FreeMB != 40960 || tm.Pct != 60.0 {
		t.Fatalf("tmp = %+v", tm)
	}
}

// fakeDarwin feeds recorded text through the injected runner.
func fakeDarwin(out map[string]string, fail bool) DarwinFacts {
	return DarwinFacts{CPUs: 8,
		Run: func(name string, args ...string) (string, error) {
			if fail {
				return "", errors.New("boom")
			}
			return out[name+" "+strings.Join(args, " ")], nil
		},
		Statfs: func(string) (int64, int64, string, string, error) {
			if fail {
				return 0, 0, "", "", errors.New("boom")
			}
			return 100 << 30, 50 << 30, "apfs", "/", nil
		}}
}

func recorded(t *testing.T) map[string]string {
	return map[string]string{
		"sysctl -n vm.loadavg":   fixture(t, "loadavg"),
		"vm_stat ":               fixture(t, "vm_stat"),
		"sysctl -n vm.swapusage": fixture(t, "swapusage"),
		"sysctl -n kern.memorystatus_vm_pressure_level": fixture(t, "pressure_normal"),
	}
}

func TestDarwinFactsReaders(t *testing.T) {
	h := fakeDarwin(recorded(t), false)
	var s Snapshot
	if errs := ApplyHostFacts(&s, h, "/tmp"); len(errs) != 0 {
		t.Fatal(errs)
	}
	if s.MemMB != 3401 || s.Load1 != 3.71 || s.NumCPU != 8 || s.MemPressure != 1 || s.Tmp == nil || s.TmpMinFreeMB != TmpMinFreeMB {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestDarwinUnreadable(t *testing.T) {
	garbage := fakeDarwin(map[string]string{}, false)
	t.Run("load", func(t *testing.T) {
		for _, h := range []DarwinFacts{fakeDarwin(nil, true), garbage} {
			if _, err := h.Load1(); err == nil {
				t.Fatal("load must be unknown")
			}
		}
	})
	t.Run("vmstat", func(t *testing.T) {
		for _, h := range []DarwinFacts{fakeDarwin(nil, true), garbage} {
			if _, err := h.MemAvailableMB(); err == nil {
				t.Fatal("memory must be unknown")
			}
		}
	})
	t.Run("swap", func(t *testing.T) {
		for _, h := range []DarwinFacts{fakeDarwin(nil, true), garbage} {
			if _, err := h.SwapUsedMB(); err == nil {
				t.Fatal("swap must be unknown")
			}
		}
	})
	t.Run("pressure", func(t *testing.T) {
		for _, h := range []DarwinFacts{fakeDarwin(nil, true), garbage} {
			if _, err := h.MemPressure(); err == nil {
				t.Fatal("pressure must be unknown")
			}
		}
		if _, err := ParseDarwinPressure("3"); err == nil {
			t.Fatal("level 3 is not a level")
		}
	})
	t.Run("statfs", func(t *testing.T) {
		if fakeDarwin(nil, true).TmpUsage("/tmp") != nil {
			t.Fatal("tmp must be nil")
		}
	})
	t.Run("gate closed", func(t *testing.T) {
		var s Snapshot
		errs := ApplyHostFacts(&s, fakeDarwin(nil, true), "/tmp")
		if len(errs) != 4 || s.MemMB != -1 || !math.IsInf(s.Load1, 1) || s.MemPressure != PressureUnknown || !s.StrictTmp {
			t.Fatalf("errs %v snapshot %+v", errs, s)
		}
		d := Decide(openSnap(s), openCfg(), Options{Role: "coder"})
		if d.OK {
			t.Fatal("gate must be closed when every reader fails")
		}
	})
}

func TestDarwinUnreadableTmpCloses(t *testing.T) {
	s := openSnap(Snapshot{StrictTmp: true})
	if d := Decide(s, openCfg(), Options{Role: "coder"}); d.OK || !strings.Contains(d.Reason, "/tmp usage unknown") {
		t.Fatalf("decision = %+v", d)
	}
	if d := Decide(s, openCfg(), Options{Role: "cleaner"}); strings.Contains(d.Reason, "/tmp usage unknown") {
		t.Fatalf("cleaner must be exempt: %+v", d)
	}
}

func TestPressureClosesMinMem(t *testing.T) {
	for lvl, open := range map[int]bool{1: true, 2: false, 4: false, 0: true} {
		d := Decide(openSnap(Snapshot{MemPressure: lvl}), openCfg(), Options{Role: "coder"})
		if d.OK != open {
			t.Fatalf("level %d: ok=%v reason=%q", lvl, d.OK, d.Reason)
		}
		if !open && !strings.Contains(d.Reason, "pressure") {
			t.Fatalf("reason %q must name the level", d.Reason)
		}
	}
}

func TestTmpFloor(t *testing.T) {
	mk := func(fs string, free int64) Snapshot {
		tm := TmpFromStatfs(500<<30, free<<20, fs, "/tmp")
		return openSnap(Snapshot{Tmp: tm, TmpMinFreeMB: TmpMinFreeMB})
	}
	d := Decide(mk("apfs", 5000), openCfg(), Options{Role: "coder"})
	if d.OK || !strings.Contains(d.Reason, "/tmp") || !strings.Contains(d.Reason, "apfs") || !strings.Contains(d.Reason, "5000 MB free") {
		t.Fatalf("apfs 5000 MB: %+v", d)
	}
	if d := Decide(mk("tmpfs", 5000), openCfg(), Options{Role: "coder"}); strings.Contains(d.Reason, "floor") {
		t.Fatalf("tmpfs must ignore the floor: %+v", d)
	}
	if d := Decide(mk("apfs", 20000), openCfg(), Options{Role: "coder"}); strings.Contains(d.Reason, "floor") {
		t.Fatalf("apfs 20000 MB is above the floor: %+v", d)
	}
}

func openCfg() Config { return baseConfig() }

// openSnap is baseSnap (every check passes) plus the host-facts extras of s.
func openSnap(s Snapshot) Snapshot {
	b := baseSnap()
	b.MemPressure, b.Tmp, b.TmpMinFreeMB, b.StrictTmp = s.MemPressure, s.Tmp, s.TmpMinFreeMB, s.StrictTmp
	return b
}

func TestUnknownMemClosesWithZeroFloor(t *testing.T) {
	c := openCfg()
	c.MinMemAvailableMB = 0
	var s Snapshot
	ApplyHostFacts(&s, fakeDarwin(nil, true), "/tmp")
	b := baseSnap()
	b.MemMB = s.MemMB
	if d := Decide(b, c, Options{Role: "coder"}); d.OK {
		t.Fatal("unknown memory must close the gate even with a floor of 0")
	}
}

const psRecorded = `    1     0     0 Mon Nov  3 08:01:02 2026 /sbin/launchd
  512     1   501 Sat Nov 15 20:45:16 2026 /opt/bin/claude --resume s-a --settings /x/settings/coder.json
  600   512   501 Sat Nov 15 20:46:00 2026 node /opt/app dir/claude --session-id s-b
99999 98765  1234 Wed Dec  2 23:59:59 2026 -zsh
`

func TestPsParse(t *testing.T) {
	procs, err := ParsePs(psRecorded, time.UTC)
	if err != nil || len(procs) != 4 {
		t.Fatalf("ParsePs = %+v, %v", procs, err)
	}
	if p := procs[1]; p.Pid != 512 || p.Ppid != 1 || p.Uid != 501 || !p.Start.Equal(time.Date(2026, 11, 15, 20, 45, 16, 0, time.UTC)) ||
		len(p.Argv) != 5 || p.Argv[0] != "/opt/bin/claude" || p.Argv[4] != "/x/settings/coder.json" {
		t.Fatalf("two-digit day line = %+v", p)
	}
	if p := procs[0]; !p.Start.Equal(time.Date(2026, 11, 3, 8, 1, 2, 0, time.UTC)) || p.Argv[0] != "/sbin/launchd" {
		t.Fatalf("one-digit (space padded) day line = %+v", p)
	}
	if p := procs[2]; p.Argv[1] != "/opt/app" || p.Argv[2] != "dir/claude" { // ps joins argv with spaces: split on spaces
		t.Fatalf("command with spaces = %+v", p)
	}
	scan := ScanFrom(procs)
	if !scan.Known || scan.Total != 1 || scan.PIDs[0] != 512 { // 600 is a child of 512
		t.Fatalf("ScanFrom = %+v", scan)
	}
}

func TestPsParseBadFails(t *testing.T) {
	for name, out := range map[string]string{
		"garbled lstart":  "  512     1   501 Xyz Nov 15 20:45:16 2026 claude\n",
		"short line":      "  512     1\n",
		"non-numeric pid": "abc 1 501 Sat Nov 15 20:45:16 2026 claude\n",
		"no command gap":  "  512     1   501 Sat Nov 15 20:45:16 2026",
		"good then bad":   psRecorded + "garbage line here\n",
	} {
		if procs, err := ParsePs(out, time.UTC); err == nil || procs != nil || !errors.Is(err, ErrUnparsable) {
			t.Errorf("%s: ParsePs = %+v, %v; want ErrUnparsable and no list", name, procs, err)
		}
	}
}

func TestPsParseFailClosesGate(t *testing.T) {
	d := DarwinFacts{Procs: func() ([]Proc, error) { return ParsePs("garbage\n", time.UTC) }}
	if _, err := d.Processes(); err == nil {
		t.Fatal("unparsable scan must be an error")
	}
	s := baseSnap()
	s.Procs = ProcScan{} // unknown
	s.StrictProcs = true
	if dec := Decide(s, baseConfig(), Options{}); dec.Reason != "host processes unknown — gate closed" {
		t.Fatalf("strict host, unknown scan: reason %q", dec.Reason)
	}
	var snap Snapshot
	d.Run = func(string, ...string) (string, error) { return "", errors.New("down") }
	d.Statfs = func(string) (int64, int64, string, string, error) { return 0, 0, "", "", errors.New("down") }
	ApplyHostFacts(&snap, d, t.TempDir())
	if !snap.StrictProcs {
		t.Fatal("ApplyHostFacts must mark a strict host")
	}
}
