package hm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/gate"
)

// Session placement: a task session may run on another hc machine (session_machines). A worker machine shares the
// factory home, the project checkouts and ~/.claude/projects at the same paths (NFS or similar) and never opens the
// factory DB itself: its `hivemill` forwards every call to the coordinator over ssh (forward.go in cmd/hivemill).
// Nothing is assumed: every pick re-checks that the machine sees this home (a fresh marker), has the same user home,
// has a forwarding hivemill and has enough memory. A machine overloaded on CPU (load.go) takes no new run while
// another one is not. The judge and the planner stay on the local core.

// localMachine names the local core in session_machines, in events and in `run status`.
const localMachine = "local"

// probeScript prints what a pick needs from a machine, in one hc exec: user home, OS, the marker it reads at the
// coordinator's path, the coordinator its hivemill forwards to (asked only when the marker matched: a machine that
// does not share the home may run hivemill against its own DB; under env -i, as sessions start, so only the worker's
// coordinator file counts, not a variable of the hc exec environment), then its load average, CPU count and memory
// (/proc/meminfo or vm_stat).
const probeScript = `echo "home=$HOME"
echo "os=$(uname -s)"
printf 'marker='; cat "$1" 2>/dev/null; echo
printf 'coordinator='
if [ "$(cat "$1" 2>/dev/null)" = "$2" ]; then env -i HOME="$HOME" "$HOME/.local/bin/hivemill" local coordinator 2>/dev/null; fi
echo
printf 'load='
if [ "$(uname -s)" = Darwin ]; then sysctl -n vm.loadavg 2>/dev/null; else cat /proc/loadavg 2>/dev/null; fi
echo
echo "cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null)"
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
	Load                          Load
}

// ParseProbe reads the output of probeScript.
func ParseProbe(out string) Probe {
	p := Probe{MemMB: -1}
	head, mem, _ := strings.Cut(out, "---mem\n")
	avg, cpus := "", 0
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
		case "load":
			avg = v
		case "cpus":
			cpus, _ = strconv.Atoi(v)
		}
	}
	p.Load = parseLoad(avg, cpus)
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

// Placement is the outcome of one pick: Machine "" is the local core; Skipped says why each ineligible entry was passed.
type Placement struct {
	Machine string
	Skipped []string
}

// PickMachine picks the machine of a new task session: among the session_machines entries that are eligible (at least
// machine_mem_floor_mb MemAvailable, the marker probe), the one with the fewest run sessions (activeByMachine); a tie goes to the earlier
// entry. An entry overloaded on CPU is passed over while another eligible one reads below the limit. The local core
// counts when listed. Skipped says why each entry was passed. No eligible entry: the local core.
func (r *Runner) PickMachine() Placement {
	var pl Placement
	floor := cfgInt(r.Store, "machine_mem_floor_mb")
	core, coreRead := "", false
	type cand struct {
		name, key string // the label in Skipped; the machine of a run ("" = local)
		load      Load
	}
	var cands []cand
	spare := false // some eligible entry reads below the CPU limit
	for _, m := range csv(r.Store.Cfg("session_machines")) {
		if m != localMachine && !coreRead {
			core, coreRead = r.coreName(), true
		}
		c := cand{name: m, key: m}
		if m == localMachine || m == core {
			mb := MemAvailableMB()
			if mb < floor {
				pl.Skipped = append(pl.Skipped, fmt.Sprintf("%s: %s MemAvailable < %d MB", localMachine, mbText(mb), floor))
				continue
			}
			c = cand{name: localMachine, load: r.cachedLoad(localMachine, LocalLoad())}
		} else {
			load, why := r.eligibleFor(m, floor)
			if why != "" {
				pl.Skipped = append(pl.Skipped, m+": "+why)
				continue
			}
			c.load = load
		}
		spare = spare || c.load.known() && !c.load.overloaded()
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return pl
	}
	active := r.activeByMachine(core)
	best, found := 0, false
	for _, c := range cands {
		if spare && c.load.overloaded() {
			pl.Skipped = append(pl.Skipped, fmt.Sprintf("%s: load %.1f > %.1f x %d CPUs", c.name, c.load.Avg, overloadPerCPU, c.load.CPUs))
			continue
		}
		if n := active[c.key]; !found || n < best {
			pl.Machine, best, found = c.key, n, true
		}
	}
	return pl
}

// activeByMachine counts the sessions of runs per machine: every busy run, and every waiting run whose task session is
// still open. "" is the local core (a run on the core's own name counts there).
func (r *Runner) activeByMachine(core string) map[string]int {
	load := map[string]int{}
	runs, _ := r.Store.Runs()
	list, listed := Terminals(r.Tools)
	for _, x := range runs {
		// without a listing, an unfinished run counts and a held one (hold closed its session) does not
		open := x.Agent != "" && (listed && alive(list, true, x.Agent) || !listed && Active(x.State))
		if !busy(x.State) && !(waiting(&x) && open) {
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

// eligibleFor is the probe of m's mode: a clone-mode machine shares nothing with this one.
func (r *Runner) eligibleFor(m string, floor int) (Load, string) {
	if r.cloneMode(m) {
		return r.cloneEligible(m, floor)
	}
	return r.eligible(m, floor)
}

// eligible probes machine m and says why it may not take a session ("" = it may), with its CPU load when it may.
func (r *Runner) eligible(m string, floor int) (Load, string) {
	if !validMachine(m) {
		return Load{}, "not a machine name"
	}
	dir := filepath.Join(r.Env.StateDir(), "placement")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Load{}, "cannot write the home marker: " + err.Error()
	}
	marker := filepath.Join(dir, "marker-"+m)
	token := uuid4()
	if err := writeFile(marker, token+"\n", 0o600); err != nil {
		return Load{}, "cannot write the home marker: " + err.Error()
	}
	defer os.Remove(marker)
	rc, out, errs := r.Tools.HC("exec", "--on", m, "sh", "-c", probeScript, "sh", marker, token)
	if rc != 0 {
		return Load{}, fmt.Sprintf("hc exec exit %d: %s", rc, firstLine(errs+out))
	}
	p := ParseProbe(out)
	switch {
	case p.Marker != token:
		return Load{}, "does not share the factory home at " + r.Env.Home
	case p.Home != r.Env.UserHome:
		return Load{}, fmt.Sprintf("user home %q is not %q", p.Home, r.Env.UserHome)
	case p.Coordinator == "":
		return Load{}, "its hivemill does not forward to a coordinator (see docs/hivemill-machines.md)"
	case p.MemMB < floor:
		return Load{}, fmt.Sprintf("%s MemAvailable < %d MB", mbText(p.MemMB), floor)
	}
	return r.cachedLoad(m, p.Load), ""
}

// readRemoteUsage reads the usage file of machine m, over the transport of the placement probe but with a short
// timeout: a slow machine must not stall a pass.
func readRemoteUsage(m string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hc", "exec", "--on", m, "sh", "-c", `cat "$HOME/.claude/usage-limits.json"`)
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// remoteUsageFiles is the usage file of every session machine but the local core, read once per pass. A machine whose
// read fails is skipped.
func (r *Runner) remoteUsageFiles() [][]byte {
	if r.RemoteUsage == nil || r.usageRead {
		return r.usageFiles
	}
	r.usageRead = true
	core, coreRead := "", false
	for _, m := range csv(r.Store.Cfg("session_machines")) {
		if m == localMachine || !validMachine(m) {
			continue
		}
		if !coreRead {
			core, coreRead = r.coreName(), true
		}
		if m == core {
			continue
		}
		if b, err := r.RemoteUsage(m); err == nil {
			r.usageFiles = append(r.usageFiles, b)
		}
	}
	return r.usageFiles
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
