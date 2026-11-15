package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"factory/go/internal/gitx"
	"factory/go/internal/identity"
)

// src (G-048 T-1462): cmd_src in bin/factory_cli.py (12250). Export a ref into the caller's scratchpad with no config,
// filter, attribute or hook of the caller's able to run. Two things stay in Python and hand over before anything is
// written: a home that is not a live home (the caller's identity is then read from another database), and a repo whose
// git config is not provably clean. git_config_refusal's remote-URL allow-list needs the project registry and the
// factory remote, so Go accepts only the keys listed in srcSafeKey and sends everything else (an unknown key, any
// remote.origin.url, an include, a non-empty attributes file) to Python, which then refuses or allows exactly as before.

func init() {
	register("src", "", handler{parse: parseSrc, run: runSrc})
}

type srcCmd struct {
	ref, dest string
	paths     []string
}

// srcTmpBase is the tmp directory scratchpads live under. Only the tests of this package move it, like
// _SRC_TEST_LIVE_HOME moves the Python side; no argument or environment variable can.
var srcTmpBase = "/tmp"

// srcPath is SRC_PATH: git and tar are looked up here, never on the caller's PATH.
const srcPath = "/usr/bin:/bin"

const (
	srcRevTimeout     = 60 * time.Second
	srcExtractTimeout = 600 * time.Second
	srcArchiveWait    = 60 * time.Second
	srcCfgTimeout     = 30 * time.Second
)

// parseSrc accepts `src REF DEST [-- PATH...]` and `src REF DEST PATH...` where no word starts with "-" (other than
// the one separator). Everything argparse could read as an option, and invalid UTF-8, hands over.
func parseSrc(rest []string) (any, bool) {
	for _, w := range rest {
		if !utf8.ValidString(w) {
			return nil, false
		}
	}
	if len(rest) < 2 || strings.HasPrefix(rest[0], "-") || strings.HasPrefix(rest[1], "-") {
		return nil, false
	}
	s := srcCmd{ref: rest[0], dest: rest[1]}
	tail := rest[2:]
	if len(tail) > 0 && tail[0] == "--" {
		tail = tail[1:]
		for _, w := range tail {
			if w == "--" {
				return nil, false
			}
		}
	} else {
		for _, w := range tail {
			if strings.HasPrefix(w, "-") {
				return nil, false
			}
		}
	}
	s.paths = tail
	return s, true
}

var (
	srcSlugBadRE   = regexp.MustCompile(`[^A-Za-z0-9]`)
	srcSessionRE   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	srcSafeKeyRE   = regexp.MustCompile(`(?i)^(core\.(repositoryformatversion|filemode|bare|logallrefupdates)|remote\.origin\.fetch|branch\..+\.(remote|merge)|user\.name)$`)
	srcSafeBoolOff = []string{"false", "no", "off", "0"}
)

// srcScratchRoots is src_scratch_roots: realpaths of the caller's own scratchpad. The second result is the refusal text.
func srcScratchRoots(c *ctx) ([]string, string, error) {
	who := c.env.Getenv("FACTORY_AGENT")
	var cwd, sid sql.NullString
	found := false
	if who != "" && who != "owner" {
		err := c.db.QueryRow("SELECT cwd, session_id FROM agents WHERE name=?", who).Scan(&cwd, &sid)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, sql.ErrNoRows):
			return nil, "", fmt.Errorf("read agent %s: %w", who, err)
		}
	}
	if !found || cwd.String == "" {
		return nil, "factory src runs as a registered agent: FACTORY_AGENT must name an agent with a registered cwd", nil
	}
	agentCwd := identity.Realpath(cwd.String)
	here, err := c.deps.getwd()
	if err != nil {
		return nil, "", errDelegate
	}
	if identity.Realpath(here) != agentCwd {
		return nil, fmt.Sprintf("run factory src from your own working directory %s, not %s", agentCwd, identity.Realpath(here)), nil
	}
	if !utf8.ValidString(agentCwd) {
		return nil, "", errDelegate
	}
	base := filepath.Join(srcTmpBase, fmt.Sprintf("claude-%d", c.env.Uid), srcSlugBadRE.ReplaceAllString(agentCwd, "-"))
	// the slug is one "-" per code point; ReplaceAllString replaces a multi-byte character once, as Python does
	var roots []string
	if sid.String != "" && !srcSessionRE.MatchString(sid.String) {
		return nil, fmt.Sprintf("registered session id %s is not a plain id", verdictRepr(sid.String)), nil
	}
	if fi, err := os.Lstat(base); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return roots, "", nil
	}
	var subs []string
	if sid.String != "" {
		subs = []string{sid.String}
	} else {
		ents, err := os.ReadDir(base)
		if err != nil {
			return roots, "", nil // OSError: the roots found so far
		}
		for _, e := range ents {
			subs = append(subs, e.Name())
		}
	}
	for _, s := range subs {
		sp := filepath.Join(base, s)
		pad := filepath.Join(sp, "scratchpad")
		sfi, e1 := os.Lstat(sp)
		pfi, e2 := os.Lstat(pad)
		if e1 == nil && e2 == nil && sfi.Mode()&os.ModeSymlink == 0 && sfi.IsDir() && pfi.Mode()&os.ModeSymlink == 0 && pfi.IsDir() {
			roots = append(roots, identity.Realpath(pad))
		}
	}
	return roots, "", nil
}

