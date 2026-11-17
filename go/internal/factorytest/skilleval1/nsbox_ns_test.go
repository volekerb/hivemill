package skilleval1

// Port of tests/test_skilleval_nsbox.py, group B (real user namespaces): NsboxNamespaceTests, RunContainedTests,
// CwdEscapeTests and RunContainedAllowListTests. They skip with the reason where namespaces are unavailable.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
)

// ---- the fake claude (helper process, mode "claude") ----

type nsbHelperCfg struct {
	Hidden      map[string]string `json:"hidden"`
	Visible     map[string]string `json:"visible"`
	Sentinel    int               `json:"sentinel"`
	SentinelDir string            `json:"sentinel_dir"`
	OuterPid    int               `json:"outer_pid"`
	Factory     string            `json:"factory"`
	FactoryEnv  []string          `json:"factory_env"`
	Sockets     map[string]string `json:"sockets"`
	Obs         string            `json:"obs"`
	Ready       string            `json:"ready"`
	Go          string            `json:"go"`
	ProxyReady  string            `json:"proxy_ready"`
}

type nsbRun struct {
	RC  int    `json:"rc"`
	Out string `json:"out"`
	Err string `json:"err"`
}

// nsbObs is what the fake claude saw from inside the namespaces.
type nsbObs struct {
	Argv            []string            `json:"argv"`
	UID             int                 `json:"uid"`
	Fetch           string              `json:"fetch"`
	Direct          string              `json:"direct"`
	Allowed         []*string           `json:"allowed"`
	Denied          []*string           `json:"denied"`
	ProxyEnv        []*string           `json:"proxy_env"`
	Ls              map[string][]string `json:"ls"`
	LsErr           map[string]string   `json:"ls_err"`
	Visible         map[string]bool     `json:"visible"`
	UmountRC        map[string]int      `json:"umount_rc"`
	TokenSet        *string             `json:"token_set"`
	Environ         map[string]string   `json:"environ"`
	EnvUnset        []*string           `json:"env_unset"`
	Cmdline         []string            `json:"cmdline"`
	Caps            map[string]string   `json:"caps"`
	NNP             string              `json:"nnp"`
	ProcPids        []int               `json:"proc_pids"`
	ProcCaps        map[string]string   `json:"proc_caps"`
	SentinelEnviron string              `json:"sentinel_environ"`
	SentinelRoot    string              `json:"sentinel_root"`
	OuterPidVisible bool                `json:"outer_pid_visible"`
	Sockets         map[string]string   `json:"sockets"`
	FactoryHelpRC   int                 `json:"factory_help_rc"`
	FactoryTask     nsbRun              `json:"factory_task"`
}

func nsbEnvPtr(k string) *string {
	if v, ok := os.LookupEnv(k); ok {
		return &v
	}
	return nil
}

func nsbSockStatus(p string) string {
	c, err := net.DialTimeout("unix", p, 2*time.Second)
	if err != nil {
		return "failed"
	}
	_ = c.Close() // a probe connection; nothing to report
	return "connected"
}

// nsbViaProxy is via_proxy(): CONNECT target through the in-namespace forwarder; reply is nil unless it was a 200.
func nsbViaProxy(target string) []*string {
	c, err := net.DialTimeout("tcp", "127.0.0.1:3128", 5*time.Second)
	if err != nil {
		e := "dial: " + err.Error()
		return []*string{&e, nil}
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	buf := make([]byte, 4096)
	n, _ := c.Read(buf) // a short or failed read shows up as an empty head, which the assertions reject
	head := nsbFirstLine(string(buf[:n]))
	var reply *string
	if strings.Contains(head, " 200 ") {
		_, _ = c.Write([]byte("ping"))
		m, _ := c.Read(buf[:100])
		r := string(buf[:m])
		reply = &r
	}
	return []*string{&head, reply}
}

func nsbStatusFields(path string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(ln, ":\t"); ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

func nsbHelperClaude() {
	var cfg nsbHelperCfg
	raw, err := os.ReadFile(os.Getenv("NSB_CFG"))
	if err == nil {
		err = json.Unmarshal(raw, &cfg)
	}
	if err != nil {
		os.Stderr.WriteString("nsb claude helper: " + err.Error() + "\n")
		os.Exit(98)
	}
	for i := 0; i < 600 && !nsbExists(cfg.ProxyReady); i++ { // the proxy outside is up once this file exists
		time.Sleep(50 * time.Millisecond)
	}
	obs := nsbObs{Argv: os.Args, UID: os.Getuid(), Ls: map[string][]string{}, LsErr: map[string]string{}, Visible: map[string]bool{},
		UmountRC: map[string]int{}, Sockets: map[string]string{}, ProcCaps: map[string]string{}, Caps: map[string]string{}}
	hc := &http.Client{Timeout: 3 * time.Second}
	if resp, err := hc.Get("http://example.com"); err == nil {
		_ = resp.Body.Close()
		obs.Fetch = "ok"
	} else {
		obs.Fetch = "fail"
	}
	if c, err := net.DialTimeout("tcp", "1.1.1."+"1:80", 3*time.Second); err == nil {
		_ = c.Close()
		obs.Direct = "ok"
	} else {
		obs.Direct = "fail"
	}
	obs.Allowed = nsbViaProxy("api.anthropic.com:443")
	obs.Denied = nsbViaProxy("example.com:443")
	obs.ProxyEnv = []*string{nsbEnvPtr("HTTPS_PROXY"), nsbEnvPtr("https_proxy")}
	for k, p := range cfg.Hidden {
		names, e := nsbLs(p)
		obs.Ls[k], obs.LsErr[k] = names, e
		rc := 0
		if err := syscall.Unmount(p, 0); err != nil {
			rc = 1
			var en syscall.Errno
			if errors.As(err, &en) {
				rc = int(en)
			}
		}
		obs.UmountRC[k] = rc
	}
	for k, p := range cfg.Visible {
		obs.Visible[k] = nsbExists(p)
	}
	obs.TokenSet = nsbEnvPtr("CLAUDE_CODE_OAUTH_TOKEN")
	obs.Environ = map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		obs.Environ[k] = v
	}
	obs.EnvUnset = []*string{nsbEnvPtr("SSH_AUTH_SOCK"), nsbEnvPtr("DBUS_SESSION_BUS_ADDRESS")}
	if b, err := os.ReadFile("/proc/self/cmdline"); err == nil {
		obs.Cmdline = strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	}
	st := nsbStatusFields("/proc/self/status")
	for _, k := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		obs.Caps[k] = st[k]
	}
	obs.NNP = st["NoNewPrivs"]
	for _, p := range nsbPids() {
		n, _ := strconv.Atoi(p)
		obs.ProcPids = append(obs.ProcPids, n)
		if eff, ok := nsbStatusFields("/proc/" + p + "/status")["CapEff"]; ok { // a process that vanished has no status
			obs.ProcCaps[p] = eff
		}
	}
	sort.Ints(obs.ProcPids)
	obs.SentinelEnviron, obs.SentinelRoot = "blocked", "blocked"
	if _, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", cfg.Sentinel)); err == nil {
		obs.SentinelEnviron = "readable"
	}
	if _, err := os.ReadDir(fmt.Sprintf("/proc/%d/root/%s", cfg.Sentinel, cfg.SentinelDir)); err == nil {
		obs.SentinelRoot = "readable"
	}
	obs.OuterPidVisible = nsbExists(fmt.Sprintf("/proc/%d", cfg.OuterPid))
	for k, p := range cfg.Sockets {
		obs.Sockets[k] = nsbSockStatus(p)
	}
	obs.FactoryHelpRC = nsbRunCLI(cfg, "--help").RC
	obs.FactoryTask = nsbRunCLI(cfg, "task", "show", "T-0001")
	b, err := json.Marshal(obs)
	if err != nil {
		os.Stderr.WriteString("nsb claude helper: " + err.Error() + "\n")
		os.Exit(97)
	}
	if err := os.WriteFile(cfg.Obs+".tmp", b, 0o600); err != nil {
		os.Stderr.WriteString("nsb claude helper: " + err.Error() + "\n")
		os.Exit(96)
	}
	if err := os.Rename(cfg.Obs+".tmp", cfg.Obs); err != nil {
		os.Stderr.WriteString("nsb claude helper: " + err.Error() + "\n")
		os.Exit(95)
	}
	if f, err := os.Create(cfg.Ready); err == nil {
		_ = f.Close() // the empty ready flag is the whole message
	}
	for i := 0; i < 300 && !nsbExists(cfg.Go); i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

func nsbExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func nsbRunCLI(cfg nsbHelperCfg, args ...string) nsbRun {
	var out, errb bytes.Buffer
	cmd := exec.Command(cfg.Factory, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = cfg.FactoryEnv, &out, &errb
	rc := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rc = ee.ExitCode()
		} else {
			return nsbRun{RC: -1, Err: err.Error()}
		}
	}
	return nsbRun{RC: rc, Out: trunc(out.String(), 200), Err: trunc(errb.String(), 200)}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- the shared scenario: NsboxNamespaceTests.setUpClass ----

type nsbScenario struct {
	obs           *nsbObs
	cmdlineHits   []string
	proxySockGone bool
	proxyDone     bool
	uid           int
	trial         string
	err           error
}

var (
	nsbScenarioOnce sync.Once
	nsbScenarioVal  *nsbScenario
)

// nsbShimEnv is the shim's allow-list env for claude: ENV_PASS and LC_* from the caller, CLAUDE_ENV_PASS, HOME=trial
// and the token. (Go has no eval shim; the filter is assembled here from the product's allow-list constants.)
func nsbShimEnv(caller []string, trial string) []string {
	out := sandbox.EnvPassFrom(caller, trial, nil)
	for _, kv := range caller {
		k, _, _ := strings.Cut(kv, "=")
		if nsbHas(sandbox.ClaudeEnvPass, k) {
			out = append(out, kv)
		}
	}
	return append(out, "CLAUDE_CODE_OAUTH_TOKEN="+nsbToken)
}

// nsbGetScenario runs the scenario once per test binary and returns its result; skips where namespaces are missing.
func nsbGetScenario(t *testing.T) *nsbScenario {
	t.Helper()
	nsbRequireUserns(t)
	nsbScenarioOnce.Do(func() { nsbScenarioVal = nsbRunScenario(t) })
	sc := nsbScenarioVal
	if sc == nil {
		t.Fatal("the namespace scenario did not complete")
	}
	if sc.err != nil {
		t.Fatalf("the namespace scenario failed: %v", sc.err)
	}
	return sc
}

