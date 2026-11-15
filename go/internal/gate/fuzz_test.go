package gate

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func FuzzParseUsage(f *testing.F) {
	seed, _ := os.ReadFile("testdata/usage.json")
	f.Add(seed)
	f.Add([]byte(`{"updated_at": "NaN"}`))
	f.Add([]byte(`[1,2]`))
	f.Add([]byte(`{"five_hour_pct": 1e999, "updated_at": 5}`))
	f.Fuzz(func(t *testing.T, b []byte) { ParseUsage(b, time.Unix(1700000000, 0)) })
}

func FuzzMeminfo(f *testing.F) {
	seed, _ := os.ReadFile("testdata/meminfo")
	f.Add(string(seed))
	f.Add("MemAvailable:\nSwapTotal: x\n")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = MemAvailableMB(s)
		_, _ = SwapUsedMB(s)
		_, _ = Load1(s)
	})
}

func FuzzParseISO(f *testing.F) {
	f.Add("2026-11-15T01:02:03+01:00")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) { ParseISO(s); StillStarting(s, time.Unix(1700000000, 0)) })
}

func FuzzArgv(f *testing.F) {
	f.Add("--resume", "abc", "--workspace=/x")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		argv := []string{a, b, c}
		ArgvSessions(argv)
		ArgvWorkspace(argv)
		isCursorAgentArgv(argv)
		statPPid(a)
		PyInt(a)
		PyFloat(b)
	})
}

func darwinSeed(f *testing.F, name string, extra ...string) {
	b, err := os.ReadFile(filepath.Join("testdata", "darwin", name))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(string(b))
	for _, e := range extra {
		f.Add(e)
	}
}

func FuzzParseDarwinLoad(f *testing.F) {
	darwinSeed(f, "loadavg", "{ }", "{ nan 1 2 }", "{ -1 0 0 }")
	f.Fuzz(func(t *testing.T, s string) {
		if v, err := ParseDarwinLoad(s); err == nil && (v < 0 || math.IsNaN(v) || math.IsInf(v, 0)) {
			t.Fatalf("load %v accepted from %q", v, s)
		}
	})
}

func FuzzParseDarwinVMStat(f *testing.F) {
	darwinSeed(f, "vm_stat", "Mach Virtual Memory Statistics: (page size of 0 bytes)\nPages free: 5.\n")
	f.Fuzz(func(t *testing.T, s string) {
		if v, err := ParseDarwinVMStat(s); err == nil && v < 0 {
			t.Fatalf("vm_stat %d accepted from %q", v, s)
		}
	})
}

func FuzzParseDarwinSwap(f *testing.F) {
	darwinSeed(f, "swapusage", "vm.swapusage: used = -1.00M", "used = 99999999999999999999M")
	f.Fuzz(func(t *testing.T, s string) {
		if v, err := ParseDarwinSwap(s); err == nil && v < 0 {
			t.Fatalf("swap %d accepted from %q", v, s)
		}
	})
}

func FuzzParseDarwinPressure(f *testing.F) {
	darwinSeed(f, "pressure_normal")
	darwinSeed(f, "pressure_warn", "3", "0", "-1")
	f.Fuzz(func(t *testing.T, s string) {
		if v, err := ParseDarwinPressure(s); err == nil && v != 1 && v != 2 && v != 4 {
			t.Fatalf("pressure %d accepted from %q", v, s)
		}
	})
}

func FuzzTmpFromStatfs(f *testing.F) {
	f.Add(int64(100<<30), int64(5<<30), "apfs", "/tmp")
	f.Add(int64(0), int64(0), "", "")
	f.Add(int64(-1), int64(-1), "tmpfs", "/")
	f.Fuzz(func(t *testing.T, total, free int64, fs, mnt string) {
		if tm := TmpFromStatfs(total, free, fs, mnt); tm != nil && (tm.Pct < 0 || tm.Pct > 100 || tm.FreeMB < 0) {
			t.Fatalf("tmp %+v from %d/%d", tm, total, free)
		}
	})
}
