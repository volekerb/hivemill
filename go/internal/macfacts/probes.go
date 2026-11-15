package macfacts

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const sandboxProfile = "(version 1)(allow default)"

// Probe names, in report order.
const (
	PSwVers        = "sw_vers"
	PUnameM        = "uname_m"
	PSandboxAllow  = "sandbox_exec_allow_default"
	PSandboxPid    = "sandbox_exec_pid_replace"
	PSandboxNested = "sandbox_exec_nested"
	PLsDirs        = "ls_ld_home_dirs"
	PTmpLink       = "ls_l_tmp"
	PTmpFstype     = "statfs_private_tmp"
	PVmStat        = "vm_stat"
	PSysctl        = "sysctl_values"
	PPsFirst       = "ps_first5"
	PPsRss         = "ps_rss_top3"
	PLsof          = "lsof_presence"
	PClaudeLogin   = "claude_login_location"
	PGhAuth        = "gh_auth_status"
	PDsclUsers     = "dscl_users_ge500"
	PPmset         = "pmset_sleep"
	PToolVersions  = "tool_versions"
	PHerdrCodesign = "herdr_codesign"
	PHerdrXattr    = "herdr_xattr"
	PIdU           = "id_u"
	PLaunchctlGui  = "launchctl_print_gui"
)

// Probes returns every probe in report order.
func Probes() []Probe {
	return []Probe{
		{PSwVers, cmdProbe("sw_vers")},
		{PUnameM, cmdProbe("uname", "-m")},
		{PSandboxAllow, probeSandboxAllow},
		{PSandboxPid, probeSandboxPid},
		{PSandboxNested, probeSandboxNested},
		{PLsDirs, probeLsDirs},
		{PTmpLink, cmdProbe("ls", "-l", "/tmp")},
		{PTmpFstype, probeFstype},
		{PVmStat, cmdProbe("vm_stat")},
		{PSysctl, probeSysctl},
		{PPsFirst, probePsFirst},
		{PPsRss, probePsRss},
		{PLsof, probeLsof},
		{PClaudeLogin, probeClaudeLogin},
		{PGhAuth, probeGhAuth},
		{PDsclUsers, probeDscl},
		{PPmset, probePmset},
		{PToolVersions, probeToolVersions},
		{PHerdrCodesign, probeHerdr("codesign", "-dv")},
		{PHerdrXattr, probeHerdr("xattr", "-l")},
		{PIdU, cmdProbe("id", "-u")},
		{PLaunchctlGui, probeLaunchctl},
	}
}

// ProbeNames lists the probe names in report order.
func ProbeNames() []string {
	var n []string
	for _, p := range Probes() {
		n = append(n, p.Name)
	}
	return n
}

func combined(r Result) string { return r.Stdout + r.Stderr }

func step(cmd string, r Result, out string) Step {
	s := Step{Cmd: cmd, Exit: r.Exit, Out: out, TimedOut: r.TimedOut}
	if r.Err != nil {
		s.Err = r.Err.Error()
	}
	return s
}

func runStep(ctx context.Context, e *Env, name string, args ...string) Step {
	r := e.Runner.Run(ctx, name, args...)
	return step(strings.Join(append([]string{name}, args...), " "), r, combined(r))
}