// srcSharedCheckouts is src_shared_checkouts: the factory home, the live homes, the running checkout and every
// project repo_path, as realpaths.
func srcSharedCheckouts(c *ctx) ([]string, error) {
	set := map[string]bool{identity.Realpath(c.env.Home()): true}
	_, homes, err := c.env.DeriveLiveHomes()
	if err != nil {
		return nil, errDelegate
	}
	for h := range homes {
		set[identity.Realpath(h)] = true
	}
	set[identity.Realpath(filepath.Dir(filepath.Dir(c.deps.pyEntry())))] = true
	rows, err := c.query("SELECT repo_path FROM projects WHERE repo_path IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("read project repos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rp string
		if err := rows.Scan(&rp); err != nil {
			return nil, fmt.Errorf("scan project repo: %w", err)
		}
		set[identity.Realpath(rp)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read project repos: %w", err)
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// srcRepo is src_repo: the repo_path of the caller's project (via its goal), else the factory home.
func srcRepo(c *ctx) (string, error) {
	var rp sql.NullString
	err := c.db.QueryRow("SELECT p.repo_path FROM agents ag JOIN goals g ON g.id=ag.goal_id JOIN projects p ON p.id=g.project_id "+
		"WHERE ag.name=?", c.who).Scan(&rp)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read repo of %s: %w", c.who, err)
	}
	if rp.String != "" {
		return identity.Realpath(rp.String), nil
	}
	return identity.Realpath(c.env.Home()), nil
}

func envWithout(env []string, drop ...string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !inList(drop, k) {
			out = append(out, kv)
		}
	}
	return out
}

// srcConfigClean reports that git_config_refusal(repo) would return nothing, deciding only the cases it can decide
// from the strict list above. false means "not provably clean": the caller hands over.
func srcConfigClean(c *ctx, repo string, opts gitx.Options) (bool, error) {
	base := envWithout(os.Environ(), "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR")
	o := opts
	o.Env = append(append([]string(nil), base...), "LC_ALL=C")
	o.Timeout = srcCfgTimeout
	p, err := gitx.Run(repo, o, "rev-parse", "--git-dir", "--git-common-dir")
	if err != nil || p.ExitCode != 0 {
		return false, nil
	}
	lines := strings.Split(pyNewlines(p.Stdout), "\n")
	if len(lines) < 2 || lines[0] == "" || lines[1] == "" {
		return false, nil
	}
	join := func(a, b string) string {
		if filepath.IsAbs(b) {
			return identity.Realpath(b)
		}
		return identity.Realpath(filepath.Join(a, b))
	}
	gitdir, common := join(repo, lines[0]), join(repo, lines[1])
	files := []string{filepath.Join(common, "config")}
	if m, err := filepath.Glob(filepath.Join(common, "worktrees", "*", "config.worktree")); err == nil {
		sort.Strings(m)
		files = append(files, m...)
	}
	files = append(files, filepath.Join(gitdir, "config.worktree"))
	seen := map[string]bool{}
	o.Env = base
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		if _, err := os.Stat(f); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, nil
		}
		q, err := gitx.Run(repo, o, "config", "--file", f, "--list", "-z")
		if err != nil || q.ExitCode != 0 {
			return false, nil
		}
		for _, ent := range strings.Split(q.Stdout, "\x00") {
			if ent == "" {
				continue
			}
			k, v, _ := strings.Cut(ent, "\n")
			if !isASCII(k) {
				return false, nil
			}
			lk := strings.ToLower(k)
			switch {
			case srcSafeKeyRE.MatchString(k):
			case lk == "user.email" && isASCII(v) && strings.HasSuffix(strings.ToLower(pyStrip(v)), noreplySuffix):
			case lk == "extensions.worktreeconfig" && inList(srcSafeBoolOff, strings.ToLower(pyStrip(v))):
			default:
				return false, nil
			}
		}
	}
	xdg := c.env.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(c.env.UserHome(), ".config")
	}
	for _, attrs := range []string{filepath.Join(common, "info", "attributes"), filepath.Join(xdg, "git", "attributes")} {
		fi, err := os.Stat(attrs)
		switch {
		case err == nil:
			if !fi.Mode().IsRegular() || fi.Size() > 0 {
				return false, nil
			}
		case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		default:
			return false, nil
		}
	}
	return true, nil
}

