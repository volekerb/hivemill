package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/install"
)

const hiClaudeInstallerURL = "https://claude.ai/install.sh"

// hiUnits puts the hivemill unit files the services step copies into the clone at dir.
func hiUnits(t *testing.T, dir string) {
	t.Helper()
	sd := filepath.Join(dir, "systemd")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"hivemill.service", "hivemill-obs.service", "hivemill-digest.service", "hivemill-digest.timer",
		"hivemill-health.service", "hivemill-health.timer"} {
		if err := os.WriteFile(filepath.Join(sd, u), []byte("# "+u+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// hiCloneWithUnits makes the fake `git clone` leave a clone that holds the unit files.
func hiCloneWithUnits(t *testing.T) hiRule {
	return hiRule{"git clone", func(argv []string) (int, string) {
		hiUnits(t, argv[len(argv)-1])
		return 0, ""
	}}
}

// hiNoOldChain fails for any call of a factory verb the installer no longer runs.
func hiNoOldChain(t *testing.T, argvs []string) {
	t.Helper()
	re := regexp.MustCompile(`/factory (init|config|project|up|agents)\b`)
	if bad := hiFind(argvs, re.MatchString); len(bad) > 0 {
		t.Errorf("old-chain calls: %v", bad)
	}
}

// ------------------------------------------------------------------------------------------------ TestFullRun

func TestHivemillInstallSystemdRunResultLineAndOrder(t *testing.T) {
	home := hiHome(t)
	fh := filepath.Join(home, "factory")
	h := hiNew(t, home, hiRules(hiCloneWithUnits(t)))
	rc, out, _ := hiRun(h, "--yes", "--port", "9000", "--max-agents", "3", "--no-scout",
		"--project-id", "demo", "--project-repo", "/r", "--project-gh", "o/r")
	hiEq(t, rc, 0, out)
	if !hiResultPass.MatchString(hiLast(out)) {
		t.Errorf("last line %q", hiLast(out))
	}
	hiEq(t, hiLast(out), "RESULT PASS url=http://127.0.0.1:9000/ version=0.3.0", "result line")
	a := h.argvs()
	want := []string{
		"git clone -- " + hiRepo + " " + fh + ".installing",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now hivemill.service hivemill-obs.service hivemill-digest.timer hivemill-health.timer",
	}
	prev := -1
	for _, w := range want {
		i := hiIndex(a, w)
		if i < 0 {
			t.Errorf("missing call %q in %v", w, a)
			continue
		}
		if i < prev {
			t.Errorf("call %q out of order (%d < %d)", w, i, prev)
		}
		prev = i
	}
	hiNoOldChain(t, a)
	if !hiExists(filepath.Join(home, ".config", "systemd", "user", "hivemill.service")) {
		t.Error("hivemill.service not installed")
	}
	for _, name := range []string{"hivemill", "worker-guard"} {
		got, err := os.Readlink(filepath.Join(home, ".local", "bin", name))
		if err != nil || got != filepath.Join(fh, "bin", name) {
			t.Errorf("%s link = %q, %v", name, got, err)
		}
	}
	if len(hiFind(a, hiSub("loginctl"))) > 0 {
		t.Error("linger only with --linger")
	}
	hiEq(t, len(h.popens), 0, "popens")
	if bad := hiFind(a, func(s string) bool {
		for _, w := range strings.Fields(s) {
			if w == "sudo" {
				return true
			}
		}
		return false
	}); len(bad) > 0 {
		t.Errorf("sudo used: %v", bad)
	}
}

// The name predates the removal of the background starts; it is kept so that no test function disappears.
func TestHivemillInstallNoSystemdRunStartsBackgroundAndWarns(t *testing.T) {
	home := hiHome(t)
	h := hiNew(t, home)
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	if !hiResultPass.MatchString(hiLast(out)) {
		t.Errorf("last line %q", hiLast(out))
	}
	hiEq(t, len(h.popens), 0, "background starts")
	hiIn(t, out, "`hivemill daemon`")
	a := h.argvs()
	if hiIndex(a, filepath.Join(home, ".local", "bin", "factory")+" up") >= 0 {
		t.Errorf("factory up ran: %v", a)
	}
	if bad := hiFind(a, func(s string) bool {
		return strings.Contains(s, "systemctl") && !strings.Contains(s, "show-environment")
	}); len(bad) > 0 {
		t.Errorf("systemctl used: %v", bad)
	}
}

func TestHivemillInstallLingerRefusalIsAWarning(t *testing.T) {
	h := hiNew(t, hiHome(t), hiRules(hiCloneWithUnits(t), hiRule1("loginctl enable-linger", 1, "Access denied")))
	rc, out, _ := hiRun(h, "--yes", "--linger")
	hiEq(t, rc, 0, out)
	hiIn(t, out, "enable-linger was refused")
	if !hiResultPass.MatchString(hiLast(out)) {
		t.Errorf("last line %q", hiLast(out))
	}
}

func TestHivemillInstallListedProjectIsSkipped(t *testing.T) {
	home := hiHome(t)
	h := hiNew(t, home, hiRules(hiRule1("project list", 0, "ID NAME\ndemo Demo\n")))
	hiMakeInstall(t, home, false) // partial clone: --resume
	hiUnits(t, filepath.Join(home, "factory"))
	rc, out, _ := hiRun(h, "--yes", "--resume", "--project-id", "demo", "--project-repo", "/r")
	hiEq(t, rc, 0, out)
	hiNoOldChain(t, h.argvs())
}

// The next three names predate the removal of the agent and UI checks; they are kept so that no test function
// disappears.
func TestHivemillInstallQueuedAgentsCountAsStarted(t *testing.T) {
	h := hiNew(t, hiHome(t), hiRules(hiRule1(" agents", 0, "NAME ORG ROLE STATUS\nboss claude boss queued\ndirector claude director live\n")))
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	hiNoOldChain(t, h.argvs())
}

func TestHivemillInstallAgentsNeverLiveFailsSmoke(t *testing.T) {
	h := hiNew(t, hiHome(t), hiRules(hiRule1(" agents", 0, "NAME ORG ROLE STATUS\n")))
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	hiNoOldChain(t, h.argvs())
	hiNotIn(t, out, "factory agents")
}

func TestHivemillInstallUINotAnsweringFailsSmoke(t *testing.T) {
	h := hiNew(t, hiHome(t))
	asked := 0
	h.httpFn = func(string) int { asked++; return 0 }
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	hiEq(t, asked, 0, "UI requests")
}

func TestHivemillInstallFailingStepResultFailAndExit1(t *testing.T) {
	h := hiNew(t, hiHome(t), hiRules(hiRule1("git clone", 128, "fatal: repository not found")))
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 1, "rc")
	hiEq(t, hiLast(out), "RESULT FAIL step=clone", "last line")
	hiNotIn(t, out, "RESULT PASS")
}

// The name predates the removal of the init step; it is kept so that no test function disappears.
func TestHivemillInstallInitWithoutInitialisedLineFails(t *testing.T) {
	h := hiNew(t, hiHome(t), hiRules(hiRule1("factory init", 0, "something else")))
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	hiNoOldChain(t, h.argvs())
}

// ------------------------------------------------------------------------------------------------ TestNoToken

func TestHivemillInstallTokenInFailingStepOutputNeverReachesStdoutOrStderr(t *testing.T) {
	leak := fmt.Sprintf("fatal: unable to access 'https://github.com/o/r.git/' with %s: denied\nGH_TOKEN=%s\n", hiToken, hiToken)
	h := hiNew(t, hiHome(t), hiEnv(map[string]string{"GH_TOKEN": hiToken, "PATH": "/usr/bin"}), hiRules(hiRule1("git clone", 128, leak)))
	rc, out, errb := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 1, "rc")
	hiNotIn(t, out, hiToken)
	hiNotIn(t, errb, hiToken)
	hiIn(t, out, "RESULT FAIL step=clone")
}

func TestHivemillInstallEnvTokenWithUnknownShapeIsStillMasked(t *testing.T) {
	odd := "not-a-known-shape-0123456789"
	h := hiNew(t, hiHome(t), hiEnv(map[string]string{"GITHUB_TOKEN": odd, "PATH": "/usr/bin"}), hiRules(hiRule1("git clone", 1, "boom "+odd)))
	_, out, errb := hiRun(h, "--yes", "--no-systemd")
	hiNotIn(t, out+errb, odd)
}

func TestHivemillInstallRedactPatterns(t *testing.T) {
	// Python called redact() directly; here each secret rides in the output of a failing step and the installer
	// must print it masked (the step tail goes through redact).
	for _, s := range []string{hiToken, "github_pat_" + strings.Repeat("A1", 12), "sk-ant-" + strings.Repeat("x", 20), "Authorization: Bearer abc"} {
		h := hiNew(t, hiHome(t), hiRules(hiRule1("git clone", 1, "x "+s+" y")))
		_, out, errb := hiRun(h, "--yes", "--no-systemd")
		hiIn(t, out, "[REDACTED]")
		hiNotIn(t, out+errb, s)
	}
}

// ------------------------------------------------------------------------------------------------ TestDownloadGuard

// hiSwapAssets replaces a pin table for one test and restores it afterwards.
func hiSwapAssets(t *testing.T, table *map[string]install.Asset, with map[string]install.Asset) {
	t.Helper()
	old := *table
	*table = with
	t.Cleanup(func() { *table = old })
}

func hiFakeAssets(name string, payload []byte, sha string, size int64) map[string]install.Asset {
	if sha == "" {
		sha = hiSHA(payload)
	}
	if size < 0 {
		size = int64(len(payload))
	}
	return map[string]install.Asset{"x86_64": {URL: "https://example.invalid/" + name, Size: size, SHA256: sha}}
}

func hiHerdrCase(t *testing.T, sha string, size int64) (*hiHost, string, int, string) {
	t.Helper()
	home := hiHome(t)
	payload := []byte("\x7fELF fake herdr")
	table := hiFakeAssets("herdr", payload, sha, size)
	h := hiNew(t, home, hiTools("git", "gh", "claude", "go"))
	h.assets[table["x86_64"].URL] = payload
	hiSwapAssets(t, &install.HerdrAssets, table)
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	return h, home, rc, out
}

func TestHivemillInstallWrongSHA256AbortsAndInstallsNothing(t *testing.T) {
	h, home, rc, out := hiHerdrCase(t, strings.Repeat("0", 64), -1)
	hiEq(t, rc, 1, out)
	hiIn(t, out, "sha256 mismatch")
	hiEq(t, hiLast(out), "RESULT FAIL step=tools", "last line")
	if hiExists(filepath.Join(home, ".local", "bin", "herdr")) {
		t.Error("herdr installed")
	}
	hiEq(t, h.mutCount("install_file"), 0, "install_file calls")
	if hiExists(filepath.Join(home, "factory")) {
		t.Error("~/factory exists")
	}
}

func TestHivemillInstallWrongSizeAbortsAndInstallsNothing(t *testing.T) {
	_, home, rc, out := hiHerdrCase(t, "", 3)
	hiEq(t, rc, 1, out)
	hiIn(t, out, "size mismatch")
	if hiExists(filepath.Join(home, ".local", "bin", "herdr")) {
		t.Error("herdr installed")
	}
}

func TestHivemillInstallCorrectSHA256InstallsExecutable(t *testing.T) {
	_, home, rc, out := hiHerdrCase(t, "", -1)
	hiEq(t, rc, 0, out)
	dest := filepath.Join(home, ".local", "bin", "herdr")
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("herdr mode %v is not executable", st.Mode())
	}
	b, _ := os.ReadFile(dest)
	hiEq(t, string(b), "\x7fELF fake herdr", "content")
}

