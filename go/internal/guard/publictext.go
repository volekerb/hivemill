package guard

import "strings"

// The public-text / frozen-repo rule (G-021, PublicText in worker-guard.py) is switched on by two rows of the factory
// DB config table (public_text_repos, frozen_repos) and decides with git subprocesses and the checker module
// ci/public_text_guard.py. Go ports only its entry conditions (PublicText.command, .git, .gh, .gh_api up to the
// point where Python starts to use the config): a command that cannot reach the rule is decided here, one that can
// goes to the Python hook when the DB file exists (owngoal.go). With no DB file the rule is off, as in Python.

var (
	ptGitSubs  = set("commit", "tag", "push", "merge", "commit-tree", "notes")
	ptGhWrites = map[string]map[string]struct{}{
		"pr":      set("create", "edit", "comment", "review", "close", "merge"),
		"issue":   set("create", "edit", "comment", "close"),
		"release": set("create", "edit"),
	}
)

// ptCommand is the entry of PublicText.command (the Analyzer calls it with the command word first).
func (a *Analyzer) ptCommand(words []string) error {
	words = ptPeel(words, a.HQ)
	if len(words) == 0 {
		return nil
	}
	name, rest := pyBasename(words[0]), words[1:]
	reaches := false
	switch {
	case name == "cd":
	case a.HQ && (in(Shells, name) || name == "eval"):
		payload := ""
		if name == "eval" {
			payload = strings.Join(rest, " ")
		} else {
			for i := 0; i+1 < len(rest); i++ {
				if dashLetters(rest[i], "c") && strings.ToLower(rest[i]) == rest[i] {
					payload = rest[i+1]
					break
				}
			}
		}
		// the payload is analysed with the rule on: any git or gh word in it may reach a write
		reaches = payload != "" && (strings.Contains(payload, "git") || strings.Contains(payload, "gh"))
	case name == "git":
		reaches = ptGitReaches(rest)
	case name == "gh":
		reaches = ptGhReaches(rest)
	}
	if reaches && !dbMissing() {
		return delegate("public-text rule: public_text_repos / frozen_repos are in the factory DB")
	}
	return nil
}

// ptGitReaches is PublicText.git up to the first use of the config: the subcommand is one of PT_GIT_SUBS (or a
// `-c alias.*` hides it) and it is not a lightweight tag. The option walk runs for both readings of `-C` (a dynamic
// cd makes the rule skip the value) and reaches on either.
func ptGitReaches(args []string) bool {
	for _, cKnown := range []bool{true, false} {
		j, alias := 0, false
		for j < len(args) && strings.HasPrefix(args[j], "-") {
			x := args[j]
			switch {
			case x == "-C":
				if cKnown && j+1 < len(args) {
					j++
				}
			case x == "--git-dir" || x == "--work-tree":
				j++
			case strings.HasPrefix(x, "--git-dir=") || strings.HasPrefix(x, "--work-tree="):
			case x == "-c" || x == "--namespace" || x == "--exec-path":
				if x == "-c" && j+1 < len(args) && strings.HasPrefix(strings.ToLower(args[j+1]), "alias.") {
					alias = true
				}
				j++
			}
			j++
		}
		if j >= len(args) || !(in(ptGitSubs, args[j]) || alias) {
			continue
		}
		if args[j] == "tag" && !anyOf(args[j+1:], annotatedTagFlag) {
			continue // a lightweight local tag is not a write to the remote
		}
		return true
	}
	return false
}

// annotatedTagFlag is re.fullmatch(r"-[a-zA-Z]*[amsF]|--(?:message|file)(?:=.*)?", a).
func annotatedTagFlag(a string) bool {
	if rest, ok := strings.CutPrefix(a, "--"); ok {
		for _, o := range []string{"message", "file"} {
			if t, ok := strings.CutPrefix(rest, o); ok && (t == "" || strings.HasPrefix(t, "=") && !strings.Contains(t, "\n")) {
				return true
			}
		}
		return false
	}
	if !strings.HasPrefix(a, "-") || len(a) < 2 {
		return false
	}
	for i := 1; i < len(a); i++ {
		if !isASCIILetter(a[i]) {
			return false
		}
	}
	return strings.ContainsRune("amsF", rune(a[len(a)-1]))
}

// ptGhReaches is PublicText.gh (and gh_api) up to the first use of the config.
func ptGhReaches(args []string) bool {
	var pos []string
	for _, x := range args {
		if !strings.HasPrefix(x, "-") {
			pos = append(pos, x)
		}
	}
	if len(pos) < 2 && !(len(pos) > 0 && pos[0] == "api") {
		return false
	}
	if pos[0] == "api" {
		return ptGhAPIBody(args)
	}
	subs, ok := ptGhWrites[pos[0]]
	return ok && in(subs, pos[1])
}

// ptGhAPIBody is the early return of PublicText.gh_api: only a write (a body, or a POST/PATCH/PUT/DELETE method,
// and not an explicit GET) reaches the config.
func ptGhAPIBody(a []string) bool {
	method, hasBody := "", false
	for j := 0; j < len(a); j++ {
		x := a[j]
		switch {
		case (x == "-X" || x == "--method") && j+1 < len(a):
			method = strings.ToUpper(a[j+1])
			j++
		case strings.HasPrefix(x, "--method="):
			method = strings.ToUpper(x[len("--method="):])
		case (x == "-f" || x == "-F" || x == "--field" || x == "--raw-field") && j+1 < len(a):
			hasBody = true
			j++
		case (strings.HasPrefix(x, "--field=") || strings.HasPrefix(x, "--raw-field=")) && !strings.Contains(x, "\n"):
			hasBody = true
		case x == "--input" && j+1 < len(a):
			hasBody = true
			j++
		case strings.HasPrefix(x, "--input="):
			hasBody = true
		case in(set("-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "-R", "--repo"), x) && j+1 < len(a):
			j++
		}
	}
	if !(hasBody || method == "POST" || method == "PATCH" || method == "PUT" || method == "DELETE") || method == "GET" {
		return false
	}
	return true
}