func nsbRunScenario(t *testing.T) (sc *nsbScenario) {
	sc = &nsbScenario{uid: os.Getuid()}
	fail := func(format string, a ...any) *nsbScenario {
		sc.err = fmt.Errorf(format, a...)
		return sc
	}
	tmp, err := os.MkdirTemp("", "nsb")
	if err != nil {
		return fail("temp dir: %w", err)
	}
	tmp = sandbox.Realpath(tmp)
	defer os.RemoveAll(tmp) // best effort: a leftover is a temp dir in os.TempDir()

	env := ft.New(t) // a throwaway FACTORY_HOME for the factory CLI check; cleaned up with the first test
	env.SeedGoal("G-001", "", "zz-test goal")
	env.SeedTask(ft.Task{})

	up := nsbEchoOnce()
	if up == nil {
		return fail("cannot start the fake upstream")
	}
	defer up.Close()

	// a fake "real home" with secrets in it, a live-looking factory dir, a runtime dir and unix sockets
	home := filepath.Join(tmp, "realhome")
	run := filepath.Join(tmp, "run")
	for _, d := range []string{".claude", ".ssh", ".config/factory", ".config/herdr", "factory"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			return fail("%w", err)
		}
	}
	if err := os.MkdirAll(run, 0o755); err != nil {
		return fail("%w", err)
	}
	for p, body := range map[string]string{filepath.Join(home, ".claude", "secret.json"): "{}", filepath.Join(home, "factory", "factory.db"): "live"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return fail("%w", err)
		}
	}
	herdr := filepath.Join(home, ".config", "herdr", "herdr.sock")
	bus := filepath.Join(run, "bus")
	for _, p := range []string{herdr, bus} {
		ln, err := net.Listen("unix", p)
		if err != nil {
			return fail("listen %s: %w", p, err)
		}
		defer ln.Close()
	}
	trials := filepath.Join(tmp, "trials")
	trial := filepath.Join(trials, "t1")
	sc.trial = trial
	for _, d := range []string{filepath.Join(trials, "t2"), filepath.Join(trial, "home"), filepath.Join(tmp, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail("%w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(trials, "t2", "other-trial-secret"), []byte("x"), 0o600); err != nil {
		return fail("%w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fail("os.Executable: %w", err)
	}
	claude := filepath.Join(tmp, "bin", "claude") // the fake claude IS this binary, under the name claude
	if err := os.Symlink(self, claude); err != nil {
		return fail("%w", err)
	}
	sentinel := exec.Command("sleep", "120")
	sentinel.Env = []string{"SENTINEL_MARK=zz-sentinel-marker"}
	if err := sentinel.Start(); err != nil {
		return fail("sentinel: %w", err)
	}
	defer func() {
		_ = sentinel.Process.Kill() // best effort teardown
		_, _ = sentinel.Process.Wait()
	}()

	hcfg := nsbHelperCfg{
		Hidden: map[string]string{"claude": filepath.Join(home, ".claude"), "factory": filepath.Join(home, "factory"),
			"ssh": filepath.Join(home, ".ssh"), "config": filepath.Join(home, ".config"), "sibling": filepath.Join(trials, "t2"),
			"runtime": run},
		Visible:  map[string]string{"trial": trial, "claude": claude},
		Sentinel: sentinel.Process.Pid, SentinelDir: strings.TrimPrefix(filepath.Join(home, ".claude"), "/"),
		OuterPid: os.Getpid(), Factory: env.GoBin, FactoryEnv: env.Environ("boss", nil),
		Sockets: map[string]string{"user_bus": bus, "herdr": herdr},
		Obs:     filepath.Join(trial, "obs.json"), Ready: filepath.Join(trial, "ready"), Go: filepath.Join(trial, "go"),
		ProxyReady: filepath.Join(trial, "proxy-ready"),
	}
	hb, err := json.Marshal(hcfg)
	if err != nil {
		return fail("%w", err)
	}
	if err := os.WriteFile(filepath.Join(trial, "cfg.json"), hb, 0o600); err != nil {
		return fail("%w", err)
	}

	cfg, err := sandbox.MakeCfg(func() sandbox.Opts {
		o := sandbox.NewOpts(trial)
		o.RealClaude, o.Home, o.TrialsRoot, o.Runtime, o.Forward, o.Unshare = claude, home, trials, run, true, nsbUnshare()
		return o
	}())
	if err != nil {
		return fail("MakeCfg: %w", err)
	}
	s1, err := sandbox.ComposeStage1(cfg, nsbHelperArgs)
	if err != nil {
		return fail("ComposeStage1: %w", err)
	}
	caller := []string{"PATH=" + trial + "/bin:/usr/bin:/bin", "HOME=" + trial + "/home", "SSH_AUTH_SOCK=/tmp/fake-agent.sock",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/fake-bus", "CLAUDE_CODE_MESSAGING_TOKEN=msg-secret", "FOO_SECRET=foo-secret",
		"LC_ALL=C.UTF-8", "CLAUDE_CODE_ENTRYPOINT=cli"}
	// stage 1 gets the shim's env plus the two socket variables the shim would have dropped already: this way
	// both layers (the allow-list and stage 1's scrub) are exercised.
	penv := append(nsbShimEnv(caller, trial), "SSH_AUTH_SOCK=/tmp/fake-agent.sock", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/fake-bus",
		"NSB_MODE=claude", "NSB_CFG="+filepath.Join(trial, "cfg.json"))
	var out bytes.Buffer
	cmd := exec.Command(s1[0], s1[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = trial, penv, &out, &out
	if err := cmd.Start(); err != nil {
		return fail("start stage 1: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() {
		_ = os.WriteFile(hcfg.Go, nil, 0o600) // lets a helper that is still waiting finish; best effort
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill() // best effort
			<-exited
		}
	}()

	// the proxy runs outside, watching the pid of the namespace wrapper, like the shim's egress.py
	psock := filepath.Join(trial, "proxy.sock")
	pready, pdone := nsbServeAsync(sandbox.ServeOpts{Sock: psock, WatchPid: cmd.Process.Pid, MaxLife: 2 * time.Minute,
		Resolver: sandbox.UpstreamResolver(map[string]string{"api.anthropic.com:443": up.Addr().String()})})
	select {
	case <-pready:
	case err := <-pdone:
		return fail("proxy: %v", err)
	case <-time.After(5 * time.Second):
		return fail("proxy never became ready")
	}
	if err := os.WriteFile(hcfg.ProxyReady, nil, 0o600); err != nil {
		return fail("%w", err)
	}
	if !nsbWaitFor(func() bool {
		select {
		case <-exited:
			return true
		default:
		}
		return nsbExists(hcfg.Ready)
	}, 60) {
		return fail("fake claude never became ready")
	}
	if !nsbExists(hcfg.Ready) {
		return fail("stage 1 exited early: %s", out.String())
	}
	ob, err := os.ReadFile(hcfg.Obs)
	if err != nil {
		return fail("%w", err)
	}
	sc.obs = new(nsbObs)
	if err := json.Unmarshal(ob, sc.obs); err != nil {
		return fail("observations: %w", err)
	}
	// (e) while claude runs: scan every cmdline on the box for the token
	for _, p := range nsbPids() {
		if b, err := os.ReadFile("/proc/" + p + "/cmdline"); err == nil && bytes.Contains(b, []byte(nsbToken)) {
			sc.cmdlineHits = append(sc.cmdlineHits, p)
		}
	}
	if err := os.WriteFile(hcfg.Go, nil, 0o600); err != nil {
		return fail("%w", err)
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		return fail("the fake claude did not exit: %s", out.String())
	}
	// the proxy is gone once claude has exited
	select {
	case <-pdone:
		sc.proxyDone = true
	case <-time.After(5 * time.Second):
	}
	sc.proxySockGone = nsbWaitFor(func() bool { return !nsbExists(psock) }, 5)
	return sc
}

// nsbEchoOnce is the TCP upstream of the scenario (not tied to a test): "UP:" + what it got.
func nsbEchoOnce() net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(append([]byte("UP:"), buf[:n]...)); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln
}

func nsbStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func nsbStrs(l []*string) []string {
	var out []string
	for _, p := range l {
		out = append(out, nsbStr(p))
	}
	return out
}

// ---- NsboxNamespaceTests ----

func TestSkillevalNsbox_ANoNetworkInside(t *testing.T) {
	o := nsbGetScenario(t).obs
	if o.Fetch != "fail" || o.Direct != "fail" {
		t.Errorf("fetch=%q direct=%q, want both fail", o.Fetch, o.Direct)
	}
}

func TestSkillevalNsbox_BProxyAllowsOnlyTheApiHost(t *testing.T) {
	o := nsbGetScenario(t).obs
	if got := nsbStrs(o.Allowed); !reflect.DeepEqual(got, []string{"HTTP/1.1 200 Connection Established", "UP:ping"}) {
		t.Errorf("allowed = %v", got)
	}
	if len(o.Denied) == 0 || nsbStr(o.Denied[0]) != "HTTP/1.1 403 Forbidden" {
		t.Errorf("denied = %v", nsbStrs(o.Denied))
	}
	if got := nsbStrs(o.ProxyEnv); !reflect.DeepEqual(got, []string{"http://127.0.0.1:3128", "http://127.0.0.1:3128"}) {
		t.Errorf("proxy env = %v", got)
	}
}

func TestSkillevalNsbox_CSecretDirsAreEmptyAndOwnPathsVisible(t *testing.T) {
	o := nsbGetScenario(t).obs
	for k, v := range o.Ls {
		if len(v) != 0 || o.LsErr[k] != "" {
			t.Errorf("%s: listing %v err %q, want an empty dir", k, v, o.LsErr[k])
		}
	}
	if !reflect.DeepEqual(o.Visible, map[string]bool{"trial": true, "claude": true}) {
		t.Errorf("visible = %v", o.Visible)
	}
}

func TestSkillevalNsbox_DRealUidAndNoUmount(t *testing.T) {
	sc := nsbGetScenario(t)
	if sc.obs.UID != sc.uid {
		t.Errorf("uid = %d, want %d", sc.obs.UID, sc.uid)
	}
	for k, rc := range sc.obs.UmountRC { // every tmpfs overlay
		if rc == 0 {
			t.Errorf("umount of %s succeeded inside", k)
		}
	}
}

func TestSkillevalNsbox_ETokenInEnvOnly(t *testing.T) {
	sc := nsbGetScenario(t)
	if sc.obs.TokenSet == nil || *sc.obs.TokenSet != nsbToken {
		t.Errorf("the token is not in claude's env: %v", sc.obs.TokenSet)
	}
	if len(sc.cmdlineHits) != 0 {
		t.Errorf("the token is on the command line of pids %v", sc.cmdlineHits)
	}
	for _, a := range append(append([]string{}, sc.obs.Argv...), sc.obs.Cmdline...) {
		if strings.Contains(a, nsbToken) {
			t.Errorf("the token is in an argv element: %q", a)
		}
	}
}

func TestSkillevalNsbox_ShimPassesOnlyAllowListedEnvToClaude(t *testing.T) {
	sc := nsbGetScenario(t)
	env := sc.obs.Environ
	for _, k := range []string{"CLAUDE_CODE_MESSAGING_TOKEN", "FOO_SECRET", "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s reached claude", k)
		}
	}
	js, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"msg-secret", "foo-secret"} {
		if strings.Contains(string(js), s) {
			t.Errorf("%q reached claude's env", s)
		}
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != nsbToken {
		t.Error("the token is not loaded")
	}
	if env["HOME"] != sc.trial {
		t.Errorf("HOME = %q, want %q", env["HOME"], sc.trial)
	}
	if env["LC_ALL"] != "C.UTF-8" || env["CLAUDE_CODE_ENTRYPOINT"] != "cli" {
		t.Errorf("LC_ALL=%q CLAUDE_CODE_ENTRYPOINT=%q", env["LC_ALL"], env["CLAUDE_CODE_ENTRYPOINT"])
	}
	if _, ok := env["PATH"]; !ok {
		t.Error("PATH is missing")
	}
	allowed := map[string]bool{"HOME": true, "CLAUDE_CODE_OAUTH_TOKEN": true, "LC_ALL": true, "HTTPS_PROXY": true, "https_proxy": true,
		"NO_PROXY": true, "no_proxy": true, "LC_CTYPE": true, "PWD": true, "SHLVL": true, "_": true, "OLDPWD": true}
	for _, k := range append(append([]string{}, sandbox.EnvPass...), sandbox.ClaudeEnvPass...) {
		allowed[k] = true
	}
	for k := range env {
		if !allowed[k] && !strings.HasPrefix(k, "NSB_") { // NSB_* is how this port tells the helper what to do
			t.Errorf("unexpected variable %s in claude's env", k)
		}
	}
}

func TestSkillevalNsbox_FOuterProcessesUnreachable(t *testing.T) {
	o := nsbGetScenario(t).obs
	if o.SentinelEnviron != "blocked" || o.SentinelRoot != "blocked" {
		t.Errorf("sentinel environ=%q root=%q, want blocked", o.SentinelEnviron, o.SentinelRoot)
	}
}

func TestSkillevalNsbox_GCmdlineHasClaudeBasenameInFirstThree(t *testing.T) {
	o := nsbGetScenario(t).obs
	n := len(o.Cmdline)
	if n > 3 {
		n = 3
	}
	var bases []string
	for _, a := range o.Cmdline[:n] {
		bases = append(bases, filepath.Base(a))
	}
	if !nsbHas(bases, "claude") {
		t.Errorf("cmdline = %v: no claude basename in the first three", o.Cmdline)
	}
}

func TestSkillevalNsbox_HFactoryCliWorksAndLiveDbIsNotReachable(t *testing.T) {
	o := nsbGetScenario(t).obs
	if o.FactoryHelpRC != 0 {
		t.Errorf("factory --help exit = %d", o.FactoryHelpRC)
	}
	if o.FactoryTask.RC != 0 {
		t.Errorf("factory task show exit = %d: %s", o.FactoryTask.RC, o.FactoryTask.Err)
	}
	if !strings.Contains(o.FactoryTask.Out, "T-0001") {
		t.Errorf("factory task show output = %q", o.FactoryTask.Out)
	}
	if len(o.Ls["factory"]) != 0 || o.LsErr["factory"] != "" { // the live-looking factory.db in the fake home is hidden
		t.Errorf("the fake live factory dir is not empty: %v %q", o.Ls["factory"], o.LsErr["factory"])
	}
}

func TestSkillevalNsbox_RealHomeResolutionIgnoresHOME(t *testing.T) {
	// HOME was <trial>/home in the caller's env; the passwd-style home (the seam) is what got hidden and read.
	o := nsbGetScenario(t).obs
	for _, k := range []string{"claude", "factory", "config"} {
		if len(o.Ls[k]) != 0 || o.LsErr[k] != "" {
			t.Errorf("%s is not hidden: %v %q", k, o.Ls[k], o.LsErr[k])
		}
	}
	if o.TokenSet == nil || *o.TokenSet != nsbToken {
		t.Error("the token is not set")
	}
}

func TestSkillevalNsbox_CapabilitiesAllZeroAndNoNewPrivs(t *testing.T) {
	o := nsbGetScenario(t).obs
	for k, v := range o.Caps {
		if v != "0000000000000000" {
			t.Errorf("%s = %q, want all zero", k, v)
		}
	}
	if len(o.Caps) != 5 || o.NNP != "1" {
		t.Errorf("caps = %v nnp = %q", o.Caps, o.NNP)
	}
}

func TestSkillevalNsbox_RuntimeDirIsHiddenAndSocketEnvUnset(t *testing.T) {
	o := nsbGetScenario(t).obs
	if _, ok := o.Ls["runtime"]; ok && (len(o.Ls["runtime"]) != 0 || o.LsErr["runtime"] != "") {
		t.Errorf("the runtime dir is not hidden: %v %q", o.Ls["runtime"], o.LsErr["runtime"])
	}
	if o.EnvUnset[0] != nil || o.EnvUnset[1] != nil {
		t.Errorf("socket variables are set: %v", nsbStrs(o.EnvUnset))
	}
}

func TestSkillevalNsbox_UnixSocketsUnreachable(t *testing.T) {
	o := nsbGetScenario(t).obs
	if want := map[string]string{"user_bus": "failed", "herdr": "failed"}; !reflect.DeepEqual(o.Sockets, want) {
		t.Errorf("sockets = %v, want %v", o.Sockets, want)
	}
}

func TestSkillevalNsbox_OwnPidNamespaceWithFreshProc(t *testing.T) {
	nsbNotInGatebox(t)
	sc := nsbGetScenario(t)
	if sc.obs.OuterPidVisible {
		t.Error("the outer pid is visible")
	}
	max := 0
	for _, p := range sc.obs.ProcPids {
		if p > max {
			max = p
		}
	}
	if max >= 100 || len(sc.obs.ProcPids) == 0 {
		t.Errorf("pids = %v: not a fresh pid namespace", sc.obs.ProcPids)
	}
}

func TestSkillevalNsbox_NoPrivilegedHelperSurvives(t *testing.T) {
	o := nsbGetScenario(t).obs
	if len(o.ProcCaps) < 2 { // claude plus the forwarder
		t.Errorf("only %d processes inside: %v", len(o.ProcCaps), o.ProcCaps)
	}
	for p, c := range o.ProcCaps {
		if c != "0000000000000000" {
			t.Errorf("pid %s has CapEff %s", p, c)
		}
	}
}

func TestSkillevalNsbox_ProxyIsGoneAfterClaudeExits(t *testing.T) {
	sc := nsbGetScenario(t)
	if !sc.proxySockGone {
		t.Error("proxy still listening")
	}
	if !sc.proxyDone {
		t.Error("the proxy did not stop after claude exited")
	}
}

// ---- contained runs: the glue of nsbox._contained_cfg / run_contained ----

// nsbContainedCfg is _contained_cfg: tmpfs over the home and the runtime dir (plus the extra hide dirs), bound back
// only the trial (rw), the cwd when it lies outside the trial (rw) and the vendored plugin (ro). Siblings stay hidden
// even when the trials root lies outside every hide tree.
func nsbContainedCfg(t testing.TB, trial, cwd, home, trialsRoot string, extra []string, vendor string) *sandbox.Cfg {
	t.Helper()
	home = sandbox.Realpath(home)
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	var hide []string
	for _, d := range append([]string{home, runtime}, extra...) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			hide = append(hide, sandbox.Realpath(d))
		}
	}
	trial = sandbox.Realpath(trial)
	root := filepath.Dir(trial)
	if trialsRoot != "" {
		root = sandbox.Realpath(trialsRoot)
	}
	rw := []string{trial}
	if cwd != "" && !nsbUnder(sandbox.Realpath(cwd), trial) {
		rw = append(rw, sandbox.Realpath(cwd))
	}
	var masks []string
	inHide := false
	for _, h := range hide {
		inHide = inHide || nsbUnder(root, h)
	}
	if !inHide {
		masks = []string{root}
	}
	var ro []string
	if vendor != "" {
		ro = []string{sandbox.Realpath(vendor)}
	}
	o := sandbox.NewOpts(trial)
	o.Home, o.TrialsRoot, o.Mode, o.HideDirs, o.HideFiles, o.Rw, o.Ro, o.Masks = home, root, "allow", hide, nil, rw, ro, masks
	o.Unshare, o.Cwd, o.Runtime = nsbUnshare(), cwd, runtime
	cfg, err := sandbox.MakeCfg(o)
	if err != nil {
		t.Fatalf("MakeCfg: %v", err)
	}
	return cfg
}

