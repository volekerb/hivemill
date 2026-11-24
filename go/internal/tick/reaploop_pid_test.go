package tick

import (
	"fmt"
	"os"
	"testing"

	"factory/go/internal/store"
)

// Python: isinstance(pid, int) and pid > 0, then os.kill(pid, 0); every
// OSError (EPERM included) is "dead". Python results quoted per row.
func TestPidAliveParity(t *testing.T) {
	self := os.Getpid()
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"own int pid", fmt.Sprintf(`{"pid": %d}`, self), true},      // alive=True
		{"float own pid", fmt.Sprintf(`{"pid": %d.0}`, self), false}, // isinstance(5.0,int) False
		{"exponent float", `{"pid": 1e2}`, false},                    // float in Python
		{"zero", `{"pid": 0}`, false},
		{"negative", `{"pid": -1}`, false},
		{"string", `{"pid": "12"}`, false},
		{"null", `{"pid": null}`, false},
		{"missing", `{}`, false},
		{"bad json", `{`, false},
		{"false", `{"pid": false}`, false},
		{"huge", `{"pid": 99999999999999999999}`, false},
	}
	for _, c := range cases {
		if got := pidAlive(c.raw); got != c.want {
			t.Errorf("%s: pidAlive(%s)=%v want %v", c.name, c.raw, got, c.want)
		}
	}
	if os.Geteuid() != 0 {
		// pid 1 belongs to root: os.kill(1, 0) raises PermissionError (an OSError) -> dead.
		if pidAlive(`{"pid": 1}`) || pidAlive(`{"pid": true}`) {
			t.Error("EPERM pid must count as dead like Python")
		}
	}
}

// Python: f"pid={pid}" (str) and json.dumps(pid) for the runtime-exit row;
// each row quotes the Python output for that spawn_args value.
func TestRuntimeExitPidFormat(t *testing.T) {
	cases := []struct {
		raw     string
		summary string // f"{pid}"
		event   string // json.dumps(pid)
	}{
		{`{"pid": 4242}`, "4242", "4242"},
		{`{"pid": 4242.0}`, "4242.0", "4242.0"},
		{`{"pid": 1e2}`, "100.0", "100.0"},
		{`{"pid": "abc"}`, "abc", `"abc"`},
		{`{"pid": null}`, "None", "null"},
		{`{"pid": true}`, "True", "true"},
		{`{"pid": -7}`, "-7", "-7"},
		{`{"pid": [1, 2.5, "x"]}`, "[1, 2.5, 'x']", `[1, 2.5, "x"]`},
	}
	for _, c := range cases {
		pid, _, err := pyPidLog(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if got := pyStrJSON(pid); got != c.summary {
			t.Errorf("%s: summary %q want %q", c.raw, got, c.summary)
		}
		if got, err := store.PyDumps(pid); err != nil || got != c.event {
			t.Errorf("%s: event %q (%v) want %q", c.raw, got, err, c.event)
		}
	}
	// Python: os.kill(10**30, 0) raises OverflowError, not an OSError: the unit fails, no reap.
	if _, _, err := pyPidLog(`{"pid": 99999999999999999999}`); err == nil {
		t.Error("integer beyond int64 must be an error")
	}
}

func FuzzPyPidLog(f *testing.F) {
	for _, s := range []string{`{"pid": 4242}`, `{"pid": 1e2}`, `{"pid": "x"}`, `{"pid": [1, {"a": 2}]}`, `{`, `{"pid": 99999999999999999999}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		pid, _, err := pyPidLog(raw)
		if err == nil {
			if _, derr := store.PyDumps(pid); derr != nil {
				t.Fatalf("decoded pid not dumpable: %v", derr)
			}
			_ = pyStrJSON(pid)
		}
	})
}
