package installer

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"factory/go/internal/install"
)

const hiAptHint = "sudo apt-get install -y git python3"

// ------------------------------------------------------------------------------------------------ TestFailureModes

func TestHivemillInstallMissingGitWithDefaultLatestRefExit3NotResolveFailure(t *testing.T) {
	h := hiNew(t, hiHome(t), hiTools()) // default --ref latest: ls-remote would run first without the prereq check
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 3, out)
	hiNotIn(t, out, "RESULT FAIL")
	hiIn(t, out, hiAptHint)
}

func TestHivemillInstallMissingGitWithExistingCloneExit3(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, true) // preflight would call `git remote get-url` on the clone: must not run without git
	h := hiNew(t, home, hiTools())
	rc, out, _ := hiRun(h, "--yes", "--resume")
	hiEq(t, rc, 3, out)
	hiIn(t, out, "apt-get install -y git python3")
	hiNotIn(t, out, "not a clone")
}

var hiTLSOnce sync.Once

// hiTLSServer starts a TLS server and makes the process trust it (SSL_CERT_FILE, read once by the first TLS use).
func hiTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	hiTLSOnce.Do(func() {
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
		dir, err := os.MkdirTemp("", "hi-tls-")
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(p, pemBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		os.Setenv("SSL_CERT_FILE", p)
		os.Setenv("SSL_CERT_DIR", dir)
	})
	// the certificate of every httptest server is the same one; check the trust really works for this process
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		t.Fatalf("system cert pool: %v", err)
	}
	if _, err := srv.Certificate().Verify(x509.VerifyOptions{Roots: pool, DNSName: "127.0.0.1"}); err != nil {
		t.Skipf("the process does not trust the test certificate (system pool loaded before SSL_CERT_FILE): %v", err)
	}
	return srv
}

func TestHivemillInstallRedirectLeavingHTTPSRefused(t *testing.T) {
	// No outside network: a local TLS server answers with a downgrade redirect and with an https redirect.
	var srv *httptest.Server
	srv = hiTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/downgrade":
			http.Redirect(w, r, "http://evil.example/install.sh", http.StatusFound)
		case "/ok":
			http.Redirect(w, r, srv.URL+"/final", http.StatusFound)
		case "/final":
			_, _ = w.Write([]byte("#!/bin/sh\necho hi\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	home := t.TempDir()
	host := &install.OSHost{HomeDir: home, Env: map[string]string{}}
	dest := filepath.Join(home, "x")
	err := host.Download(srv.URL+"/downgrade", dest)
	var sf *install.StepFail
	if !errors.As(err, &sf) {
		t.Fatalf("downgrade redirect: want a StepFail, got %v", err)
	}
	hiIn(t, err.Error(), "refusing redirect")
	if hiExists(dest) {
		t.Error("a refused redirect must not leave the destination file")
	}
	if err := host.Download(srv.URL+"/ok", dest); err != nil {
		t.Fatalf("https redirect: %v", err)
	}
	b, _ := os.ReadFile(dest)
	hiEq(t, string(b), "#!/bin/sh\necho hi\n", "downloaded body")
}

func TestHivemillInstallMissingGitRealHostExit3(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the installer refuses root before the prerequisite check")
	}
	// The real host (no fake answering git): an empty PATH makes git unrunnable.
	td := t.TempDir()
	rc, out, errb := hiBin(t, []string{"PATH=" + td, "HOME=" + td}, "--yes")
	hiEq(t, rc, 3, out+errb)
	hiIn(t, out+errb, hiAptHint)
}

func TestHivemillInstallFailedCheckoutLeavesNoCloneAndResumeIsANormalInstall(t *testing.T) {
	home := hiHome(t)
	fh := filepath.Join(home, "factory")
	h := hiNew(t, home, hiRules(hiRule1("fetch origin", 1, "fatal: couldn't find remote ref v9.9.9")))
	rc, out, _ := hiRun(h, "--yes", "--ref", "v9.9.9", "--no-systemd")
	hiEq(t, rc, 1, out)
	if !strings.HasPrefix(hiLast(out), "RESULT FAIL step=clone") {
		t.Errorf("last line %q", hiLast(out))
	}
	if hiExists(fh) {
		t.Error("a failed checkout must not leave ~/factory behind")
	}
	if hiExists(fh + ".installing") {
		t.Error("a failed checkout must not leave ~/factory.installing behind")
	}
	h2 := hiNew(t, home)
	rc, out, _ = hiRun(h2, "--yes", "--resume", "--ref", "v0.3.0", "--no-systemd")
	hiEq(t, rc, 0, out)
	if len(hiFind(h2.argvs(), hiSub("checkout --detach"))) == 0 {
		t.Error("resume must check the ref out")
	}
}

func TestHivemillInstallCloneIsRenamedIntoPlaceOnlyAfterCheckout(t *testing.T) {
	h := hiNew(t, hiHome(t))
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 0, out)
	hiEq(t, h.mutCount("rename"), 1, "renames")
	lastCheckout := -1
	for i, m := range h.muts {
		if m.kind == "run" && strings.Contains(strings.Join(m.argv, " "), "checkout") {
			lastCheckout = i
		}
	}
	rename := hiIndex(h.mutKinds(), "rename")
	if lastCheckout < 0 || rename < 0 || lastCheckout >= rename {
		t.Errorf("checkout at %d must precede the rename at %d", lastCheckout, rename)
	}
}

