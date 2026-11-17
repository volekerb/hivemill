package skilleval1

// Port of tests/test_skilleval_nsbox.py, group A (no namespaces): the CONNECT proxy, its allow-lists and lifetime,
// the token rules, the composed stage argv and the clean-skip rule.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/sandbox"
	"factory/go/internal/skilleval"
)

// nsbUpstream is the proxy fixture: an echo server on a unix socket standing in for api.anthropic.com.
func nsbUpstream(t *testing.T) string {
	t.Helper()
	path := filepath.Join(nsbShort(t), "up.sock")
	nsbEcho(t, "unix", path)
	return path
}

// ---- ProxyTests ----

func nsbProxy(t *testing.T) string {
	up := nsbUpstream(t)
	return nsbServe(t, nil, sandbox.UpstreamResolver(map[string]string{"api.anthropic.com:443": up}))
}

func TestSkillevalNsbox_AllowedConnectRelaysBothWays(t *testing.T) {
	sock := nsbProxy(t)
	c := nsbConnect(t, sock, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
	if got := nsbFirstLine(nsbRecv(t, c, 4096)); got != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("status = %q", got)
	}
	for _, msg := range []string{"hello", "again"} {
		if _, err := c.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		if got := nsbReadN(t, c, len("UP:"+msg)); got != "UP:"+msg {
			t.Errorf("reply = %q, want %q", got, "UP:"+msg)
		}
	}
}

func TestSkillevalNsbox_HostIsCaseInsensitive(t *testing.T) {
	sock := nsbProxy(t)
	if got := nsbStatus(t, sock, "CONNECT API.Anthropic.COM:443 HTTP/1.1\r\n\r\n"); !strings.Contains(got+" ", " 200 ") {
		t.Errorf("status = %q, want a 200", got)
	}
}

func TestSkillevalNsbox_DeniedTargetsGet403(t *testing.T) {
	sock := nsbProxy(t)
	for _, req := range []string{
		"CONNECT example.com:443 HTTP/1.1\r\n\r\n",
		"CONNECT api.anthropic.com:80 HTTP/1.1\r\n\r\n",
		"CONNECT api.anthropic.com.evil.test:443 HTTP/1.1\r\n\r\n",
		"CONNECT xapi.anthropic.com:443 HTTP/1.1\r\n\r\n",
		"GET http://api.anthropic.com/ HTTP/1.1\r\nHost: api.anthropic.com\r\n\r\n",
		"GET / HTTP/1.1\r\n\r\n",
	} {
		t.Run(strings.SplitN(req, "\r\n", 2)[0], func(t *testing.T) {
			if got := nsbStatus(t, sock, req); got != "HTTP/1.1 403 Forbidden" {
				t.Errorf("%q: status = %q", req, got)
			}
		})
	}
}

func TestSkillevalNsbox_DeniedTargetIsNeverResolved(t *testing.T) {
	up := nsbUpstream(t)
	var mu sync.Mutex
	var seen []string
	res := func(host string, port int) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, host+":"+strconv.Itoa(port))
		return "unix", up, nil
	}
	sock := nsbServe(t, nil, res)
	c := nsbConnect(t, sock, "CONNECT example.com:443 HTTP/1.1\r\n\r\n")
	if got := nsbRecv(t, c, 100); !strings.Contains(got, " 403 ") {
		t.Errorf("answer = %q, want a 403", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Errorf("the resolver was called for a denied target: %v", seen)
	}
}

// ---- WorkerAllowListTests ----

var nsbWorkerHosts = []string{"api.anthropic.com", "github.com", "api.github.com", "codeload.githubusercontent.com",
	"objects.githubusercontent.com", "pypi.org", "files.pythonhosted.org"}

func nsbWorkerProxy(t *testing.T) string {
	up := nsbUpstream(t)
	table := map[string]string{}
	for _, h := range nsbWorkerHosts {
		table[h+":443"] = up
	}
	return nsbServe(t, sandbox.WorkerAllow, sandbox.UpstreamResolver(table))
}