func cmdProbe(name string, args ...string) func(context.Context, *Env) []Step {
	return func(ctx context.Context, e *Env) []Step { return []Step{runStep(ctx, e, name, args...)} }
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func filterLines(s string, keep func(string) bool) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if keep(l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func probeSandboxAllow(ctx context.Context, e *Env) []Step {
	path, err := e.LookPath("sandbox-exec")
	steps := []Step{{Cmd: "which sandbox-exec", Exit: boolExit(err == nil), Out: path}}
	if err != nil {
		steps[0].Err = err.Error()
		return steps
	}
	return append(steps, runStep(ctx, e, path, "-p", sandboxProfile, "/usr/bin/true"))
}

const pidScript = `echo "shell pid=$$"; ps -o pid=,ppid=,comm= -p $$`

func probeSandboxPid(ctx context.Context, e *Env) []Step {
	return []Step{runStep(ctx, e, "sandbox-exec", "-p", sandboxProfile, "/bin/sh", "-c", pidScript)}
}

func probeSandboxNested(ctx context.Context, e *Env) []Step {
	return []Step{runStep(ctx, e, "sandbox-exec", "-p", sandboxProfile,
		"sandbox-exec", "-p", sandboxProfile, "/bin/sh", "-c", pidScript)}
}

func probeLsDirs(ctx context.Context, e *Env) []Step {
	return []Step{runStep(ctx, e, "ls", "-ld", e.Home,
		filepath.Join(e.Home, ".claude"), filepath.Join(e.Home, ".ssh"),
		filepath.Join(e.Home, "Library", "Keychains"), "/Users/Shared")}
}

func probeFstype(ctx context.Context, e *Env) []Step {
	t, err := e.Fstype("/private/tmp")
	s := Step{Cmd: "statfs /private/tmp (Fstypename)", Out: t}
	if err != nil {
		s.Exit, s.Err = 1, err.Error()
	}
	return []Step{s}
}

func probeSysctl(ctx context.Context, e *Env) []Step {
	var steps []Step
	for _, k := range []string{"vm.loadavg", "vm.swapusage", "hw.memsize", "hw.ncpu",
		"hw.perflevel0.logicalcpu", "hw.perflevel1.logicalcpu", "kern.memorystatus_vm_pressure_level"} {
		steps = append(steps, runStep(ctx, e, "sysctl", "-n", k))
	}
	return steps
}

func probePsFirst(ctx context.Context, e *Env) []Step {
	r := e.Runner.Run(ctx, "ps", "-axo", "pid=,ppid=,uid=,lstart=,command=")
	return []Step{step("ps -axo pid=,ppid=,uid=,lstart=,command= (first 5 lines)", r, firstLines(combined(r), 5))}
}

func probePsRss(ctx context.Context, e *Env) []Step {
	r := e.Runner.Run(ctx, "ps", "-axo", "rss=,command=", "-m")
	return []Step{step("ps -axo rss=,command= -m (first 3 lines)", r, firstLines(combined(r), 3))}
}

func probeLsof(ctx context.Context, e *Env) []Step {
	path, err := e.LookPath("lsof")
	steps := []Step{{Cmd: "which lsof", Exit: boolExit(err == nil), Out: path}}
	if err != nil {
		steps[0].Err = err.Error()
		return steps
	}
	return append(steps, runStep(ctx, e, path, "-v"))
}

func boolExit(ok bool) int {
	if ok {
		return 0
	}
	return 1
}

// probeClaudeLogin records only the exit code of the Keychain lookup; its
// stdout and stderr are discarded and never stored.
func probeClaudeLogin(ctx context.Context, e *Env) []Step {
	r := e.Runner.Run(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials")
	kc := Step{Cmd: `security find-generic-password -s "Claude Code-credentials"`, Exit: r.Exit,
		TimedOut: r.TimedOut, Out: "(output discarded; exit code only)"}
	if r.Err != nil {
		kc.Err = "could not run"
	}
	exists, detail := e.Stat(filepath.Join(e.Home, ".claude", ".credentials.json"))
	f := Step{Cmd: "stat ~/.claude/.credentials.json (existence only, contents not read)",
		Exit: boolExit(exists), Out: fmt.Sprintf("exists=%t %s", exists, detail)}
	return []Step{kc, f}
}

var storageLine = regexp.MustCompile(`(?i)keyring|hosts\.yml|\(file\)|storage`)

func probeGhAuth(ctx context.Context, e *Env) []Step {
	r := e.Runner.Run(ctx, "gh", "auth", "status")
	out := filterLines(combined(r), func(l string) bool { return storageLine.MatchString(l) })
	return []Step{step("gh auth status (only lines naming the storage)", r, out)}
}

func probeDscl(ctx context.Context, e *Env) []Step {
	r := e.Runner.Run(ctx, "dscl", ".", "-list", "/Users", "UniqueID")
	out := filterLines(r.Stdout, func(l string) bool {
		f := strings.Fields(l)
		if len(f) != 2 {
			return false
		}
		id, err := strconv.Atoi(f[1])
		return err == nil && id >= 500
	})
	return []Step{step("dscl . -list /Users UniqueID (ids >= 500)", r, out)}
}

func probePmset(ctx context.Context, e *Env) []Step {
	r := e.Runner.Run(ctx, "pmset", "-g")
	out := filterLines(combined(r), func(l string) bool {
		l = strings.ToLower(l)
		return strings.Contains(l, "sleep") || strings.Contains(l, "autorestart")
	})
	return []Step{step("pmset -g (sleep/autorestart lines)", r, out)}
}

func probeToolVersions(ctx context.Context, e *Env) []Step {
	var steps []Step
	for _, t := range [][]string{{"python3", "--version"}, {"git", "--version"}, {"go", "version"},
		{"herdr", "--version"}, {"hc", "--version"}} {
		path, err := e.LookPath(t[0])
		if err != nil {
			steps = append(steps, Step{Cmd: "which " + t[0], Exit: 1, Out: "not on PATH"})
			continue
		}
		steps = append(steps, runStep(ctx, e, path, t[1:]...))
	}
	return steps
}

func probeHerdr(tool string, flag string) func(context.Context, *Env) []Step {
	return func(ctx context.Context, e *Env) []Step {
		if e.HerdrPath == "" {
			return []Step{{Cmd: tool + " " + flag + " <herdr>", Exit: 0, Out: "skipped: no --herdr given"}}
		}
		return []Step{runStep(ctx, e, tool, flag, e.HerdrPath)}
	}
}

func probeLaunchctl(ctx context.Context, e *Env) []Step {
	r := e.Runner.Run(ctx, "launchctl", "print", fmt.Sprintf("gui/%d", e.Uid))
	return []Step{step(fmt.Sprintf("launchctl print gui/%d", e.Uid), r, "(output discarded; exit code only)")}
}
