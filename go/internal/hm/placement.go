package hm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"factory/go/internal/gate"
)

// Session placement: a task session may run on another hc machine (session_machines). A worker machine shares the
// factory home, the project checkouts and ~/.claude/projects at the same paths (NFS or similar) and never opens the
// factory DB itself: its `hivemill` forwards every call to the coordinator over ssh (forward.go in cmd/hivemill).
// Nothing is assumed: every pick re-checks that the machine sees this home (a fresh marker), has the same user home,
// has a forwarding hivemill and has enough memory. The judge and the planner stay on the local core.

// localMachine names the local core in session_machines, in events and in `run status`.
const localMachine = "local"

// probeScript prints what a pick needs from a machine, in one hc exec: user home, OS, the marker it reads at the
// coordinator's path, the coordinator its hivemill forwards to (asked only when the marker matched: a machine that
// does not share the home may run hivemill against its own DB; under env -i, as sessions start, so only the worker's
// coordinator file counts, not a variable of the hc exec environment), then its memory (/proc/meminfo or vm_stat).
const probeScript = `echo "home=$HOME"
echo "os=$(uname -s)"
printf 'marker='; cat "$1" 2>/dev/null; echo
printf 'coordinator='
if [ "$(cat "$1" 2>/dev/null)" = "$2" ]; then env -i HOME="$HOME" "$HOME/.local/bin/hivemill" local coordinator 2>/dev/null; fi
echo
echo '---mem'
if [ "$(uname -s)" = Darwin ]; then
  echo "pressure=$(sysctl -n kern.memorystatus_vm_pressure_level 2>/dev/null)"
  vm_stat
else
  cat /proc/meminfo
fi`

// Probe is what one machine reported to a pick.
type Probe struct {
	Home, OS, Marker, Coordinator string
	MemMB                         int // -1 = unreadable
}

// ParseProbe reads the output of probeScript.
func ParseProbe(out string) Probe {
	p := Probe{MemMB: -1}
	head, mem, _ := strings.Cut(out, "---mem\n")
	for _, line := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "home":
			p.Home = v
		case "os":
			p.OS = v
		case "marker":
			p.Marker = v
		case "coordinator":
			p.Coordinator = v
		}
	}
	if p.OS == "Darwin" {
		p.MemMB = darwinMemMB(mem)
	} else {
		p.MemMB = linuxMemMB(mem)
	}
	return p
}

// linuxMemMB is MemAvailable of a /proc/meminfo text, in MB (-1 when absent).
func linuxMemMB(meminfo string) int {
	for _, line := range strings.Split(meminfo, "\n") {
		if fs := strings.Fields(line); len(fs) >= 2 && fs[0] == "MemAvailable:" {
			if kb, err := strconv.Atoi(fs[1]); err == nil {
				return kb / 1024
			}
		}
	}
	return -1
}

// darwinMemMB is (free + inactive + speculative) pages of a vm_stat text, in MB, the formula of the old pipeline and
// of internal/gate. A "pressure=N" line other than 1 (normal) counts as no memory; an unreadable text is -1.
func darwinMemMB(text string) int {
	if i := strings.Index(text, "pressure="); i >= 0 {
		lvl, _, _ := strings.Cut(text[i+len("pressure="):], "\n")
		if strings.TrimSpace(lvl) != "1" {
			return 0
		}
	}
	mb, err := gate.ParseDarwinVMStat(text)
	if err != nil {
		return -1
	}
	return int(mb)
}

// placementOn reports whether session_machines lists any machine (empty = every session on the local core).
func placementOn(s Store) bool { return len(csv(s.Cfg("session_machines"))) > 0 }

// Placement is the outcome of one pick: Machine "" is the local core; Skipped says why each earlier entry was passed.
type Placement struct {
	Machine string
	Skipped []string
}

// PickMachine picks the machine of a new task session: among the session_machines entries that are eligible (at least
// machine_mem_floor_mb MemAvailable, the marker probe), the one with the fewest active runs; a tie goes to the earlier
// entry. The local core counts when listed. Skipped says why each ineligible entry was passed. No eligible entry: the
// local core.
func (r *Runner) PickMachine() Placement {
	var pl Placement
	floor := cfgInt(r.Store, "machine_mem_floor_mb")
	core, coreRead := "", false
	var load map[string]int
	best, bestLoad, found := "", 0, false
	for _, m := range csv(r.Store.Cfg("session_machines")) {
		if m != localMachine && !coreRead {
			core, coreRead = r.coreName(), true
		}
		isLocal := m == localMachine || m == core
		if isLocal {
			mb := MemAvailableMB()
			if mb < floor {
				pl.Skipped = append(pl.Skipped, fmt.Sprintf("%s: %s MemAvailable < %d MB", localMachine, mbText(mb), floor))
				continue
			}
		} else if why := r.eligible(m, floor); why != "" {
			pl.Skipped = append(pl.Skipped, m+": "+why)
			continue
		}
		if load == nil {
			load = r.activeByMachine(core)
		}
		key, n := m, 0
		if isLocal {
			key = ""
		}
		n = load[key]
		if !found || n < bestLoad {
			best, bestLoad, found = key, n, true
		}
	}
	pl.Machine = best
	return pl
}

