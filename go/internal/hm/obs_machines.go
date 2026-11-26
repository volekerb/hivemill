package hm

import (
	"sync"
	"time"

	"factory/go/internal/shlex"
)

// The Machines page of `hivemill obs`: this machine (the coordinator) and every session_machines entry, with the runs
// whose session counts there (the count placement uses), the free slots under max_concurrent, MemAvailable and the time
// of the machine's newest event. It only reads: the memory probe is placement's, with a marker that never matches, so it
// writes nothing and runs no hivemill on the machine.

// MachineCard is one machine of the page.
type MachineCard struct {
	Name        string       `json:"name"`
	Coordinator bool         `json:"coordinator"`
	Down        bool         `json:"down"`
	Running     []RunningRun `json:"running"`
	FreeSlots   int          `json:"free_slots"`       // cap minus running, never below 0
	MemMB       *int         `json:"mem_available_mb"` // null: unknown
	LastSeen    string       `json:"last_seen"`        // the machine's newest event ("" = none)
}

// RunningRun is a run whose session counts on a machine.
type RunningRun struct {
	Task string `json:"task"`
	Key  string `json:"-"` // the task page's key ("" = no page: no event under these filters)
}

// MachinesPage is the page's model; Cap is max_concurrent, one limit for all machines together.
type MachinesPage struct {
	Cap   int           `json:"cap"`
	Cards []MachineCard `json:"machines"`
}

type machineProbe struct {
	down bool
	mem  int // MB, -1 unknown
	at   time.Time
}

// probeMachine reads one session machine: hc's connection state, then MemAvailable when it is not down.
func (r *Runner) probeMachine(m string) machineProbe {
	p := machineProbe{mem: -1}
	if !validMachine(m) {
		return p
	}
	if p.down = machineDown(r.Tools, m); p.down {
		return p
	}
	var rc int
	var out string
	if r.cloneMode(m) {
		rc, out, _ = r.rsh(m, "", 10*time.Second, "sh -c "+shlex.Quote(probeScript)+" sh /nonexistent -")
	} else {
		rc, out, _ = r.Tools.HC("exec", "--on", m, "sh", "-c", probeScript, "sh", "/nonexistent", "-")
	}
	if rc == 0 {
		p.mem = ParseProbe(out).MemMB
	}
	return p
}

// probes reads the machines at once; one answer serves the page for coreTTL, so a reload does not probe again.
func (s *obsServer) probes(names []string) map[string]machineProbe {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probed == nil {
		s.probed = map[string]machineProbe{}
	}
	fresh := make([]machineProbe, len(names))
	var wg sync.WaitGroup
	for i, m := range names {
		if c, ok := s.probed[m]; ok && s.now().Sub(c.at) < coreTTL {
			fresh[i] = c
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fresh[i] = s.r.probeMachine(m)
		}()
	}
	wg.Wait()
	out := map[string]machineProbe{}
	for i, m := range names {
		if fresh[i].at.IsZero() {
			fresh[i].at = s.now()
		}
		s.probed[m], out[m] = fresh[i], fresh[i]
	}
	return out
}

// machinesPage builds the cards: the coordinator first, then session_machines in order.
func (s *obsServer) machinesPage(o *Obs) *MachinesPage {
	r, me, core := s.r, s.r.Machine(), ""
	entries := csv(r.Store.Cfg("session_machines"))
	if len(entries) > 0 {
		core = s.coreName()
	}
	var others []string
	seen := map[string]bool{}
	for _, m := range entries {
		if m != localMachine && m != core && m != me && !seen[m] {
			others, seen[m] = append(others, m), true
		}
	}
	probed := s.probes(others)
	active := r.activeRuns(core)

	p := &MachinesPage{Cap: cfgInt(r.Store, "max_concurrent")}
	card := func(name string, tasks []string, mem int) MachineCard {
		c := MachineCard{Name: name, Running: []RunningRun{}, LastSeen: o.LastSeen[name]}
		for _, task := range tasks {
			run := RunningRun{Task: task}
			if k := TaskKey(me, task); o.Tasks[k] != nil {
				run.Key = k
			}
			c.Running = append(c.Running, run)
		}
		c.FreeSlots = max(0, p.Cap-len(c.Running))
		if mem >= 0 {
			c.MemMB = &mem
		}
		return c
	}
	// the coordinator's runs: those under "" (local or the core) and those placed under its own name
	mine := active[""]
	if me != core {
		mine = append(mine, active[me]...)
	}
	local := card(me, mine, MemAvailableMB())
	local.Coordinator = true
	p.Cards = []MachineCard{local}
	for _, m := range others {
		c := card(m, active[m], probed[m].mem)
		c.Down = probed[m].down
		p.Cards = append(p.Cards, c)
	}
	return p
}
