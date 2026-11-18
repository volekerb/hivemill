package merge

import (
	"fmt"
	"strings"
)

// Verdict is the newest verdicts row of one role for a task (the row's verdict and head_sha; head NULL reads "").
type Verdict struct {
	Present       bool
	Verdict, Head string
}

// Kind is what verdict_pass_holds_for_pr_head returns first.
type Kind string

// The four answers of verdict_pass_holds_for_pr_head.
const (
	Missing      Kind = "missing"
	Holds        Kind = "holds"
	Carried      Kind = "carry"
	StaleVerdict Kind = "stale"
)

// Outcome is one role's answer: for Carried From/MainSHA/Merges, for StaleVerdict From/Why.
type Outcome struct {
	Role    string
	Kind    Kind
	From    string
	Why     string
	MainSHA string
	Merges  []string
}

// Evaluate is verdict_pass_holds_for_pr_head. carry is the memo of verdict head -> verdict_carries result (shared by
// both roles); unavailable, when non-nil, is the answer used instead of calling carries (gh repo view failed);
// carries runs the real check and is only called for a non-empty head with a known repo URL.
func Evaluate(role string, v Verdict, prHead string, carry map[string]Carry, unavailable *Carry, haveRepoURL bool,
	carries func(x string) (Carry, error)) (Outcome, error) {
	if !v.Present || v.Verdict != "pass" {
		return Outcome{Role: role, Kind: Missing}, nil
	}
	x := v.Head
	if x == prHead || pyLower(x) == pyLower(prHead) {
		return Outcome{Role: role, Kind: Holds}, nil
	}
	c, seen := carry[x]
	if !seen {
		switch {
		case unavailable != nil:
			c = *unavailable
		case !haveRepoURL:
			c = Carry{Why: "cannot verify, gh repo view failed"}
		case x != "":
			var err error
			if c, err = carries(x); err != nil {
				return Outcome{}, err
			}
		default:
			c = Carry{Why: "verdict has no head"}
		}
		carry[x] = c
	}
	if c.OK {
		return Outcome{Role: role, Kind: Carried, From: x, MainSHA: c.MainSHA, Merges: c.Merges}, nil
	}
	return Outcome{Role: role, Kind: StaleVerdict, From: x, Why: c.Why}, nil
}

// GateInput is what the first, always-run part of the gate decides on.
type GateInput struct {
	PR       PR
	Waited   int
	Outcomes []Outcome // reviewer, then qa
	Stale    Stale
	Required string // required_checks of the PR's project, else the global one
	NoAuto   bool   // the project has auto_merge=0
}

// Problems is the problem list of _judge_gate_and_merge for a head that contains main (Stale.OK) or whose staleness
// could not be decided; a plainly STALE head never gets here (the caller hands it to Python: refresh and behind_ok are
// not ported). The order is Python's: verdicts, then the staleness text in front, then state, draft, conflicts, checks,
// required checks, auto_merge.
func Problems(in GateInput) ([]string, error) {
	var problems []string
	sha := in.PR.HeadSHA
	for _, o := range in.Outcomes {
		switch o.Kind {
		case Missing:
			problems = append(problems, fmt.Sprintf("no %s PASS", o.Role))
		case StaleVerdict:
			problems = append(problems, fmt.Sprintf("%s PASS is for %s, head is now %s (%s)", o.Role, short(o.From), short(sha), o.Why))
		}
	}
	if in.Stale.Why == "STALE" && !in.Stale.OK {
		return nil, fmt.Errorf("stale head: %w", ErrUnsupported)
	}
	if !in.Stale.OK {
		problems = append([]string{"cannot verify head contains origin/main: " + in.Stale.Why}, problems...)
	}
	if in.PR.State != "OPEN" {
		problems = append(problems, "PR is "+in.PR.State)
	}
	if in.PR.Draft {
		problems = append(problems, "PR is a draft")
	}
	if in.PR.Mergeable == "CONFLICTING" {
		problems = append(problems, "PR has conflicts")
	}
	bad, err := ChecksOK(in.PR.Rollup)
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		w := ""
		if in.Waited != 0 {
			w = fmt.Sprintf(" (waited %ds)", in.Waited)
		}
		problems = append(problems, "checks not green: "+strings.Join(bad, ", ")+w)
	}
	miss, err := MissingChecks(in.Required, in.PR.Rollup)
	if err != nil {
		return nil, err
	}
	problems = append(problems, miss...)
	if in.NoAuto {
		problems = append(problems, "project has auto_merge=0 — boss must get the owner's OK")
	}
	return problems, nil
}

// ClassifyGateRefusalCause is classify_gate_refusal_cause.
func ClassifyGateRefusalCause(detail string) string {
	d := pyLower(detail)
	for _, k := range []string{"stale", "behind", "conflict", "carries", "head moved", "stale-only", "stale-loop", "cannot verify", "refresh"} {
		if strings.Contains(d, k) {
			return "stale_or_conflicting_branch"
		}
	}
	if strings.Contains(d, "checks not green") || strings.Contains(d, "required check") {
		return "missing_test"
	}
	return "gate_refusal"
}
