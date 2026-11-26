package hm

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// proofSh records every proof command; a remote one answers with its end token and exit code rc, unless the machine is
// in lost (the transport fails: no token).
func proofSh(r *Runner, rc int, lost map[string]bool) *[][]string {
	var got [][]string
	r.Tools.Sh = func(dir string, timeout time.Duration, env []string, argv ...string) (int, string) {
		got = append(got, append([]string{dir, timeout.String()}, argv...))
		if argv[0] != "hc" {
			return rc, "ran here\n"
		}
		if lost[argv[3]] {
			return 1, "connection lost\n"
		}
		for _, a := range argv {
			if strings.HasPrefix(a, "hm-proof-ran-") {
				return rc, "ran there\n" + a + "\n"
			}
		}
		return 127, "no token"
	}
	return &got
}

func proofEvents(t *testing.T, r *Runner) string {
	b, err := os.ReadFile(r.Log.Path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProofRunsOnRunMachine(t *testing.T) {
	r, _, _ := placementRunner(t, map[string]fakeWorker{"w1": {cloneHead: strings.Repeat("a", 40)}})
	got := proofSh(r, 3, nil)
	wt, logs := t.TempDir(), t.TempDir()
	crit := []Criterion{{ID: 1, Kind: "cmd", Proof: "make check", Expect: "exit 3"}}
	run := &Run{Task: "T-0001", Worktree: wt, Machine: "w1"}
	res := r.RerunProofs(run, crit, logs)
	if len(res) != 1 || res[0].OK == nil || !*res[0].OK {
		t.Fatalf("the exit code of the worker is the result: %+v", res)
	}
	if text, _ := os.ReadFile(res[0].Log); string(text) != "ran there\n" {
		t.Errorf("the end token is not part of the log: %q", text)
	}
	cmd := strings.Join((*got)[0], " ")
	for _, want := range []string{wt + " 15m0s hc exec --on w1 env -i PATH=" + sessionPath(r.Env.UserHome), "HOME=" + r.Env.UserHome,
		" sh " + wt + " hm-proof-ran-", " bash -c make check"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in %q", want, cmd)
		}
	}
	ev := proofEvents(t, r)
	if !strings.Contains(ev, `"event":"gate.proof_run"`) || !strings.Contains(ev, `"machine":"w1"`) || !strings.Contains(ev, `"seconds":`) {
		t.Errorf("gate.proof_run: %s", ev)
	}
	// "local" and the core's own name stay local, with no hc exec
	for _, m := range []string{"", "local", "coord-core"} {
		*got = nil
		run.Machine = m
		r.RerunProofs(run, crit, logs)
		if len(*got) != 1 || strings.Join((*got)[0], " ") != wt+" 15m0s bash -c make check" {
			t.Errorf("machine %q runs here: %v", m, *got)
		}
	}
}

// The shell a worker runs: the proof's directory and exit code are kept, and the token marks that it ran.
func TestRemoteProofShell(t *testing.T) {
	dir := t.TempDir()
	out, err := exec.Command("sh", "-c", remoteProof, "sh", dir, "tok-end", "bash", "-c", `echo "$PWD"; exit 7`).CombinedOutput()
	code := 0
	if x, ok := err.(*exec.ExitError); ok {
		code = x.ExitCode()
	}
	if code != 7 || !strings.Contains(string(out), dir) || !strings.HasSuffix(string(out), "tok-end\n") {
		t.Errorf("exit %d out %q", code, out)
	}
	out, err = exec.Command("sh", "-c", remoteProof, "sh", dir+"/gone", "tok-end", "true").CombinedOutput()
	if err == nil || strings.Contains(string(out), "tok-end") {
		t.Errorf("a missing directory is not a proof that ran: %v %q", err, out)
	}
}

func TestProofFallbackWhenUnreachable(t *testing.T) {
	worker := func(load float64) fakeWorker {
		return fakeWorker{memMB: 9000, shared: true, coord: "c", cloneHead: strings.Repeat("a", 40), load: load, cpus: 4}
	}
	r, s, _ := placementRunner(t, map[string]fakeWorker{"w-busy": worker(5), "w-idle": worker(1)})
	withLocalLoad(t, Load{3, 4})
	_ = s.SetCfg("session_machines", "w-gone,w-busy,w-idle,local")
	got := proofSh(r, 0, nil)
	crit := []Criterion{{ID: 1, Kind: "cmd", Proof: "true", Expect: "exit 0"}, {ID: 2, Kind: "cmd", Proof: "true", Expect: "exit 0"}}
	run := &Run{Task: "T-0001", Worktree: t.TempDir(), Machine: "w-gone"}
	res := r.RerunProofs(run, crit, t.TempDir())
	// argv is recorded after dir and timeout: hc exec --on <machine>
	if len(res) != 2 || !*res[0].OK || !*res[1].OK || len(*got) != 2 || (*got)[0][5] != "w-idle" || (*got)[1][5] != "w-idle" {
		t.Fatalf("the least loaded machine takes both proofs: %v", *got)
	}
	ev := proofEvents(t, r)
	if strings.Count(ev, `"event":"gate.proof_fallback"`) != 1 || !strings.Contains(ev, `"to":"w-idle"`) {
		t.Errorf("one gate.proof_fallback: %s", ev)
	}
	// the transport fails under the proof: it runs again on the next machine, at last on the local core (the busiest here)
	r.loads = nil
	withLocalLoad(t, Load{8, 4})
	_ = s.SetCfg("session_machines", "w-idle,w-busy,local")
	run.Machine = "w-idle"
	got = proofSh(r, 0, map[string]bool{"w-idle": true, "w-busy": true})
	res = r.RerunProofs(run, crit[:1], t.TempDir())
	if len(res) != 1 || !*res[0].OK || len(*got) != 3 || (*got)[2][2] != "bash" {
		t.Fatalf("lost transports end on the local core: %+v %v", res, *got)
	}
	// no other machine listed: local, and a failed proof stays failed
	_ = s.SetCfg("session_machines", "")
	run.Machine = "w-gone"
	got = proofSh(r, 1, nil)
	res = r.RerunProofs(run, crit[:1], t.TempDir())
	if len(res) != 1 || *res[0].OK || !strings.HasPrefix(res[0].Note, "exit 1") || len(*got) != 1 || (*got)[0][2] != "bash" {
		t.Errorf("local: %+v %v", res, *got)
	}
	// a timeout is a result, not an unreachable machine
	run.Machine = "w-idle"
	calls := 0
	r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { calls++; return 124, "slow\n" }
	res = r.RerunProofs(run, crit[:1], t.TempDir())
	if len(res) != 1 || *res[0].OK || res[0].Note != "timed out after 15m" || calls != 1 {
		t.Errorf("timeout: %+v after %d calls", res, calls)
	}
	// the local limit ends a stalled connection: the worker no longer answers, so the proof runs elsewhere
	hcFake, stalled := r.Tools.HC, false
	r.Tools.HC = func(args ...string) (int, string, string) {
		if stalled && args[0] == "exec" {
			return 1, "", "connection lost"
		}
		return hcFake(args...)
	}
	r.Tools.Sh = func(_ string, _ time.Duration, _ []string, argv ...string) (int, string) {
		if argv[0] == "hc" {
			stalled = true
			return 124, ""
		}
		return 0, ""
	}
	res = r.RerunProofs(run, crit[:1], t.TempDir())
	if len(res) != 1 || !*res[0].OK || !stalled {
		t.Errorf("stalled connection: %+v", res)
	}
}

func TestPickMachineSkipsOverloaded(t *testing.T) {
	worker := func(load float64) fakeWorker {
		return fakeWorker{memMB: 9000, shared: true, coord: "c", load: load, cpus: 4}
	}
	workers := map[string]fakeWorker{"w1": worker(6.1), "w2": worker(6)}
	r, s, _ := placementRunner(t, workers)
	withLocalMem(t, 8000)
	_ = s.SetCfg("session_machines", "w1,w2")
	for _, task := range []string{"T-0001", "T-0002"} {
		_ = s.PutRun(&Run{Task: task, State: "working", Machine: "w2"})
	}
	// w1 is above 1.5 x 4 and w2 is not: w2 takes the run although it has more active runs
	pl := r.PickMachine()
	if pl.Machine != "w2" || len(pl.Skipped) != 1 || pl.Skipped[0] != "w1: load 6.1 > 1.5 x 4 CPUs" {
		t.Fatalf("overloaded skipped: %+v", pl)
	}
	// one reading holds for 60 s
	workers["w1"] = worker(0)
	if pl := r.PickMachine(); pl.Machine != "w2" {
		t.Errorf("cached load: %+v", pl)
	}
	for m, c := range r.loads {
		c.at = c.at.Add(-loadTTL)
		r.loads[m] = c
	}
	if pl := r.PickMachine(); pl.Machine != "w1" || len(pl.Skipped) != 0 {
		t.Errorf("read again after 60 s: %+v", pl)
	}
	// every machine overloaded: the fewest active runs, as before
	r.loads = nil
	workers["w1"], workers["w2"] = worker(20), worker(30)
	if pl := r.PickMachine(); pl.Machine != "w1" || len(pl.Skipped) != 0 {
		t.Errorf("all overloaded: %+v", pl)
	}
	// the local core is weighed too
	r.loads = nil
	workers["w2"] = worker(1)
	withLocalLoad(t, Load{40, 4})
	_ = s.SetCfg("session_machines", "local,w2")
	if pl := r.PickMachine(); pl.Machine != "w2" || len(pl.Skipped) != 1 || !strings.HasPrefix(pl.Skipped[0], "local: load 40.0") {
		t.Errorf("overloaded local core: %+v", pl)
	}
}

// The Machines table sums the proofs of each machine in the window.
func TestObsProofTime(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ev := func(ts, name string, data map[string]any) Event {
		return Event{TS: ts, Machine: "core", TaskID: "T-0001", Event: name, Data: data}
	}
	proof := func(ts, machine string, secs float64) Event {
		return ev(ts, "gate.proof_run", map[string]any{"machine": machine, "seconds": secs})
	}
	// the task is placed on w1: a proof that ran locally still counts for the writer
	o := BuildObs([]Event{ev("2026-01-01T00:00:00Z", "task.dispatched", map[string]any{"machine": "w1"}),
		proof("2026-01-01T00:00:00Z", "w1", 600), proof("2026-03-10T10:00:00Z", "w1", 90), proof("2026-03-10T11:00:00Z", "w1", 30),
		proof("2026-03-10T11:00:00Z", "local", 60)}, ObsFilter{}, "core", nil, now)
	rows := []sourceLine{{ObsSource: ObsSource{Machine: "core"}}, {ObsSource: ObsSource{Machine: "w1"}}, {ObsSource: ObsSource{Machine: "w2"}}}
	o.proofTime(rows, windowStart(now, 7))
	if rows[0].Proofs != 1 || rows[0].ProofMin != 1 || rows[1].Proofs != 2 || rows[1].ProofMin != 2 || rows[2].Proofs != 0 {
		t.Errorf("rows %+v", rows)
	}
}
