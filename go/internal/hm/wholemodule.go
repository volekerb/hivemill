package hm

import (
	"fmt"
	"path"
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

// wholeModuleGoTest reports a `go test ./...` in the shell command (also inside `sh -c`), whatever its flags. In an
// internal, cmd or pkg directory (cd, pushd or go -C), ./... is only that subtree and passes.
func wholeModuleGoTest(command string) bool { return wholeModuleIn("", command) }

func wholeModuleIn(dir, command string) bool {
	words, err := shlex.Script(command)
	if err != nil { // prose with a lone quote after a test: command
		words = strings.Fields(command)
	}
	join := func(base, to string) string {
		if path.IsAbs(to) {
			return to
		}
		return path.Join(base, to)
	}
	subtree := func(d string) bool {
		return slices.ContainsFunc(strings.Split(d, "/"), func(s string) bool { return s == "internal" || s == "cmd" || s == "pkg" })
	}
	var shells, pushed []string // the directory outside each open subshell and each pushd
	var inShell, inGo, inTest bool
	goDir := dir
	skip := -1 // the index of a redirect target or a -C directory: never read as a command
	for i, w := range words {
		if i == skip {
			continue
		}
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		switch {
		case strings.ContainsAny(w, "<>") && strings.Trim(w, "<>&|") == "": // a redirect: the command goes on
			skip = i + 1
		case w != "" && strings.Trim(w, "();<>|&") == "": // an operator ends the command
			for _, c := range w {
				if c == '(' {
					shells = append(shells, dir)
				} else if c == ')' && len(shells) > 0 {
					dir, shells = shells[len(shells)-1], shells[:len(shells)-1]
				}
			}
			inShell, inGo, inTest = false, false, false
		case w == "cd":
			dir = join(dir, next)
		case w == "pushd":
			pushed, dir = append(pushed, dir), join(dir, next)
		case w == "popd" && len(pushed) > 0:
			dir, pushed = pushed[len(pushed)-1], pushed[:len(pushed)-1]
		case path.Base(w) == "sh" || path.Base(w) == "bash" || path.Base(w) == "zsh":
			inShell = true
		case inShell && strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "c"):
			if wholeModuleIn(dir, next) {
				return true
			}
			inShell = false
		case path.Base(w) == "go":
			inGo, inTest, goDir = true, false, dir
		case inGo && w == "-C":
			goDir, skip = join(dir, next), i+1
		case inGo && strings.HasPrefix(w, "-C="):
			goDir = join(dir, strings.TrimPrefix(w, "-C="))
		case inGo && w == "test":
			inTest = true
		case inTest && w == "./..." && !subtree(goDir):
			return true
		}
	}
	return false
}
