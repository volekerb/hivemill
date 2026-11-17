package macbox

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeNode struct {
	dir  bool
	mode os.FileMode
	uid  int
}

type fakeInfo struct {
	name string
	n    *fakeNode
}

func (f fakeInfo) Name() string { return f.name }
func (f fakeInfo) Size() int64  { return 0 }
func (f fakeInfo) Mode() os.FileMode {
	if f.n.dir {
		return fs.ModeDir | f.n.mode
	}
	return f.n.mode
}
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.n.dir }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Uid: uint32(f.n.uid)} }

// fakeRunner models the owner's filesystem and the launch chain
// sudo -> launcher -> /usr/bin/sandbox-exec -> /bin/sh. Probe answers depend
// on the files really present, so a probe of a missing canary is DENIED (the
// vacuous case the preflight must avoid by creating it).
type fakeRunner struct {
	files    map[string]*fakeNode
	accounts map[string]bool
	sudoFail map[string]bool
	// chainBroken makes the launcher refuse every call (exit 3, no output).
	chainBroken bool
	// seatbeltLeaks are paths the profile wrongly lets the worker read.
	seatbeltLeaks map[string]bool
	// seatbeltDenyClone makes the profile deny the clone (read and write).
	seatbeltDenyClone bool
	// workerReadable are owner-home paths the filesystem view lets workers read.
	workerReadable map[string]bool
	// controlUnreadable hides FactoryHome and the clone from the worker view.
	controlUnreadable bool
	// roWritable are dirs where _hivemill_ro can create files without a profile.
	roWritable map[string]bool
	// seenCanary records canaries that existed when a probe read them.
	seenCanary map[string]bool
	calls      []string
}

func (f *fakeRunner) Run(name string, args ...string) (int, string, string) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if name == "/usr/bin/dscl" {
		if f.accounts[strings.TrimPrefix(args[2], "/Users/")] {
			return 0, "", ""
		}
		return 56, "", "eDSRecordNotFound"
	}
	if name != DefaultSudo || len(args) < 3 || args[0] != "-n" || args[1] != "-u" {
		return 127, "", "unexpected command " + name
	}
	account := args[2]
	if f.sudoFail[account] {
		return 1, "", "sudo: a password is required"
	}
	rest := args[3:]
	meta := 0
	for len(rest) > 0 && strings.Contains(rest[0], "=") {
		meta++
		rest = rest[1:]
	}
	if len(rest) < 1 || rest[0] != DefaultLauncher {
		return 1, "", "sudo: not allowed to run " + strings.Join(rest, " ")
	}
	if f.chainBroken {
		return 3, "", "macbox launcher: refusing"
	}
	launchArgv, err := SplitLauncherArgv(rest[1:])
	if err != nil || meta != len(LauncherMetaEnv) {
		return 2, "", fmt.Sprintf("macbox launcher: %v meta=%d", err, meta)
	}
	profile, prog := launchArgv[2], launchArgv[3:]
	if n, ok := f.files[profile]; !ok || n.dir {
		return 65, "", "sandbox-exec: cannot read profile " + profile
	}
	open := !strings.HasSuffix(profile, "/profile.sb")
	if prog[0] == "/usr/bin/true" {
		return 0, "", ""
	}
	if prog[0] != "/bin/sh" || prog[1] != "-c" {
		return 127, "", "unexpected program"
	}
	script, params := prog[2], prog[4:]
	switch script {
	case readScript, writeScript:
		p := params[0]
		clone := strings.HasPrefix(p, baseClone)
		var allowed bool
		if clone {
			allowed = !f.seatbeltDenyClone && (script == readScript || account == AccountRW)
		} else {
			allowed = f.seatbeltLeaks[p]
		}
		if script == readScript {
			_, exists := f.files[p]
			if exists {
				f.seenCanary[p] = true
			}
			allowed = allowed && exists
		}
		if allowed {
			return 0, "ALLOWED\n", ""
		}
		return 0, "DENIED\n", ""
	case viewScript:
		if !open {
			return 1, "", "view probe must run under the open profile"
		}
		var out strings.Builder
		if !f.controlUnreadable {
			out.WriteString("CONTROL-OK\n")
		}
		for _, p := range params[1:] {
			if f.workerReadable[p] {
				fmt.Fprintf(&out, "READABLE %s\n", p)
			}
		}
		out.WriteString("PROBE-DONE\n")
		return 0, out.String(), ""
	case aclScript:
		if account != AccountRO || !open {
			return 1, "", "acl probe must run as _hivemill_ro under the open profile"
		}
		var out strings.Builder
		if !f.controlUnreadable {
			out.WriteString("CONTROL-OK\n")
		}
		for _, d := range params[1:] {
			if f.roWritable[d] {
				fmt.Fprintf(&out, "WROTE %s\n", d)
			}
		}
		out.WriteString("PROBE-DONE\n")
		return 0, out.String(), ""
	}
	return 127, "", "unknown probe script"
}

