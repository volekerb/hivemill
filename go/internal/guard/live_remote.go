package guard

import (
	"bufio"
	"os"
	"regexp"
	"strings"

	"factory/go/internal/shlex"
)

// T-1200: live-checkout common dir — block shared repo config/remote writes and push URL overrides.

var (
	reLiveRemoteCfg  = regexp.MustCompile(`(?i)^(remote\.[\p{L}\p{N}_]+\.(url|pushurl|fetch)|url\..+\.(insteadof|pushinsteadof)|include\.path|includeif\..+\.path)=`)
	rePushURLCred    = regexp.MustCompile(`^(?:https?|git|ssh)://[^/\s]+:[^/\s]+@|^[^/\s]+:[^/\s]+@[\w.-]+:`)
	rePushURLCredSCP = regexp.MustCompile(`^[^/\s]+@[^/\s]+:`)
	rePushURLScheme  = regexp.MustCompile(`^[\p{L}\p{N}_.-]+://`)
)

// factoryURLShapeOK mirrors worker-guard.py FACTORY_URL_SHAPE (RE2 has no lookahead).
func factoryURLShapeOK(url string) bool {
	const pre = "https://github.com/"
	if !strings.HasPrefix(url, pre) || !strings.HasSuffix(url, ".git") {
		return false
	}
	rest := strings.TrimSuffix(url[len(pre):], ".git")
	owner, repo, ok := strings.Cut(rest, "/")
	if !ok || owner == "" || repo == "" {
		return false
	}
	if owner == "." || owner == ".." || repo == "." || repo == ".." {
		return false
	}
	if strings.HasPrefix(owner, "./") || strings.HasPrefix(owner, "../") {
		return false
	}
	if strings.HasPrefix(repo, "..git") || strings.HasPrefix(repo, "...git") {
		return false
	}
	for _, part := range []string{owner, repo} {
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
				return false
			}
		}
	}
	return true
}

func normalizePushURL(url string) string {
	return strings.TrimRight(url, "/")
}

func pushURLHasCredentials(url string) bool {
	if url == "" || url == "origin" || url == "-" {
		return false
	}
	if rePushURLCred.MatchString(url) {
		return true
	}
	if strings.Contains(url, "://") {
		body := strings.SplitN(url, "://", 2)[1]
		if strings.HasPrefix(body, "@") {
			return true
		}
		host := strings.SplitN(body, "/", 2)[0]
		return strings.Contains(host, ":") && strings.Contains(host, "@")
	}
	return rePushURLCredSCP.MatchString(url)
}

// factoryRemoteURL is config factory_remote_url from the guard export; "" when unset, invalid or unreadable.
func factoryRemoteURL() (string, error) {
	e, err := exportNeeded()
	if err != nil {
		return "", err
	}
	if e == nil {
		return "", nil
	}
	url := strings.TrimSpace(e.Config["factory_remote_url"])
	if url == "" || !factoryURLShapeOK(url) {
		return "", nil
	}
	return url, nil
}

func gitCommonDir(cwd string, cwdOK bool, gitDir, workTree *string) string {
	if !cwdOK {
		return ""
	}
	if gitDir != nil {
		gd := pyRealpath(pyJoin(cwd, expandUser(*gitDir)))
		comm := pyJoin(gd, "commondir")
		if isFile(comm) {
			rel, err := readOneLine(comm)
			if err != nil {
				return ""
			}
			return pyRealpath(pyJoin(gd, rel))
		}
		return gd
	}
	p := cwd
	if workTree != nil {
		p = pyRealpath(pyJoin(cwd, expandUser(*workTree)))
	} else {
		p = pyRealpath(cwd)
	}
	for {
		g := pyJoin(p, ".git")
		bare := isFile(pyJoin(p, "HEAD")) && isDir(pyJoin(p, "objects"))
		if isDir(g) {
			return pyRealpath(g)
		}
		if bare && !pathExists(pyJoin(p, "commondir")) {
			return pyRealpath(p)
		}
		if isFile(g) {
			first, err := readOneLine(g)
			if err != nil || !strings.HasPrefix(strings.ToLower(first), "gitdir:") {
				return ""
			}
			gitdir := pyRealpath(pyJoin(p, strings.TrimSpace(first[7:])))
			comm := pyJoin(gitdir, "commondir")
			if isFile(comm) {
				rel, err := readOneLine(comm)
				if err != nil {
					return ""
				}
				return pyRealpath(pyJoin(gitdir, rel))
			}
			return gitdir
		}
		parent := pyDirname(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

func readOneLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return "", sc.Err()
	}
	return strings.TrimSpace(sc.Text()), nil
}

func isLiveGitCommon(common string) bool {
	if common == "" {
		return false
	}
	common = pyRealpath(common)
	homes, err := gitHomes()
	if err != nil {
		return true
	}
	for _, h := range homes {
		if common == pyRealpath(pyJoin(h, ".git")) {
			return true
		}
	}
	return false
}

func usesLiveCommon(cwd string, cwdOK bool, gitDir, workTree *string) bool {
	return isLiveGitCommon(gitCommonDir(cwd, cwdOK, gitDir, workTree))
}

func liveCommonConfigPath(cwd string, cwdOK bool, gitDir, workTree *string) string {
	common := gitCommonDir(cwd, cwdOK, gitDir, workTree)
	if common == "" {
		return ""
	}
	return pyJoin(common, "config")
}

func (a *Analyzer) configTargetsLiveCommonFile(files []string, cwd string, cwdOK bool, gitDir, workTree *string) bool {
	target := liveCommonConfigPath(cwd, cwdOK, gitDir, workTree)
	if target == "" {
		return false
	}
	saved, savedOK := a.ccwd, a.ccwdKnown
	a.ccwd, a.ccwdKnown = cwd, cwdOK
	defer func() { a.ccwd, a.ccwdKnown = saved, savedOK }()
	for _, f := range files {
		if f == "" {
			continue
		}
		t := expandUser(substVars(a.cfgExpand(f), a.vars))
		if t == "" || strings.ContainsAny(t, "$`*?[{") || (!strings.HasPrefix(t, "/") && !cwdOK) {
			return true
		}
		if !strings.HasPrefix(t, "/") {
			t = pyJoin(cwd, t)
		}
		if pyRealpath(t) == pyRealpath(target) {
			return true
		}
	}
	return false
}

// configWorktreeScope is worker-guard.py config_worktree_scope.
func configWorktreeScope(args []string) bool {
	skip, seenKey := false, false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		abbrev := len(a) >= 3 && strings.HasPrefix(a, "--") && !strings.Contains(a, "=") &&
			(strings.HasPrefix("--type", a) || strings.HasPrefix("--default", a) || strings.HasPrefix("--comment", a) ||
				strings.HasPrefix("--blob", a) || strings.HasPrefix("--value", a))
		if in(ConfigValueOpts, a) || abbrev {
			skip = true
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			seenKey = true
		}
		if a == "--worktree" || strings.HasPrefix(a, "--worktree=") {
			if !seenKey {
				return true
			}
		}
	}
	return false
}

