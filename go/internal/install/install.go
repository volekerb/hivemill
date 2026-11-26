package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Pinned tools. machine -> (url, size, sha256), kept equal to the Python reference, to the fresh-box test and to
// docs/SETUP.md.
const (
	claudeInstallerURL = "https://claude.ai/install.sh"
	herdrVersion       = "v0.9.5"
	herdrRelease       = "https://github.com/herdrdev/herdr/releases/download/" + herdrVersion
	ghVersion          = "2.103.3"
	ghRelease          = "https://github.com/cli/cli/releases/download/v" + ghVersion
	tailLines          = 15
	xcodeHint          = "xcode-select --install"
	darwinBootNote     = "boot units: launchd user agents (hivemill.*) in ~/Library/LaunchAgents"
	darwinLingerNote   = "--linger: not applicable on macOS (launchd user agents run while you are logged in)"
	aptHint            = "sudo apt-get install -y git python3"
)

// Asset is one pinned download.
type Asset struct {
	URL    string
	Size   int64
	SHA256 string
}

// HerdrAssets and GHAssets are the pinned machine -> asset tables.
var (
	HerdrAssets = map[string]Asset{
		"x86_64":  {herdrRelease + "/herdr-linux-x86_64", 30285672, "14d9e8bc8b4a9e0118d77c32266f76d461f09b40342eb23752960bd89f59929d"},
		"aarch64": {herdrRelease + "/herdr-linux-aarch64", 27900752, "17d7536f2da50859523680ef279dffcac8e4a235f4e46fe8da6c2ec44141897c"},
		// macOS entries are keyed "darwin/<arch>"; Linux keeps the bare machine name
		"darwin/aarch64": {herdrRelease + "/herdr-macos-aarch64", 22536176, "fb77c2232a05a8035421382c894392f4174e4e38a18e48af3dd0290157c3a7e2"},
	}
	GHAssets = map[string]Asset{
		"x86_64":         {ghRelease + "/gh_" + ghVersion + "_linux_amd64.tar.gz", 15031972, "4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d"},
		"aarch64":        {ghRelease + "/gh_" + ghVersion + "_linux_arm64.tar.gz", 13661485, "ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63"},
		"darwin/aarch64": {ghRelease + "/gh_" + ghVersion + "_macOS_arm64.zip", 14106091, "d700207eae9ae98fe2b89535c2c087c07823d4628c56364695a5a07ef311c062"},
	}
	archAlias = map[string]string{"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}
)

var (
	tagRe     = regexp.MustCompile(`^v(\p{Nd}+)\.(\p{Nd}+)\.(\p{Nd}+)\n?$`) // Python \d and $ semantics
	versionRe = regexp.MustCompile(`hivemill (\p{Nd}+\.\p{Nd}+\.\p{Nd}+)`)
	pyVerRe   = regexp.MustCompile(`Python (\p{Nd}+)\.(\p{Nd}+)`)
)

// Ctx is the state of one run. In plan mode Do records and prints the argv and runs nothing.
type Ctx struct {
	host    Host
	args    *Args
	out     io.Writer
	plan    bool
	home    string
	fh      string
	bindir  string
	factory string
	n       int
	tmp     string
	ref     string
	port    int
	version string
}

// NewCtx builds the context of a run.
func NewCtx(host Host, args *Args, out io.Writer, plan bool) *Ctx {
	home := host.Home()
	bindir := filepath.Join(home, ".local", "bin")
	return &Ctx{host: host, args: args, out: out, plan: plan, home: home, fh: filepath.Join(home, "factory"),
		bindir: bindir, factory: filepath.Join(bindir, "factory"), port: args.Port}
}

// emit prints one redacted line. A write error on the output stream cannot be reported anywhere else.
func (c *Ctx) emit(text string) {
	text = redact(text)
	env := c.host.Environ()
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if val := env[key]; utf8.RuneCountInString(val) >= 20 {
			text = strings.ReplaceAll(text, val, "[REDACTED]")
		}
	}
	_, _ = fmt.Fprintln(c.out, text) // nothing to do if the output stream is gone
}

func (c *Ctx) note(text string) { c.emit("    " + text) }

// do runs one mutating command. A failure with check set returns a StepFail with the redacted tail.
func (c *Ctx) do(argv []string, what, cwd string, timeout time.Duration, check bool) (int, string, error) {
	if c.plan {
		c.emit("    $ " + strings.Join(argv, " "))
		return 0, "", nil
	}
	rc, out := c.host.Run(argv, cwd, timeout, true)
	if check && rc != 0 {
		return rc, out, stepf("%s failed (rc=%d):\n%s", what, rc, tailOf(out))
	}
	return rc, out, nil
}

func (c *Ctx) doDefault(argv []string, what string) (string, error) {
	_, out, err := c.do(argv, what, "", 600*time.Second, true)
	return out, err
}

// read runs one read-only command (also in plan mode).
func (c *Ctx) read(argv []string, timeout time.Duration) (int, string) {
	return c.host.Run(argv, "", timeout, false)
}