// hiGHMember is the tarball member the installer extracts: gh_<version>_linux_amd64/bin/gh, with the version read
// from the pinned URL.
func hiGHMember(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`/download/v([0-9.]+)/gh_([0-9.]+)_linux_amd64\.tar\.gz$`).FindStringSubmatch(install.GHAssets["x86_64"].URL)
	if m == nil || m[1] != m[2] {
		t.Fatalf("unexpected gh pin %q", install.GHAssets["x86_64"].URL)
	}
	return fmt.Sprintf("gh_%s_linux_amd64/bin/gh", m[1])
}

type hiTarEntry struct {
	name, link string
	body       string
	typ        byte
}

func hiTarball(t *testing.T, entries ...hiTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o755, Linkname: e.link}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestHivemillInstallGHTarballVerifiedAndMemberExtracted(t *testing.T) {
	home := hiHome(t)
	ghBin := filepath.Join(home, ".local", "bin", "gh")
	payload := hiTarball(t, hiTarEntry{name: hiGHMember(t), body: "#!/bin/sh\n"})
	table := hiFakeAssets("gh.tar.gz", payload, "", -1)
	h := hiNew(t, home, hiTools("git", "herdr", "claude", "go"))
	h.assets[table["x86_64"].URL] = payload
	hiSwapAssets(t, &install.GHAssets, table)
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	b, err := os.ReadFile(ghBin)
	if err != nil {
		t.Fatal(err)
	}
	hiEq(t, string(b), "#!/bin/sh\n", "gh content")

	bad := hiFakeAssets("gh.tar.gz", payload, strings.Repeat("1", 64), -1)
	h2 := hiNew(t, home, hiTools("git", "herdr", "claude", "go"))
	h2.assets[bad["x86_64"].URL] = payload
	if err := os.Remove(ghBin); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "factory")); err != nil {
		t.Fatal(err)
	}
	hiSwapAssets(t, &install.GHAssets, bad)
	rc, out, _ = hiRun(h2, "--yes", "--no-systemd")
	hiEq(t, rc, 1, out)
	if hiExists(ghBin) {
		t.Error("gh installed from a tarball with the wrong sha256")
	}
}

