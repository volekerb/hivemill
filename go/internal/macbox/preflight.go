package macbox

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"syscall"
)

// Refusal reasons match ADR 0003 section 3.2 (sandbox-refused).
const (
	ReasonOS       = "os"
	ReasonUser     = "user"
	ReasonSudo     = "sudo"
	ReasonSeatbelt = "seatbelt"
	ReasonView     = "view"
	ReasonACL      = "acl"
)

// Refusal is a fail-closed preflight outcome.
type Refusal struct {
	Reason string
	Why    string
}

func (r *Refusal) Error() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("macbox preflight %s: %s", r.Reason, r.Why)
}

// Spec is everything preflight needs. GOOS overrides runtime.GOOS in tests.
type Spec struct {
	GOOS        string
	OwnerUID    int
	OwnerHome   string
	FactoryHome string
	Role        string
	Account     string
	Launcher    string
	ProfilePath string
	CloneDir    string
	RealClaude  string
	AgentDir    string
	OAuthPath   string
	GhPath      string
	ProfileIn   ProfileInput
	// SkipSeatbeltSelfTest disables probe chain checks (fail-closed regression).
	SkipSeatbeltSelfTest bool
}

// Runner runs external commands for preflight.
type Runner interface {
	LookPath(file string) (string, error)
	Run(name string, args ...string) (exit int, stdout, stderr string)
	Stat(name string) (os.FileInfo, error)
}

// Preflight returns nil or a *Refusal. Never falls back to unsandboxed.
func Preflight(spec Spec, r Runner) error {
	goos := spec.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" {
		return &Refusal{Reason: ReasonOS, Why: "macbox is darwin-only (got " + goos + ")"}
	}
	account := spec.Account
	if account == "" {
		var err error
		account, err = AccountForRole(spec.Role)
		if err != nil {
			return &Refusal{Reason: ReasonUser, Why: err.Error()}
		}
	}
	if err := checkAccounts(r, AccountRO, AccountRW); err != nil {
		return err
	}
	if err := checkSudo(r, account); err != nil {
		return err
	}
	if err := checkSandboxExec(r); err != nil {
		return err
	}
	if !spec.SkipSeatbeltSelfTest {
		if err := checkSeatbeltProbes(spec, r, account); err != nil {
			return err
		}
	}
	if err := checkViewProbe(spec, r, account); err != nil {
		return err
	}
	if err := checkACLProbe(spec, r); err != nil {
		return err
	}
	if err := checkTokens(spec, r); err != nil {
		return err
	}
	return nil
}

func checkAccounts(r Runner, accounts ...string) error {
	for _, u := range accounts {
		exit, _, stderr := r.Run("dscl", ".", "-read", "/Users/"+u, "RecordType")
		if exit != 0 {
			return &Refusal{Reason: ReasonUser, Why: fmt.Sprintf("account %s missing: %s", u, strings.TrimSpace(stderr))}
		}
	}
	return nil
}

func checkSudo(r Runner, account string) error {
	exit, _, stderr := r.Run("sudo", "-n", "-u", account, "true")
	if exit != 0 {
		return &Refusal{Reason: ReasonSudo, Why: fmt.Sprintf("sudo -n -u %s true failed: %s", account, strings.TrimSpace(stderr))}
	}
	return nil
}

func checkSandboxExec(r Runner) error {
	p, err := r.LookPath("sandbox-exec")
	if err != nil || p == "" {
		return &Refusal{Reason: ReasonSeatbelt, Why: "sandbox-exec not found"}
	}
	return nil
}

func checkSeatbeltProbes(spec Spec, r Runner, account string) error {
	plan := SelfTestPlan(spec.ProfileIn)
	for _, probe := range plan {
		wantOK := probe.WantAllowed
		if probe.SetupAsOwner {
			wantOK = false
		}
		ok, err := runProbe(spec, r, account, probe, true)
		if err != nil {
			return &Refusal{Reason: ReasonSeatbelt, Why: fmt.Sprintf("probe %s: %v", probe.Name, err)}
		}
		if ok != wantOK {
			return &Refusal{Reason: ReasonSeatbelt, Why: fmt.Sprintf("probe %s: got allowed=%v want %v", probe.Name, ok, wantOK)}
		}
	}
	return nil
}

func runProbe(spec Spec, r Runner, account string, probe Probe, withProfile bool) (allowed bool, err error) {
	if !withProfile {
		exit, _, _ := r.Run("sudo", "-n", "-u", account, "/bin/sh", "-c", probeArgs(probe))
		return exit == 0, nil
	}
	exit, _, _ := r.Run("sudo", "-n", "-u", account, spec.Launcher, "--",
		"sandbox-exec", "-f", spec.ProfilePath, spec.RealClaude,
		"/bin/sh", "-c", probeArgs(probe))
	return exit == 0, nil
}

func probeArgs(probe Probe) string {
	switch probe.Kind {
	case ProbeWrite:
		return fmt.Sprintf("touch %s", shellQuote(probe.Path))
	default:
		return fmt.Sprintf("test -r %s", shellQuote(probe.Path))
	}
}

func shellQuote(p string) string {
	if p == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(p, "'", `'\'\'\'`) + "'"
}

func checkViewProbe(spec Spec, r Runner, account string) error {
	outside := path.Join(spec.OwnerHome, ".hivemill-preflight-outside")
	exit, _, _ := r.Run("sudo", "-n", "-u", account, "test", "-r", outside)
	if exit == 0 {
		return &Refusal{Reason: ReasonView, Why: "worker can read outside the allow-list in the owner home"}
	}
	return nil
}

func checkACLProbe(spec Spec, r Runner) error {
	if spec.CloneDir == "" {
		return &Refusal{Reason: ReasonACL, Why: "clone dir is empty"}
	}
	targets := []string{
		path.Join(spec.CloneDir, ".hivemill-acl-probe"),
		path.Join(spec.CloneDir, ".git", "hooks", ".hivemill-acl-probe"),
	}
	for _, t := range targets {
		exit, _, _ := r.Run("sudo", "-n", "-u", AccountRO, "touch", t)
		if exit == 0 {
			return &Refusal{Reason: ReasonACL, Why: fmt.Sprintf("_hivemill_ro created %s without a profile", t)}
		}
	}
	return nil
}

func checkTokens(spec Spec, r Runner) error {
	for _, p := range []string{spec.OAuthPath, spec.GhPath} {
		if p == "" {
			return &Refusal{Reason: ReasonACL, Why: "token path is empty"}
		}
		fi, err := r.Stat(p)
		if err != nil {
			return &Refusal{Reason: ReasonACL, Why: fmt.Sprintf("token %s: %v", p, err)}
		}
		if fi.Mode().Perm()&0o177 != 0 {
			return &Refusal{Reason: ReasonACL, Why: fmt.Sprintf("token %s is not mode 0600", p)}
		}
		if spec.OwnerUID > 0 {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != spec.OwnerUID {
				return &Refusal{Reason: ReasonACL, Why: fmt.Sprintf("token %s is not owned by uid %d", p, spec.OwnerUID)}
			}
		}
	}
	return nil
}

// AsRefusal unwraps *Refusal from err.
func AsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}
