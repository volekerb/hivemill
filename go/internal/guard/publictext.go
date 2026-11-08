package guard

import "strings"

// The public-text / frozen-repo rule (G-021, PublicText in worker-guard.py) is switched on by two rows of the factory
// DB config table (public_text_repos, frozen_repos); the guard export carries them (guardexport.go). Since T-0887 the
// whole rule is Go (ptrule.go, the checker in scrub.go and ptcheck.go): nothing goes to the Python hook. The entry
// conditions below (PublicText.command, .git, .gh, .gh_api up to the point where Python starts to use the config)
// decide whether the export is read at all: a command that cannot reach the rule needs no export, as before.

var (
	ptGitSubs  = set("commit", "tag", "push", "merge", "commit-tree", "notes")
	ptGhWrites = map[string]map[string]struct{}{
		"pr":      set("create", "new", "edit", "comment", "review", "close", "merge"),
		"issue":   set("create", "new", "edit", "comment", "close"),
		"release": set("create", "new", "edit"),
	}
)

// ghPositionals is worker-guard.py PublicText.gh's pos: skip -R/--repo flag values (T-1156).
func ghPositionals(a []string) []string {
	var pos []string
	for k, x := range a {
		if strings.HasPrefix(x, "-") {
			continue
		}
		if k > 0 && (a[k-1] == "-R" || a[k-1] == "--repo") {
			continue
		}
		pos = append(pos, x)
	}
	return pos
}

// ptShellPayload is the code of `sh -c CODE` / `eval ARGS` for PublicText.command (HQ mode), "" for none.
func ptShellPayload(name string, rest []string) string {
	if name == "eval" {
		return strings.Join(rest, " ")
	}
	for i := 0; i+1 < len(rest); i++ {
		if dashLetters(rest[i], "c") && strings.ToLower(rest[i]) == rest[i] {
			return rest[i+1]
		}
	}
	return ""
}

// ptMayHoldGit reports whether code could, once its quoting is resolved, hold a git or gh word.
func ptMayHoldGit(code string) bool {
	plain := strings.NewReplacer("'", "", "\"", "", "\\", "").Replace(code)
	return strings.Contains(plain, "git") || strings.Contains(plain, "gh")
}

// ptCommand is PublicText.command (the Analyzer calls it with the command word first).
func (a *Analyzer) ptCommand(words []string) error {
	pt := a.pt
	words = ptPeel(words, a.HQ)
	if len(words) == 0 {
		return nil
	}
	name, rest := pyBasename(words[0]), words[1:]
	if name == "cd" {
		pt.cd(rest)
		return nil
	}
	shell, payload := a.HQ && (in(Shells, name) || name == "eval"), ""
	reaches := false
	var cwd string
	var cwdOK, alias bool
	var j int
	switch {
	case shell:
		payload = ptShellPayload(name, rest)
		reaches = payload != "" && ptMayHoldGit(payload) // the payload is analysed with the rule on
	case name == "git":
		cwd, cwdOK, j, alias = pt.gitWalk(rest)
		reaches = ptGitReaches(rest)
	case name == "gh":
		reaches = ptGhReaches(rest)
	}
	if !reaches {
		return nil
	}
	if err := pt.loadConfig(); err != nil {
		return err
	}
	if !pt.active {
		return nil
	}
	switch {
	case shell:
		return pt.shellPayload(payload)
	case name == "git":
		return pt.git(rest, cwd, cwdOK, j, alias)
	}
	return pt.gh(rest)
}

// shellPayload is the shell branch of PublicText.command: the payload is analysed (HQ mode) with this rule.
func (pt *publicText) shellPayload(payload string) error {
	saved, savedOK := pt.pcwd, pt.pcwdOK
	defer func() { pt.pcwd, pt.pcwdOK = saved, savedOK }()
	an, err := NewAnalyzer(pt.cwd, "", true)
	if err != nil {
		return err
	}
	an.pt = pt
	if err := an.script(payload, 1); err != nil {
		if isPublicBlock(err) || IsDelegate(err) {
			return err
		}
		if ptWriteHint(payload) {
			return pubBlock("a write command to a public repo cannot be parsed; use plain git/gh commands")
		}
	}
	return nil
}

// ptWriteHint is PT_WRITE_HINT.search:
// \b(?:git\s+(?:-\S+\s+\S+\s+)*(?:commit|tag|push|merge)|gh\s+(?:pr|issue|release|api))\b
func ptWriteHint(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 'g' || wordBefore(s, i) {
			continue
		}
		if strings.HasPrefix(s[i:], "git") && ptGitHint(s, i+3) {
			return true
		}
		if strings.HasPrefix(s[i:], "gh") && ptGhHint(s, i+2) {
			return true
		}
	}
	return false
}

func keywordAt(s string, p int, kws ...string) bool {
	for _, k := range kws {
		if strings.HasPrefix(s[p:], k) && !wordAt(s, p+len(k)) {
			return true
		}
	}
	return false
}

func skipNonSpace(s string, i int) int {
	for i < len(s) && !isSpaceAt(s, i) {
		i += runeLen(s, i)
	}
	return i
}

func ptGitHint(s string, p int) bool {
	q := skipSpace(s, p)
	if q == p {
		return false
	}
	positions := []int{q}
	for pos := q; pos < len(s) && s[pos] == '-'; {
		a := skipNonSpace(s, pos+1)
		if a == pos+1 {
			break
		}
		b := skipSpace(s, a)
		if b == a || b >= len(s) {
			break
		}
		c := skipNonSpace(s, b)
		d := skipSpace(s, c)
		if c == b || d == c {
			break
		}
		positions = append(positions, d)
		pos = d
	}
	for k := len(positions) - 1; k >= 0; k-- {
		if keywordAt(s, positions[k], "commit", "tag", "push", "merge") {
			return true
		}
	}
	return false
}

func ptGhHint(s string, p int) bool {
	q := skipSpace(s, p)
	return q > p && keywordAt(s, q, "pr", "issue", "release", "api")
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
	pos := ghPositionals(args)
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
