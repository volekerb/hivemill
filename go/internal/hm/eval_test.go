package hm

import (
	"strings"
	"testing"
)

// evalRunner records every gh call; `pr view` answers with the given PR state and draft flag.
func evalRunner(t *testing.T, state string, draft bool) (*Runner, *SQLite, *[]string) {
	r, s := testRunner(t, "")
	var calls []string
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case len(args) > 1 && args[0] == "pr" && args[1] == "list":
			return 0, "https://github.com/zz-test/www/pull/1", ""
		case len(args) > 1 && args[0] == "pr" && args[1] == "view":
			d := map[bool]string{true: "true", false: "false"}[draft]
			return 0, `{"number":1,"state":"` + state + `","headRefOid":"` + strings.Repeat("a", 40) + `","isDraft":` + d +
				`,"mergeable":"MERGEABLE","statusCheckRollup":[]}`, ""
		}
		return 0, "", ""
	}
	return r, s, &calls
}

func merged(calls []string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, "pr merge") {
			return true
		}
	}
	return false
}

func TestEvalNeverMerges(t *testing.T) {
	pr := "https://github.com/zz-test/www/pull/1"
	for _, c := range []struct {
		name, state, prState string
		want                 string
	}{
		{"judge MERGE ends the eval", Judged, "OPEN", Evaluated},
		{"merge step refuses", Merging, "OPEN", Failed},
		{"PR merged outside the runner", Gated, "MERGED", Failed},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, s, calls := evalRunner(t, c.prState, true)
			run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Branch: "factory/t-0001",
				State: c.state, Eval: true, EvalMode: EvalFull, PRURL: pr, Head: strings.Repeat("a", 40),
				Verdicts: []Verdict{{Verdict: "MERGE"}}}
			_ = s.PutRun(run)
			if _, err := r.Advance("T-0001", nil, false); err != nil {
				t.Fatal(err)
			}
			got, _ := s.Run("T-0001")
			if got.State != c.want || merged(*calls) {
				t.Fatalf("state %s (want %s), gh calls %v", got.State, c.want, *calls)
			}
			closed := false
			for _, x := range *calls {
				closed = closed || strings.HasPrefix(x, "pr close")
			}
			if !closed {
				t.Errorf("a full eval run's PR is closed: %v", *calls)
			}
		})
	}
	// control: the same verdict on a normal run moves on to the merge step
	r, s, _ := evalRunner(t, "OPEN", false)
	_ = s.PutRun(&Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", State: Judged, PRURL: pr,
		Verdicts: []Verdict{{Verdict: "MERGE"}}})
	run := mustRun(t, s)
	r.judged(run)
	if run.State != Merging {
		t.Errorf("a normal run's MERGE goes to the merge step, got %s", run.State)
	}
}

func mustRun(t *testing.T, s *SQLite) *Run {
	run, err := s.Run("T-0001")
	if err != nil || run == nil {
		t.Fatal(err)
	}
	return run
}

func TestEvalWritesNoVerdictRow(t *testing.T) {
	r, s, _ := evalRunner(t, "OPEN", true)
	run := &Run{Task: "T-0001", PRURL: "u", JudgeHead: "h", Eval: true, Judges: []string{"j"}}
	if err := r.RecordVerdict(run, &Verdict{Verdict: "MERGE"}, "sid", "j"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM verdicts WHERE task_id='T-0001'`).Scan(&n)
	if n != 0 || run.State != Judged {
		t.Errorf("eval verdict rows %d, state %s", n, run.State)
	}
	run.Eval = false
	_ = r.RecordVerdict(run, &Verdict{Verdict: "MERGE"}, "sid", "j")
	_ = s.db.QueryRow(`SELECT count(*) FROM verdicts WHERE task_id='T-0001'`).Scan(&n)
	if n != 1 {
		t.Errorf("a normal verdict writes one row, got %d", n)
	}
}

func TestEvalStartNeedsADraftPR(t *testing.T) {
	r, s, _ := evalRunner(t, "OPEN", false)
	_, _ = s.db.Exec(`UPDATE tasks SET acceptance='1. x - cmd:true => exit 0'`)
	if err := r.EvalStart("T-0001", EvalFull); err == nil || !strings.Contains(err.Error(), "draft") {
		t.Fatalf("a ready PR must be refused: %v", err)
	}
	if err := r.EvalStart("T-0001", "merge"); err == nil {
		t.Error("an unknown mode is refused")
	}
}

func TestEvalKickoffReadsLikeAResumedTask(t *testing.T) {
	run := &Run{Agent: "claude-coder-t-0001-a7", Branch: "factory/t-0001", Base: "main", PRURL: "https://x/pull/1"}
	k := EvalKickoff(run, &Task{ID: "T-0001", Title: "t", Spec: "s", Acceptance: "1. x - cmd:true => exit 0"})
	if strings.Contains(strings.ToLower(k), "eval") || !strings.Contains(k, "Do not start over") || !strings.Contains(k, "/code-review") {
		t.Errorf("kickoff: %q", k)
	}
}
