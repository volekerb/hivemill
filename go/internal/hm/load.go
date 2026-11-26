package hm

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// CPU load of the session machines: placement and the proof fallback pass over a machine that is overloaded.

const (
	overloadPerCPU = 1.5              // a 1-minute load above this per CPU is overloaded
	loadTTL        = 60 * time.Second // one reading of a machine's load holds this long
)

// Load is the 1-minute load average of a machine and its CPU count. CPUs 0 = unreadable.
type Load struct {
	Avg  float64
	CPUs int
}

func (l Load) known() bool { return l.CPUs > 0 }

func (l Load) perCPU() float64 { return l.Avg / float64(l.CPUs) }

// overloaded is false for an unknown load: a machine is passed over only on a reading.
func (l Load) overloaded() bool { return l.known() && l.perCPU() > overloadPerCPU }

// parseLoad reads a load average text (/proc/loadavg, or the "{ 1.2 1.0 0.9 }" of macOS) and a CPU count.
func parseLoad(avg string, cpus int) Load {
	fs := strings.Fields(strings.Trim(strings.TrimSpace(avg), "{}"))
	if len(fs) == 0 || cpus <= 0 {
		return Load{}
	}
	v, err := strconv.ParseFloat(fs[0], 64)
	if err != nil {
		return Load{}
	}
	return Load{Avg: v, CPUs: cpus}
}

// LocalLoad is the load of this machine.
var LocalLoad = func() Load {
	var b []byte
	var err error
	if runtime.GOOS == "darwin" {
		b, err = exec.Command("sysctl", "-n", "vm.loadavg").Output()
	} else {
		b, err = os.ReadFile("/proc/loadavg")
	}
	if err != nil {
		return Load{}
	}
	return parseLoad(string(b), runtime.NumCPU())
}

type loadReading struct {
	load Load
	at   time.Time
}

// cachedLoad is the reading of machine m that is in force: the one of the last loadTTL, else fresh, which then holds.
func (r *Runner) cachedLoad(m string, fresh Load) Load {
	if c, ok := r.loads[m]; ok && time.Since(c.at) < loadTTL {
		return c.load
	}
	if !fresh.known() {
		return fresh
	}
	if r.loads == nil {
		r.loads = map[string]loadReading{}
	}
	r.loads[m] = loadReading{fresh, time.Now()}
	return fresh
}