func TestHivemillInstallOSErrorDuringAWriteEndsWithResultFail(t *testing.T) {
	perm := &fs.PathError{Op: "mkdir", Path: "/ro/.local/bin", Err: syscall.EACCES}
	for _, c := range []struct {
		what, step string
		tools      []string
	}{{"mkdirs", "tools", []string{"git"}}, {"symlink", "link", []string{"git", "herdr", "gh", "claude"}}} {
		h := hiNew(t, hiHome(t), hiTools(c.tools...))
		if c.what == "mkdirs" {
			h.mkdirsErr = perm
		} else {
			h.symlinkErr = perm
		}
		rc, out, errb := hiRun(h, "--yes", "--no-systemd")
		hiEq(t, rc, 1, c.what+": "+out+errb)
		hiEq(t, hiLast(out), "RESULT FAIL step="+c.step, c.what)
	}
}

func TestHivemillInstallOSErrorMessageIsRedacted(t *testing.T) {
	h := hiNew(t, hiHome(t))
	h.symlinkErr = errors.New("disk error " + hiToken)
	rc, out, errb := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 1, "rc")
	hiNotIn(t, out+errb, hiToken)
}

func TestHivemillInstallUsageErrorsNeverEchoAValue(t *testing.T) {
	for _, argv := range [][]string{{"--check=" + hiToken}, {"--yes=" + hiToken}, {"--port", hiToken}, {"--bogus", hiToken}, {"--dry-run", "--ref"}} {
		rc, out, errb := hiBin(t, hiPlainEnv(t.TempDir()), argv...)
		hiEq(t, rc, 2, strings.Join(argv, " ")+": "+errb)
		hiNotIn(t, out+errb, "abcdefgh")
	}
}

// ------------------------------------------------------------------------------------------------ TestPrereqsAndAuth

func TestHivemillInstallMissingGitExit3PrintsHintAndChangesNothing(t *testing.T) {
	h := hiNew(t, hiHome(t), hiTools())
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 3, out)
	hiIn(t, out, hiAptHint)
	for _, m := range h.muts {
		if m.kind != "rmtree" {
			t.Errorf("unexpected mutation %+v", m)
		}
	}
}

func TestHivemillInstallOldPythonExit3(t *testing.T) {
	h := hiNew(t, hiHome(t), hiRules(hiRule1("python3 --version", 0, "Python 3.9.2")))
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 3, "rc")
	hiIn(t, out, "python3 3.11 or newer")
}

func TestHivemillInstallGHNotLoggedInExit3BeforeClone(t *testing.T) {
	home := hiHome(t)
	h := hiNew(t, home)
	h.ghAuth = false
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 3, out)
	hiIn(t, out, "Run this in your own terminal: gh auth login")
	if len(hiFind(h.argvs(), hiSub("clone"))) > 0 {
		t.Error("clone ran before the login check")
	}
	if hiExists(filepath.Join(home, "factory")) {
		t.Error("~/factory exists")
	}
}