func (f *fakeRunner) Stat(name string) (os.FileInfo, error) {
	n, ok := f.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
	}
	return fakeInfo{name: path.Base(name), n: n}, nil
}

func (f *fakeRunner) ReadDir(name string) ([]string, error) {
	if n, ok := f.files[name]; !ok || !n.dir {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	var out []string
	for p := range f.files {
		if path.Dir(p) == name && p != name {
			out = append(out, path.Base(p))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeRunner) WriteFile(name string, _ []byte, perm os.FileMode) error {
	if n, ok := f.files[path.Dir(name)]; !ok || !n.dir {
		return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f.files[name] = &fakeNode{mode: perm, uid: 501}
	return nil
}

func (f *fakeRunner) Remove(name string) error {
	if _, ok := f.files[name]; !ok {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
	}
	delete(f.files, name)
	return nil
}

const (
	baseHome  = "/Users/o"
	baseClone = "/Users/o/factory/worktrees/x"
	baseAgent = "/Users/o/.local/a"
)

func baseProfileIn() ProfileInput {
	return ProfileInput{
		Role: RoleCoder, OwnerHome: baseHome, FactoryHome: "/Users/o/factory",
		AgentDir: baseAgent, CloneDir: baseClone, CloneWritable: true,
	}
}

func baseSpec() Spec {
	return Spec{
		GOOS: "darwin", OwnerUID: 501, OwnerHome: baseHome, FactoryHome: "/Users/o/factory",
		Role: "coder", Launcher: DefaultLauncher, ProfilePath: baseAgent + "/profile.sb",
		CloneDir: baseClone, AgentDir: baseAgent,
		OAuthPath: "/Users/o/.config/factory/cred-oauth",
		GhPath:    "/Users/o/.config/factory/cred-gh-rw",
		ProfileIn: baseProfileIn(),
	}
}

func goodRunner() *fakeRunner {
	dir := func() *fakeNode { return &fakeNode{dir: true, mode: 0o755, uid: 501} }
	return &fakeRunner{
		accounts: map[string]bool{AccountRO: true, AccountRW: true},
		files: map[string]*fakeNode{
			"/usr/bin/sandbox-exec":               {mode: 0o755},
			baseHome:                              dir(),
			baseHome + "/factory":                 dir(),
			baseHome + "/factory/worktrees":       dir(),
			baseClone:                             dir(),
			baseClone + "/.git":                   dir(),
			baseClone + "/.git/hooks":             dir(),
			baseHome + "/.local":                  dir(),
			baseAgent:                             dir(),
			baseAgent + "/profile.sb":             {mode: 0o644, uid: 501},
			baseHome + "/.config":                 dir(),
			baseHome + "/.config/factory":         dir(),
			"/Users/o/.config/factory/cred-oauth": {mode: 0o600, uid: 501},
			"/Users/o/.config/factory/cred-gh-rw": {mode: 0o600, uid: 501},
			baseHome + "/Library":                 dir(),
			baseHome + "/Library/Keychains":       dir(),
			baseHome + "/Documents":               dir(),
		},
		seenCanary: map[string]bool{},
	}
}

func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	ref, ok := AsRefusal(err)
	if !ok || ref.Reason != reason {
		t.Fatalf("got %v, want a %q refusal", err, reason)
	}
}

func TestNonDarwinOS(t *testing.T) {
	for _, goos := range []string{"linux", "freebsd", "windows"} {
		s := baseSpec()
		s.GOOS = goos
		r := goodRunner()
		wantReason(t, Preflight(s, r), ReasonOS)
		if len(r.calls) != 0 {
			t.Fatalf("%s: non-darwin must refuse before running anything: %v", goos, r.calls)
		}
	}
}

func TestPreflight(t *testing.T) {
	cases := []struct {
		name   string
		reason string // "" means accepted
		spec   func(*Spec)
		runner func(*fakeRunner)
	}{
		{name: "os/refused", reason: ReasonOS, spec: func(s *Spec) { s.GOOS = "linux" }},
		{name: "os/accepted"},
		{name: "user/refused/missing-account", reason: ReasonUser, runner: func(r *fakeRunner) { delete(r.accounts, AccountRO) }},
		{name: "user/refused/unsandboxed-role", reason: ReasonUser, spec: func(s *Spec) { s.Role = "judge" }},
		{name: "user/accepted"},
		{name: "sudo/refused", reason: ReasonSudo, runner: func(r *fakeRunner) { r.sudoFail = map[string]bool{AccountRW: true} }},
		{name: "sudo/refused/ro-account", reason: ReasonSudo, runner: func(r *fakeRunner) { r.sudoFail = map[string]bool{AccountRO: true} }},
		{name: "sudo/refused/launcher-refuses", reason: ReasonSudo, runner: func(r *fakeRunner) { r.chainBroken = true }},
		{name: "sudo/refused/no-agent-dir", reason: ReasonSudo, runner: func(r *fakeRunner) { delete(r.files, baseAgent) }},
		{name: "sudo/accepted"},
		{name: "seatbelt/refused/no-sandbox-exec", reason: ReasonSeatbelt, runner: func(r *fakeRunner) { delete(r.files, SandboxExec) }},
		{name: "seatbelt/refused/no-profile", reason: ReasonSeatbelt, runner: func(r *fakeRunner) { delete(r.files, baseAgent+"/profile.sb") }},
		{name: "seatbelt/refused/canary-leaks", reason: ReasonSeatbelt, runner: func(r *fakeRunner) {
			r.seatbeltLeaks = map[string]bool{baseHome + "/.hivemill-selftest-canary": true}
		}},
		{name: "seatbelt/refused/keychain-leaks", reason: ReasonSeatbelt, runner: func(r *fakeRunner) {
			r.seatbeltLeaks = map[string]bool{baseHome + "/Library/Keychains/.hivemill-selftest-canary": true}
		}},
		{name: "seatbelt/refused/token-readable", reason: ReasonSeatbelt, runner: func(r *fakeRunner) {
			r.seatbeltLeaks = map[string]bool{"/Users/o/.config/factory/cred-gh-rw": true}
		}},
		{name: "seatbelt/refused/clone-denied", reason: ReasonSeatbelt, runner: func(r *fakeRunner) { r.seatbeltDenyClone = true }},
		{name: "seatbelt/refused/canary-dir-missing", reason: ReasonSeatbelt, runner: func(r *fakeRunner) { delete(r.files, baseHome+"/Library/Keychains") }},
		{name: "seatbelt/accepted"},
		{name: "view/refused", reason: ReasonView, runner: func(r *fakeRunner) {
			r.workerReadable = map[string]bool{baseHome + "/Documents": true}
		}},
		{name: "view/refused/home-listable", reason: ReasonView, runner: func(r *fakeRunner) {
			r.workerReadable = map[string]bool{baseHome: true}
		}},
		{name: "view/refused/token-dir", reason: ReasonView, runner: func(r *fakeRunner) {
			r.workerReadable = map[string]bool{baseHome + "/.config": true}
		}},
		{name: "view/refused/control-unreadable", reason: ReasonView, runner: func(r *fakeRunner) { r.controlUnreadable = true }},
		{name: "view/refused/home-unlistable-by-owner", reason: ReasonView, spec: func(s *Spec) {
			s.OwnerHome = "/Users/nobody"
		}},
		{name: "view/accepted", runner: func(r *fakeRunner) {
			// Allow-listed entries (factory, the agent dir's ancestor) may be readable.
			r.workerReadable = map[string]bool{baseHome + "/factory": true, baseHome + "/.local": true}
		}},
		{name: "acl/refused", reason: ReasonACL, runner: func(r *fakeRunner) {
			r.roWritable = map[string]bool{baseClone + "/.git/hooks": true}
		}},
		{name: "acl/refused/clone", reason: ReasonACL, runner: func(r *fakeRunner) {
			r.roWritable = map[string]bool{baseClone: true}
		}},
		{name: "acl/refused/hooks-missing", reason: ReasonACL, runner: func(r *fakeRunner) { delete(r.files, baseClone+"/.git/hooks") }},
		{name: "acl/refused/token-mode", reason: ReasonACL, runner: func(r *fakeRunner) {
			r.files["/Users/o/.config/factory/cred-gh-rw"].mode = 0o644
		}},
		{name: "acl/refused/token-owner", reason: ReasonACL, runner: func(r *fakeRunner) {
			r.files["/Users/o/.config/factory/cred-oauth"].uid = 502
		}},
		{name: "acl/refused/token-missing", reason: ReasonACL, runner: func(r *fakeRunner) {
			delete(r.files, "/Users/o/.config/factory/cred-oauth")
		}},
		{name: "acl/refused/owner-uid-unset", reason: ReasonACL, spec: func(s *Spec) { s.OwnerUID = 0 }},
		{name: "acl/accepted"},
		{name: "accepted/reviewer", spec: func(s *Spec) {
			s.Role = "reviewer"
			s.ProfileIn.Role, s.ProfileIn.CloneWritable = RoleReviewer, false
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := baseSpec()
			if c.spec != nil {
				c.spec(&s)
			}
			r := goodRunner()
			if c.runner != nil {
				c.runner(r)
			}
			err := Preflight(s, r)
			if c.reason == "" {
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			wantReason(t, err, c.reason)
		})
	}
}

// TestPreflightAcceptedRealChain checks the all-pass run is not vacuous: every
// canary existed when it was probed, every probe went through sudo and the
// launcher, and nothing the preflight created is left behind.
func TestPreflightAcceptedRealChain(t *testing.T) {
	r := goodRunner()
	before := goodRunner().files
	if err := Preflight(baseSpec(), r); err != nil {
		t.Fatalf("all-pass: %v", err)
	}
	for _, p := range SelfTestPlan(baseProfileIn()) {
		if p.SetupAsOwner && !r.seenCanary[p.Path] {
			t.Errorf("canary %s did not exist when probed", p.Path)
		}
	}
	for p := range r.files {
		if _, ok := before[p]; !ok {
			t.Errorf("preflight left %s behind", p)
		}
	}
	if len(r.files) != len(before) {
		t.Errorf("file count %d, want %d", len(r.files), len(before))
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, DefaultSudo) && !strings.Contains(c, DefaultLauncher+" -- "+SandboxExec+" -f ") {
			t.Errorf("sudo call bypasses the launcher chain: %s", c)
		}
	}
}

// TestPreflightCleansUpOnRefusal: a refused run removes its canaries too.
func TestPreflightCleansUpOnRefusal(t *testing.T) {
	r := goodRunner()
	r.seatbeltLeaks = map[string]bool{baseHome + "/.hivemill-selftest-canary": true}
	wantReason(t, Preflight(baseSpec(), r), ReasonSeatbelt)
	for p := range r.files {
		if strings.Contains(p, ".hivemill-") {
			t.Errorf("left behind %s", p)
		}
	}
}

func TestViewAllowed(t *testing.T) {
	allowed := []string{"/Users/o/factory", "/Users/o/.local/state/factory/workers/a"}
	for p, want := range map[string]bool{
		"/Users/o/factory":     true,
		"/Users/o/.local":      true,
		"/Users/o/factoryx":    false,
		"/Users/o/.ssh":        false,
		"/Users/o/.config":     false,
		"/Users/o/.localstate": false,
	} {
		if got := viewAllowed(p, allowed); got != want {
			t.Errorf("viewAllowed(%s) = %v, want %v", p, got, want)
		}
	}
}
