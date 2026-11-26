package hm

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Where a proof of the gate runs: the machine of the run, so the coordinator does not carry every run's test load. A
// worker sees the clone at the same path (placement.go), so only the command moves.

const proofTimeout = 15 * time.Minute

// remoteProof is the shell of a proof on a worker: in the directory $1, with a time limit there too (the local one
// ends only the hc client), then the token $2 on stderr. The token tells a proof that ran from a transport that failed.
const remoteProof = `cd "$1" || exit 125
tok=$2
shift 2
if command -v timeout >/dev/null 2>&1; then set -- timeout -k 10 900 "$@"; fi
"$@"
rc=$?
echo "$tok" >&2
exit $rc`

// proofRunner runs the proofs of one run. at is the machine in use ("" = the local core); bad holds why a machine was
// given up.
type proofRunner struct {
	r      *Runner
	run    *Run
	at     string
	chosen bool
	bad    map[string]string
	core   *string // the core's own name, asked once
}

// workerOf is the hc machine to reach for m: "" when m is the local core under any of its names.
func (p *proofRunner) workerOf(m string) string {
	if m == "" || m == localMachine {
		return ""
	}
	if p.core == nil {
		name := p.r.coreName()
		p.core = &name
	}
	if m == *p.core {
		return ""
	}
	return m
}

// start takes the run's machine, or a fallback when the clone cannot be read there.
func (p *proofRunner) start() {
	if p.chosen {
		return
	}
	p.chosen, p.bad = true, map[string]string{}
	m := p.workerOf(p.run.Machine)
	if m == "" {
		return
	}
	why := "not a machine name"
	if validMachine(m) {
		why = p.r.cloneOn(m, p.run.Worktree)
	}
	if why == "" {
		p.at = m
		return
	}
	p.fallback(m, why)
}

// fallback leaves machine from for the least loaded session machine that can take a proof, else the local core.
func (p *proofRunner) fallback(from, why string) {
	r := p.r
	p.bad[from] = why
	type cand struct {
		machine string
		ratio   float64
	}
	var cands []cand
	floor := cfgInt(r.Store, "machine_mem_floor_mb")
	for _, name := range csv(r.Store.Cfg("session_machines")) {
		m := p.workerOf(name)
		if _, ok := p.bad[m]; ok {
			continue
		}
		var load Load
		if m == "" {
			if MemAvailableMB() < floor {
				continue // still the last resort below
			}
			load = r.cachedLoad(localMachine, LocalLoad())
		} else {
			got, bad := r.eligible(m, floor)
			if bad == "" {
				bad = r.cloneOn(m, p.run.Worktree)
			}
			if bad != "" {
				p.bad[m] = bad
				continue
			}
			load = got
		}
		c := cand{m, math.Inf(1)} // an unknown load ranks after every reading
		if load.known() {
			c.ratio = load.perCPU()
		}
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].ratio < cands[j].ratio })
	p.at = ""
	if len(cands) > 0 {
		p.at = cands[0].machine
	}
	_ = r.Log.Emit("gate.proof_fallback", p.run, Event{Data: map[string]any{"machine": from, "to": machineLabel(p.at), "why": why}})
}

// sh runs one proof in dir and records where and how long (gate.proof_run). The result is that of Tools.Sh.
func (p *proofRunner) sh(criterion int, dir string, argv []string) (int, string) {
	p.start()
	for {
		began := time.Now()
		rc, text, ran := p.try(dir, argv)
		if !ran { // the local core always runs, so this ends there at the latest
			p.fallback(p.at, fmt.Sprintf("the proof did not run there: exit %d: %s", rc, firstLine(text)))
			continue
		}
		_ = p.r.Log.Emit("gate.proof_run", p.run, Event{Data: map[string]any{"machine": machineLabel(p.at), "criterion": criterion,
			"exit": rc, "seconds": math.Round(time.Since(began).Seconds()*10) / 10}})
		return rc, text
	}
}

// try runs argv on the machine in use. ran is false when a worker did not run it to its end; a timeout on a worker
// that still answers is a result.
func (p *proofRunner) try(dir string, argv []string) (rc int, text string, ran bool) {
	if p.at == "" {
		rc, text = p.r.Tools.Sh(dir, proofTimeout, cleanEnv(), argv...)
		return rc, text, true
	}
	token := "hm-proof-ran-" + uuid4()
	// the base environment of a session: nothing else of the coordinator's goes on a command line
	home := p.r.Env.UserHome
	hc := []string{"hc", "exec", "--on", p.at, "env", "-i", "PATH=" + sessionPath(home), "HOME=" + home, "LANG=C.UTF-8",
		"FACTORY_HOME=" + p.r.Env.Home, "sh", "-c", remoteProof, "sh", dir, token}
	rc, text = p.r.Tools.Sh(dir, proofTimeout, nil, append(hc, argv...)...)
	if strings.Contains(text, token) {
		return rc, strings.Replace(text, token+"\n", "", 1), true
	}
	// 124 without the token is the local limit: a slow proof, unless the connection stalled and the worker is gone
	return rc, text, rc == 124 && p.r.cloneOn(p.at, p.run.Worktree) == ""
}
