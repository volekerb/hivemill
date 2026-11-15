package guard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Go port of ci/pre_push_public_text.py (T-1485): the git pre-push hook body. It refuses a push to a
// public_text_repos remote when the public-text guard finds private data in the pushed range. Fails closed:
// unreadable config, unparsable remote while a public repo is configured, missing terms file or any check error
// refuses the push. Owner terms are never printed.
//
// The factory DB is read through the ConfigReader the caller passes (the cmd main owns the sqlite dependency, so this
// package stays free of it: worker-guard imports it).

// CIConfigReader returns the config rows public_text_repos and scrub_terms_file of the factory DB under home.
type CIConfigReader func(home string) (map[string]string, error)

// ciPrePushGitTimeout is the timeout=30 of pre_push_public_text.git and has_commit.
var ciPrePushGitTimeout = 30 * time.Second // a var only so a test can lower it

var (
	ciZeroSha     = regexp.MustCompile(`^0+$`)
	ciURLQuery    = regexp.MustCompile(`[?#][^\n]*\z`)
	ciURLScheme   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://[^/]*/`)
	ciURLScp      = regexp.MustCompile(`^(?:[^/@:]*@)?[^/@:]+:`)
	ciDotGit      = regexp.MustCompile(`(?i)\.git(\n?)\z`)
	ciRefPrefixRe = regexp.MustCompile(`^refs/(?:heads|tags)/`)
)

// CIRepoSlug is repo_slug: OWNER/NAME (lower case) of a remote URL or local path ending OWNER/NAME(.git); ok false
// when it holds no two-part name.
func CIRepoSlug(url string) (string, bool) {
	u := ciURLQuery.ReplaceAllString(strings.TrimFunc(url, pyIsSpace), "")
	u = ciURLScheme.ReplaceAllString(u, "")
	u = ciURLScp.ReplaceAllString(u, "")
	var parts []string
	for _, x := range strings.Split(strings.TrimRight(u, "/"), "/") {
		if x != "" {
			parts = append(parts, x)
		}
	}
	if len(parts) < 2 {
		return "", false
	}
	owner, name := parts[len(parts)-2], ciDotGit.ReplaceAllString(parts[len(parts)-1], "$1")
	if owner == "" || name == "" || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", false
	}
	return pyLowerFull(owner + "/" + name), true
}

func ciPrePushRefuse(errw *ciOut, msg string) int {
	errw.printf("pre-push: refused (%s)\n", msg)
	return 1
}

// ciGitLocal runs git as pre_push_public_text.git/has_commit do: the caller's environment, a 30 s timeout.
func ciGitLocal(args ...string) (stdout []byte, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), ciPrePushGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = 2 * time.Second // a grandchild holding the pipe open must not outlive the deadline
	out, runErr := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, false, fmt.Errorf("git %s did not finish in %s: %w", args[0], ciPrePushGitTimeout, ctx.Err())
	}
	var ee *exec.ExitError
	switch {
	case runErr == nil:
		return out, true, nil
	case errors.As(runErr, &ee):
		return out, false, nil
	}
	return nil, false, fmt.Errorf("run git %s: %w", args[0], runErr)
}

// CIPrePushPublicTextMain is pre_push_public_text.main(argv, stdin): args without the program name; the return
// value is the process exit code.
func CIPrePushPublicTextMain(args []string, stdin io.Reader, stderr io.Writer, readConfig CIConfigReader) int {
	errw := &ciOut{w: stderr}
	code := ciPrePush(args, stdin, errw, readConfig)
	if errw.err != nil {
		return 1 // a push that cannot say why it was refused is refused
	}
	return code
}

func ciPrePush(args []string, stdin io.Reader, errw *ciOut, readConfig CIConfigReader) int {
	if len(args) < 4 || args[0] != "--home" {
		return ciPrePushRefuse(errw, "usage: pre_push_public_text.py --home DIR REMOTE URL")
	}
	home, url := args[1], args[3]
	cfg, err := readConfig(home)
	if err != nil {
		return ciPrePushRefuse(errw, "factory config unreadable (OperationalError); retry, or tell your lead")
	}
	public := map[string]bool{}
	for _, x := range strings.Split(cfg["public_text_repos"], ",") {
		if s, ok := CIRepoSlug(x); ok {
			public[s] = true
		}
	}
	if len(public) == 0 {
		return 0
	}
	if slug, ok := CIRepoSlug(url); ok && !public[slug] {
		return 0
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return ciPrePushRefuse(errw, "cannot read the pushed refs from stdin: "+err.Error())
	}
	var lines [][]string
	for _, ln := range pyLines(pyDecodeReplace(data)) {
		if f := pyFields(ln); len(f) > 0 {
			lines = append(lines, f)
		}
	}
	terms, err := ciTermsOrFail(cfg["scrub_terms_file"], nil)
	if err != nil {
		return ciPrePushUsage(errw, err)
	}
	sc := newCIScanner(terms, true)
	var allow *ciAllowlist // loaded at the first push check, as check_push does
	nhits, ids := 0, map[string]bool{}
	for _, ln := range lines {
		if len(ln) < 4 {
			continue
		}
		sha, remoteRef, remoteSha := ln[1], ln[2], ln[3]
		if ciZeroSha.MatchString(sha) {
			continue // a delete publishes no commits
		}
		for _, p := range ciCheckText(ciRefPrefixRe.ReplaceAllString(remoteRef, ""), sc) {
			nhits++
			ids[p.id] = true
		}
		base := ""
		if !ciZeroSha.MatchString(remoteSha) {
			if _, has, herr := ciGitLocal("cat-file", "-e", remoteSha+"^{commit}"); herr != nil {
				return ciPrePushRefuse(errw, herr.Error())
			} else if has {
				base = remoteSha
			}
		}
		if base == "" {
			out, ok, merr := ciGitLocal("merge-base", sha, "refs/remotes/origin/main")
			if merr != nil {
				return ciPrePushRefuse(errw, merr.Error())
			}
			if ok {
				base = strings.TrimFunc(string(out), pyIsSpace)
			}
		}
		if base == "" {
			return ciPrePushRefuse(errw, "no base to check the push against: run git fetch origin so origin/main exists")
		}
		if allow == nil {
			if allow, err = ciLoadAllowlist(ciDefaultAllowlist(home), errw.w); err != nil {
				return ciPrePushUsage(errw, err)
			}
		}
		hits, err := ciCheckPush(".", base, sha, sc, allow)
		if err != nil {
			return ciPrePushUsage(errw, err)
		}
		for _, h := range hits {
			nhits++
			ids[h.id] = true
		}
	}
	if nhits > 0 {
		var sorted []string
		for id := range ids {
			sorted = append(sorted, id)
		}
		sort.Strings(sorted)
		return ciPrePushRefuse(errw, fmt.Sprintf("public text: %d hits: %s; reword or drop the commits (git rebase -i), then push again", nhits, strings.Join(sorted, ", ")))
	}
	return 0
}

// ciPrePushUsage is the `except mod.UsageError` of main: a usage error refuses the push with its message; any other
// error is no UsageError in Python (a crash, exit 1 with a traceback): here it refuses too, with its text.
func ciPrePushUsage(errw *ciOut, err error) int {
	var ue *usageError
	if errors.As(err, &ue) {
		return ciPrePushRefuse(errw, ue.Msg)
	}
	return ciPrePushRefuse(errw, err.Error())
}