func (a *Analyzer) gitLivePushGuard(rest []string) error {
	if a.HQ {
		return nil
	}
	i := 0
	cwd, cwdOK := a.ccwd, a.ccwdKnown
	var gitDir, workTree *string
	remoteCfg := false
	aliasMap := map[string]string{}
	for i < len(rest) && strings.HasPrefix(rest[i], "-") {
		o := rest[i]
		val, hasVal := "", false
		if in(GitValueOpts, o) && i+1 < len(rest) {
			val, hasVal, i = rest[i+1], true, i+2
		} else {
			if strings.HasPrefix(o, "-c") && !strings.HasPrefix(o, "--") && len(o) > 2 {
				val, hasVal, o = o[2:], true, "-c"
			} else if strings.HasPrefix(o, "--git-dir=") || strings.HasPrefix(o, "--work-tree=") {
				j := strings.IndexByte(o, '=')
				o, val, hasVal = o[:j], o[j+1:], true
			}
			i++
		}
		switch {
		case o == "-c" && hasVal:
			if body, ok := aliasAssign(val); ok {
				name, b := body, ""
				if j := strings.IndexByte(body, '='); j >= 0 {
					name, b = body[:j], body[j+1:]
				}
				aliasMap[name] = b
			} else if reLiveRemoteCfg.MatchString(val) {
				remoteCfg = true
			}
		case o == "-C" && hasVal && cwdOK:
			if dynamicDir(val) {
				cwdOK = false
			} else {
				cwd = pyJoin(cwd, expandUser(val))
			}
		case o == "--git-dir" && hasVal:
			v := val
			gitDir = &v
		case o == "--work-tree" && hasVal:
			v := val
			workTree = &v
		}
	}
	if i >= len(rest) || !usesLiveCommon(cwd, cwdOK, gitDir, workTree) {
		return nil
	}
	sub := rest[i]
	var pushArgs []string
	if sub == "push" || sub == "send-pack" {
		pushArgs = rest[i+1:]
	} else if body, ok := aliasMap[sub]; ok {
		aw, err := shlex.Split(body)
		if err != nil {
			return nil
		}
		if len(aw) > 0 && (aw[0] == "push" || aw[0] == "send-pack") {
			pushArgs = append(append([]string(nil), aw[1:]...), rest[i+1:]...)
		}
	}
	if pushArgs == nil {
		return nil
	}
	return a.gitLivePushGuardPushArgs(pushArgs, remoteCfg)
}

func (a *Analyzer) gitLivePushGuardPushArgs(args []string, remoteCfg bool) error {
	if remoteCfg {
		return gitBlockf("`git -c remote.<name>.url=... push` overrides the shared origin from a worktree; push with " +
			"origin or the plain factory URL only (T-1200)")
	}
	var pos []string
	k := 0
	for k < len(args) {
		a := args[k]
		if a == "-o" || a == "--push-option" || a == "--receive-pack" || a == "--exec" || a == "--repo" {
			k += 2
			continue
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--repo=") {
			k++
			continue
		}
		if strings.HasPrefix(a, "--repo=") {
			pos = append(pos, strings.SplitN(a, "=", 2)[1])
		} else if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
		k++
	}
	remote := ""
	if len(pos) > 0 {
		remote = pos[0]
	}
	if remote != "" && pushURLHasCredentials(remote) {
		return gitBlockf("git push: a remote URL must not embed credentials; use origin or the plain https remote (T-1200)")
	}
	fac, err := factoryRemoteURL()
	if err != nil {
		return err
	}
	if remote != "" && rePushURLScheme.MatchString(remote) {
		if fac == "" || normalizePushURL(remote) != normalizePushURL(fac) {
			return gitBlockf("git push: only the configured factory origin URL or the `origin` remote is allowed from a " +
				"worktree of the live checkout (T-1200)")
		}
	}
	return nil
}