func TestSkillevalNsbox_SetIsExactlyTheSpecHostsOn443(t *testing.T) {
	var want []string
	for _, h := range nsbWorkerHosts {
		want = append(want, h+":443")
	}
	got := append([]string{}, sandbox.WorkerAllow...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorkerAllow = %v, want %v", got, want)
	}
	if sandbox.AllowDefault != "api.anthropic.com:443" {
		t.Errorf("AllowDefault = %q", sandbox.AllowDefault)
	}
}

func TestSkillevalNsbox_EveryListedHostIsRelayed(t *testing.T) {
	sock := nsbWorkerProxy(t)
	for _, h := range nsbWorkerHosts {
		t.Run(h, func(t *testing.T) {
			c := nsbConnect(t, sock, "CONNECT "+h+":443 HTTP/1.1\r\n\r\n")
			if got := nsbRecv(t, c, 100); !strings.Contains(got, " 200 ") {
				t.Fatalf("answer = %q, want a 200", got)
			}
			if _, err := c.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if got := nsbReadN(t, c, 4); got != "UP:x" {
				t.Errorf("reply = %q", got)
			}
		})
	}
}

func TestSkillevalNsbox_UnlistedLookalikesPortsAndMethodsAreDenied(t *testing.T) {
	sock := nsbWorkerProxy(t)
	for _, req := range []string{
		"CONNECT example.com:443 HTTP/1.1\r\n\r\n",
		"CONNECT github.com:80 HTTP/1.1\r\n\r\n",
		"CONNECT github.com:22 HTTP/1.1\r\n\r\n",
		"CONNECT github.com.evil.test:443 HTTP/1.1\r\n\r\n",
		"CONNECT evilgithub.com:443 HTTP/1.1\r\n\r\n",
		"CONNECT gist.github.com:443 HTTP/1.1\r\n\r\n",
		"CONNECT raw.githubusercontent.com:443 HTTP/1.1\r\n\r\n",
		"CONNECT pypi.org:8443 HTTP/1.1\r\n\r\n",
		"GET http://github.com/ HTTP/1.1\r\nHost: github.com\r\n\r\n",
	} {
		t.Run(strings.SplitN(req, "\r\n", 2)[0], func(t *testing.T) {
			if got := nsbStatus(t, sock, req); got != "HTTP/1.1 403 Forbidden" {
				t.Errorf("%q: status = %q", req, got)
			}
		})
	}
}

func TestSkillevalNsbox_SingleHostDefaultStillDeniesTheExtraHosts(t *testing.T) {
	if _, _, ok := sandbox.AllowedTarget("CONNECT github.com:443 HTTP/1.1", []string{sandbox.AllowDefault}); ok {
		t.Error("the single-host default allowed github.com")
	}
	host, port, ok := sandbox.AllowedTarget("CONNECT GitHub.com:443 HTTP/1.1", sandbox.WorkerAllow)
	if !ok || host != "GitHub.com" || port != 443 {
		t.Errorf("AllowedTarget = (%q, %d, %v), want (GitHub.com, 443, true)", host, port, ok)
	}
}

// ---- ProxyLifetimeTests ----
//
// Python starts a real egress.py with nsbox.start_proxy; the Go proxy is sandbox.Serve (no start_proxy exists), so
// the watched-pid and max-life cases drive Serve directly and the stop cases drive skilleval.StopProxy.

// nsbSleeper starts `sleep 60`, killed and reaped when the test ends.
func nsbSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill() // already dead in the tests that kill it themselves
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func TestSkillevalNsbox_ExitsWhenWatchedPidDies(t *testing.T) {
	watched := nsbSleeper(t)
	sock := filepath.Join(nsbShort(t), "p.sock")
	ready, done := nsbServeAsync(sandbox.ServeOpts{Sock: sock, WatchPid: watched.Process.Pid, MaxLife: time.Minute})
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy never became ready")
	}
	select {
	case err := <-done:
		t.Fatalf("proxy exited while its watched pid lives: %v", err)
	default:
	}
	if err := watched.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := watched.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy outlived its watched pid")
	}
}

