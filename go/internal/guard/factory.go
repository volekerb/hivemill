package guard

import (
	"os"
	"strings"
)

// The factory-CLI rules of the Analyzer: temp FACTORY_HOME detection, identity and scrub variables, `factory
// up|tick|...`, the evaluator's factory subset and the broad reads. Decisions that need the factory DB are the
// Python hook's (owngoal.go).

// tempHome is a port of Analyzer.temp_home: assigns has FACTORY_HOME=<path> with a static path that is not a live home.
func (a *Analyzer) tempHome(assigns []string) bool {
	var vals []string
	for _, x := range assigns {
		if v, ok := strings.CutPrefix(x, "FACTORY_HOME="); ok {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return false
	}
	v := vals[len(vals)-1]
	if v == "" || strings.ContainsAny(v, "$`*?[{") {
		return false // dynamic: could be the live home
	}
	p := expandUser(v)
	if !strings.HasPrefix(p, "/") {
		p = pyJoin(a.Cwd, p)
	}
	return !containsStr(analyzerLiveHomes(), pyRealpath(p))
}

// outsideLive is a port of Analyzer.outside_live.
func (a *Analyzer) outsideLive(path string) bool {
	p := expandUser(path)
	if !strings.HasPrefix(p, "/") {
		p = pyJoin(a.Cwd, p)
	}
	p = pyRealpath(p)
	h := mustHost()
	homes := append(analyzerLiveHomes(), h.liveHomes...)
	homes = append(homes, h.liveHome)
	for _, home := range homes {
		if Under(p, home) {
			return false
		}
	}
	return true
}

// agentTempHome is a port of Analyzer.agent_temp_home: assigns has FACTORY_HOME=<path> outside the live home AND
// ~/factory (worktrees included), static or exactly $(mktemp [-d] [-p <static dir outside ~/factory>]).
func (a *Analyzer) agentTempHome(assigns []string) bool {
	var vals []string
	for _, x := range assigns {
		if v, ok := strings.CutPrefix(x, "FACTORY_HOME="); ok {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 || vals[len(vals)-1] == "" {
		return false
	}
	v := vals[len(vals)-1]
	if strings.HasPrefix(v, "$(mktemp") && strings.HasSuffix(v, ")") {
		ws := pyFields(v[2 : len(v)-1])
		d, hasD := "", false
		for k := 1; k < len(ws); {
			switch {
			case ws[k] == "-d":
				k++
			case ws[k] == "-p" && k+1 < len(ws):
				d, hasD = ws[k+1], true
				k += 2
			default:
				return false
			}
		}
		if !hasD { // mktemp falls back to $TMPDIR: it must not point into the factory home
			for _, x := range assigns {
				if strings.HasPrefix(x, "TMPDIR=") {
					return false
				}
			}
			d = os.Getenv("TMPDIR")
			if d == "" {
				d = "/tmp"
			}
		}
		return !strings.ContainsAny(d, "$`*?[{") && a.outsideLive(d)
	}
	if strings.ContainsAny(v, "$`*?[{") {
		return false
	}
	return a.outsideLive(v)
}

// checkIdentity is a port of Analyzer.check_identity.
func (a *Analyzer) checkIdentity(assigns []string) error {
	own := os.Getenv("FACTORY_AGENT")
	for _, x := range assigns {
		name, v := x, ""
		if k := strings.IndexByte(x, '='); k >= 0 {
			name, v = x[:k], x[k+1:]
		}
		name = strings.TrimRight(name, "+")
		if !containsStr(IdentVars, name) {
			continue
		}
		if name == "FACTORY_AGENT" && own != "" && v == own && v != "owner" {
			continue
		}
		if a.agentTempHome(assigns) {
			continue
		}
		return blockf("workers may not assign " + name + " (identity); only FACTORY_AGENT=<own name>, " +
			"or with a temp FACTORY_HOME in the same command")
	}
	return nil
}

// evaluatorFactory is a port of Analyzer.evaluator_factory: the evaluator runs in a sandbox with a temp
// FACTORY_HOME: only whoami, task show, inbox, msg and done, and only against a home that is not the live one (the
// command's own FACTORY_HOME=, else the hook's env).
func (a *Analyzer) evaluatorFactory(sub string, hasSub bool, nonopt []string, assigns []string) error {
	if !(hasSub && (sub == "whoami" || sub == "inbox" || sub == "msg" || sub == "done") ||
		len(nonopt) >= 2 && nonopt[0] == "task" && nonopt[1] == "show") {
		return blockf("the evaluator may not run `factory " + sub + "`; allowed: whoami, task show, inbox, msg, done")
	}
	var ok bool
	if anyOf(assigns, func(x string) bool { return strings.HasPrefix(x, "FACTORY_HOME=") }) {
		ok = a.agentTempHome(assigns)
	} else {
		env := os.Getenv("FACTORY_HOME")
		ok = env != "" && !containsStr(analyzerLiveHomes(), pyRealpath(env)) && a.outsideLive(env)
	}
	if !ok {
		return blockf("the evaluator may run factory commands only against its temp FACTORY_HOME, not the live home")
	}
	return nil
}

// factoryUnit is a port of Analyzer.factory_unit: block `factory up|tick|relaunch|spawn|clean` (also via
// python3 <path>/factory) in the live home.
func (a *Analyzer) factoryUnit(name string, rest []string, assigns []string) error {
	if interpreterFull(name) {
		si, ci, _ := InterpArgs(name, rest)
		stdinCode := (si < 0 || rest[si] == "-") && ci < 0 && !anyOf(rest, reDashLettM.MatchString)
		if (ci >= 0 && strings.Contains(rest[ci], "factory_cli")) || (stdinCode && strings.Contains(a.text, "factory_cli")) {
			return blockf("inline code naming factory_cli runs the factory CLI around the guard; run `factory <cmd>`")
		}
		fi := factoryScript(name, rest)
		if fi < 0 {
			return nil
		}
		rest = rest[fi+1:]
	} else if name != "factory" {
		return nil
	}
	var nonopt []string
	for _, x := range rest {
		if !strings.HasPrefix(x, "-") {
			nonopt = append(nonopt, x)
		}
	}
	sub, hasSub := "", false
	if len(nonopt) > 0 {
		sub, hasSub = nonopt[0], true
	}
	if a.Role == "evaluator" {
		if err := a.evaluatorFactory(sub, hasSub, nonopt, assigns); err != nil {
			return err
		}
	}
	if hasSub {
		if _, ok := BroadReads[sub]; ok {
			if err := a.broadRead(sub, rest, assigns); err != nil {
				return err
			}
		}
	}
	if !hasSub || !in(FactoryUnitCmds, sub) {
		return nil
	}
	if sub == "clean" && a.Role == "cleaner" {
		return nil
	}
	if a.tempHome(assigns) {
		return nil
	}
	return blockf("workers may not run `factory " + sub + "`")
}

// broadRead is a port of Analyzer.broad_read: block broad factory reads (res, agents, goal list, task list, ...)
// against the live home for workers. rest is the tokens after `factory` (options included). Exempt: --role
// staff-engineer, `agents` for --role cleaner, `goal show <own goal>`, a temp FACTORY_HOME. Fail closed on options:
// any option token before the action word (or before the id of `goal show`) blocks the command.
func (a *Analyzer) broadRead(sub string, rest []string, assigns []string) error {
	if a.Role == "staff-engineer" {
		return nil
	}
	seconds := BroadReads[sub]
	shown := "factory " + sub
	subI := 0
	for subI < len(rest) && strings.HasPrefix(rest[subI], "-") {
		subI++
	}
	after := rest[subI+1:]
	if seconds != nil {
		actI := -1
		for i, x := range after {
			if !strings.HasPrefix(x, "-") {
				actI = i
				break
			}
		}
		if actI < 0 {
			return nil // no action word: argparse usage error, reads nothing
		}
		action := after[actI]
		switch {
		case subI > 0 || actI > 0:
			shown += " <options> " + action
		case sub == "goal" && action == "show":
			gid, hasGid := "", len(after) > 1
			if hasGid {
				gid = after[1]
			}
			if hasGid && !strings.HasPrefix(gid, "-") && !ownGoalKnown() {
				return delegate("broad_read: own_goal of the agent is in the factory DB")
			}
			shown += " show"
		case in(seconds, action):
			shown += " " + action
		default:
			return nil
		}
	}
	if sub == "agents" && a.Role == "cleaner" {
		return nil
	}
	if a.tempHome(assigns) || a.agentTempHome(assigns) {
		return nil
	}
	return blockf("workers may not run broad factory reads (`" + shown + "`) against the live home; use " +
		"factory task show <id> / goal show <own goal>, or set a temp FACTORY_HOME in the same command")
}