// activeByMachine counts the active runs per machine; "" is the local core (a run on the core's own name counts there).
func (r *Runner) activeByMachine(core string) map[string]int {
	load := map[string]int{}
	runs, _ := r.Store.Runs()
	for _, x := range runs {
		if !Active(x.State) {
			continue
		}
		m := x.Machine
		if m == localMachine || (core != "" && m == core) {
			m = ""
		}
		load[m]++
	}
	return load
}

func mbText(mb int) string {
	if mb < 0 {
		return "unknown"
	}
	return strconv.Itoa(mb) + " MB"
}

// eligible probes machine m and says why it may not take a session ("" = it may).
func (r *Runner) eligible(m string, floor int) string {
	if !validMachine(m) {
		return "not a machine name"
	}
	dir := filepath.Join(r.Env.StateDir(), "placement")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "cannot write the home marker: " + err.Error()
	}
	marker := filepath.Join(dir, "marker-"+m)
	token := uuid4()
	if err := writeFile(marker, token+"\n", 0o600); err != nil {
		return "cannot write the home marker: " + err.Error()
	}
	defer os.Remove(marker)
	rc, out, errs := r.Tools.HC("exec", "--on", m, "sh", "-c", probeScript, "sh", marker, token)
	if rc != 0 {
		return fmt.Sprintf("hc exec exit %d: %s", rc, firstLine(errs+out))
	}
	p := ParseProbe(out)
	switch {
	case p.Marker != token:
		return "does not share the factory home at " + r.Env.Home
	case p.Home != r.Env.UserHome:
		return fmt.Sprintf("user home %q is not %q", p.Home, r.Env.UserHome)
	case p.Coordinator == "":
		return "its hivemill does not forward to a coordinator (see docs/hivemill-machines.md)"
	case p.MemMB < floor:
		return fmt.Sprintf("%s MemAvailable < %d MB", mbText(p.MemMB), floor)
	}
	return ""
}

// validMachine refuses a name hc or ssh could read as an option or a path.
func validMachine(m string) bool {
	if m == "" || strings.HasPrefix(m, "-") {
		return false
	}
	for _, c := range m {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// cloneOn says why machine m does not see the clone wt at the same commit as here ("" = it does). The clone shares
// objects with the project checkout, which the home marker does not prove.
func (r *Runner) cloneOn(m, wt string) string {
	rc, here, _ := r.Tools.Git(wt, "rev-parse", "HEAD")
	if rc != 0 {
		return "cannot read the clone head here"
	}
	rc, there, errs := r.Tools.HC("exec", "--on", m, "git", "-C", wt, "rev-parse", "HEAD")
	if rc != 0 {
		return fmt.Sprintf("git rev-parse there: exit %d: %s", rc, firstLine(errs+there))
	}
	if strings.TrimSpace(here) != strings.TrimSpace(there) {
		return "the clone head differs there"
	}
	return ""
}

// trustOn adds workspace trust for the clone in the worker's own ~/.claude.json (it is not shared), through the
// worker's hivemill (`local trust` never forwards).
func (r *Runner) trustOn(m, wt string) error {
	bin := filepath.Join(r.Env.UserHome, ".local", "bin", "hivemill")
	if rc, out, errs := r.Tools.HC("exec", "--on", m, "env", "FACTORY_HOME="+r.Env.Home, bin, "local", "trust", wt); rc != 0 {
		return fmt.Errorf("exit %d: %s", rc, firstLine(errs+out))
	}
	return nil
}

// machineDown reports whether hc lists machine m as not connected. A session there is not lost: hc keeps it and lists
// it again on reconnect, so reconcile must not resume it meanwhile. A machine hc does not list, or a listing hc cannot
// give, is not "down" (the normal loss rules apply).
func machineDown(t Tools, m string) bool {
	rc, out, _ := t.HC("ls", "--json")
	if rc != 0 {
		return false
	}
	var doc struct {
		Machines []struct{ Name, State string } `json:"machines"`
	}
	if json.Unmarshal([]byte(out), &doc) != nil {
		return false
	}
	for _, x := range doc.Machines {
		if x.Name == m {
			return x.State != "connected"
		}
	}
	return false
}

// machineLabel is the machine of a run or session as events and `run status` show it.
func machineLabel(m string) string {
	if m == "" {
		return localMachine
	}
	return m
}