func nsbUnder(p, base string) bool {
	return p == base || strings.HasPrefix(p, strings.TrimRight(base, "/")+"/")
}

// nsbContained is run_contained for a helper-process target: env nil means the allow-list (ENV_PASS, LC_*,
// HOME=trial, extra); the helper's NSB_* variables ride along as extras.
func nsbContained(t testing.TB, cfg *sandbox.Cfg, argv []string, cwd string, env []string, timeout time.Duration, extra map[string]string) sandbox.Contained {
	t.Helper()
	if env == nil {
		env = sandbox.EnvPassFrom(os.Environ(), cfg.Trial, extra)
	}
	res, err := sandbox.RunContained(cfg, argv, cwd, env, timeout)
	if err != nil {
		t.Fatalf("RunContained: %v", err)
	}
	return res
}

func nsbSelf(t testing.TB) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return self
}

func nsbHelperArgv(t testing.TB) []string {
	t.Helper()
	return append([]string{nsbSelf(t)}, nsbHelperArgs...)
}

// ---- RunContainedTests ----

func nsbHelperHiding() {
	home := os.Getenv("NSB_HOME")
	t2 := os.Getenv("NSB_A")
	_, dbErr := os.Stat(filepath.Join(home, "factory", "factory.db"))
	cwd, _ := os.Getwd() // a failed Getwd shows up as an empty cwd, which the assertion rejects
	fetch := "fail"
	hc := &http.Client{Timeout: 3 * time.Second}
	if resp, err := hc.Get("http://example.com"); err == nil {
		_ = resp.Body.Close()
		fetch = "ok"
	}
	nsbEmit(map[string]any{"claude": nsbLsAny(filepath.Join(home, ".claude")), "fac": nsbLsAny(filepath.Join(home, "factory")),
		"db": dbErr == nil, "sib": nsbLsAny(t2), "fetch": fetch, "uid": os.Getuid(),
		"env": []*string{nsbEnvPtr("CLAUDE_CODE_OAUTH_TOKEN"), nsbEnvPtr("HTTPS_PROXY"), nsbEnvPtr("https_proxy")}, "cwd": cwd}, 7)
}

