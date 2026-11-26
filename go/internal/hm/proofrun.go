package hm

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Where a proof of the gate runs: the machine of the run, so the coordinator does not carry every run's test load. A
// worker sees the clone at the same path (placement.go), so only the command moves.

const proofTimeout = 15 * time.Minute

// hc exec ends a command after 60 s unless told otherwise, and takes 600 s at most; hcCut is what it writes then.
const (
	workerTimeout = min(proofTimeout, 600*time.Second)
	hcCut         = "[hc] timed out after"
)

// timeoutNote names the limit that applied to a proof that ended with exit 124.
func timeoutNote(limit time.Duration) string {
	return fmt.Sprintf("timed out after %dm", int(limit.Minutes()))
}

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
	p.moved(from, p.at, why)
}

// moved records that a proof leaves machine from for machine to.
func (p *proofRunner) moved(from, to, why string) {
	_ = p.r.Log.Emit("gate.proof_fallback", p.run, Event{Data: map[string]any{"machine": from, "to": machineLabel(to), "why": why}})
}

// sh runs one proof in dir and records where and how long (gate.proof_run). rc and text are those of Tools.Sh; limit
// is the time limit of the run that counts. A proof the worker's limit cuts runs once more on the local core, which
// allows longer; the worker stays in use for the next proof.
func (p *proofRunner) sh(criterion int, dir string, argv []string) (rc int, text string, limit time.Duration) {
	p.start()
	for {
		began := time.Now()
		got := p.try(dir, argv)
		if !got.ran { // the local core always runs, so this ends there at the latest
			p.fallback(p.at, fmt.Sprintf("the proof did not run there: exit %d: %s", got.rc, firstLine(got.text)))
			continue
		}
		_ = p.r.Log.Emit("gate.proof_run", p.run, Event{Data: map[string]any{"machine": machineLabel(p.at), "criterion": criterion,
			"exit": got.rc, "seconds": math.Round(time.Since(began).Seconds()*10) / 10}})
		if !got.cut {
			return got.rc, got.text, got.limit
		}
		worker := p.at
		p.moved(worker, "", "the proof was cut there: "+timeoutNote(got.limit))
		p.at = ""
		defer func() { p.at = worker }()
	}
}

// proofTry is one run of a proof. ran is false when a worker did not run it to its end; cut is true when hc exec ended
// it at its limit on a worker that still answers.
type proofTry struct {
	rc       int
	text     string
	limit    time.Duration
	ran, cut bool
}

// try runs argv on the machine in use. A timeout on a worker that still answers is a run.
func (p *proofRunner) try(dir string, argv []string) proofTry {
	if p.at == "" {
		rc, text := p.r.Tools.Sh(dir, proofTimeout, cleanEnv(), argv...)
		return proofTry{rc: rc, text: text, limit: proofTimeout, ran: true}
	}
	token := "hm-proof-ran-" + uuid4()
	// the base environment of a session: nothing else of the coordinator's goes on a command line
	home := p.r.Env.UserHome
	hc := []string{"hc", "exec", "--on", p.at, "--timeout", strconv.Itoa(int(workerTimeout.Seconds())),
		"env", "-i", "PATH=" + sessionPath(home), "HOME=" + home, "LANG=C.UTF-8",
		"FACTORY_HOME=" + p.r.Env.Home, "sh", "-c", remoteProof, "sh", dir, token}
	rc, text := p.r.Tools.Sh(dir, proofTimeout, nil, append(hc, argv...)...)
	if strings.Contains(text, token) {
		return proofTry{rc: rc, text: strings.Replace(text, token+"\n", "", 1), limit: workerTimeout, ran: true}
	}
	// 124 without the token is a limit, hc's or the local one: a slow proof, unless the connection stalled and the
	// worker is gone
	got := proofTry{rc: rc, text: text, limit: proofTimeout, ran: rc == 124 && p.r.cloneOn(p.at, p.run.Worktree) == ""}
	if rc == 124 && strings.Contains(text, hcCut) {
		got.limit, got.cut = workerTimeout, got.ran
	}
	return got
}