func TestSkillevalNsbox_ExitsAfterMaxLifetime(t *testing.T) {
	sock := filepath.Join(nsbShort(t), "p.sock")
	_, done := nsbServeAsync(sandbox.ServeOpts{Sock: sock, MaxLife: time.Second})
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("proxy outlived max_life")
	}
}

// nsbFakeProxyProc is a process that looks like our proxy to StopProxy (its cmdline names egress.py) and dies on
// SIGTERM; the shell loop keeps it alive.
func nsbFakeProxyProc(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "while :; do sleep 1; done", "egress.py")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }() // reaps it so a dead proxy is not a zombie
	t.Cleanup(func() {
		_ = cmd.Process.Kill() // already dead after StopProxy
		<-exited
	})
	return cmd
}

func TestSkillevalNsbox_StopProxyKillsAndIsIdempotent(t *testing.T) {
	trial := filepath.Join(t.TempDir(), "trials", "t1")
	nsbMkdirs(t, trial)
	cmd := nsbFakeProxyProc(t)
	pid := cmd.Process.Pid
	pidfile := filepath.Join(trial, "proxy.pid")
	nsbWrite(t, pidfile, strconv.Itoa(pid)+"\n", 0o644)
	if !sandbox.PidAlive(pid) {
		t.Fatal("the stand-in proxy is not running")
	}
	skilleval.StopProxy(trial)
	if !nsbWaitFor(func() bool { return !sandbox.PidAlive(pid) }, 5) {
		t.Error("StopProxy left the proxy running")
	}
	if _, err := os.Stat(pidfile); err == nil {
		t.Error("proxy.pid was not removed")
	}
	skilleval.StopProxy(trial) // second call: no error, no panic
}

func TestSkillevalNsbox_StopProxyIgnoresARecycledPid(t *testing.T) {
	trial := t.TempDir()
	other := nsbSleeper(t)
	nsbWrite(t, filepath.Join(trial, "proxy.pid"), strconv.Itoa(other.Process.Pid)+"\n", 0o644)
	skilleval.StopProxy(trial)
	if !sandbox.PidAlive(other.Process.Pid) {
		t.Error("StopProxy killed a process that is not our proxy")
	}
}

// ---- ShimTests (the token rules; Go has no eval shim writer, the worker shim shares ReadToken) ----

// nsbShimCfg is a worker shim cfg whose stage-1 unshare does not exist: it can get as far as exec'ing it, i.e. past
// the token check, and no further.
func nsbShimCfg(t *testing.T, tmp, tokenFile string) *sandbox.ShimCfg {
	t.Helper()
	gh := filepath.Join(tmp, "gh-token")
	nsbWrite(t, gh, "ghp_dummy\n", 0o600)
	trial := filepath.Join(tmp, "trials", "t1")
	claude := filepath.Join(tmp, "bin", "claude")
	ns := nsbCfg(t, trial, claude, filepath.Join(tmp, "home"), func(o *sandbox.Opts) {
		o.Unshare, o.Forward = "/nonexistent/unshare", true
	})
	return &sandbox.ShimCfg{NS: ns, Home: trial + "/home", ConfigDir: trial + "/cfg", Gitconfig: trial + "/gitconfig",
		OAuthToken: tokenFile, GhToken: gh, Role: "evaluator"}
}

// nsbRunShim runs `worker-shim` through sandbox.Main (the process entry point) and returns exit code and stderr+stdout.
func nsbRunShim(t *testing.T, sc *sandbox.ShimCfg) (int, string) {
	t.Helper()
	js, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code, handled := sandbox.Main([]string{"worker-shim", string(js)}, strings.NewReader(""), &out, &errb)
	if !handled {
		t.Fatal("worker-shim was not handled")
	}
	return code, errb.String() + out.String()
}

