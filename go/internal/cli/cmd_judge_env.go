package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/ghx"
	"factory/go/internal/gitx"
	"factory/go/internal/merge"
)

// The process side of `factory judge`: git and gh calls with Python's argv, env and timeouts, the git-config
// allow-list (git_config_refusal) and the helpers the merge gate leans on. Everything the Python code would crash on, or
// answer with text this port cannot reproduce, is merge.ErrUnsupported / errDelegate, which the caller turns into a
// hand-over to Python before anything was written.

var judgeGHTimeout = 120 * time.Second // gh_json's default timeout (a var so a test can shorten it)

const (
	judgeMergeTimeout   = 180 * time.Second // `gh pr merge` (_judge_gate_and_merge)
	judgeCloseTimeout   = 60 * time.Second  // `gh pr close` (close_landed_pr)
	judgeMainRunTimeout = 30 * time.Second  // main_head_run gh calls
	judgeGitCfgTimeout  = 30 * time.Second  // git_config_refusal's git calls
	judgePollDefault    = 30                // JUDGE_POLL_S
	judgeMergePolls     = 6                 // JUDGE_MERGE_POLLS
	judgeMergePollS     = 5                 // JUDGE_MERGE_POLL_S
	emptyTree           = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

// ghFail is a gh that ran and failed: Python's gh_json prints "factory: <msg>" and raises SystemExit(msg).
type ghFail struct{ msg string }

func (e *ghFail) Error() string { return e.msg }

// asExit is what an uncaught gh_json failure ends the command with: exit 1 and the message (gh_json already printed
// "factory: <msg>" when it failed, caught or not, see ghJSONRaw).
func (e *ghFail) asExit() error {
	return &rawExit{Code: 1, Stderr: e.msg + "\n"}
}

type judgeEnv struct {
	c    *ctx
	home string
	opts gitx.Options
}

func newJudgeEnv(c *ctx) (*judgeEnv, error) {
	_, homes, err := c.env.DeriveLiveHomes()
	if err != nil {
		return nil, errDelegate
	}
	live := make([]string, 0, len(homes))
	for h := range homes {
		live = append(live, h)
	}
	sort.Strings(live)
	home := c.env.Home()
	return &judgeEnv{c: c, home: home, opts: gitx.Options{Home: home, LiveHomes: live}}, nil
}

func (j *judgeEnv) ghEnv() []string { return ghx.GitEnv(j.opts) }

// carryEnv is _carry_env: no replace refs, no grafts, no attributes at all.
func (j *judgeEnv) carryEnv() []string {
	base := j.c.deps.Environ
	if base == nil {
		base = os.Environ()
	}
	drop := map[string]bool{"GIT_NO_REPLACE_OBJECTS": true, "GIT_GRAFT_FILE": true, "GIT_ATTR_NOSYSTEM": true, "GIT_ATTR_SOURCE": true}
	env := make([]string, 0, len(base)+4)
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE="+os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_ATTR_SOURCE="+emptyTree)
}

// runner is the merge.Runner of one repo.
type runner struct {
	j    *judgeEnv
	repo string
}

func (r runner) Git(args ...string) (int, string, string) {
	o := r.j.opts
	o.Env = r.j.carryEnv()
	return gitx.RC(r.repo, o, append(append([]string{}, merge.HardenedGitFlags...), args...)...)
}

func (r runner) MainSHA(ownerRepo string, timeout time.Duration) (string, string, int, error) {
	res, err := ghx.Run(r.j.c.deps.Environ, "", timeout, "api", "repos/"+ownerRepo+"/commits/main", "--jq", ".sha")
	return res.Stdout, res.Stderr, res.ExitCode, err
}

func (r runner) IsTimeout(err error) bool {
	var te *gitx.TimeoutError
	return errors.As(err, &te)
}

// ghJSONRaw is gh_json: stdout of `gh args...` (120 s). A gh that failed is *ghFail; a gh that cannot start, a timeout
// or text that is not valid UTF-8 are Python tracebacks, which Go leaves to Python (errDelegate).
func (j *judgeEnv) ghJSONRaw(args ...string) (string, error) {
	res, err := ghx.Run(j.c.deps.Environ, "", judgeGHTimeout, args...)
	if err != nil {
		return "", errDelegate
	}
	if res.ExitCode != 0 {
		if !utf8.ValidString(res.Stderr) {
			return "", errDelegate
		}
		first := ""
		if lines := pySplitlines(pyStrip(res.Stderr)); len(lines) > 0 {
			first = lines[0]
		}
		n := len(args)
		if n > 3 {
			n = 3
		}
		msg := fmt.Sprintf("gh %s: %s", strings.Join(args[:n], " "), first)
		fmt.Fprintf(j.c.errb, "factory: %s\n", msg) // gh_json prints before it raises, even when the caller catches it
		return "", &ghFail{msg: msg}
	}
	if !utf8.ValidString(res.Stdout) {
		return "", errDelegate
	}
	return res.Stdout, nil
}

