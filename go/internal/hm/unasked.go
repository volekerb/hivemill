package hm

import (
	"fmt"
	"slices"
	"strings"

	"factory/go/internal/citools/risktier"
)

// Unasked changes the judge found plausibly fine (unasked_ok): they merge, and the owner reads them afterwards in a PR
// comment, judge.unasked_ok events and the daily digest.

// unaskedRiskPaths are the paths whose unasked changes the judge cannot clear: guard, hooks, role settings and
// prompts, merge and judge code, workflows. The project's sensitive_paths of the tier rules count as well.
var unaskedRiskPaths = []string{
	".github/workflows/**", "roles/**",
	"go/internal/guard/**", "go/cmd/worker-guard/**", "go/internal/shlex/**", "go/internal/spawn/settings.go",
	"go/internal/hm/*hook*", "go/internal/hm/guard*", "go/internal/hm/judge*", "go/internal/hm/unasked*",
	"go/internal/hm/merge*", "go/internal/hm/run.go", "go/internal/hm/gate.go", "go/internal/hm/cigate.go",
	"go/internal/hm/risk.go", "go/internal/hm/tierrules.go", "go/internal/hm/session.go", "go/internal/hm/store.go",
	"go/internal/hm/resilience.go", "go/internal/sandbox/**", "go/internal/macbox/**", "go/internal/gowrap/**",
	"go/internal/hm/assets/task*", "go/internal/hm/assets/planner*", "go/internal/hm/assets/skills.json",
	"go/internal/hm/assets/tier_rules.json", "go/internal/citools/risktier/**", "go/internal/citools/affected/**",
	"go/internal/hm/spec.go", "go/internal/hm/scope.go", "go/internal/hm/proofrun.go", "go/internal/hm/tools.go",
	"go/internal/gitx/**", "go/internal/ghx/**", "go/cmd/hivemill/**",
	"go/internal/hm/ciwait.go", "go/internal/hm/projectgate.go", "go/internal/hm/pr*", "go/internal/hm/touchedproofs.go",
	"go/internal/hm/wholemodule.go", "go/internal/hm/trust.go", "go/internal/install/**", "ci/**",
	"bin/factory", "bin/factory_cli.py",
}

func unmappedChange(c Change) bool {
	return c.Criterion == nil || strings.TrimSpace(*c.Criterion) == ""
}

// clearedUnasked: no criterion asks for the change, and the judge marked it plausibly fine and said why.
func clearedUnasked(c Change) bool {
	return unmappedChange(c) && c.UnaskedOK && strings.TrimSpace(c.Why) != ""
}

// unaskedRisk says, for one cleared change of the run's diff, why it may touch a risk-tier path ("" = it does not).
// While the diff changes such a path, a change is outside it only when its file names another changed file. Anything
// the runner cannot decide counts as risk.
func (r *Runner) unaskedRisk(run *Run) func(Change) string {
	rules, _, err := r.TierRules(run.Project)
	if err != nil {
		return func(Change) string { return "the tier rules cannot be read" }
	}
	// --no-renames: a file moved out of a risk path still counts under its old name
	// -z: no quoting of unusual names. The judged head, not HEAD: the clone may have moved on since.
	rc, out, _ := r.Tools.Git(run.Worktree, "diff", "--name-only", "--no-renames", "-z", "origin/"+run.Base+"..."+run.JudgeHead)
	files := strings.FieldsFunc(out, func(c rune) bool { return c == 0 })
	if rc != 0 || len(files) == 0 {
		return func(Change) string { return "the changed files cannot be listed" }
	}
	only := risktier.Rules{SensitivePaths: append(slices.Clone(unaskedRiskPaths), rules.SensitivePaths...)}
	var risky []string
	for _, f := range files {
		if sensitivePath(f, only) {
			risky = append(risky, f)
		}
	}
	return func(c Change) string {
		f := strings.TrimSpace(c.File)
		switch {
		case len(risky) == 0:
			return ""
		case slices.Contains(risky, f):
			return f + " is a risk-tier path"
		case !slices.Contains(files, f):
			return "the diff changes the risk-tier path " + risky[0] + " and the change names no changed file"
		}
		return ""
	}
}

