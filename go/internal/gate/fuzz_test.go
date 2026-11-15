package gate

import (
	"os"
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
