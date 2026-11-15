package sandbox

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"factory/go/internal/shlex"
)

// Worker GitHub credential (G-010, owner rule R-c): a scoped git credential helper and a gh shim, fail closed.
//
// SECURITY CODE. A worker's `git push` and `gh` calls must use a worker token, never the owner's
// ~/.config/gh/hosts.yml or the owner's git credential helper. PrepareDir runs in HQ and writes
// <root>/<agent>/{gitconfig, bin/gh, gh/}: paths and commands only, no secret. PaneEnv is what the pane adds to its
// env (paths only). The helper and the shim read the token at CALL time.

// Worker credential constants.
const (
	GhNoTokenMsg = "worker gh token unavailable"
	GhHost       = "github.com"
	GhUser       = "x-access-token"
	ShimExit     = 4
)

var (
	slugRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

// ValidSlug reports whether s is an OWNER/NAME repo slug.
func ValidSlug(s string) bool { return slugRE.MatchString(s) }

func printableNoSpace(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ReadTokenFile is the token in path, or "" : a regular file, mode 0600, own uid, never through a symlink, non-empty
// and printable without whitespace.
func ReadTokenFile(path string) string {
	if path == "" {
		return ""
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return ""
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != os.Getuid() {
		return ""
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	tok := strings.TrimSpace(string(b))
	if !printableNoSpace(tok) {
		return ""
	}
	return tok
}

// WorkerToken is $GH_TOKEN when set (the sandbox case), else the file named by $FACTORY_GH_TOKEN_FILE; "" when
// neither works. A set but malformed GH_TOKEN is refused, never replaced by the file.
func WorkerToken(env map[string]string) string {
	if tok := strings.TrimSpace(env["GH_TOKEN"]); tok != "" {
		if printableNoSpace(tok) {
			return tok
		}
		return ""
	}
	return ReadTokenFile(env["FACTORY_GH_TOKEN_FILE"])
}

// EnvMap is the process environment as a map.
func EnvMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func parseSlugs(arg string) []string {
	var out []string
	for _, s := range strings.Split(arg, ",") {
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	for _, s := range out {
		if !slugRE.MatchString(s) {
			return nil
		}
	}
	return out
}

// CredentialRequest parses the key=value lines git sends the helper.
func CredentialRequest(r io.Reader) map[string]string {
	req := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimRight(sc.Text(), "\n"), "="); ok {
			req[k] = v
		}
	}
	return req
}

// GitCredential is one helper call; the return is the exit code. store/erase are no-ops; get answers only for an
// allowed repo over https on github.com. Any failure is one stderr line naming no token, nothing on stdout.
func GitCredential(slugArg, action string, stdin io.Reader, stdout, stderr io.Writer, env map[string]string) int {
	switch action {
	case "store", "erase":
		return 0
	case "get":
	default:
		fmt.Fprintf(stderr, "ghcred: unknown action %q\n", action)
		return 1
	}
	slugs := parseSlugs(slugArg)
	req := CredentialRequest(stdin)
	path := req["path"]
	allowed := slugs != nil && req["protocol"] == "https" && req["host"] == GhHost
	if allowed {
		allowed = false
		for _, s := range slugs {
			if path == s || path == s+".git" {
				allowed = true
			}
		}
	}
	if !allowed {
		fmt.Fprintln(stderr, "ghcred: no worker credential for this host or repository")
		return 1
	}
	tok := WorkerToken(env)
	if tok == "" {
		fmt.Fprintf(stderr, "%s: check the worker token file named in the factory config\n", GhNoTokenMsg)
		return 1
	}
	fmt.Fprintf(stdout, "username=%s\npassword=%s\n", GhUser, tok)
	return 0
}

// GhShimEnv is the env the real gh runs with: the caller's, minus the other GitHub token variables, plus the
// worker token. ok is false (gh must not run, exit ShimExit) when the token is unavailable.
func GhShimEnv(env []string, configDir string) (out []string, ok bool) {
	tok := WorkerToken(EnvMap(env))
	if tok == "" {
		return nil, false
	}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GH_TOKEN", "GH_CONFIG_DIR", "GH_PROMPT_DISABLED":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GH_TOKEN="+tok, "GH_CONFIG_DIR="+configDir, "GH_PROMPT_DISABLED=1"), true
}

// GhShim execs the real gh with the worker token; it returns ShimExit without running it when the token is
// unavailable or gh cannot be run.
func GhShim(realGh, configDir string, args []string, env []string, stderr io.Writer) int {
	benv, ok := GhShimEnv(env, configDir)
	if !ok {
		fmt.Fprintf(stderr, "%s: gh not run\n", GhNoTokenMsg)
		return ShimExit
	}
	if err := syscall.Exec(realGh, append([]string{realGh}, args...), benv); err != nil {
		fmt.Fprintf(stderr, "ghcred: cannot run the real gh (%v); re-run the worker gh setup\n", err)
	}
	return ShimExit
}

// CheckTokenPath is the resolved token path; an error unless it is a regular file directly in <home>/.config/factory.
func CheckTokenPath(tokenPath, home string) (string, error) {
	cfgDir := Realpath(filepath.Join(home, ".config", "factory"))
	real := Realpath(tokenPath)
	if filepath.Dir(real) != cfgDir || !isFile(real) || isLink(tokenPath) {
		return "", fmt.Errorf("worker gh token must be a regular file directly in %s, not %s", cfgDir, tokenPath)
	}
	return real, nil
}

// GitValue is `git config --global --includes --get key` as the owner, run in cwd so the owner's includeIf gitdir
// rules apply. A missing or non-directory cwd falls back to "/". Empty when unset.
func GitValue(home, key, cwd string) string {
	if cwd == "" || !isDir(cwd) {
		cwd = "/"
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	cmd := exec.Command("git", "config", "--global", "--includes", "--get", key)
	cmd.Dir = cwd
	cmd.Env = []string{"HOME=" + home, "PATH=" + path, "GIT_CONFIG_NOSYSTEM=1"}
	t := time.AfterFunc(20*time.Second, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	out, _ := cmd.Output()
	t.Stop()
	return strings.TrimSpace(string(out))
}

// gq is a git-config double-quoted value.
func gq(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}

// writeAtomic writes name in the directory dirFd with a temp file (O_EXCL, never a followed name) and a rename.
func writeAtomic(dirFd int, name, data string, mode os.FileMode) error {
	dir := fdp(dirFd)
	tmp := fmt.Sprintf(".%s.%d.tmp", name, os.Getpid())
	fd, err := syscall.Open(dir+"/"+tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode))
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, werr := f.WriteString(data)
	if cerr := f.Chmod(mode); werr == nil {
		werr = cerr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(dir+"/"+tmp, dir+"/"+name)
	}
	if werr != nil {
		_ = os.Remove(dir + "/" + tmp)
	}
	return werr
}

// openDir creates (0700) if needed and opens path (relative to parentFd when >= 0) as a directory fd; a symlink or
// foreign owner is refused.
func openDir(path string, parentFd int) (int, error) {
	full := path
	if parentFd >= 0 {
		full = fdp(parentFd) + "/" + path
	}
	if err := syscall.Mkdir(full, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
		return -1, err
	}
	fd, err := syscall.Open(full, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil || int(st.Uid) != os.Getuid() {
		syscall.Close(fd)
		return -1, fmt.Errorf("%s is not owned by uid %d", path, os.Getuid())
	}
	if err := syscall.Fchmod(fd, 0o700); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

// GitConfigText renders the per-agent gitconfig: identity, https-path credentials, the scoped helper.
func GitConfigText(name, email, helper string) string {
	lines := []string{"[user]"}
	if name != "" {
		lines = append(lines, "\tname = "+gq(name))
	}
	if email != "" {
		lines = append(lines, "\temail = "+gq(email))
	}
	lines = append(lines, "[credential]", "\tuseHttpPath = true", fmt.Sprintf(`[credential "https://%s"]`, GhHost), "\thelper = "+gq(helper), "")
	return strings.Join(lines, "\n")
}

// GhHelper is the git credential helper command for slugs, run as this binary.
func GhHelper(self string, slugs []string) string {
	return fmt.Sprintf("!%s git-credential %s", shlex.Quote(self), shlex.Quote(strings.Join(slugs, ",")))
}

// PrepareDirOpts are PrepareDir's arguments; Self is the binary the helper and the shim run ("" = this one).
type PrepareDirOpts struct {
	Agent, Role, TokenPath string
	Slugs                  []string
	RealGh                 string
	Home, Root, Cwd, Self  string
	Identity               *[2]string // user.name, user.email; nil = the owner's global git config
}

// PrepareDir writes the per-agent dir and returns its path. Role is for the caller's records; nothing here depends
// on it (a role without a gh credential simply never gets a token path from the caller).
func PrepareDir(o PrepareDirOpts) (string, error) {
	if !nameRE.MatchString(o.Agent) {
		return "", fmt.Errorf("bad agent name %q", o.Agent)
	}
	if len(o.Slugs) == 0 {
		return "", fmt.Errorf("worker gh repos must be OWNER/NAME slugs, got %q", o.Slugs)
	}
	for _, s := range o.Slugs {
		if !slugRE.MatchString(s) {
			return "", fmt.Errorf("worker gh repos must be OWNER/NAME slugs, got %q", o.Slugs)
		}
	}
	if !filepath.IsAbs(o.RealGh) {
		return "", fmt.Errorf("real gh must be an absolute path, got %q", o.RealGh)
	}
	home := o.Home
	if home == "" {
		home = RealHome()
	}
	if _, err := CheckTokenPath(o.TokenPath, home); err != nil {
		return "", err
	}
	root := o.Root
	if root == "" {
		root = filepath.Join(home, ".local", "state", "factory", "ghcred")
	}
	self := o.Self
	if self == "" {
		self, _ = os.Executable()
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	agentDir := filepath.Join(root, o.Agent)
	var name, email string
	if o.Identity != nil {
		name, email = o.Identity[0], o.Identity[1]
	} else {
		name, email = GitValue(home, "user.name", o.Cwd), GitValue(home, "user.email", o.Cwd)
	}
	shim := fmt.Sprintf("#!/bin/sh\nexec %s gh-shim %s %s \"$@\"\n", shlex.Quote(self), shlex.Quote(o.RealGh), shlex.Quote(filepath.Join(agentDir, "gh")))
	rootFd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	dirFd, err := openDir(o.Agent, rootFd)
	syscall.Close(rootFd)
	if err != nil {
		return "", err
	}
	defer syscall.Close(dirFd)
	if err := writeAtomic(dirFd, "gitconfig", GitConfigText(name, email, GhHelper(self, o.Slugs)), 0o600); err != nil {
		return "", err
	}
	binFd, err := openDir("bin", dirFd)
	if err != nil {
		return "", err
	}
	err = writeAtomic(binFd, "gh", shim, 0o700)
	syscall.Close(binFd)
	if err != nil {
		return "", err
	}
	ghFd, err := openDir("gh", dirFd)
	if err != nil {
		return "", err
	}
	syscall.Close(ghFd)
	return agentDir, nil
}

// PaneEnv is the env a pane adds: paths only, never a token.
func PaneEnv(agentDir, tokenPath, callerPath string) map[string]string {
	if callerPath == "" {
		callerPath = "/usr/bin:/bin"
	}
	return map[string]string{
		"PATH":                  filepath.Join(agentDir, "bin") + ":" + callerPath,
		"GIT_CONFIG_GLOBAL":     filepath.Join(agentDir, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM":   "1",
		"GIT_TERMINAL_PROMPT":   "0",
		"GH_CONFIG_DIR":         filepath.Join(agentDir, "gh"),
		"FACTORY_GH_TOKEN_FILE": tokenPath,
	}
}
