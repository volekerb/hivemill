package tick

import (
	"fmt"
	"os"
	"testing"
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