// Extra (the Go code extracts files): a tar member that is a symlink, dangling or looping, is refused, never followed.
func TestHivemillInstallTarballSymlinkMemberDanglingAndLoopRefused(t *testing.T) {
	member := hiGHMember(t)
	for name, e := range map[string]hiTarEntry{
		"dangling": {name: member, typ: tar.TypeSymlink, link: "/nonexistent/hi-target"},
		"loop":     {name: member, typ: tar.TypeSymlink, link: filepath.Base(member)},
		"hardlink": {name: member, typ: tar.TypeLink, link: "gh_x/other"},
		"dir":      {name: member, typ: tar.TypeDir},
	} {
		td := t.TempDir()
		tarball := filepath.Join(td, "gh.tar.gz")
		if err := os.WriteFile(tarball, hiTarball(t, e), 0o644); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(td, "gh")
		host := &install.OSHost{HomeDir: td, Env: map[string]string{}}
		err := host.ExtractMember(tarball, member, dest)
		if err == nil {
			t.Errorf("%s: a non-regular member was extracted", name)
		} else {
			hiIn(t, err.Error(), "not a regular file")
		}
		if hiExists(dest) {
			t.Errorf("%s: destination was created", name)
		}
	}
	// a member that is missing altogether
	td := t.TempDir()
	tarball := filepath.Join(td, "gh.tar.gz")
	if err := os.WriteFile(tarball, hiTarball(t, hiTarEntry{name: "other/file", body: "x"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&install.OSHost{HomeDir: td}).ExtractMember(tarball, member, filepath.Join(td, "gh")); err == nil {
		t.Error("missing member was not reported")
	}
}

// Extra: an install destination that is a dangling or looping symlink is replaced, never written through.
func TestHivemillInstallInstallFileOverDanglingAndLoopSymlinks(t *testing.T) {
	td := t.TempDir()
	src := filepath.Join(td, "src")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(td, "outside", "victim")
	dangling := filepath.Join(td, "dangling")
	loopA, loopB := filepath.Join(td, "loopA"), filepath.Join(td, "loopB")
	for link, target := range map[string]string{dangling: outside, loopA: loopB, loopB: loopA} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	host := &install.OSHost{HomeDir: td, Env: map[string]string{}}
	for _, dest := range []string{dangling, loopA} {
		if err := host.InstallFile(src, dest, 0o755); err != nil {
			t.Fatalf("%s: %v", dest, err)
		}
		st, err := os.Lstat(dest)
		if err != nil || !st.Mode().IsRegular() {
			t.Errorf("%s: want a regular file after install, got %v %v", dest, st, err)
		}
	}
	if hiExists(filepath.Dir(outside)) {
		t.Error("the install wrote through the dangling symlink")
	}
}

// Extra: a binary link refuses a dangling or looping symlink that points elsewhere, and keeps one that is right.
func TestHivemillInstallSymlinkDanglingAndLoopFactoryLink(t *testing.T) {
	td := t.TempDir()
	host := &install.OSHost{HomeDir: td, Env: map[string]string{}}
	target := filepath.Join(td, "factory", "bin", "hivemill")
	dangling := filepath.Join(td, "dangling")
	if err := os.Symlink("/nonexistent/elsewhere", dangling); err != nil {
		t.Fatal(err)
	}
	if err := host.Symlink(target, dangling); err == nil {
		t.Error("a link that points elsewhere was replaced")
	}
	loop := filepath.Join(td, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	if err := host.Symlink(target, loop); err == nil {
		t.Error("a self-referencing link was replaced")
	}
	same := filepath.Join(td, "same") // dangling, but already the right target
	if err := os.Symlink(target, same); err != nil {
		t.Fatal(err)
	}
	if err := host.Symlink(target, same); err != nil {
		t.Errorf("a link that already points right must be kept: %v", err)
	}
	reg := filepath.Join(td, "reg")
	if err := os.WriteFile(reg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := host.Symlink(target, reg); err == nil {
		t.Error("a regular file was replaced")
	}
}

func hiClaudeCase(t *testing.T, mkdtemp func() (string, error)) (*hiHost, int, string) {
	t.Helper()
	payload := []byte("#!/bin/sh\necho hi\n")
	h := hiNew(t, hiHome(t), hiTools("git", "herdr", "gh", "go"))
	h.assets[hiClaudeInstallerURL] = payload
	h.mkdtempFn = mkdtemp
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	hiIn(t, out, hiSHA(payload))
	return h, rc, out
}

func hiNoPipeNoCurl(t *testing.T, h *hiHost) {
	t.Helper()
	// the command word, not the whole line: the random tmpdir in the path may itself contain "curl"
	if bad := hiFind(h.argvs(), func(a string) bool { return strings.Contains(a, "|") || strings.Fields(a)[0] == "curl" }); len(bad) > 0 {
		t.Errorf("piped or curl: %v", bad)
	}
}

func TestHivemillInstallClaudeInstallerIsAFileWithLoggedSHANeverPiped(t *testing.T) {
	h, _, _ := hiClaudeCase(t, nil)
	run := hiFind(h.argvs(), func(a string) bool { return strings.HasPrefix(a, "bash ") && strings.Contains(a, "claude-install.sh") })
	hiEq(t, len(run), 1, fmt.Sprint("installer runs: ", run))
	hiNoPipeNoCurl(t, h)
}

func TestHivemillInstallClaudeInstallerTestIndependentOfTmpdirName(t *testing.T) {
	h, _, _ := hiClaudeCase(t, func() (string, error) { return os.MkdirTemp(t.TempDir(), "curl-") }) // worst case name: holds the string curl
	run := hiFind(h.argvs(), func(a string) bool { return strings.HasPrefix(a, "bash ") && strings.Contains(a, "claude-install.sh") })
	hiEq(t, len(run), 1, fmt.Sprint("installer runs: ", run))
	if len(run) == 1 {
		hiIn(t, run[0], "curl-")
	}
	hiNoPipeNoCurl(t, h)
}

func TestHivemillInstallNonHTTPSDownloadRefused(t *testing.T) {
	home := t.TempDir()
	err := (&install.OSHost{HomeDir: home, Env: map[string]string{}}).Download("http://example.invalid/x", filepath.Join(home, "x"))
	if _, ok := err.(*install.StepFail); !ok {
		t.Fatalf("want a StepFail, got %T %v", err, err)
	}
	if hiExists(filepath.Join(home, "x")) {
		t.Error("a refused download created a file")
	}
}

func TestHivemillInstallUnsupportedArchFailsCleanly(t *testing.T) {
	h := hiNew(t, hiHome(t), hiTools("git", "gh", "claude", "go"))
	h.machine = "riscv64"
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 1, "rc")
	hiEq(t, hiLast(out), "RESULT FAIL step=tools", "last line")
}

// ------------------------------------------------------------------------------------------------ TestPins

func TestHivemillInstallHerdrPinEqualsFreshboxAndSetupDoc(t *testing.T) {
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	fbBytes, err := os.ReadFile(filepath.Join(root, "ci", "freshbox_test.py"))
	if err != nil {
		t.Fatal(err)
	}
	fb := string(fbBytes)
	ver := regexp.MustCompile(`(?m)^HERDR_VERSION = "([^"]+)"$`).FindStringSubmatch(fb)
	if ver == nil {
		t.Fatal("no HERDR_VERSION in ci/freshbox_test.py")
	}
	block := regexp.MustCompile(`(?s)HERDR_ASSETS = \{.*?\n\}`).FindString(fb)
	entry := regexp.MustCompile(`"([a-z0-9_]+)": \(HERDR_RELEASE \+ "([^"]+)", ([0-9]+),\s*"([0-9a-f]{64})"\)`)
	rel := "https://github.com/herdrdev/herdr/releases/download/" + ver[1]
	fbAssets := map[string]install.Asset{}
	for _, m := range entry.FindAllStringSubmatch(block, -1) {
		size, _ := strconv.ParseInt(m[3], 10, 64)
		fbAssets[m[1]] = install.Asset{URL: rel + m[2], Size: size, SHA256: m[4]}
	}
	if len(fbAssets) != 2 {
		t.Fatalf("parsed %d freshbox herdr assets from %q", len(fbAssets), block)
	}
	for _, arch := range []string{"x86_64", "aarch64"} { // the Linux entries; the darwin one has no freshbox counterpart
		if install.HerdrAssets[arch] != fbAssets[arch] {
			t.Errorf("%s: installer pin %+v != freshbox %+v", arch, install.HerdrAssets[arch], fbAssets[arch])
		}
	}
	docBytes, err := os.ReadFile(filepath.Join(root, "docs", "SETUP.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(docBytes)
	hiIn(t, doc, install.HerdrAssets["x86_64"].SHA256)
	hiIn(t, doc, install.HerdrAssets["aarch64"].SHA256)
	hiIn(t, doc, "/"+ver[1]+"/")
	for arch, as := range install.HerdrAssets {
		if !strings.HasPrefix(as.URL, "https://github.com/herdrdev/herdr/releases/download/"+ver[1]+"/") {
			t.Errorf("%s: URL %s is not under the pinned release %s", arch, as.URL, ver[1])
		}
	}
}

func TestHivemillInstallGHPinShape(t *testing.T) {
	re := regexp.MustCompile(`^https://github\.com/cli/cli/releases/download/v([0-9.]+)/gh_([0-9.]+)_`)
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if len(install.GHAssets) == 0 {
		t.Fatal("no gh pins")
	}
	for arch, as := range install.GHAssets {
		m := re.FindStringSubmatch(as.URL)
		if m == nil || m[1] != m[2] {
			t.Errorf("%s: url %s", arch, as.URL)
		}
		if !sha.MatchString(as.SHA256) {
			t.Errorf("%s: sha %q", arch, as.SHA256)
		}
		if as.Size <= 1000000 {
			t.Errorf("%s: size %d", arch, as.Size)
		}
	}
}

// ------------------------------------------------------------------------------------------------ TestSourceRules

// hiInstallerSources is the non-test Go source of the installer: go/internal/install and go/cmd/hivemill-install.
func hiInstallerSources(t *testing.T) map[string]string {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, dir := range []string{filepath.Join(root, "go", "internal", "install"), filepath.Join(root, "go", "cmd", "hivemill-install")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no sources in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			out[f] = string(b)
		}
	}
	return out
}

func TestHivemillInstallStdlibOnlyAndSelfContained(t *testing.T) {
	srcs := hiInstallerSources(t)
	var files []string
	for f := range srcs {
		files = append(files, f)
	}
	sort.Strings(files)
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, srcs[f], parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first := strings.SplitN(p, "/", 2)[0]
			// stdlib has no dot in its first path element; the one allowed module package is the installer itself
			if strings.Contains(first, ".") || (strings.HasPrefix(p, "factory/") && p != "factory/go/internal/install") {
				t.Errorf("%s imports %s: the installer must be stdlib only and self-contained", filepath.Base(f), p)
			}
		}
		if re := regexp.MustCompile(`ci/|bin/factory_cli`); re.MatchString(srcs[f]) {
			t.Errorf("%s mentions %q", filepath.Base(f), re.FindString(srcs[f]))
		}
	}
}

func TestHivemillInstallNoWideBindPromptsOrElevation(t *testing.T) {
	wide := "0.0.0." + "0"
	for f, src := range hiInstallerSources(t) {
		base := filepath.Base(f)
		hiNotIn(t, src, wide)
		var sudo []string
		for _, ln := range strings.Split(src, "\n") {
			if regexp.MustCompile(`\bsudo\b`).MatchString(ln) {
				sudo = append(sudo, strings.Join(strings.Fields(ln), " "))
			}
		}
		for _, ln := range sudo {
			if !strings.HasSuffix(ln, `= "sudo apt-get install -y git python3"`) {
				t.Errorf("%s: unexpected sudo line %q", base, ln)
			}
		}
		if re := regexp.MustCompile(`getpass|ReadPassword|bufio\.NewScanner\(os`); re.MatchString(src) {
			t.Errorf("%s: reads input (%q)", base, re.FindString(src))
		}
		hiCheckStdinUse(t, base, src)
		if re := regexp.MustCompile(`--(gh-)?token`); re.MatchString(src) {
			t.Errorf("%s: mentions a token option (%q)", base, re.FindString(src))
		}
	}
}

// hiCheckStdinUse enforces the stdin rule: os.Stdin appears only in host.go, and there only inside IsTerminal, WaitEnter
// and RunTTY; WaitEnter and RunTTY must check the character-device mode of stdin before using it.
func hiCheckStdinUse(t *testing.T, base, src string) {
	t.Helper()
	if !strings.Contains(src, "os.Stdin") {
		return
	}
	if base != "host.go" {
		t.Errorf("%s: os.Stdin outside host.go", base)
		return
	}
	af, err := parser.ParseFile(token.NewFileSet(), base, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"IsTerminal": true, "WaitEnter": true, "RunTTY": true}
	seen := map[string]bool{}
	for _, d := range af.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		body := src[fd.Body.Pos()-1 : fd.Body.End()-1]
		if !strings.Contains(body, "os.Stdin") {
			continue
		}
		name := fd.Name.Name
		if !allowed[name] {
			t.Errorf("host.go: os.Stdin inside %s", name)
			continue
		}
		seen[name] = true
		if name != "IsTerminal" && !regexp.MustCompile(`!isCharDevice\(os\.Stdin\)`).MatchString(body) {
			t.Errorf("host.go: %s uses os.Stdin without checking that it is a character device", name)
		}
	}
	for _, n := range []string{"WaitEnter", "RunTTY"} {
		if !seen[n] {
			t.Errorf("host.go: %s no longer checks os.Stdin", n)
		}
	}
}

// A run with logins missing and stdin an open pipe that nobody writes to or closes must stop at once with exit 3.
func TestHivemillInstallNoTTYOpenStdinDoesNotBlock(t *testing.T) {
	home := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	// stub tools first on PATH, so that the tools step finds them and downloads nothing
	stubDir := t.TempDir()
	for name, body := range map[string]string{
		"claude": "#!/bin/sh\necho '1.0.0 (stub claude)'\n",
		"go":     "#!/bin/sh\necho 'go version go1.25.0 linux/amd64'\n",
		"herdr":  "#!/bin/sh\necho 'herdr 1.0.0 (stub)'\n",
		"gh":     "#!/bin/sh\n[ \"$1 $2\" = 'auth status' ] && exit 1\necho 'gh version 1.0.0 (stub)'\n",
	} {
		if err := os.WriteFile(filepath.Join(stubDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command(buildCmd(t, "hivemill-install"), "--yes", "--no-systemd")
	c.Env = []string{"HOME=" + home, "PATH=" + stubDir + ":/usr/bin:/bin"}
	c.Stdin = r
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		rc := 0
		var ee *exec.ExitError
		if err != nil {
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			rc = ee.ExitCode()
		}
		all := out.String() + errb.String()
		if rc != 3 || !strings.Contains(all, "not logged in. Do this, then re-run the installer with --resume:") {
			t.Errorf("rc=%d\n%s", rc, all)
		}
		hiNotIn(t, all, "claude installer")
	case <-time.After(30 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("the installer blocked with an open stdin pipe")
	}
}
