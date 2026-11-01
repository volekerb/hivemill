package guard

import (
	"os"
	"regexp"
	"strings"
)

// G-029 T-0846 (worker-guard.py): the researcher role. A no-push role that may clone a public https repo into its
// research dir <state>/factory/research/<name> and write only there or in its scratchpad; WORKER_GUARD_STATE (the
// hook's own env, tests only) replaces <state> (default ~/.local/state).

const researcherRole = "researcher"

var (
	researchGhGroups = set("pr", "issue", "release", "repo", "run")
	researchGhRead   = set("view", "list", "diff", "status", "checks")
	researchFactory  = set("whoami", "inbox", "msg", "done", "report")
	cloneFlags       = set("--single-branch", "--no-tags", "--filter=blob:none", "--quiet", "-q")
	cloneBranch      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	cloneURL         = regexp.MustCompile(`^https://(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}/[A-Za-z0-9._~+/-]+$`)
	cloneDepth       = regexp.MustCompile(`^--depth=[0-9]+$`)
	cloneDigits      = regexp.MustCompile(`^[0-9]+$`)

	// \s is spelled out (gitSp) and \b as a non-word follower: Python's are unicode-aware, RE2's are ASCII.
	researchSecretText = regexp.MustCompile(`oauth-token|rw-token|\.credentials\.json|(?:^|[/` + gitSp + `'"=~:])\.(?:ssh|gnupg|netrc|aws)(?:[/` +
		gitSp + `'";]|$)|\.config/(?:factory|gh)(?:[^\p{L}\p{N}_]|$)`)
	researchSecrets = []string{".ssh", ".gnupg", ".aws", ".netrc", ".git-credentials", ".npmrc", ".docker", ".kube", ".config/gcloud", ".config/factory", ".config/gh", ".claude/.credentials.json"}
)

func researchRoot() string {
	base := os.Getenv("WORKER_GUARD_STATE")
	if base == "" {
		base = pyJoin(mustHost().pwDir, ".local/state")
	}
	return pyRealpath(pyJoin(base, "factory/research"))
}

// researchDir is research_dir: root/<first component of cwd below root>; "" where Python returns None.
func researchDir(cwd string) string {
	root := researchRoot()
	c := pyRealpath(cwd)
	if c == root || !Under(c, root) {
		return ""
	}
	return root + "/" + strings.SplitN(c[len(root)+1:], "/", 2)[0]
}

// researcherArea is researcher_area: path (real and lexical) lies in cwd's research dir or in the scratchpad.
func researcherArea(path, cwd string) bool {
	rd := researchDir(cwd)
	for _, p := range []string{pyRealpath(path), pyNormpath(path)} {
		if !((rd != "" && Under(p, rd)) || inScratchpad(p)) {
			return false
		}
	}
	return true
}

func researcherSecretPath(p string) bool {
	pw := mustHost().pwDir
	for _, t := range researchSecrets {
		for _, base := range []string{pw, pyRealpath(pw)} {
			s := pyJoin(base, t)
			if Under(p, s) || Under(s, p) {
				return true
			}
		}
	}
	b := pyBasename(p)
	return strings.HasSuffix(b, "-token") || strings.HasSuffix(b, ".credentials.json") ||
		(strings.HasPrefix(p, "/proc/") && strings.HasSuffix(p, "/environ"))
}

// IsResearcher reports whether role is the researcher role.
func IsResearcher(role string) bool { return role == researcherRole }

// ResearcherTool is a port of researcher_tool: Edit/Write/MultiEdit/NotebookEdit only inside the research dir or
// scratchpad; Read/Grep never a secret.
func ResearcherTool(data map[string]any) (int, string) {
	ti := toolInput(data)
	cwd := payloadCwd(data)
	tool, _ := data["tool_name"].(string)
	var raw any
	switch {
	case in(PersistEditTools, tool):
		raw = ti["file_path"]
		if !pyTruthy(raw) {
			raw = ti["notebook_path"]
		}
	case tool == "Read":
		raw = ti["file_path"]
	default:
		raw = ti["path"]
		if !pyTruthy(raw) {
			raw = cwd
		}
	}
	rs, ok := raw.(string)
	if !ok || rs == "" {
		return 2, "worker-guard: blocked (tool call without a readable path, failing closed)\n"
	}
	p := cfgExpand(rs)
	if !strings.HasPrefix(p, "/") {
		p = pyJoin(cwd, p)
	}
	if in(PersistEditTools, tool) {
		if strings.Contains(p, "$") || !researcherArea(p, cwd) {
			return 2, "worker-guard: blocked (" + pyRepr(rs) + "): a researcher writes only inside its research dir " +
				"<state>/factory/research/<name> and its scratchpad. Tell your lead if this is wrong.\n"
		}
	} else if strings.Contains(p, "$") || researcherSecretPath(pyRealpath(p)) || researcherSecretPath(pyNormpath(p)) {
		return 2, "worker-guard: blocked (" + pyRepr(rs) + "): a researcher may not read credentials or tokens\n"
	}
	return 0, ""
}

