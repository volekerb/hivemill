package macfacts

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu    sync.Mutex
	res   map[string]Result // keyed by "name arg arg"
	calls []string
	sleep chan struct{} // when set, Run blocks until closed (ignores ctx)
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) Result {
	key := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if f.sleep != nil {
		<-f.sleep
	}
	if r, ok := f.res[key]; ok {
		return r
	}
	return Result{Exit: 127, Err: errors.New("not found"), Stderr: "fake: no such command"}
}

// Token-shaped fixtures are built at run time so the source holds no secret-like literal.
var (
	ghTok  = "gh" + "o_" + strings.Repeat("Ab1", 10)
	antTok = "sk-" + "ant-" + "api03-" + strings.Repeat("Zy_", 5)
	hexTok = strings.Repeat("0123456789", 4) + "abcd"
)

func testEnv(r Runner) *Env {
	return &Env{
		Runner: r, Home: "/Users/tester", TempDir: "/tmp/x", Uid: 501,
		Timeout: time.Second,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		LookPath: func(n string) (string, error) {
			if n == "sandbox-exec" || n == "git" {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Stat:   func(string) (bool, string) { return false, "no such file" },
		Fstype: func(string) (string, error) { return "apfs", nil },
	}
}

func TestProbeList(t *testing.T) {
	want := []string{
		"sw_vers", "uname_m", "sandbox_exec_allow_default", "sandbox_exec_pid_replace",
		"sandbox_exec_nested", "ls_ld_home_dirs", "ls_l_tmp", "statfs_private_tmp", "vm_stat",
		"sysctl_values", "ps_first5", "ps_rss_top3", "lsof_presence", "claude_login_location",
		"gh_auth_status", "dscl_users_ge500", "pmset_sleep", "tool_versions", "herdr_codesign",
		"herdr_xattr", "id_u", "launchctl_print_gui",
	}
	got := ProbeNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probe names\n got %v\nwant %v", got, want)
	}
	for _, n := range got {
		t.Log("probe:", n)
	}
}

func TestProbeCommands(t *testing.T) {
	f := &fakeRunner{res: map[string]Result{}}
	e := testEnv(f)
	e.HerdrPath = "/opt/herdr"
	rep := Render(e, Collect(context.Background(), e, Probes()))
	for _, want := range []string{
		"sw_vers", "uname -m",
		"/usr/bin/sandbox-exec -p (version 1)(allow default) /usr/bin/true",
		"ls -ld /Users/tester /Users/tester/.claude /Users/tester/.ssh /Users/tester/Library/Keychains /Users/Shared",
		"ls -l /tmp", "vm_stat", "sysctl -n kern.memorystatus_vm_pressure_level",
		"ps -axo rss=,command= -m", "codesign -dv /opt/herdr", "xattr -l /opt/herdr",
		"id -u", "launchctl print gui/501", "statfs /private/tmp",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	for _, c := range f.calls {
		for _, bad := range []string{"sudo", "rm ", "chmod", "bootstrap"} {
			if strings.Contains(c, bad) {
				t.Errorf("forbidden command run: %s", c)
			}
		}
	}
}

func TestRedact(t *testing.T) {
	cases := []string{
		"token " + ghTok + " end",
		"key " + antTok + " end",
		"hash " + hexTok + " end",
	}
	for _, in := range cases {
		got := Redact(in)
		if !strings.Contains(got, "<redacted>") || got == in {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
	if got := Redact("plain text 1234 abc"); got != "plain text 1234 abc" {
		t.Errorf("over-redacted: %q", got)
	}
	// secrets in probe output never reach the report
	f := &fakeRunner{res: map[string]Result{"sw_vers": {Stdout: ghTok}}}
	e := testEnv(f)
	rep := Render(e, Collect(context.Background(), e, Probes()[:1]))
	if strings.Contains(rep, ghTok) || !strings.Contains(rep, "<redacted>") {
		t.Errorf("report leaked token:\n%s", rep)
	}
}

func TestKeychainKeepsNoOutput(t *testing.T) {
	secret := "pass" + "word: hunter2"
	f := &fakeRunner{res: map[string]Result{
		"security find-generic-password -s Claude Code-credentials": {Stdout: secret, Stderr: secret, Exit: 0},
	}}
	e := testEnv(f)
	rep := Render(e, Collect(context.Background(), e, []Probe{{PClaudeLogin, probeClaudeLogin}}))
	if strings.Contains(rep, "hunter2") {
		t.Fatalf("keychain output stored:\n%s", rep)
	}
	if !strings.Contains(rep, "Exit code: 0") || !strings.Contains(rep, "exit code only") {
		t.Errorf("exit code missing:\n%s", rep)
	}
	if !strings.Contains(rep, "exists=false") {
		t.Errorf("credentials file existence missing:\n%s", rep)
	}
}

func TestTimeout(t *testing.T) {
	f := &fakeRunner{sleep: make(chan struct{})}
	defer close(f.sleep)
	e := testEnv(f)
	e.Timeout = 50 * time.Millisecond
	start := time.Now()
	secs := Collect(context.Background(), e, []Probe{{PSwVers, cmdProbe("sw_vers")}})
	if time.Since(start) > 2*time.Second {
		t.Fatal("probe was not cut")
	}
	if len(secs) != 1 || !secs[0].Steps[0].TimedOut {
		t.Fatalf("not recorded as timed out: %+v", secs)
	}
	if !strings.Contains(Render(e, secs), "(timed out)") {
		t.Error("report does not say timed out")
	}
}

func TestProbeFails(t *testing.T) {
	f := &fakeRunner{res: map[string]Result{}} // every command "not found"
	e := testEnv(f)
	probes := append(Probes(), Probe{"panics", func(context.Context, *Env) []Step { panic("boom") }})
	secs := Collect(context.Background(), e, probes)
	if len(secs) != len(probes) {
		t.Fatalf("got %d sections, want %d", len(secs), len(probes))
	}
	rep := Render(e, secs)
	if !strings.Contains(rep, "Exit code: 127") || !strings.Contains(rep, "panic: boom") {
		t.Errorf("failures not recorded:\n%s", rep)
	}
}

func TestFilters(t *testing.T) {
	f := &fakeRunner{res: map[string]Result{
		"dscl . -list /Users UniqueID": {Stdout: "root 0\n_spotlight 89\nalice 501\nbob 502\n"},
		"pmset -g":                     {Stdout: "System-wide power settings:\n sleep 1\n womp 1\n autorestart 0\n"},
		"gh auth status":               {Stderr: "github.com\n  ✓ Logged in to github.com account a (keyring)\n  - Token: " + ghTok + "\n"},
		"ps -axo pid=,ppid=,uid=,lstart=,command=": {Stdout: "1\n2\n3\n4\n5\n6\n7\n"},
	}}
	e := testEnv(f)
	rep := Render(e, Collect(context.Background(), e, []Probe{{PDsclUsers, probeDscl}, {PPmset, probePmset}, {PGhAuth, probeGhAuth}, {PPsFirst, probePsFirst}}))
	for _, want := range []string{"alice 501", "bob 502", "sleep 1", "autorestart 0", "(keyring)"} {
		if !strings.Contains(rep, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"root 0", "_spotlight", "womp", "gho_", "\n6\n"} {
		if strings.Contains(rep, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, rep)
		}
	}
}

func TestTruncation(t *testing.T) {
	f := &fakeRunner{res: map[string]Result{"vm_stat": {Stdout: strings.Repeat("z", 10000)}}}
	e := testEnv(f)
	rep := Render(e, Collect(context.Background(), e, []Probe{{PVmStat, cmdProbe("vm_stat")}}))
	if n := strings.Count(rep, "z"); n < 4096 || n > 4200 || !strings.Contains(rep, "[truncated]") {
		t.Errorf("z count %d, truncated=%v", n, strings.Contains(rep, "[truncated]"))
	}
}
