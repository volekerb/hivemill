package guard

import "strings"

// T-1035 / T-1064: `git config` read classifier (ports worker-guard.py config_parse / config_is_read). Exact tables
// only; anything not proven a read is a write (fail closed).

const configLate = "\x00late"

var (
	configFlags = set("--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "--list", "-l",
		"--show-origin", "--show-scope", "--name-only", "--all", "--regexp", "--global", "--local", "--system",
		"--worktree", "--null", "-z", "--bool", "--int", "--bool-or-int", "--bool-or-str", "--path",
		"--expiry-date", "--includes", "--no-includes")
	configTrailing = set("--show-origin", "--show-scope", "--name-only", "--all", "--regexp", "--global", "--local", "--system",
		"--worktree", "--null", "-z", "--bool", "--int", "--bool-or-int", "--bool-or-str", "--path",
		"--expiry-date", "--includes", "--no-includes")
	configParseValueOpts = set("--type", "-t", "--default", "--comment", "--blob", "--value", "--file", "-f")
	configActionFlags    = set("--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "--list", "-l")
	configWordWrite      = set("set", "unset", "edit", "rename-section", "remove-section")
)

// configParse is a port of config_parse: (flags, positionals), or ok=false when a token is outside the exact table.
func configParse(args []string) (flags, pos []string, ok bool) {
	k := 0
	for k < len(args) {
		a := args[k]
		k++
		switch {
		case in(configFlags, a) && (len(pos) == 0 || in(configTrailing, a)):
			flags = append(flags, a)
			if len(pos) > 0 {
				flags = append(flags, configLate)
			}
		case in(configParseValueOpts, a) || configParseValueOptEq(a):
			if len(pos) > 0 {
				flags = append(flags, configLate)
			}
			if !strings.Contains(a, "=") {
				if k >= len(args) {
					return nil, nil, false
				}
				k++
			}
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, nil, false
		default:
			pos = append(pos, a)
		}
	}
	return flags, pos, true
}

func configParseValueOptEq(a string) bool {
	if !strings.HasPrefix(a, "--") || !strings.Contains(a, "=") {
		return false
	}
	name, _, _ := strings.Cut(a, "=")
	return in(configParseValueOpts, name)
}

// configIsRead is a port of config_is_read.
func configIsRead(args []string) bool {
	flags, pos, ok := configParse(args)
	if !ok {
		return false
	}
	if sliceHas(flags, "--regexp") && (len(pos) == 0 || pos[0] != "get") {
		return false
	}
	var actions []string
	for _, f := range flags {
		if in(configActionFlags, f) {
			actions = append(actions, f)
		}
	}
	if len(actions) > 1 {
		return false
	}
	if len(actions) > 0 {
		return len(pos) <= 2
	}
	if sliceHas(flags, configLate) && (len(pos) == 0 || (pos[0] != "get" && pos[0] != "list")) {
		return false
	}
	if len(pos) > 0 && pos[0] == "get" {
		return len(pos) <= 3
	}
	if len(pos) > 0 && pos[0] == "list" {
		return len(pos) == 1
	}
	if len(pos) > 1 {
		return false
	}
	if len(pos) == 1 && (pos[0] == "set" || in(configWordWrite, pos[0])) {
		return false
	}
	return true
}

func sliceHas(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

// configArgsWrite is a port of Analyzer.config_args_write.
func configArgsWrite(args []string) bool {
	return !configIsRead(args)
}

// configMutates is a port of Analyzer.config_mutates.
func configMutates(args []string) bool {
	return !configIsRead(args)
}

// configValueOptAbbrev matches the git_persist strip loop: unique abbreviations of value options (not --file).
func configValueOptAbbrev(a string) bool {
	if len(a) < 3 || !strings.HasPrefix(a, "--") || strings.Contains(a, "=") {
		return false
	}
	for _, o := range []string{"--type", "--default", "--comment", "--blob", "--value"} {
		if strings.HasPrefix(o, a) {
			return true
		}
	}
	return false
}

// configExpansionRefuse mirrors worker-guard.py config_expansion_refuse; T-1103 and T-1137 extend it.
// T-1152: only the swallowed-flag clause — a token after a value option must not be a config flag or write word.
func (a *Analyzer) configExpansionRefuse(toks []string) error {
	if a.HQ {
		return nil
	}
	skip := false
	for _, t := range toks {
		if skip {
			skip = false
			if in(configFlags, t) || in(configWordWrite, t) {
				return blockf("git config: " + pyRepr(t) + " follows a value option, so git takes it as the option's value and " +
					"not as an action; put the action flag first, or write the arguments literally (T-1063, T-1097)")
			}
			continue
		}
		if in(configParseValueOpts, t) || configValueOptAbbrev(t) {
			skip = true
		}
	}
	return nil
}
