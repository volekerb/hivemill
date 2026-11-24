// Package sizeclass maps a PR diff to a size class (small, medium, large), an open-to-merge budget in minutes and the
// checks to run (G-056). The risky decision is tier.Classify; this package never reads the risky lists itself.
//
// Like ci-tier it is meant to run from the BASE commit so a PR cannot change its own class. Any git or classify error
// gives the fail-safe: class=large, budget_min=40, checks=full.
package sizeclass

import (
	"fmt"
	"strconv"
	"strings"

	"factory/go/internal/citools/affected"
	"factory/go/internal/citools/tier"
)

const (
	SmallMax  = 40
	MediumMax = 300

	BudgetSmall  = 10
	BudgetMedium = 30
	BudgetLarge  = 40
)

// Result is the selector output.
type Result struct {
	Class     string
	BudgetMin int
	Checks    string
	Lines     int
	Reason    string
}

// Failsafe is the result for anything that cannot be decided: the large class with the full suite.
func Failsafe(reason string) Result {
	return Result{Class: "large", BudgetMin: BudgetLarge, Checks: "full", Lines: 0, Reason: reason}
}

// countLines returns added + deleted lines from `git diff --numstat --no-renames base...head`; odd is true when a
// row has no line counts (binary file).
func countLines(root, base, head string) (lines int, odd bool, err error) {
	out, err := affected.Git(root, "diff", "--numstat", "--no-renames", base+"..."+head)
	if err != nil {
		return 0, false, err
	}
	if out == "" {
		return 0, false, nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			return 0, false, fmt.Errorf("malformed numstat line %q", line)
		}
		a, errA := strconv.Atoi(parts[0])
		d, errD := strconv.Atoi(parts[1])
		if errA != nil || errD != nil || a < 0 || d < 0 {
			odd = true
			continue
		}
		lines += a + d
	}
	return lines, odd, nil
}

// Select classifies the diff base...head.
func Select(root, base, head string) Result {
	lines, odd, err := countLines(root, base, head)
	if err != nil {
		return Failsafe(fmt.Sprintf("git error: %v", err))
	}
	tr, why, err := tier.Classify(root, base, head)
	if err != nil {
		return Failsafe(fmt.Sprintf("classify error: %v", err))
	}
	risky := tr == "risky" || odd
	if odd && tr != "risky" {
		why = "binary or non-line numstat row"
	}
	if risky {
		return Result{Class: "large", BudgetMin: BudgetLarge, Checks: "full", Lines: lines, Reason: "risky: " + why}
	}
	plan, err := tier.Plan(root, base, head)
	if err != nil {
		return Failsafe(fmt.Sprintf("plan error: %v", err))
	}
	switch {
	case lines <= SmallMax:
		checks := "smoke"
		if plan.Mode == "docs" {
			checks = "docs"
		}
		return Result{"small", BudgetSmall, checks, lines, fmt.Sprintf("%d changed lines (<= %d), %s", lines, SmallMax, why)}
	case lines <= MediumMax:
		return Result{"medium", BudgetMedium, plan.Mode, lines, fmt.Sprintf("%d changed lines (%d to %d), %s", lines, SmallMax+1, MediumMax, why)}
	}
	return Result{"large", BudgetLarge, plan.Mode, lines, fmt.Sprintf("%d changed lines (> %d), %s", lines, MediumMax, why)}
}