// prState is pr_state(url).
func (j *judgeEnv) prState(url string) (merge.PR, error) {
	out, err := j.ghJSONRaw("pr", "view", url, "--json",
		"number,state,headRefOid,headRefName,baseRefName,mergeable,isDraft,statusCheckRollup,url")
	if err != nil {
		return merge.PR{}, err
	}
	pr, perr := merge.ParsePR([]byte(out))
	if perr != nil {
		return merge.PR{}, errDelegate
	}
	return pr, nil
}

// repoURL is gh_json("repo", "view", owner_repo, "--json", "url")["url"]: the URL, or the text Python's except clause
// formats ("failMsg", ok=false). Text Go cannot reproduce is errDelegate.
func (j *judgeEnv) repoURL(ownerRepo string) (url, failMsg string, err error) {
	out, err := j.ghJSONRaw("repo", "view", ownerRepo, "--json", "url")
	var gf *ghFail
	if errors.As(err, &gf) {
		return "", gf.msg, nil
	}
	if err != nil {
		return "", "", err
	}
	var v map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &v) != nil {
		return "", "", errDelegate // Python's JSONDecodeError text
	}
	raw, ok := v["url"]
	if !ok {
		return "", "'url'", nil // KeyError('url')
	}
	var u string
	if json.Unmarshal(raw, &u) != nil || u == "" {
		return "", "", errDelegate
	}
	return u, "", nil
}

// ---- git config allow-list (git_config_refusal) -----------------------------------------------------------------

var (
	judgeAllowedKey = regexp.MustCompile(`(?i)^(core\.(repositoryformatversion|filemode|bare|logallrefupdates)|remote\.origin\.(url|fetch)` +
		`|branch\..+\.(remote|merge)|extensions\.worktreeconfig|user\.(name|email))$`)
	judgeIncludeKey = regexp.MustCompile(`(?i)^include(if)?\.`)
)

// gitConfigClean reports whether the repo-scope git config of `repo` passes git_config_refusal. It answers false for
// everything Python would refuse AND for what only Python can decide (a remote.origin.url that is neither the
// configured factory URL nor a plain absolute path under a temp home): the caller then hands the command to Python,
// which makes the refusal, records the event and sends the director message.
func (j *judgeEnv) gitConfigClean(repo string, factoryURL string) bool {
	base := j.c.deps.Environ
	if base == nil {
		base = os.Environ()
	}
	env := []string{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k != "GIT_DIR" && k != "GIT_WORK_TREE" && k != "GIT_COMMON_DIR" && k != "LC_ALL" {
			env = append(env, kv)
		}
	}
	o := j.opts
	o.Env = append(env, "LC_ALL=C")
	o.Timeout = judgeGitCfgTimeout
	r, err := gitx.Run(repo, o, "rev-parse", "--git-dir", "--git-common-dir")
	if err != nil {
		return false
	}
	if r.ExitCode != 0 {
		return strings.Contains(r.Stderr, "not a git repository") // no repo, no repo config for git to act on
	}
	lines := strings.Split(r.Stdout, "\n")
	if len(lines) < 2 || lines[0] == "" || lines[1] == "" {
		return false
	}
	resolve := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(repo, p)
		}
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			return rp
		}
		return filepath.Clean(p)
	}
	gitdir, common := resolve(lines[0]), resolve(lines[1])
	files := []string{filepath.Join(common, "config")}
	if m, _ := filepath.Glob(filepath.Join(common, "worktrees", "*", "config.worktree")); m != nil {
		sort.Strings(m)
		files = append(files, m...)
	}
	files = append(files, filepath.Join(gitdir, "config.worktree"))
	seen := map[string]bool{}
	fileProto := "always"
	if live, err := j.c.env.HomeIsLive(); err != nil || live {
		fileProto = "never"
	}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		if _, err := os.Stat(f); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false
		}
		q, err := gitx.Run(repo, o, "config", "--file", f, "--list", "-z")
		if err != nil || q.ExitCode != 0 {
			return false
		}
		for _, ent := range strings.Split(q.Stdout, "\x00") {
			if ent == "" {
				continue
			}
			k, v, _ := strings.Cut(ent, "\n")
			lk := strings.ToLower(k)
			switch {
			case judgeIncludeKey.MatchString(k) || !judgeAllowedKey.MatchString(k):
				return false
			case lk == "remote.origin.url":
				ok := (fileProto == "always" && strings.HasPrefix(v, "/") && !strings.Contains(v, "::")) ||
					(factoryURL != "" && v == factoryURL)
				if !ok {
					return false
				}
			case lk == "user.email":
				if !strings.HasSuffix(strings.ToLower(pyStrip(v)), "@users.noreply.github.com") {
					return false
				}
			case lk == "extensions.worktreeconfig":
				switch strings.ToLower(pyStrip(v)) {
				case "false", "no", "off", "0":
				default:
					return false
				}
			}
		}
	}
	xdg := j.c.env.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(j.c.deps.homeDir(), ".config")
	}
	for _, attrs := range []string{filepath.Join(common, "info", "attributes"), filepath.Join(xdg, "git", "attributes")} {
		st, err := os.Stat(attrs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !st.Mode().IsRegular() || st.Size() > 0 {
			return false
		}
	}
	return true
}