func TestSkillevalNsbox_HidingNoNetworkNoSecretsAndOutputReturned(t *testing.T) {
	nsbRequireUserns(t)
	nsbNotInGatebox(t)
	tmp := nsbShort(t)
	home := filepath.Join(tmp, "realhome")
	trials := filepath.Join(tmp, "trials")
	trial := filepath.Join(trials, "t1")
	nsbMkdirs(t, filepath.Join(home, ".claude"), filepath.Join(home, "factory"), trial, filepath.Join(trials, "t2"))
	nsbWrite(t, filepath.Join(home, ".claude", "secret.json"), "{}", 0o600)
	nsbWrite(t, filepath.Join(home, "factory", "factory.db"), "live", 0o600)
	nsbWrite(t, filepath.Join(trials, "t2", "secret"), "x", 0o600)
	cfg := nsbContainedCfg(t, trial, trial, home, trials, nil, "")
	env := append(os.Environ(), "CLAUDE_CODE_OAUTH_TOKEN="+nsbToken, "HTTPS_PROXY=http://127.0.0.1:3128",
		"NSB_MODE=hiding", "NSB_HOME="+home, "NSB_A="+filepath.Join(trials, "t2"))
	r := nsbContained(t, cfg, nsbHelperArgv(t), trial, env, 30*time.Second, nil)
	if r.ReturnCode != 7 {
		t.Fatalf("exit = %d, want 7\n%s", r.ReturnCode, r.Output)
	}
	if r.TimedOut {
		t.Error("timed out")
	}
	var obs struct {
		Claude, Fac, Sib any
		DB               bool `json:"db"`
		Fetch            string
		UID              int
		Env              []*string
		Cwd              string
	}
	nsbParseEmit(t, r.Output, &obs)
	if obs.Claude != "ERR" { // allow-list: ~/.claude is not bound back, so it does not exist
		t.Errorf("claude = %v, want ERR", obs.Claude)
	}
	if obs.Fac != "ERR" {
		t.Errorf("fac = %v, want ERR", obs.Fac)
	}
	if obs.DB {
		t.Error("factory.db is reachable")
	}
	if obs.Sib != "ERR" { // masked: the sibling dir does not exist
		t.Errorf("sib = %v, want ERR", obs.Sib)
	}
	if obs.Fetch != "fail" {
		t.Errorf("fetch = %q", obs.Fetch)
	}
	if obs.UID != os.Getuid() {
		t.Errorf("uid = %d", obs.UID)
	}
	if got := nsbStrs(obs.Env); !reflect.DeepEqual(got, []string{"<nil>", "<nil>", "<nil>"}) {
		t.Errorf("env = %v, want all unset", got)
	}
	if obs.Cwd != trial {
		t.Errorf("cwd = %q, want %q", obs.Cwd, trial)
	}
	if strings.Contains(r.Output, nsbToken) {
		t.Error("the token is in the output")
	}
}