// overruleMerge holds a MERGE to its unasked changes. One without unasked_ok and a why turns it into a REJECT: an
// unasked change never merges unmarked. A marked one in a risk-tier path turns it into a HOLD. Otherwise it stays.
func overruleMerge(v *Verdict, risk func(Change) string) {
	var rejected, held []string
	for _, c := range v.Changes {
		switch {
		case !unmappedChange(c):
		case !clearedUnasked(c):
			rejected = append(rejected, truncate(c.Change, 200))
		default:
			if why := risk(c); why != "" {
				held = append(held, truncate(c.Change, 200)+" ("+why+")")
			}
		}
	}
	var verdict, reason string
	switch {
	case len(rejected) > 0:
		verdict, reason = VerdictReject, "the judge said MERGE, but these changes map to no task criterion and are not marked "+
			"unasked_ok with a why; remove them, or the owner adds a criterion that asks for them: "+strings.Join(rejected, " | ")
	case len(held) > 0:
		verdict, reason = VerdictHold, "the judge said MERGE, but these unasked changes may touch a risk-tier path, where the "+
			"owner decides: "+strings.Join(held, " | ")
	default:
		return
	}
	v.JudgeVerdict, v.Verdict = v.Verdict, verdict
	v.Reasons = append([]string{reason}, v.Reasons...)
}

// reportUnasked runs once, when the run's PR has merged: one judge.unasked_ok event per cleared unasked change of the
// last verdict (a MERGE, or a HOLD the owner merged), and one PR comment that lists them. The repo may be public, so
// text that the public-text guard does not pass is left out of the comment. The comment says so when the merged head is
// not the judged one.
func (r *Runner) reportUnasked(run *Run, merged string) {
	if run.Eval || len(run.Verdicts) == 0 {
		return
	}
	v := run.Verdicts[len(run.Verdicts)-1]
	if v.Verdict == VerdictReject {
		return
	}
	var list strings.Builder
	n := 0
	for _, c := range v.Changes {
		if !clearedUnasked(c) {
			continue
		}
		n++
		change, why := oneLine(c.Change, 300), oneLine(c.Why, 300)
		_ = r.Log.Emit("judge.unasked_ok", run, Event{Role: "judge", Data: map[string]any{"change": change, "why": why, "pr": run.PRURL}})
		// an indented code block: quoted diff lines cannot mention anyone or break the markdown
		fmt.Fprintf(&list, "    - %s\n      why: %s\n", change, why)
	}
	if n == 0 || run.PRURL == "" {
		return
	}
	body := "Unasked changes merged\n\nNo task criterion asked for these; the judge found them plausibly fine.\n\n"
	if merged != "" && merged != run.JudgeHead {
		body += fmt.Sprintf("The judge read head %.12s; the PR merged at %.12s. What changed in between was not judged.\n\n", run.JudgeHead, merged)
	}
	args, why := r.guardArgs()
	rc := 1
	if why == "" {
		rc, _ = r.guardText(args, list.String())
	}
	switch {
	case why != "":
		r.printf("%s: unasked changes left out of the PR comment: %s\n", run.Task, why)
		body += fmt.Sprintf("%d changes, left out here: the public-text guard could not run. See the daily digest.\n", n)
	case rc != 0:
		body += fmt.Sprintf("%d changes, left out here: the public-text guard did not pass them. See the daily digest.\n", n)
	default:
		body += list.String()
	}
	if rc, _, errs := r.Tools.GH(body, "pr", "comment", run.PRURL, "--body-file", "-"); rc != 0 {
		r.printf("%s: PR comment failed: %s\n", run.Task, firstLine(errs))
	}
}

// oneLine is s on one line, at most max bytes, never ending in half a character.
func oneLine(s string, max int) string {
	return strings.ToValidUTF8(truncate(strings.Join(strings.Fields(s), " "), max), "")
}