func TestSkillevalNsbox_ShimFileModeAndContent(t *testing.T) {
	// Go has no eval shim writer (write_shim); the nearest is the worker shim text. Its file mode is the caller's
	// business (nothing writes it here), so only the content rules are checked.
	tmp := t.TempDir()
	token := filepath.Join(tmp, "home", ".config", "factory", "eval-oauth-token")
	sc := nsbShimCfg(t, tmp, token)
	text, err := sandbox.ShimScript("/usr/local/bin/factory-go", sc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "#!/") {
		t.Errorf("no shebang: %q", text[:20])
	}
	claude := filepath.Join(tmp, "bin", "claude")
	for _, want := range []string{token, claude} { // the token path is baked in, absolute
		if !strings.Contains(text, want) {
			t.Errorf("shim text lacks %s", want)
		}
	}
	for _, banned := range []string{"$HOME", "expanduser", "Path.home", `environ.get("HOME`, nsbToken} {
		if strings.Contains(text, banned) {
			t.Errorf("shim text contains %q", banned)
		}
	}
}

func TestSkillevalNsbox_TokenMissingExits3(t *testing.T) {
	tmp := t.TempDir()
	code, out := nsbRunShim(t, nsbShimCfg(t, tmp, filepath.Join(tmp, "home", ".config", "factory", "eval-oauth-token")))
	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	if !strings.Contains(out, "owner decision D3 pending: no eval token") {
		t.Errorf("output = %q", out)
	}
}

func TestSkillevalNsbox_TokenMode0644Exits3(t *testing.T) {
	tmp := t.TempDir()
	token := filepath.Join(tmp, "home", ".config", "factory", "eval-oauth-token")
	nsbWrite(t, token, nsbToken, 0o644)
	code, out := nsbRunShim(t, nsbShimCfg(t, tmp, token))
	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	if !strings.Contains(out, "owner decision D3 pending: no eval token") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, nsbToken) {
		t.Error("the token leaked into the output")
	}
}

func TestSkillevalNsbox_TokenSymlinkIsRefused(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real-token")
	nsbWrite(t, real, nsbToken, 0o600)
	link := filepath.Join(tmp, "home", ".config", "factory", "eval-oauth-token")
	nsbMkdirs(t, filepath.Dir(link))
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.ReadToken(link); !errors.Is(err, sandbox.ErrNoToken) {
		t.Errorf("ReadToken(symlink) error = %v, want ErrNoToken", err)
	}
	if code, _ := nsbRunShim(t, nsbShimCfg(t, tmp, link)); code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
}

func TestSkillevalNsbox_TokenMode0600ProceedsPastTheCheck(t *testing.T) {
	tmp := t.TempDir()
	token := filepath.Join(tmp, "home", ".config", "factory", "eval-oauth-token")
	nsbWrite(t, token, nsbToken+"\n", 0o600)
	got, err := sandbox.ReadToken(token)
	if err != nil || got != nsbToken {
		t.Fatalf("ReadToken = %q, %v; want the token", got, err)
	}
	code, out := nsbRunShim(t, nsbShimCfg(t, tmp, token)) // unshare is bogus: it gets as far as exec'ing it
	if code == 3 {
		t.Errorf("exit = 3: stopped at the token check; output %q", out)
	}
	if strings.Contains(out, "D3 pending") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, nsbToken) {
		t.Error("the token leaked into the output")
	}
}

// ---- ComposedArgvTests ----

type nsbArgvFx struct {
	tmp, claude, home string
	cfg               *sandbox.Cfg
	args              []string
}

func nsbArgv(t *testing.T) *nsbArgvFx {
	t.Helper()
	tmp := t.TempDir()
	fx := &nsbArgvFx{tmp: tmp, claude: filepath.Join(tmp, "bin", "claude"), home: filepath.Join(tmp, "home"),
		args: []string{"--model", "sonnet", "-p", "hi there"}}
	fx.cfg = nsbCfg(t, filepath.Join(tmp, "trials", "t1"), fx.claude, fx.home, func(o *sandbox.Opts) { o.Forward = true })
	return fx
}