// researcherText is the raw-text secret rule of check().
func researcherText(cmd string) error {
	if researchSecretText.MatchString(cmd) {
		return blockf("a researcher may not name credentials, tokens or the factory/gh config dirs")
	}
	return nil
}

// researcherCmd is a port of Analyzer.researcher_cmd: gh only to read, factory only to report.
func (a *Analyzer) researcherCmd(name string, rest []string) error {
	var pos []string
	for _, x := range rest {
		if !strings.HasPrefix(x, "-") {
			pos = append(pos, x)
		}
	}
	switch name {
	case "gh":
		ok := false
		if len(pos) > 0 {
			switch {
			case pos[0] == "search":
				ok = true
			case in(researchGhGroups, pos[0]):
				ok = len(pos) > 1 && in(researchGhRead, pos[1]) && len(rest) > 1 && rest[0] == pos[0] && rest[1] == pos[1]
			case pos[0] == "api":
				ok = !anyOf(rest, func(x string) bool {
					for _, p := range []string{"-X", "-f", "-F", "--method", "--field", "--raw-field", "--input"} {
						if strings.HasPrefix(x, p) {
							return true
						}
					}
					return false
				})
			}
		}
		if !ok {
			return blockf("a researcher may run gh only to read (search, <pr|issue|release|repo|run> view|list|diff|status|" +
				"checks, api without a method or field flag)")
		}
	case "factory":
		ok := len(pos) > 0 && (in(researchFactory, pos[0]) || (len(pos) > 1 && ((pos[0] == "task" && (pos[1] == "show" || pos[1] == "note")) ||
			(pos[0] == "goal" && pos[1] == "show"))))
		if !ok {
			return blockf("a researcher runs only factory whoami, inbox, msg, done, report, task show|note, goal show")
		}
	}
	return nil
}

// gitClone is a port of Analyzer.git_clone: a public https clone into the research dir or scratchpad.
func (a *Analyzer) gitClone(args []string, usedC bool) error {
	if usedC || a.gdyn {
		return a.gitDeny("git clone with -C or after a cd whose target is only known at run time")
	}
	var pos []string
	ended := false
	for i := 0; i < len(args); i++ {
		x := args[i]
		nxt := ""
		if i+1 < len(args) {
			nxt = args[i+1]
		}
		switch {
		case !ended && x == "--":
			ended = true
		case !ended && strings.HasPrefix(x, "-"):
			switch {
			case in(cloneFlags, x) || cloneDepth.MatchString(x) ||
				(strings.HasPrefix(x, "--branch=") && cloneBranch.MatchString(x[9:])):
			case (x == "--depth" && cloneDigits.MatchString(nxt)) || ((x == "--branch" || x == "-b") && cloneBranch.MatchString(nxt)):
				i++
			default:
				return a.gitDeny("git clone option " + pyRepr(x) + " can run a program, read a config or name another source")
			}
		default:
			pos = append(pos, x)
		}
	}
	if len(pos) != 2 {
		return a.gitDeny("git clone needs exactly a literal https URL and a destination directory")
	}
	url, dest := pos[0], pos[1]
	if !cloneURL.MatchString(url) {
		return a.gitDeny("git clone source " + pyRepr(url) + " is not a plain https://<host>/<path> URL")
	}
	if strings.Contains(dest, "~") || strings.Contains(dest, `\`) || strings.ContainsAny(dest, "*?[{") || anyOf(strings.Split(dest, "/"), func(s string) bool { return s == ".." }) {
		return a.gitDeny("git clone destination " + pyRepr(dest) + " is not a plain path inside the research dir or scratchpad")
	}
	full := dest
	if !strings.HasPrefix(dest, "/") {
		full = pyJoin(a.gcwd, dest)
	}
	if !(researcherArea(a.gcwd, a.gcwd) && researcherArea(full, a.gcwd)) {
		return a.gitDeny("git clone destination " + pyRepr(dest) + " (or the current directory) is outside your research dir " +
			"<state>/factory/research/<name> and your scratchpad")
	}
	return nil
}
