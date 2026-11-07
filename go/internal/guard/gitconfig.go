package guard

import (
	"regexp"
	"strings"
)

var (
	configGlobChar   = regexp.MustCompile(`[*?\[]`)
	configKeyZshGlob = regexp.MustCompile(`[()|<>^~#]`)
)

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

func configTokenExpansionBuilt(t string) bool {
	if strings.Contains(t, "`") || strings.Contains(t, "{") {
		return true
	}
	for i := 0; i+1 < len(t); i++ {
		if t[i] == '$' {
			return true
		}
	}
	return false
}

// configExpansionValueOpt matches config_expansion_refuse value-option detection (includes --file abbreviations).
func configExpansionValueOpt(t string) bool {
	if in(configParseValueOpts, t) {
		return true
	}
	if len(t) >= 3 && strings.HasPrefix(t, "--") && !strings.Contains(t, "=") {
		for _, o := range []string{"--type", "--default", "--comment", "--blob", "--value", "--file"} {
			if strings.HasPrefix(o, t) {
				return true
			}
		}
	}
	return false
}

// configExpansionRefuse mirrors worker-guard.py config_expansion_refuse.
func (a *Analyzer) configExpansionRefuse(toks []string) error {
	if a.HQ {
		return nil
	}
	read, unsure, skip := false, false, false
	var key string
	hasKey := false
	expansionRefuse := func(t string) error {
		return blockf("git config: argument " + t + " is built from an expansion, so it could be a write; " +
			"write the arguments literally (T-1063)")
	}
	for n, t := range toks {
		if configTokenExpansionBuilt(t) || (configGlobChar.MatchString(t) && !read) {
			return expansionRefuse(t)
		}
		if skip {
			skip = false
			if in(configFlags, t) || in(configWordWrite, t) {
				return blockf("git config: `" + t + "` follows a value option, so git takes it as the option's value and " +
					"not as an action; put the action flag first, or write the arguments literally (T-1063, T-1097)")
			}
			continue
		}
		name, _, _ := strings.Cut(t, "=")
		switch {
		case configExpansionValueOpt(t):
			skip = true
		case strings.HasPrefix(t, "-") && t != "-" && !in(configFlags, t) && !in(configActionFlags, t) &&
			!in(configParseValueOpts, name):
			unsure = true
		case (in(configActionFlags, t) || (n == 0 && (t == "get" || t == "list"))) && !unsure:
			read = true
		case !hasKey && !strings.HasPrefix(t, "-"):
			hasKey = true
			key = t
		}
	}
	if read {
		return nil
	}
	if skip {
		return blockf("git config: a value option has no value, so zsh may have expanded a glob away from it and " +
			"the command could be a write; write the arguments literally (T-1063, T-1097)")
	}
	if (!unsure && !hasKey) || (hasKey && configKeyZshGlob.MatchString(key)) {
		scope := ""
		for _, t := range toks {
			if t == "--global" || t == "--system" {
				scope = t
				break
			}
		}
		if scope != "" {
			return blockf("`git config " + scope + "` write: " + persistWhy)
		}
		return blockf("git config: no read action flag is proven and no literal key is given (a zsh glob such as " +
			"(a|b), <->, ^z, a~b or al# expands into more arguments, or the key is missing), so it could " +
			"be a write; write the arguments literally (T-1063, T-1097)")
	}
	return nil
}