func TestSkillevalNsbox_Stage1(t *testing.T) {
	fx := nsbArgv(t)
	s1, err := sandbox.ComposeStage1(fx.cfg, fx.args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s1[1:4], []string{"-r", "-n", "-m"}) {
		t.Errorf("s1[1:4] = %v", s1[1:4])
	}
	i := -1
	for j, a := range s1 {
		if a == "--propagation" {
			i = j
			break
		}
	}
	if i < 0 || s1[i+1] != "private" {
		t.Errorf("no --propagation private in %v", s1)
	}
	for _, w := range []string{"--pid", "--mount-proc"} {
		if !nsbHas(s1, w) {
			t.Errorf("%s missing from %v", w, s1)
		}
	}
	if !reflect.DeepEqual(s1[len(s1)-len(fx.args):], fx.args) {
		t.Errorf("the args are not the tail of %v", s1)
	}
}

func TestSkillevalNsbox_Stage2(t *testing.T) {
	fx := nsbArgv(t)
	s2 := sandbox.ComposeStage2(fx.cfg, fx.args)
	if want := []string{"--map-user=" + strconv.Itoa(os.Getuid()), "--map-group=" + strconv.Itoa(os.Getgid())}; !reflect.DeepEqual(s2[1:3], want) {
		t.Errorf("s2[1:3] = %v, want %v", s2[1:3], want)
	}
	tail := s2[len(s2)-len(fx.args)-1:]
	if want := append([]string{fx.claude}, fx.args...); !reflect.DeepEqual(tail, want) {
		t.Errorf("tail = %v, want %v", tail, want)
	}
	if !filepath.IsAbs(fx.claude) || filepath.Base(tail[0]) != "claude" {
		t.Errorf("the target %q is not an absolute path named claude", tail[0])
	}
	for _, cap := range []string{"--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all"} {
		if !nsbHas(s2, cap) {
			t.Errorf("%s missing from %v", cap, s2)
		}
	}
}

func TestSkillevalNsbox_TokenIsInNoArgvElement(t *testing.T) {
	fx := nsbArgv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", nsbToken)
	cfg := nsbCfg(t, filepath.Join(fx.tmp, "trials", "t1"), fx.claude, fx.home, func(o *sandbox.Opts) { o.Forward = true })
	s1, err := sandbox.ComposeStage1(cfg, fx.args)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range append(s1, sandbox.ComposeStage2(cfg, fx.args)...) {
		if strings.Contains(a, nsbToken) {
			t.Errorf("the token is in an argv element: %q", a)
		}
	}
}

func TestSkillevalNsbox_ContainedConfigHasNoProxyAndScrubsSecrets(t *testing.T) {
	fx := nsbArgv(t)
	cfg := nsbCfg(t, filepath.Join(fx.tmp, "trials", "t1"), "", fx.home, nil)
	if cfg.ForwardSock != nil {
		t.Errorf("forward_sock = %v, want none", *cfg.ForwardSock)
	}
	if len(cfg.EnvSet) != 0 {
		t.Errorf("env_set = %v, want empty", cfg.EnvSet)
	}
	for _, v := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "HTTPS_PROXY", "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"} {
		if !nsbHas(cfg.Scrub, v) {
			t.Errorf("%s is not scrubbed: %v", v, cfg.Scrub)
		}
	}
}

func TestSkillevalNsbox_HideDirsResolveFromTheGivenHome(t *testing.T) {
	fx := nsbArgv(t)
	for _, d := range []string{fx.home + "/.claude", fx.home + "/factory"} {
		if !nsbHas(fx.cfg.HideDirs, d) {
			t.Errorf("%s is not in hide_dirs %v", d, fx.cfg.HideDirs)
		}
	}
}

// ---- EnvAllowListComposeTests ----

func TestSkillevalNsbox_EnvPassIsSmallAndHasNoSecretNames(t *testing.T) {
	got := append([]string{}, sandbox.EnvPass...)
	sort.Strings(got)
	if want := []string{"LANG", "PATH", "TERM", "TMPDIR"}; !reflect.DeepEqual(got, want) {
		t.Errorf("EnvPass = %v, want %v", got, want)
	}
}

