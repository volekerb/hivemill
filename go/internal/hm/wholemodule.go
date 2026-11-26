package hm

import (
	"fmt"
	"slices"
	"strings"

	"factory/go/internal/shlex"
)

// FilingProblems is the spec gate of a task being filed or checked (`task lint|show|add|update`): LintProblems, plus
// proofs too costly to file. Dispatch keeps to LintProblems, so a task filed before such a rule still runs.
func FilingProblems(acceptance string) []string {
	probs := LintProblems(acceptance)
	for _, c := range Criteria(acceptance) {
		if (c.Kind == "cmd" || c.Kind == "test") && wholeModuleGoTest(c.Proof) {
			probs = append(probs, fmt.Sprintf("criterion %d: name the packages the task touches (go test ./internal/hm/...); "+
				"CI runs the full suite", c.ID))
		}
	}
	return probs
}

// wholeModuleGoTest reports a `go test ./...` in the shell command, whatever its flags. After a cd (or go -C) into an
// internal, cmd or pkg directory, ./... is only that subtree and passes.
func wholeModuleGoTest(command string) bool {
	words, err := shlex.Script(command)
	if err != nil { // prose with a lone quote after a test: command
		words = strings.Fields(command)
	}
	subtree := func(dir string) bool {
		return slices.ContainsFunc(strings.Split(dir, "/"), func(s string) bool { return s == "internal" || s == "cmd" || s == "pkg" })
	}
	var inGo, inTest, inSub, dashC bool
	for i, w := range words {
		switch {
		case w != "" && strings.Trim(w, "();<>|&") == "": // an operator ends the command
			inGo, inTest, dashC = false, false, false
		case w == "cd" && i+1 < len(words):
			inSub = subtree(words[i+1])
		case w == "go":
			inGo, dashC = true, false
		case inGo && !inTest && w == "-C" && i+1 < len(words):
			dashC = subtree(words[i+1])
		case inGo && w == "test":
			inTest = true
		case inTest && w == "./..." && !inSub && !dashC:
			return true
		}
	}
	return false
}