func lookIn(dirs []string, name string) (string, bool) {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

func runSrc(c *ctx, parsed any) (int, error) {
	a := parsed.(srcCmd)
	live, err := c.env.HomeIsLive()
	if err != nil || !live {
		return 0, errDelegate // the caller's row is read from the live home's database, which Python opens
	}
	for _, p := range a.paths {
		if strings.Contains(p, "\x00") {
			return 0, dieErr(2, "bad path")
		}
	}
	if strings.Contains(a.ref, "\x00") || strings.Contains(a.dest, "\x00") {
		return 0, dieErr(2, "bad ref or dest")
	}
	roots, msg, err := srcScratchRoots(c)
	if err != nil {
		return 0, err
	}
	if msg != "" {
		return 0, dieErr(2, msg)
	}
	if len(roots) == 0 {
		return 0, dieErr(2, "no scratchpad found for your session (<tmp>/claude-<uid>/<your cwd slug>/<your session>/scratchpad); "+
			"run factory src from the directory your session started in")
	}
	dest := identity.Realpath(a.dest)
	linkPath := strings.TrimRight(a.dest, "/")
	if linkPath == "" {
		linkPath = "/"
	}
	if fi, err := os.Lstat(linkPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return 0, dieErr(2, fmt.Sprintf("dest %s is a symlink; pass a real directory inside your scratchpad", a.dest))
	}
	inside := false
	for _, r := range roots {
		if dest != r && strings.HasPrefix(dest, r+"/") {
			inside = true
		}
	}
	if !inside {
		return 0, dieErr(2, fmt.Sprintf("dest %s is not inside your scratchpad (%s); pick a subdirectory there", dest, roots[0]))
	}
	shared, err := srcSharedCheckouts(c)
	if err != nil {
		return 0, err
	}
	for _, sh := range shared {
		if dest == sh || strings.HasPrefix(dest, sh+"/") || strings.HasPrefix(sh, dest+"/") {
			return 0, dieErr(2, fmt.Sprintf("dest %s overlaps the shared checkout %s; use a directory in your scratchpad outside it", dest, sh))
		}
	}
	repo, err := srcRepo(c)
	if err != nil {
		return 0, err
	}
	opts, err := gitOptions(c)
	if err != nil {
		return 0, err
	}
	clean, err := srcConfigClean(c, repo, opts)
	if err != nil {
		return 0, err
	}
	if !clean {
		return 0, errDelegate // Python decides: refuse with the offending keys, or export
	}
	gitExe, ok1 := lookIn(strings.Split(srcPath, ":"), "git")
	tarExe, ok2 := lookIn(strings.Split(srcPath, ":"), "tar")
	if !ok1 || !ok2 {
		return 0, errDelegate
	}

	created := ""
	if _, err := os.Lstat(dest); err == nil {
		fi, serr := os.Stat(dest)
		ents, rerr := os.ReadDir(dest)
		if serr != nil || !fi.IsDir() || rerr != nil || len(ents) > 0 {
			return 0, dieErr(2, fmt.Sprintf("dest %s exists and is not an empty directory; pick a new or empty one", dest))
		}
	} else {
		created = dest
		for {
			if _, err := os.Lstat(filepath.Dir(created)); err == nil {
				break
			}
			created = filepath.Dir(created)
		}
		if err := os.MkdirAll(dest, 0o777); err != nil {
			return 0, dieErr(1, fmt.Sprintf("cannot create %s: %v", dest, err))
		}
	}
	tmpHome, err := os.MkdirTemp("", "factory-src-home-")
	if err != nil {
		return 0, dieErr(1, fmt.Sprintf("cannot create a temporary home: %v", err))
	}
	env := []string{"PATH=" + srcPath, "HOME=" + tmpHome, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1"}
	gitArgs := func(more ...string) []string {
		argv := []string{"-C", repo}
		for _, kv := range opts.Overrides() {
			argv = append(argv, "-c", kv[0]+"="+kv[1])
		}
		argv = append(argv, "-c", "core.attributesFile=/dev/null")
		return append(argv, more...)
	}
	rc, why := 1, ""
	func() {
		vctx, cancel := context.WithTimeout(context.Background(), srcRevTimeout)
		defer cancel()
		v := exec.CommandContext(vctx, gitExe, gitArgs("rev-parse", "--verify", "--end-of-options", a.ref+"^{tree}")...)
		v.Env = env
		v.WaitDelay = time.Second
		if err := v.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				why = fmt.Sprintf("ref %s is not a tree-ish in %s; check `git -C %s rev-parse` with a branch, tag or sha", verdictRepr(a.ref), repo, repo)
			} else {
				why = fmt.Sprintf("%T: %v", err, err)
			}
			return
		}
		why, rc = srcExtract(gitExe, tarExe, gitArgs("archive", "--format=tar", "--end-of-options", a.ref), a.paths, dest, env)
	}()
	if err := os.RemoveAll(tmpHome); err != nil {
		fmt.Fprintf(c.errb, "factory: cannot remove %s: %v\n", tmpHome, err)
	}
	if rc != 0 {
		if created != "" {
			os.RemoveAll(created) // best effort, as shutil.rmtree(ignore_errors=True)
		} else if ents, err := os.ReadDir(dest); err == nil {
			for _, e := range ents {
				os.RemoveAll(filepath.Join(dest, e.Name())) // best effort
			}
		}
		return 0, dieErr(1, why)
	}
	fmt.Fprintf(c.out, "extracted %s to %s\n", a.ref, dest)
	return 0, nil
}