func TestSkillevalNsbox_TimeoutKillsTheWholeTree(t *testing.T) {
	nsbRequireUserns(t)
	tmp := nsbShort(t)
	trials := filepath.Join(tmp, "trials")
	trial := filepath.Join(trials, "t1")
	nsbMkdirs(t, trial, filepath.Join(trials, "t2"), filepath.Join(tmp, "home"))
	marker := "zz-nsbox-sleeper-4242"
	// the durations are unique to this port so another run's sleepers are not mistaken for stragglers
	script := "MARK=" + marker + " sleep 3004242 & sh -c 'sleep 3014242 ; echo " + marker + "' & sleep 3004242"
	cfg := nsbContainedCfg(t, trial, trial, filepath.Join(tmp, "home"), trials, nil, "")
	t0 := time.Now()
	r := nsbContained(t, cfg, []string{"/bin/sh", "-c", script}, trial, nil, 2*time.Second, nil)
	if !r.TimedOut {
		t.Error("the run did not time out")
	}
	if el := time.Since(t0); el >= 20*time.Second {
		t.Errorf("took %v", el)
	}
	stragglers := func() []string {
		var hits []string
		for _, p := range nsbPids() {
			if c, err := os.ReadFile("/proc/" + p + "/cmdline"); err == nil &&
				(bytes.HasPrefix(c, []byte("sleep\x003004242")) || bytes.HasPrefix(c, []byte("sleep\x003014242")) || bytes.Contains(c, []byte(marker))) {
				hits = append(hits, p)
			}
		}
		return hits
	}
	if !nsbWaitFor(func() bool { return len(stragglers()) == 0 }, 5) {
		t.Errorf("left behind: %v", stragglers())
	}
}

// ---- CwdEscapeTests ----

// nsbHelperProbe is PROBE: what a checker started with an inherited cwd can reach through `..` and /proc/<pid>/cwd.
func nsbHelperProbe() {
	fac := os.Getenv("NSB_A")
	cwd, _ := os.Getwd() // an empty cwd fails the assertion that compares it
	out := map[string]any{"cwd": cwd, "up": nsbLsAny(".."), "sib": nsbLsAny("../t2"), "sibfile": nsbReadAny("../t2/sib-secret"),
		"p1": nsbLinkAny("/proc/1/cwd"), "p1ls": nsbLsAny("/proc/1/cwd"), "p1up": nsbLsAny("/proc/1/cwd/.."),
		"p1sib": nsbReadAny("/proc/1/cwd/../t2/sib-secret")}
	for _, pid := range nsbPids() {
		b, err := os.ReadFile("/proc/" + pid + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
		if len(args) == 3 && args[1] == "forward" { // the loopback forwarder: <self> forward <sock>
			out["fwd"] = map[string]any{"cwd": nsbLinkAny("/proc/" + pid + "/cwd"), "ls": nsbLsAny("/proc/" + pid + "/cwd"),
				"up": nsbLsAny("/proc/" + pid + "/cwd/.."), "sib": nsbReadAny("/proc/" + pid + "/cwd/../t2/sib-secret"),
				"root": nsbLsAny("/proc/" + pid + "/root/" + strings.TrimPrefix(fac, "/"))}
		}
	}
	nsbEmit(out, 0)
}

type nsbCwdFx struct {
	tmp, home, fac, trials, trial, wt, vis string
}

// nsbCwdFixture is CwdEscapeFixture: a fake real home whose factory dir holds the trials root, a sibling trial and a
// worktree, all hidden.
func nsbCwdFixture(t *testing.T) *nsbCwdFx {
	t.Helper()
	tmp := nsbShort(t)
	fx := &nsbCwdFx{tmp: tmp, home: filepath.Join(tmp, "realhome")}
	fx.fac = filepath.Join(fx.home, "factory")
	fx.trials = filepath.Join(fx.fac, "trials")
	fx.trial = filepath.Join(fx.trials, "t1")
	fx.wt = filepath.Join(fx.fac, "worktrees", "w1")
	fx.vis = filepath.Join(tmp, "vis") // visible, outside every hide dir and not above one
	nsbMkdirs(t, fx.trial, filepath.Join(fx.trials, "t2"), fx.wt, fx.vis)
	nsbWrite(t, filepath.Join(fx.trials, "t2", "sib-secret"), "SIBLING-SECRET", 0o600)
	nsbWrite(t, filepath.Join(fx.wt, "wt-secret"), "WORKTREE-SECRET", 0o600)
	return fx
}

func (fx *nsbCwdFx) cfg(t *testing.T, cwd string) *sandbox.Cfg {
	return nsbContainedCfg(t, fx.trial, cwd, fx.home, fx.trials, []string{fx.fac}, "")
}

// contained runs the probe: rc must be 0; returns what it saw.
func (fx *nsbCwdFx) contained(t *testing.T, cwd string) map[string]any {
	t.Helper()
	cfg := fx.cfg(t, cwd)
	r := nsbContained(t, cfg, nsbHelperArgv(t), cwd, nil, 30*time.Second, map[string]string{"NSB_MODE": "probe", "NSB_A": fx.fac})
	if r.ReturnCode != 0 {
		t.Fatalf("exit = %d\n%s", r.ReturnCode, r.Output)
	}
	var obs map[string]any
	nsbParseEmit(t, r.Output, &obs)
	return obs
}

func (fx *nsbCwdFx) assertContained(t *testing.T, obs map[string]any, where string) {
	t.Helper()
	blob, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SIBLING-SECRET", "WORKTREE-SECRET", "worktrees", `"t2"`, "wt-secret", "sib-secret"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("%s: %s reachable: %s", where, leak, blob)
		}
	}
	for k, want := range map[string]string{"sibfile": "ERR", "p1sib": "ERR", "cwd": fx.trial, "p1": fx.trial} {
		if obs[k] != want {
			t.Errorf("%s: %s = %v, want %q", where, k, obs[k], want)
		}
	}
}