func tailOf(text string) string {
	var lines []string
	for _, ln := range pySplitLines(text) {
		if pyStrip(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	for i, ln := range lines {
		lines[i] = "    " + ln
	}
	return redact(strings.Join(lines, "\n"))
}

// ----------------------------------------------------------------------------------------------- latest tag

// resolveLatest is the highest ^vX.Y.Z$ tag among `git ls-remote --tags` lines, or "". Pre-release tags never match.
func resolveLatest(lsRemote string) string {
	var best *[3]*big.Int
	bestName := ""
	for _, ln := range pySplitLines(lsRemote) {
		parts := pyFields(ln)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "refs/tags/") {
			continue
		}
		name := strings.TrimPrefix(parts[1], "refs/tags/")
		name = strings.TrimSuffix(name, "^{}")
		m := tagRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		key := [3]*big.Int{digitsToInt(m[1]), digitsToInt(m[2]), digitsToInt(m[3])}
		if best == nil || cmpKey(key, *best) > 0 {
			best, bestName = &key, name
		}
	}
	return bestName
}

func cmpKey(a, b [3]*big.Int) int {
	for i := range a {
		if c := a[i].Cmp(b[i]); c != 0 {
			return c
		}
	}
	return 0
}

// ----------------------------------------------------------------------------------------------- --check

type checkItem struct{ status, name, detail string }

func pyAtLeast(out string, major, minor int64) (found, ok bool) {
	m := pyVerRe.FindStringSubmatch(out)
	if m == nil {
		return false, false
	}
	maj, mnr := digitsToInt(m[1]), digitsToInt(m[2])
	if c := maj.Cmp(big.NewInt(major)); c != 0 {
		return true, c > 0
	}
	return true, mnr.Cmp(big.NewInt(minor)) >= 0
}

// memGiB reads the RAM size: `sysctl -n hw.memsize` on macOS, the host's own reader elsewhere.
func memGiB(h Host) (float64, error) {
	if h.GOOS() != "darwin" {
		return h.MemGiB()
	}
	rc, out := h.Run([]string{"sysctl", "-n", "hw.memsize"}, "", 0, false)
	if rc != 0 {
		return 0, fmt.Errorf("sysctl -n hw.memsize failed (rc=%d)", rc)
	}
	n, ok := asciiDigits64(pyStrip(out))
	if !ok || n <= 0 {
		return 0, errors.New("sysctl -n hw.memsize printed no byte count")
	}
	return float64(n) / (1024.0 * 1024.0 * 1024.0), nil
}

func asciiDigits64(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && !strings.ContainsAny(s, "+-_ ")
}

func checkItems(h Host, noSystemd bool) ([]checkItem, float64, bool) {
	darwin := h.GOOS() == "darwin"
	var items []checkItem
	add := func(status, name, detail string) { items = append(items, checkItem{status, name, detail}) }
	rc, out := h.Run([]string{"python3", "--version"}, "", 0, false)
	found, ok := false, false
	if rc == 0 {
		found, ok = pyAtLeast(out, 3, 11)
	}
	switch {
	case !found:
		add("missing", "python3>=3.11", "python3 not found")
	case ok:
		add("ok", "python3>=3.11", pyStrip(out))
	default:
		add("missing", "python3>=3.11", pyStrip(out)+" is too old; 3.11 or newer needed")
	}
	rc, out = h.Run([]string{"git", "--version"}, "", 0, false)
	if rc == 0 {
		add("ok", "git", pyStrip(out))
	} else {
		add("missing", "git", "not found")
	}
	rc, out = h.Run([]string{"gh", "--version"}, "", 0, false)
	haveGH := rc == 0
	if haveGH {
		detail := ""
		if lines := pySplitLines(out); pyStrip(out) != "" && len(lines) > 0 {
			detail = lines[0]
		}
		add("ok", "gh", detail)
		if rc, _ = h.Run([]string{"gh", "auth", "status"}, "", 0, false); rc == 0 {
			add("ok", "gh-auth", "logged in")
		} else {
			add("missing", "gh-auth", "not logged in: run `gh auth login` in your own terminal")
		}
	} else {
		add("missing", "gh", "not found")
		add("missing", "gh-auth", "gh is not installed")
	}
	rc, out = h.Run([]string{"herdr", "--version"}, "", 0, false)
	if rc == 0 {
		add("ok", "herdr", pyStrip(out))
	} else {
		add("missing", "herdr", "not found")
	}
	rc, out = h.Run([]string{"claude", "--version"}, "", 0, false)
	if rc == 0 {
		add("ok", "claude", pyStrip(out))
	} else {
		add("missing", "claude", "not found")
	}
	if h.Exists(filepath.Join(h.Home(), ".claude", ".credentials.json")) {
		add("ok", "claude-auth", "credentials file present")
	} else {
		add("missing", "claude-auth", "not logged in: run `claude` in your own terminal and complete /login")
	}
	if !darwin { // no systemd on macOS: runCheck prints the launchd note instead
		if rc, _ = h.Run([]string{"systemctl", "--user", "show-environment"}, "", 0, false); rc == 0 {
			add("ok", "systemd-user", "user session reachable")
		} else if noSystemd {
			add("warn", "systemd-user", "no user systemd session (--no-systemd)")
		} else {
			add("missing", "systemd-user", "no user systemd session; pass --no-systemd to install without it")
		}
	}
	gib, err := memGiB(h)
	haveMem := err == nil
	if !haveMem && darwin {
		add("missing", "ram", "cannot read the memory size: "+err.Error())
	} else if !haveMem {
		add("warn", "ram", "unknown: "+err.Error())
	} else if gib < 4 {
		add("warn", "ram", fmt.Sprintf("%.1f GiB (below 4 GB)", gib))
	} else {
		add("ok", "ram", fmt.Sprintf("%.1f GiB", gib))
	}
	cpus := h.CPUCount()
	if cpus < 2 {
		add("warn", "cpu", fmt.Sprintf("%d CPU (below 2)", cpus))
	} else {
		add("ok", "cpu", fmt.Sprintf("%d CPU", cpus))
	}
	rc, out = h.Run([]string{"go", "version"}, "", 0, false)
	if rc == 0 {
		add("ok", "go", pyStrip(out))
	} else {
		add("warn", "go", "not found; optional, guard_impl defaults to python")
	}
	return items, gib, haveMem
}

func suggestedMaxAgents(gib float64, have bool) int {
	if !have {
		return 2
	}
	return min(6, max(2, int(gib)))
}

func runCheck(h Host, a *Args, out io.Writer) int {
	if err := pinRefusal(h); err != nil {
		var stop *Stop
		if errors.As(err, &stop) {
			_, _ = fmt.Fprintln(out, stop.Msg) // nothing to do if stdout is gone
			return stop.Code
		}
		_, _ = fmt.Fprintln(out, err)
		return 1
	}
	items, gib, have := checkItems(h, a.NoSystemd)
	missing := false
	for _, it := range items {
		_, _ = fmt.Fprintf(out, "%s %s %s\n", it.status, it.name, redact(it.detail)) // nothing to do if stdout is gone
		if it.status == "missing" {
			missing = true
		}
	}
	if h.GOOS() == "darwin" {
		_, _ = fmt.Fprintln(out, darwinBootNote)
	}
	_, _ = fmt.Fprintf(out, "suggested-max-agents %d\n", suggestedMaxAgents(gib, have))
	if missing {
		return 3
	}
	return 0
}

// ----------------------------------------------------------------------------------------------- preflight

func normURL(u string) string {
	u = strings.TrimRight(pyStrip(u), "/")
	u = strings.TrimSuffix(u, ".git")
	return strings.ToLower(u)
}

func preflight(c *Ctx) error {
	h, a := c.host, c.args
	if h.Euid() == 0 {
		return stopf(1, "refusing to run as root: run this as your normal user (it never uses elevated rights)")
	}
	if h.Environ()["FACTORY_AGENT"] != "" {
		return stopf(1, "FACTORY_AGENT is set: this is an agent pane. Run the installer in your own terminal")
	}
	if err := pinRefusal(h); err != nil {
		return err
	}
	if err := checkPrereqs(c); err != nil { // missing git must exit 3 before the first git call below
		return err
	}
	if h.Exists(c.fh) {
		rc, out := c.read([]string{"git", "-C", c.fh, "remote", "get-url", "origin"}, 60*time.Second)
		if rc != 0 || normURL(out) != normURL(a.RepoURL) {
			return stopf(1, "%s exists and is not a clone of %s; nothing was touched", c.fh, a.RepoURL)
		}
		if h.Exists(filepath.Join(c.fh, "db", "factory.db")) {
			if !a.Resume {
				return stopf(4, "already installed (%s/db/factory.db exists); nothing was touched. To finish a partial install pass --resume", c.fh)
			}
		} else if !a.Resume {
			return stopf(4, "%s is already a clone but has no database: a partial install. Re-run with --resume to finish it; nothing was touched", c.fh)
		}
	} else if a.Resume {
		c.note(fmt.Sprintf("--resume: no clone at %s yet, so this is a normal install", c.fh))
	}
	return nil
}

func resolveRef(c *Ctx) (string, error) {
	a := c.args
	if a.Ref != "latest" {
		return a.Ref, nil
	}
	rc, out := c.read([]string{"git", "ls-remote", "--tags", a.RepoURL}, 120*time.Second)
	if rc != 0 {
		return "", stepf("git ls-remote --tags failed (rc=%d):\n%s", rc, tailOf(out))
	}
	tag := resolveLatest(out)
	if tag == "" {
		return "", stepf("no release tag vX.Y.Z found in %s; pass --ref", a.RepoURL)
	}
	return tag, nil
}

// ----------------------------------------------------------------------------------------------- steps

func checkPrereqs(c *Ctx) error {
	var missing []string
	rc, out := c.read([]string{"python3", "--version"}, 60*time.Second)
	found, ok := false, false
	if rc == 0 {
		found, ok = pyAtLeast(out, 3, 11)
	}
	if !found || !ok {
		missing = append(missing, "python3 3.11 or newer")
	}
	if rc, _ = c.read([]string{"git", "--version"}, 60*time.Second); rc != 0 {
		missing = append(missing, "git")
	}
	if len(missing) > 0 {
		return stopf(3, "missing: %s. Run this yourself, then re-run the installer:\n  %s", strings.Join(missing, ", "), c.hint())
	}
	return nil
}

func stepPrereqs(c *Ctx) (string, error) {
	if err := checkPrereqs(c); err != nil {
		return "", err
	}
	return "python3>=3.11 and git present (the installer itself needs no Python)", nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read side
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetchVerified(c *Ctx, name string, as Asset) (string, error) {
	path := filepath.Join(c.tmp, name)
	if c.plan {
		c.note(fmt.Sprintf("download %s -> %s, then verify size %d and sha256 %s", as.URL, path, as.Size, as.SHA256))
		return path, nil
	}
	if err := c.host.Download(as.URL, path); err != nil {
		return "", err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Size() != as.Size {
		return "", stepf("%s size mismatch: got %d, pinned %d; nothing installed", name, st.Size(), as.Size)
	}
	got, err := sha256File(path)
	if err != nil {
		return "", err
	}
	if got != as.SHA256 {
		return "", stepf("%s sha256 mismatch; nothing installed", name)
	}
	return path, nil
}

// pinKey is the pin-table key of a host: the bare arch on Linux, "darwin/aarch64" on Apple silicon, "" otherwise.
func pinKey(goos, machine string) string {
	arch := archAlias[machine]
	switch goos {
	case "linux":
		return arch
	case "darwin":
		if arch == "aarch64" {
			return "darwin/aarch64"
		}
	}
	return ""
}

func assetFor(table map[string]Asset, goos, machine, what string) (Asset, error) {
	key := pinKey(goos, machine)
	as, ok := table[key]
	if key == "" || !ok {
		return Asset{}, stepf("no pinned %s release for %s/%s; install it yourself into ~/.local/bin", what, goos, machine)
	}
	return as, nil
}

// pinRefusal is the exit-2 refusal for a host with no pinned release (Intel Macs, other operating systems).
func pinRefusal(h Host) error {
	if h.GOOS() == "linux" || pinKey(h.GOOS(), h.Machine()) != "" { // Linux keeps its own unsupported-arch error at the tools step
		return nil
	}
	arch := h.Machine()
	if a, ok := archAlias[arch]; ok {
		arch = a
	}
	return stopf(2, "hivemill-install: no pinned release for %s/%s (Apple silicon only on macOS)", h.GOOS(), arch)
}

// hint is the command that installs git and python3 on this host.
func (c *Ctx) hint() string {
	if c.host.GOOS() == "darwin" {
		return xcodeHint
	}
	return aptHint
}

func have(c *Ctx, argv ...string) bool {
	rc, _ := c.read(argv, 60*time.Second)
	return rc == 0
}

func stepTools(c *Ctx) (string, error) {
	var done []string
	machine := c.host.Machine()
	if !c.plan {
		if err := c.host.Mkdirs(c.bindir); err != nil {
			return "", fmt.Errorf("create %s: %w", c.bindir, err)
		}
	}
	goos := c.host.GOOS()
	if !have(c, "herdr", "--version") {
		as, err := assetFor(HerdrAssets, goos, machine, "herdr")
		if err != nil {
			return "", err
		}
		src, err := fetchVerified(c, "herdr", as)
		if err != nil {
			return "", err
		}
		if c.plan {
			c.note(fmt.Sprintf("install it as %s/herdr", c.bindir))
		} else if err := c.host.InstallFile(src, filepath.Join(c.bindir, "herdr"), 0o755); err != nil {
			return "", err
		}
		done = append(done, fmt.Sprintf("herdr %s (size+sha256 verified)", herdrVersion))
	}
	if !have(c, "gh", "--version") {
		as, err := assetFor(GHAssets, goos, machine, "gh")
		if err != nil {
			return "", err
		}
		archive, kind, member := "gh.tar.gz", "tarball", ""
		if goos == "darwin" {
			archive, kind = "gh.zip", "zip"
			member = fmt.Sprintf("gh_%s_macOS_arm64/bin/gh", ghVersion)
		} else {
			arch := "arm64"
			if archAlias[machine] == "x86_64" {
				arch = "amd64"
			}
			member = fmt.Sprintf("gh_%s_linux_%s/bin/gh", ghVersion, arch)
		}
		src, err := fetchVerified(c, archive, as)
		if err != nil {
			return "", err
		}
		if c.plan {
			c.note(fmt.Sprintf("extract %s from the %s and install it as %s/gh", member, kind, c.bindir))
		} else {
			ex := filepath.Join(c.tmp, "gh")
			extract := c.host.ExtractMember
			if goos == "darwin" {
				extract = c.host.ExtractZipMember
			}
			if err := extract(src, member, ex); err != nil {
				return "", err
			}
			if err := c.host.InstallFile(ex, filepath.Join(c.bindir, "gh"), 0o755); err != nil {
				return "", err
			}
		}
		done = append(done, fmt.Sprintf("gh %s (size+sha256 verified)", ghVersion))
	}
	if !have(c, "claude", "--version") {
		path := filepath.Join(c.tmp, "claude-install.sh")
		digest := "<sha256>"
		if c.plan {
			c.note(fmt.Sprintf("download %s -> %s (to a file, sha256 logged, never piped into a shell)", claudeInstallerURL, path))
		} else {
			if err := c.host.Download(claudeInstallerURL, path); err != nil {
				return "", err
			}
			var err error
			if digest, err = sha256File(path); err != nil {
				return "", err
			}
		}
		if _, err := c.doDefault([]string{"bash", path}, "claude installer"); err != nil {
			return "", err
		}
		done = append(done, "claude installer sha256 "+digest)
	}
	if len(done) == 0 {
		return "herdr, gh and claude already present", nil
	}
	return strings.Join(done, "; "), nil
}

// authRounds is how many times an interactive run lets the user log in before it gives up with the exit-3 stop.
const authRounds = 3

const claudeLoginPrompt = "Log in to Claude in another terminal: run `claude`, complete /login, then /exit. Press Enter here when done."

func authStop(todo []string) error {
	return stopf(3, "not logged in. Do this, then re-run the installer with --resume:\n  %s", strings.Join(todo, "\n  "))
}

func stepAuth(c *Ctx) (string, error) {
	// a terminal plus --yes: let the user log in and carry on; anything else keeps the exit-3 hand-off
	interactive := c.args.Yes && !c.plan && c.host.IsTerminal()
	for round := 0; ; round++ {
		var todo []string
		ghOK := have(c, "gh", "auth", "status")
		if !ghOK {
			todo = append(todo, "Run this in your own terminal: gh auth login")
		}
		claudeOK := c.host.Exists(filepath.Join(c.home, ".claude", ".credentials.json"))
		if !claudeOK {
			todo = append(todo, "Run this in your own terminal: claude   (then complete /login and /exit)")
		}
		if len(todo) == 0 || c.plan {
			break
		}
		if !interactive || round >= authRounds {
			return "", authStop(todo)
		}
		ghFailed := false
		if !ghOK {
			// gh's own flow on the terminal: the installer never sees, prints or stores what gh prints there
			c.note("running `gh auth login` (gh asks the questions)")
			rc := c.host.RunTTY([]string{"gh", "auth", "login"})
			if rc != 0 { // success is judged by the re-check
				c.note(fmt.Sprintf("`gh auth login` exited %d", rc))
			}
			if rc != 0 || !have(c, "gh", "auth", "status") {
				ghFailed = true
				env := c.host.Environ()
				if env["GH_TOKEN"] != "" || env["GITHUB_TOKEN"] != "" {
					c.note("GH_TOKEN or GITHUB_TOKEN is set, and gh uses it instead of a login: unset it or replace it")
				}
			}
		}
		prompt := ""
		if !claudeOK {
			prompt = claudeLoginPrompt
		} else if ghFailed && round+1 < authRounds {
			prompt = "Press Enter here to run `gh auth login` again."
		}
		if prompt != "" {
			c.emit(prompt)
			if err := c.host.WaitEnter(); err != nil {
				return "", authStop(todo)
			}
		}
	}
	return "gh and Claude are logged in (the installer never reads a token)", nil
}

func stepClone(c *Ctx) (string, error) {
	if c.host.Exists(c.fh) {
		return fmt.Sprintf("kept the existing clone at %s (--resume: no checkout, no ref change)", c.fh), nil
	}
	if c.ref == "" {
		ref, err := resolveRef(c)
		if err != nil {
			return "", err
		}
		c.ref = ref
	}
	// clone beside ~/factory and rename after the checkout: a failed run never leaves a clone at ~/factory that
	// --resume would take for a finished one (it would keep the default branch instead of the requested ref)
	part := c.fh + ".installing"
	if !c.plan {
		c.host.Rmtree(part)
	}
	if _, err := c.doDefault([]string{"git", "clone", "--", c.args.RepoURL, part}, "git clone"); err != nil {
		return "", err
	}
	var err error
	if c.args.Ref == "latest" {
		_, err = c.doDefault([]string{"git", "-C", part, "checkout", "--detach", "refs/tags/" + c.ref}, "git checkout")
	} else if _, err = c.doDefault([]string{"git", "-C", part, "fetch", "origin", "--", c.ref}, "git fetch ref"); err == nil {
		_, err = c.doDefault([]string{"git", "-C", part, "checkout", "--detach", "FETCH_HEAD"}, "git checkout")
	}
	if err != nil {
		c.host.Rmtree(part)
		return "", err
	}
	if c.plan {
		c.emit(fmt.Sprintf("    $ mv %s %s", part, c.fh))
	} else if err := c.host.Rename(part, c.fh); err != nil {
		return "", fmt.Errorf("move the clone into place: %w", err)
	}
	return fmt.Sprintf("cloned %s at %s", c.args.RepoURL, c.ref), nil
}

func stepLink(c *Ctx) (string, error) {
	installSkills(c)
	target := filepath.Join(c.fh, "bin", "factory")
	if c.plan {
		c.emit(fmt.Sprintf("    $ ln -s %s %s", target, c.factory))
	} else {
		if err := c.host.Mkdirs(c.bindir); err != nil {
			return "", fmt.Errorf("create %s: %w", c.bindir, err)
		}
		if err := c.host.Symlink(target, c.factory); err != nil {
			return "", err
		}
	}
	onPath := false
	for _, p := range strings.Split(c.host.Environ()["PATH"], ":") {
		if strings.TrimRight(p, "/") == c.bindir {
			onPath = true
		}
	}
	if !onPath {
		c.emit(fmt.Sprintf("    note: %s is not on your PATH. Add this line to your shell profile yourself: export PATH=\"$HOME/.local/bin:$PATH\"", c.bindir))
		return fmt.Sprintf("linked %s; not on PATH, calling it by absolute path", c.factory), nil
	}
	return "linked " + c.factory, nil
}

func stepInit(c *Ctx) (string, error) {
	if c.host.Exists(filepath.Join(c.fh, "db", "factory.db")) {
		return "database exists: init skipped", nil
	}
	out, err := c.doDefault([]string{c.factory, "init"}, "factory init")
	if err != nil {
		return "", err
	}
	if !c.plan && !strings.Contains(out, "initialised") {
		return "", stepf("`factory init` did not print an `initialised` line:\n%s", tailOf(out))
	}
	return "factory init done", nil
}

func stepConfig(c *Ctx) (string, error) {
	a := c.args
	dbExisted := c.host.Exists(filepath.Join(c.fh, "db", "factory.db")) && !c.plan
	type kv struct{ k, v string }
	var sets []kv
	// the default bind is the default-route address, which would put the UI on the LAN
	if a.Resume && dbExisted {
		_, cur := c.read([]string{c.factory, "config", "serve_bind"}, 60*time.Second)
		if pyStrip(cur) != "" {
			c.note(fmt.Sprintf("--resume: serve_bind is already %s, kept", pyRepr(pyStrip(cur))))
		} else {
			sets = append(sets, kv{"serve_bind", Loopback})
		}
	} else {
		sets = append(sets, kv{"serve_bind", Loopback})
	}
	if a.Port != 0 {
		sets = append(sets, kv{"serve_port", strconv.Itoa(a.Port)})
	}
	if a.MaxAgents != "" {
		sets = append(sets, kv{"max_live_agents", a.MaxAgents})
	}
	if a.NoScout {
		sets = append(sets, kv{"scout_enabled", "0"})
	}
	for _, s := range sets {
		if _, err := c.doDefault([]string{c.factory, "config", s.k, s.v}, "factory config "+s.k); err != nil {
			return "", err
		}
	}
	if a.Port == 0 && a.Resume && dbExisted {
		rc, out := c.read([]string{c.factory, "config", "serve_port"}, 60*time.Second)
		if n, ok := asciiDigits(pyStrip(out)); rc == 0 && ok {
			c.port = n
		}
	}
	if len(sets) == 0 {
		return "no config change", nil
	}
	parts := make([]string, len(sets))
	for i, s := range sets {
		parts[i] = s.k + "=" + s.v
	}
	return "set " + strings.Join(parts, ", "), nil
}

// asciiDigits is str.isdigit() followed by int() for the values `factory config` prints.
func asciiDigits(s string) (int, bool) {
	n, ok := pyInt(s)
	if !ok || s == "" || strings.ContainsAny(s, "+-_ ") || !n.IsInt64() || n.Int64() > 1<<31 {
		return 0, false
	}
	return int(n.Int64()), true
}

// pyRepr is repr() of a str for the plain strings printed here.
func pyRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	return q + strings.ReplaceAll(s, q, `\`+q) + q
}

func stepProject(c *Ctx) (string, error) {
	a := c.args
	if a.ProjectID == "" || a.ProjectRepo == "" {
		return "no --project-id/--project-repo: skipped", nil
	}
	if !c.plan {
		rc, out := c.read([]string{c.factory, "project", "list"}, 60*time.Second)
		if rc == 0 {
			for _, f := range pyFields(out) {
				if f == a.ProjectID {
					return fmt.Sprintf("project %s already listed: skipped", a.ProjectID), nil
				}
			}
		}
	}
	name := a.ProjectName
	if name == "" {
		name = a.ProjectID
	}
	argv := []string{c.factory, "project", "add", a.ProjectID, "--name", name, "--repo", a.ProjectRepo}
	if a.ProjectGH != "" {
		argv = append(argv, "--gh", a.ProjectGH)
	}
	if _, err := c.doDefault(argv, "factory project add"); err != nil {
		return "", err
	}
	return fmt.Sprintf("project %s added", a.ProjectID), nil
}

func stepServices(c *Ctx) (string, error) {
	a := c.args
	if c.host.GOOS() == "darwin" {
		if a.Linger {
			c.emit("    " + darwinLingerNote)
		}
		if !a.NoSystemd {
			return stepBootUnits(c)
		}
	}
	if a.NoSystemd {
		return servicesBackground(c)
	}
	if _, _, err := c.do([]string{"bash", filepath.Join(c.fh, "systemd", "install.sh"), "--with-serve"}, "systemd install", c.fh, 600*time.Second, true); err != nil {
		return "", err
	}
	if a.Linger {
		rc, _, _ := c.do([]string{"loginctl", "enable-linger"}, "loginctl enable-linger", "", 600*time.Second, false)
		if rc != 0 {
			c.emit(fmt.Sprintf("    warning: loginctl enable-linger was refused (rc=%d); the factory will not start at boot until you run it with the right to do so", rc))
		}
	}
	if _, err := c.doDefault([]string{"systemctl", "--user", "start", "herdr-server.service", "factory-tick.timer",
		"factory-serve.service", "factory-up.service"}, "systemctl start"); err != nil {
		return "", err
	}
	return "systemd user units installed and started (the tick timer keeps the factory running)", nil
}

func servicesBackground(c *Ctx) (string, error) {
	logdir := filepath.Join(c.home, ".cache", "hivemill-install")
	if c.plan {
		c.emit("    $ mkdir -p " + logdir)
		c.emit("    $ (background) herdr server")
	} else {
		if err := c.host.Mkdirs(logdir); err != nil {
			return "", fmt.Errorf("create %s: %w", logdir, err)
		}
		if err := c.host.Popen([]string{"herdr", "server"}, filepath.Join(logdir, "herdr-server.log")); err != nil {
			return "", err
		}
	}
	ready := false
	for i := 0; i < 20; i++ {
		if c.plan {
			c.emit("    $ herdr workspace list   (until exit 0, 20 s)")
			ready = true
			break
		}
		if rc, _ := c.read([]string{"herdr", "workspace", "list"}, 60*time.Second); rc == 0 {
			ready = true
			break
		}
		c.host.Sleep(time.Second)
	}
	if !ready {
		return "", stepf("`herdr workspace list` never exited 0 within 20 s (see %s/herdr-server.log)", logdir)
	}
	if c.plan {
		c.emit(fmt.Sprintf("    $ (background) %s serve --bind %s", c.factory, Loopback))
	} else if err := c.host.Popen([]string{c.factory, "serve", "--bind", Loopback}, filepath.Join(logdir, "factory-serve.log")); err != nil {
		return "", err
	}
	if _, err := c.doDefault([]string{c.factory, "up"}, "factory up"); err != nil {
		return "", err
	}
	c.emit("WARNING: no tick timer: agents will not be nudged and queued spawns will not start")
	return "herdr server and factory serve running in the background; factory up ran once", nil
}

// agentStatus maps NAME -> STATUS from the table `factory agents` prints.
func agentStatus(out string) map[string]string {
	rows := map[string]string{}
	lines := pySplitLines(out)
	if len(lines) == 0 {
		return rows
	}
	head := pyFields(lines[0])
	ni, si := indexOf(head, "NAME"), indexOf(head, "STATUS")
	if ni < 0 || si < 0 {
		return rows
	}
	for _, ln := range lines[1:] {
		f := pyFields(ln)
		if len(f) > max(ni, si) {
			rows[f[ni]] = f[si]
		}
	}
	return rows
}

func indexOf(s []string, v string) int {
	for i, e := range s {
		if e == v {
			return i
		}
	}
	return -1
}

func stepSmoke(c *Ctx) (string, error) {
	if c.plan {
		c.emit(fmt.Sprintf("    $ %s --version   (expects 'hivemill X.Y.Z')", c.factory))
		c.emit(fmt.Sprintf("    $ %s agents   (boss and director live, or queued)", c.factory))
		c.emit(fmt.Sprintf("    $ GET http://%s:%d/ and /goals   (expect 200)", Loopback, c.port))
		return "plan", nil
	}
	rc, out := c.read([]string{c.factory, "--version"}, 60*time.Second)
	m := versionRe.FindStringSubmatch(out)
	if rc != 0 || m == nil {
		return "", stepf("`factory --version` did not print 'hivemill X.Y.Z':\n%s", tailOf(out))
	}
	c.version = m[1]
	var states map[string]string
	okAgents := false
	for i := 0; i < 30; i++ {
		rc, out = c.read([]string{c.factory, "agents"}, 60*time.Second)
		states = map[string]string{}
		if rc == 0 {
			states = agentStatus(out)
		}
		if liveOrQueued(states["boss"]) && liveOrQueued(states["director"]) {
			okAgents = true
			break
		}
		c.host.Sleep(2 * time.Second)
	}
	if !okAgents {
		return "", stepf("boss/director are not live or queued after 60 s:\n%s", tailOf(out))
	}
	detail := fmt.Sprintf("boss %s, director %s", states["boss"], states["director"])
	for _, path := range []string{"/", "/goals"} {
		url := fmt.Sprintf("http://%s:%d%s", Loopback, c.port, path)
		code := 0
		for i := 0; i < 15; i++ {
			if code = c.host.HTTPStatus(url); code == 200 {
				break
			}
			c.host.Sleep(2 * time.Second)
		}
		if code != 200 {
			got := "nothing"
			if code != 0 {
				got = strconv.Itoa(code)
			}
			return "", stepf("GET %s answered %s, expected 200", url, got)
		}
	}
	return fmt.Sprintf("version %s; %s; UI answers 200", c.version, detail), nil
}

func liveOrQueued(s string) bool { return s == "live" || s == "queued" }

type step struct {
	name string
	fn   func(*Ctx) (string, error)
}

var steps = []step{
	{"prereqs", stepPrereqs}, {"tools", stepTools}, {"auth", stepAuth}, {"clone", stepClone},
	{"link", stepLink}, {"init", stepInit}, {"config", stepConfig}, {"project", stepProject},
	{"services", stepServices}, {"smoke", stepSmoke},
}

// finish prints the outcome of a failed run and returns its exit code.
func (c *Ctx) finish(name, failPrefix string, err error) int {
	var stop *Stop
	if errors.As(err, &stop) {
		c.emit(stop.Msg)
		return stop.Code
	}
	c.emit(fmt.Sprintf("[FAIL] %s: %s", name, err))
	c.emit("RESULT FAIL step=" + failPrefix + name)
	return 1
}

func runSteps(c *Ctx) int {
	name := "preflight"
	err := func() error {
		defer func() {
			if c.tmp != "" && !c.plan {
				c.host.Rmtree(c.tmp)
			}
		}()
		if err := preflight(c); err != nil {
			return err
		}
		name = "prereqs"
		if err := checkPrereqs(c); err != nil { // git must be known to exist before the first git call (ls-remote)
			return err
		}
		name = "resolve-ref"
		if !c.host.Exists(c.fh) {
			ref, err := resolveRef(c)
			if err != nil {
				return err
			}
			c.ref = ref
		}
		if !c.plan && !c.args.NoSystemd && c.host.GOOS() != "darwin" {
			if rc, _ := c.read([]string{"systemctl", "--user", "show-environment"}, 60*time.Second); rc != 0 {
				return stopf(3, "no user systemd session here. Either log in to a session that has one, or re-run with "+
					"--no-systemd (CI, containers: the factory then runs as plain background processes without a tick timer)")
			}
		}
		if c.plan {
			c.tmp = "<tmpdir>"
		} else {
			tmp, err := c.host.Mkdtemp()
			if err != nil {
				return fmt.Errorf("create a temp dir: %w", err)
			}
			c.tmp = tmp
		}
		for _, s := range steps {
			name = s.name
			c.n++
			mode := "run"
			if c.plan {
				mode = "plan"
			}
			c.emit(fmt.Sprintf("[%s] %d %s", mode, c.n, s.name))
			detail, err := s.fn(c)
			if err != nil {
				return err
			}
			if !c.plan {
				c.emit(fmt.Sprintf("[ok] %d %s: %s", c.n, s.name, detail))
			}
		}
		return nil
	}()
	if err != nil {
		return c.finish(name, "", err)
	}
	if c.plan {
		c.emit("plan only: nothing was changed")
		return 0
	}
	c.emit(fmt.Sprintf("RESULT PASS url=http://%s:%d/ version=%s", Loopback, c.port, c.version))
	return 0
}

// Main runs the installer with argv (without the program name) and returns the exit code.
func Main(argv []string, host Host, out, errw io.Writer) int {
	a, err := ParseArgs(argv)
	if err == nil && a.UninstallBootUnits {
		if host.GOOS() != "darwin" {
			_, _ = fmt.Fprintln(errw, "hivemill-install: --uninstall-boot-units is for macOS (launchd); on Linux run systemd/uninstall or systemctl --user disable")
			return 2
		}
		return runUninstallBootUnits(host, a, out)
	}
	if err != nil {
		var help helpRequested
		if errors.As(err, &help) {
			_, _ = fmt.Fprintln(out, helpText) // nothing to do if stdout is gone
			return 0
		}
		var stop *Stop
		if errors.As(err, &stop) {
			_, _ = fmt.Fprintln(errw, stop.Msg)
			return stop.Code
		}
		_, _ = fmt.Fprintln(errw, err)
		return 2
	}
	if a.HerdrApp {
		return runHerdrApp(host, a, out)
	}
	if a.Check {
		return runCheck(host, a, out)
	}
	c := NewCtx(host, a, out, a.DryRun)
	if a.Port == 0 {
		c.port = DefaultPort
	}
	return runSteps(c)
}