// srcExtract is `git archive ... | tar -x -C dest --no-same-owner`: (failure text, rc). rc 0 means extracted.
func srcExtract(gitExe, tarExe string, archiveArgs, paths []string, dest string, env []string) (string, int) {
	actx, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	arch := exec.CommandContext(actx, gitExe, append(archiveArgs, paths...)...)
	arch.Env = env
	var archErr bytes.Buffer
	arch.Stderr = &archErr
	pipe, err := arch.StdoutPipe()
	if err != nil {
		return fmt.Sprintf("%T: %v", err, err), 1
	}
	tctx, cancelT := context.WithTimeout(context.Background(), srcExtractTimeout)
	defer cancelT()
	tar := exec.CommandContext(tctx, tarExe, "-x", "-C", dest, "--no-same-owner")
	tar.Env = env
	tar.Stdin = pipe
	var tarErr bytes.Buffer
	tar.Stderr = &tarErr
	if err := arch.Start(); err != nil {
		return fmt.Sprintf("%T: %v", err, err), 1
	}
	if err := tar.Start(); err != nil {
		cancelA()
		arch.Wait() // reap; its exit status is the failure being reported
		return fmt.Sprintf("%T: %v", err, err), 1
	}
	// The child tar holds its own copy of the read end. Drop ours so that if tar exits early, git archive sees a
	// closed pipe (SIGPIPE) and ends at once instead of blocking until srcArchiveWait (Python: arch.stdout.close()).
	pipe.Close()
	terr := tar.Wait()
	if tctx.Err() != nil {
		cancelA()
		arch.Wait() // reaped; the timeout below is the failure being reported
		return fmt.Sprintf("TimeoutExpired: tar timed out after %d seconds", int(srcExtractTimeout.Seconds())), 1
	}
	done := make(chan error, 1)
	go func() { done <- arch.Wait() }()
	var aerr error
	select {
	case aerr = <-done:
	case <-time.After(srcArchiveWait):
		cancelA()
		<-done
		return fmt.Sprintf("TimeoutExpired: git archive timed out after %d seconds", int(srcArchiveWait.Seconds())), 1
	}
	replace := func(b []byte) string { return strings.Map(func(r rune) rune { return r }, string(b)) }
	var ee *exec.ExitError
	switch {
	case aerr != nil && errors.As(aerr, &ee):
		return "git archive failed: " + pyStrip(replace(archErr.Bytes())), 1
	case terr != nil && errors.As(terr, &ee):
		return "tar failed: " + pyStrip(replace(tarErr.Bytes())), 1
	case aerr != nil:
		return fmt.Sprintf("%T: %v", aerr, aerr), 1
	case terr != nil:
		return fmt.Sprintf("%T: %v", terr, terr), 1
	}
	return "", 0
}