func TestSkillevalNsbox_CheckerWithCwdInTrialCannotReachSiblingViaDotdotOrProc(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	fx.assertContained(t, fx.contained(t, fx.trial), "check cwd=trial")
}

func TestSkillevalNsbox_CwdUnderTheTrialDirIsAcceptedEvenInsideAHideDir(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	sub := filepath.Join(fx.trial, "work")
	nsbMkdirs(t, sub)
	obs := fx.contained(t, sub)
	if obs["cwd"] != sub || obs["sibfile"] != "ERR" {
		t.Errorf("cwd = %v sibfile = %v", obs["cwd"], obs["sibfile"])
	}
}

func TestSkillevalNsbox_CheckerWithNoCwdFromAHiddenDirLandsInTheTrial(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(fx.wt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("chdir back: %v", err)
		}
	})
	// cfg cwd None, the process cwd (inherited) is the hidden worktree
	cfg := fx.cfg(t, "")
	r := nsbContained(t, cfg, nsbHelperArgv(t), "", nil, 30*time.Second, map[string]string{"NSB_MODE": "probe", "NSB_A": fx.fac})
	if r.ReturnCode != 0 {
		t.Fatalf("exit = %d\n%s", r.ReturnCode, r.Output)
	}
	var obs map[string]any
	nsbParseEmit(t, r.Output, &obs)
	fx.assertContained(t, obs, "check cwd=None")
}

func TestSkillevalNsbox_VisibleCwdOutsideTheHiddenDirsIsKept(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	obs := fx.contained(t, fx.vis)
	if obs["cwd"] != fx.vis || obs["sibfile"] != "ERR" {
		t.Errorf("cwd = %v sibfile = %v", obs["cwd"], obs["sibfile"])
	}
}

func TestSkillevalNsbox_ShimClaudeAndForwarderCannotReachHiddenContent(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	claude := filepath.Join(fx.tmp, "bin", "claude")
	nsbMkdirs(t, filepath.Dir(claude))
	if err := os.Symlink(nsbSelf(t), claude); err != nil {
		t.Fatal(err)
	}
	cfg := nsbCfg(t, fx.trial, claude, fx.home, func(o *sandbox.Opts) {
		o.TrialsRoot, o.HideDirs, o.Forward, o.Unshare = fx.trials, []string{fx.fac}, true, nsbUnshare()
	})
	s1, err := sandbox.ComposeStage1(cfg, nsbHelperArgs)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(s1[0], s1[1:]...)
	var out bytes.Buffer
	cmd.Dir, cmd.Stdout, cmd.Stderr = fx.trial, &out, &out
	cmd.Env = []string{"PATH=/usr/bin:/bin", "NSB_MODE=probe", "NSB_A=" + fx.fac}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stage 1 failed: %v\n%s", err, out.String())
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill() // the run hung; report it
		t.Fatalf("timed out\n%s", out.String())
	}
	var obs map[string]any
	nsbParseEmit(t, out.String(), &obs)
	fx.assertContained(t, obs, "claude")
	fwd, ok := obs["fwd"].(map[string]any)
	if !ok {
		t.Fatalf("forwarder not found inside the namespace: %v", obs)
	}
	blob, err := json.Marshal(fwd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "t2") || strings.Contains(string(blob), "SIBLING-SECRET") {
		t.Errorf("the forwarder reaches hidden content: %s", blob)
	}
	if fwd["sib"] != "ERR" || fwd["root"] != "ERR" {
		t.Errorf("forwarder sib = %v root = %v, want ERR", fwd["sib"], fwd["root"])
	}
}

func TestSkillevalNsbox_SymlinkedHideDirIsHidden(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	link := filepath.Join(fx.tmp, "linkhome")
	if err := os.Symlink(fx.fac, link); err != nil {
		t.Fatal(err)
	}
	cfg := nsbContainedCfg(t, fx.trial, fx.vis, fx.home, fx.trials, []string{link}, "")
	r := nsbContained(t, cfg, nsbHelperArgv(t), fx.vis, nil, 30*time.Second,
		map[string]string{"NSB_MODE": "ls2", "NSB_A": fx.fac, "NSB_B": link})
	if r.ReturnCode != 0 {
		t.Fatalf("exit = %d\n%s", r.ReturnCode, r.Output)
	}
	var got [2][]string
	nsbParseEmit(t, r.Output, &got)
	want := []string{"trials"} // the real dir behind the symlink is hidden: only the kept trial path
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("the real dir lists %v, want %v", got[0], want)
	}
	if !reflect.DeepEqual(got[1], want) {
		t.Errorf("via the link: %v, want %v", got[1], want)
	}
}

func TestSkillevalNsbox_RunContainedDefaultEnvIsAnAllowList(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "msg-secret")
	t.Setenv("SOME_UNKNOWN_VAR", "x")
	t.Setenv("LC_ALL", "C.UTF-8")
	cfg := fx.cfg(t, fx.vis)
	r := nsbContained(t, cfg, nsbHelperArgv(t), fx.vis, nil, 30*time.Second,
		map[string]string{"NSB_MODE": "env", "EXTRA_OK": "1"})
	if r.ReturnCode != 0 {
		t.Fatalf("exit = %d\n%s", r.ReturnCode, r.Output)
	}
	var env map[string]string
	nsbParseEmit(t, r.Output, &env)
	for _, k := range []string{"CLAUDE_CODE_MESSAGING_TOKEN", "SOME_UNKNOWN_VAR"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s reached the target", k)
		}
	}
	if js, _ := json.Marshal(env); strings.Contains(string(js), "msg-secret") { // Marshal of a map[string]string cannot fail
		t.Error("msg-secret reached the target")
	}
	if env["HOME"] != fx.trial || env["LC_ALL"] != "C.UTF-8" || env["EXTRA_OK"] != "1" {
		t.Errorf("HOME=%q LC_ALL=%q EXTRA_OK=%q", env["HOME"], env["LC_ALL"], env["EXTRA_OK"])
	}
	if _, ok := env["PATH"]; !ok {
		t.Error("PATH is missing")
	}
}