// ---- CleanSkipTests ----

func TestSkillevalNsbox_UsernsAvailableReportsTheReason(t *testing.T) {
	ok, why := sandbox.UsernsAvailable("/nonexistent/unshare")
	if ok {
		t.Error("a missing unshare reported namespaces available")
	}
	if !strings.Contains(why, "/nonexistent/unshare") {
		t.Errorf("reason = %q, want it to name the missing binary", why)
	}
}

func TestSkillevalNsbox_GroupBSkipsWithReasonWhenNamespacesAreUnavailable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"ANoNetworkInside", "ProxyIsGoneAfterClaudeExits", "HidingNoNetworkNoSecretsAndOutputReturned",
		"TimeoutKillsTheWholeTree", "CheckerWithCwdInTrialCannotReachSiblingViaDotdotOrProc",
		"UnlistedHomeSubdirIsAbsentTrialIsWritablePluginIsReadOnly"}
	for i, n := range names {
		names[i] = "TestSkillevalNsbox_" + n
	}
	cmd := exec.Command(self, "-test.run=^("+strings.Join(names, "|")+")$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "NSBOX_TEST_UNSHARE=/nonexistent/unshare", "NSB_MODE=")
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() { out, runErr = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill() // the child hung: stop it, then report below
		<-done
		t.Fatal("the child run timed out")
	}
	text := string(out)
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			t.Fatalf("the child run failed: %v\n%s", runErr, text)
		}
		t.Fatal(runErr)
	}
	if strings.Contains(text, "--- FAIL") || strings.Contains(text, "FAIL\t") {
		t.Errorf("a group B test failed:\n%s", text)
	}
	if !strings.Contains(text, "user namespaces unavailable: cannot run /nonexistent/unshare") {
		t.Errorf("no clean skip reason in:\n%s", text)
	}
	if got := strings.Count(text, "--- SKIP"); got != len(names) {
		t.Errorf("%d tests skipped, want %d:\n%s", got, len(names), text)
	}
	if !strings.Contains(text, "PASS") {
		t.Errorf("the child run did not end in PASS:\n%s", text)
	}
}

// ---- Go-native extras ----

// TestSkillevalNsbox_RealHomeIgnoresHOME: the home that is hidden is the passwd one (RealHome), never $HOME.
func TestSkillevalNsbox_RealHomeIgnoresHOME(t *testing.T) {
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Skipf("no passwd entry for uid %d: %v", os.Getuid(), err)
	}
	t.Setenv("HOME", "/nonexistent-nsb-home")
	if got := sandbox.RealHome(); got != u.HomeDir {
		t.Errorf("RealHome() = %q, want the passwd home %q", got, u.HomeDir)
	}
}

// TestSkillevalNsbox_WorkerShimBadCfgExits2: a cfg that is not JSON never reaches the token check.
func TestSkillevalNsbox_WorkerShimBadCfgExits2(t *testing.T) {
	var out, errb bytes.Buffer
	code, handled := sandbox.Main([]string{"worker-shim", "{not json"}, strings.NewReader(""), &out, &errb)
	if !handled || code != 2 || !strings.Contains(errb.String(), "bad shim cfg") {
		t.Errorf("code=%d handled=%v stderr=%q", code, handled, errb.String())
	}
}

// TestSkillevalNsbox_TokenRefusalNeverNamesTheToken: the refusal names the path and the rule, never the value.
func TestSkillevalNsbox_TokenRefusalNeverNamesTheToken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tok")
	nsbWrite(t, p, nsbToken, 0o640)
	_, err := sandbox.ReadToken(p)
	if !errors.Is(err, sandbox.ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}
	if strings.Contains(err.Error(), nsbToken) || !strings.Contains(err.Error(), p) {
		t.Errorf("error = %q", err)
	}
	if err := syscall.Access(p, 0); err != nil {
		t.Errorf("the token file vanished: %v", err)
	}
}