// homeDir is os.path.expanduser("~") for the XDG fallback.
func (d *Deps) homeDir() string {
	if d.Env.Getenv("HOME") != "" {
		return d.Env.Getenv("HOME")
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// ---- main-run probes -------------------------------------------------------------------------------------------

// mainHeadRun is main_head_run: main's head sha and the newest tests.yml push run for exactly that head (nil: none).
// The error text is Python's; merge.ErrUnsupported where it cannot be reproduced.
func (j *judgeEnv) mainHeadRun(slug string) (string, map[string]any, error) {
	env := j.ghEnv()
	r, err := ghx.Run(env, "", judgeMainRunTimeout, "api", "repos/"+slug+"/commits/main", "--jq", ".sha")
	if err != nil {
		return "", nil, judgeProbeErr(err)
	}
	head := pyStrip(r.Stdout)
	if r.ExitCode != 0 || head == "" {
		return "", nil, fmt.Errorf("gh api commits/main exit %d: %s", r.ExitCode, pyStrip(r.Stderr))
	}
	r, err = ghx.Run(env, "", judgeMainRunTimeout, "run", "list", "-R", slug, "--workflow", "tests.yml", "--branch", "main",
		"--event", "push", "--limit", "10", "--json", "databaseId,status,conclusion,headSha,url,createdAt")
	if err != nil {
		return "", nil, judgeProbeErr(err)
	}
	if r.ExitCode != 0 {
		return "", nil, fmt.Errorf("gh run list exit %d: %s", r.ExitCode, pyStrip(r.Stderr))
	}
	var runs []map[string]any
	dec := json.NewDecoder(bytes.NewReader([]byte(r.Stdout)))
	if err := dec.Decode(&runs); err != nil {
		return "", nil, merge.ErrUnsupported
	}
	for _, run := range runs {
		if run == nil {
			return "", nil, merge.ErrUnsupported
		}
		if s, ok := run["headSha"].(string); ok && s == head {
			return head, run, nil
		}
	}
	return head, nil, nil
}

// judgeProbeErr maps a failed process start (OSError text is not reproducible) to ErrUnsupported; a timeout keeps
// Python's "Command '[...]' timed out after N seconds" text.
func judgeProbeErr(err error) error {
	var te *gitx.TimeoutError
	if errors.As(err, &te) {
		return err
	}
	return merge.ErrUnsupported
}

// ---- small helpers ---------------------------------------------------------------------------------------------

// judgeInt is int(s) for a plain ASCII decimal (optional minus sign); anything else Python's int() reads differently
// (spaces, underscores, non-ASCII digits, a plus sign) is ok=false.
func judgeInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	i := 0
	if s[0] == '-' {
		i = 1
	}
	if i == len(s) || len(s)-i > 9 {
		return 0, false
	}
	n := 0
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	if s[0] == '-' {
		n = -n
	}
	return n, true
}

// judgeUnknownMerge is UNKNOWN_MERGE_ERROR.search: \bHTTP 5\d\d\b|gateway|timed? ?out|service unavailable (re.I, str
// patterns, so \d and \b are Unicode ones).
func judgeUnknownMerge(stderr string) bool {
	low := strings.ToLower(stderr)
	for _, k := range []string{"gateway", "service unavailable"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	if judgeTimedOut.MatchString(stderr) {
		return true
	}
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }
	for _, loc := range judgeHTTP5.FindAllStringIndex(stderr, -1) {
		before, _ := utf8.DecodeLastRuneInString(stderr[:loc[0]])
		after, _ := utf8.DecodeRuneInString(stderr[loc[1]:])
		if (loc[0] == 0 || !isWord(before)) && (loc[1] == len(stderr) || !isWord(after)) {
			return true
		}
	}
	return false
}

var (
	judgeTimedOut = regexp.MustCompile(`(?i)timed? ?out`)
	judgeHTTP5    = regexp.MustCompile(`(?i)HTTP 5[\p{Nd}][\p{Nd}]`)
)