func TestHivemillInstallClaudeNotLoggedInExit3(t *testing.T) {
	home := hiHome(t)
	if err := os.Remove(filepath.Join(home, ".claude", ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	rc, out, _ := hiRun(hiNew(t, home), "--yes")
	hiEq(t, rc, 3, "rc")
	hiIn(t, out, "claude")
}

func TestHivemillInstallNoSystemdSessionExit3ExplainsBothOptions(t *testing.T) {
	home := hiHome(t)
	h := hiNew(t, home)
	h.systemd = false
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 3, "rc")
	hiIn(t, out, "--no-systemd")
	if hiExists(filepath.Join(home, "factory")) {
		t.Error("~/factory exists")
	}
}

// ------------------------------------------------------------------------------------------------ TestLatestTag

func TestHivemillInstallHighestVXYZWins(t *testing.T) {
	_, out, _ := hiRun(hiNew(t, hiHome(t)), "--dry-run")
	hiIn(t, out, "refs/tags/v0.10.0")
	hiNotIn(t, out, "refs/tags/v0.9.0")
	hiNotIn(t, out, "refs/tags/v1.0.0-rc1")
	peeled := "aa\trefs/tags/v0.2.0\nbb\trefs/tags/v0.2.0^{}\ncc\trefs/tags/v0.11.1\ndd\trefs/tags/v0.11.1^{}\n"
	h := hiNew(t, hiHome(t))
	h.tags = peeled
	_, out, _ = hiRun(h, "--dry-run")
	hiIn(t, out, "checkout --detach refs/tags/v0.11.1")
	hiNotIn(t, out, "v0.2.0")
}

func TestHivemillInstallNoMatchingTagIsNone(t *testing.T) {
	for _, tags := range []string{"aa\trefs/tags/v1.0.0-rc1\nbb\trefs/tags/release-3\n", ""} {
		h := hiNew(t, hiHome(t))
		h.tags = tags
		rc, out, _ := hiRun(h, "--yes", "--no-systemd")
		hiEq(t, rc, 1, out)
		hiIn(t, out, "no release tag")
		hiEq(t, len(hiFind(h.argvs(), hiSub("checkout"))), 0, "checkouts")
	}
}

func TestHivemillInstallNoMatchingTagExit1(t *testing.T) {
	home := hiHome(t)
	h := hiNew(t, home)
	h.tags = "aa\trefs/tags/v1.0.0-rc1\n"
	rc, out, _ := hiRun(h, "--yes", "--no-systemd")
	hiEq(t, rc, 1, out)
	hiIn(t, out, "no release tag")
	if hiExists(filepath.Join(home, "factory")) {
		t.Error("~/factory exists")
	}
}

func TestHivemillInstallCheckoutUsesTheResolvedTag(t *testing.T) {
	home := hiHome(t)
	h := hiNew(t, home)
	hiRun(h, "--yes", "--no-systemd")
	want := "git -C " + filepath.Join(home, "factory") + ".installing checkout --detach refs/tags/v0.10.0"
	if hiIndex(h.argvs(), want) < 0 {
		t.Errorf("want %q in %v", want, h.argvs())
	}
}

func TestHivemillInstallExplicitRefIsFetchedThenDetached(t *testing.T) {
	home := hiHome(t)
	part := filepath.Join(home, "factory") + ".installing"
	h := hiNew(t, home)
	rc, out, _ := hiRun(h, "--yes", "--no-systemd", "--ref", "main")
	hiEq(t, rc, 0, out)
	argvs := h.argvs()
	hiEq(t, len(hiFind(argvs, hiSub("ls-remote"))), 0, "ls-remote calls")
	if hiIndex(argvs, "git -C "+part+" fetch origin -- main") < 0 {
		t.Errorf("no fetch of main in %v", argvs)
	}
	if hiIndex(argvs, "git -C "+part+" checkout --detach FETCH_HEAD") < 0 {
		t.Errorf("no detach of FETCH_HEAD in %v", argvs)
	}
}