// TestSkillevalNsbox_CwdAboveOrInsideAHideDirLandsInTheTrial is the Go-native counterpart of the Python refusal
// (test_cwd_equal_to_above_or_inside_a_hide_dir_is_refused): Go has no run_contained wrapper that refuses such a cwd;
// stage 1 itself never keeps one that is hidden and lands in the trial.
func TestSkillevalNsbox_CwdAboveOrInsideAHideDirLandsInTheTrial(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbCwdFixture(t)
	for _, cwd := range []string{fx.fac, fx.home, fx.wt, fx.trials, filepath.Join(fx.trials, "t2")} {
		t.Run(strings.TrimPrefix(cwd, fx.tmp), func(t *testing.T) {
			cfg := fx.cfg(t, "")
			cfg.Cwd = &cwd // the cwd is hidden: only `want` is set, nothing is bound for it
			r := nsbContained(t, cfg, nsbHelperArgv(t), cwd, nil, 30*time.Second, map[string]string{"NSB_MODE": "probe", "NSB_A": fx.fac})
			if r.ReturnCode != 0 {
				t.Fatalf("exit = %d\n%s", r.ReturnCode, r.Output)
			}
			var obs map[string]any
			nsbParseEmit(t, r.Output, &obs)
			fx.assertContained(t, obs, "cwd="+cwd)
		})
	}
}

// ---- RunContainedAllowListTests ----

func nsbHelperAllow() {
	home, trial, plugin := os.Getenv("NSB_HOME"), os.Getenv("NSB_TRIAL"), os.Getenv("NSB_PLUGIN")
	w := func(p string) any {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			var en syscall.Errno
			if errors.As(err, &en) {
				return int(en)
			}
			return err.Error()
		}
		return "written"
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	nsbEmit(map[string]any{"notes": exists(filepath.Join(home, "notes")), "home": nsbLsAny(home),
		"sib": exists(filepath.Join(filepath.Dir(trial), "t2")), "trial": w(filepath.Join(trial, "work", "out.txt")),
		"plugin_read": exists(filepath.Join(plugin, "SKILL.md")), "plugin_write": w(filepath.Join(plugin, "new.txt"))}, 0)
}

type nsbAllowFx struct{ tmp, home, trials, trial, plugin string }

func nsbAllowFixture(t *testing.T) *nsbAllowFx {
	t.Helper()
	tmp := nsbShort(t)
	fx := &nsbAllowFx{tmp: tmp, home: filepath.Join(tmp, "home")}
	fx.trials = filepath.Join(fx.home, "factory", "trials")
	fx.trial = filepath.Join(fx.trials, "t1")
	fx.plugin = filepath.Join(fx.home, "vendor", "plug")
	nsbMkdirs(t, filepath.Join(fx.trial, "work"), fx.plugin, filepath.Join(fx.home, "notes"), filepath.Join(fx.trials, "t2"))
	nsbWrite(t, filepath.Join(fx.home, "notes", "todo.txt"), "PRIVATE-NOTE", 0o644)
	nsbWrite(t, filepath.Join(fx.plugin, "SKILL.md"), "skill", 0o644)
	nsbWrite(t, filepath.Join(fx.trials, "t2", "secret"), "x", 0o600)
	return fx
}

func (fx *nsbAllowFx) run(t *testing.T, argv []string, vendor string, extra map[string]string) sandbox.Contained {
	t.Helper()
	cfg := nsbContainedCfg(t, fx.trial, filepath.Join(fx.trial, "work"), fx.home, fx.trials, nil, vendor)
	return nsbContained(t, cfg, argv, filepath.Join(fx.trial, "work"), nil, 30*time.Second, extra)
}

func TestSkillevalNsbox_UnlistedHomeSubdirIsAbsentTrialIsWritablePluginIsReadOnly(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbAllowFixture(t)
	r := fx.run(t, nsbHelperArgv(t), fx.plugin, map[string]string{"NSB_MODE": "allow", "NSB_HOME": fx.home, "NSB_TRIAL": fx.trial, "NSB_PLUGIN": fx.plugin})
	if r.ReturnCode != 0 {
		t.Fatalf("exit = %d\n%s", r.ReturnCode, r.Output)
	}
	var obs struct {
		Notes       bool     `json:"notes"`
		Home        []string `json:"home"`
		Sib         bool     `json:"sib"`
		Trial       any      `json:"trial"`
		PluginRead  bool     `json:"plugin_read"`
		PluginWrite any      `json:"plugin_write"`
	}
	nsbParseEmit(t, r.Output, &obs)
	if obs.Notes {
		t.Error("an unlisted home subdir is visible")
	}
	if !reflect.DeepEqual(obs.Home, []string{"factory", "vendor"}) { // only the path prefixes of what is bound back
		t.Errorf("home = %v", obs.Home)
	}
	if obs.Sib {
		t.Error("the sibling trial is visible")
	}
	if obs.Trial != "written" {
		t.Errorf("trial write = %v", obs.Trial)
	}
	if !nsbExists(filepath.Join(fx.trial, "work", "out.txt")) {
		t.Error("the write did not reach the trial dir")
	}
	if !obs.PluginRead {
		t.Error("the plugin is not readable")
	}
	if obs.PluginWrite != float64(syscall.EROFS) { // 30
		t.Errorf("plugin write = %v, want EROFS (30)", obs.PluginWrite)
	}
	if nsbExists(filepath.Join(fx.plugin, "new.txt")) {
		t.Error("the plugin dir was written to")
	}
}

func TestSkillevalNsbox_PluginIsNotVisibleWhenNotListed(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbAllowFixture(t)
	r := fx.run(t, []string{"/bin/sh", "-c", "if [ -e '" + fx.plugin + "' ]; then echo True; else echo False; fi"}, "", nil)
	if r.ReturnCode != 0 {
		t.Fatalf("exit = %d\n%s", r.ReturnCode, r.Output)
	}
	if got := strings.TrimSpace(r.Output); got != "False" {
		t.Errorf("output = %q, want False", got)
	}
}

func TestSkillevalNsbox_FailClosedWhenABindSourceIsMissing(t *testing.T) {
	nsbRequireUserns(t)
	fx := nsbAllowFixture(t)
	marker := filepath.Join(fx.trial, "ran")
	r := fx.run(t, []string{"/bin/sh", "-c", "echo x > '" + marker + "'"}, filepath.Join(fx.home, "vendor", "gone"), nil)
	if r.ReturnCode == 0 {
		t.Error("exit = 0 with a missing bind source")
	}
	if !strings.Contains(r.Output, "gone") {
		t.Errorf("output = %q, want it to name the missing path", r.Output)
	}
	if nsbExists(marker) {
		t.Error("the target must not run with a partial view")
	}
}
