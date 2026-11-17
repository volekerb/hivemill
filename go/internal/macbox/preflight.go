package macbox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"runtime"
	"sort"
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

func refuse(reason, format string, a ...any) *Refusal {
	return &Refusal{Reason: reason, Why: fmt.Sprintf(format, a...)}
}

// Spec is everything preflight needs. GOOS overrides runtime.GOOS in tests.
type Spec struct {
	GOOS        string
	OwnerUID    int    // the owner's uid; token files must be owned by it
	OwnerHome   string // the owner's home; the view probe walks its entries
	FactoryHome string
	Role        string // picks the worker account (AccountForRole)
	Launcher    string // the root-owned macbox-launch
	ProfilePath string // the agent's profile.sb
	CloneDir    string // the task's coder clone (ACL probe target)
	AgentDir    string // per-agent dir (DirsFor); owner-writable
	OAuthPath   string
	GhPath      string
	ProfileIn   ProfileInput // the input profile.sb was built from (self-test plan)
}

// Runner runs external commands and touches the owner's filesystem for
// preflight. OSRunner is the real one; tests inject a fake.
type Runner interface {
	Run(name string, args ...string) (exit int, stdout, stderr string)
	Stat(name string) (os.FileInfo, error)
	ReadDir(name string) ([]string, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	// CreateExclusive creates name and fails (fs.ErrExist) if it exists.
	CreateExclusive(name string, data []byte, perm os.FileMode) error
	Remove(name string) error
}

// OSRunner is the Runner backed by os and os/exec.
type OSRunner struct{}

// Run runs name with args. A command that cannot start reports exit -1.
func (OSRunner) Run(name string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String(), errb.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String(), errb.String()
	default:
		return -1, out.String(), errb.String() + err.Error()
	}
}

// Stat is os.Lstat (a symlinked token file or clone is refused by the checks).
func (OSRunner) Stat(name string) (os.FileInfo, error) { return os.Lstat(name) }

// ReadDir returns the entry names of a directory.
func (OSRunner) ReadDir(name string) ([]string, error) {
	ents, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, nil
}

// WriteFile is os.WriteFile.
func (OSRunner) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// CreateExclusive creates name with O_EXCL: an existing file (a foreign
// canary, or one a concurrent preflight owns) is an error, never reused.
func (OSRunner) CreateExclusive(name string, data []byte, perm os.FileMode) (err error) {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	_, err = f.Write(data)
	return err
}

// Remove is os.Remove.
func (OSRunner) Remove(name string) error { return os.Remove(name) }

// Probe scripts. Each prints a fixed marker so a chain that did not run (sudo
// or launcher refusal, sandbox-exec error) is told apart from a denial: no
// marker is a refusal, never a pass.
const (
	readScript  = `if test -r "$1"; then echo ALLOWED; else echo DENIED; fi`
	writeScript = `if touch "$1"; then rm -f "$1"; echo ALLOWED; else echo DENIED; fi`
	viewScript  = `ctl=$1; shift; if test -r "$ctl"; then echo CONTROL-OK; fi; for p in "$@"; do if test -r "$p"; then echo "READABLE $p"; fi; done; echo PROBE-DONE`
	aclScript   = `ctl=$1; shift; if test -r "$ctl"; then echo CONTROL-OK; fi; for d in "$@"; do f="$d/.hivemill-acl-probe"; if touch "$f"; then rm -f "$f"; echo "WROTE $d"; fi; done; echo PROBE-DONE`
)

// openProfile lets everything: a probe under it sees only the filesystem ACLs
// ("without the profile") while still going through sudo and the launcher.
const openProfile = "(version 1)\n(allow default)\n"

const canaryBody = "hivemill preflight canary\n"

// newNonce returns the per-run suffix of the self-test probe paths: the pid
// plus 64 random bits, so concurrent preflights never share a canary. Tests
// replace it.
var newNonce = func() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(b[:])), nil
}

// fileID is the (device, inode) identity of fi; ok is false when the
// platform stat is not available, which callers treat as undecidable.
func fileID(fi os.FileInfo) (id [2]uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return id, false
	}
	return [2]uint64{uint64(st.Dev), st.Ino}, true // Dev is int32 on darwin, uint64 on linux
}

// Preflight returns nil or a *Refusal. Any check that cannot decide refuses;
// there is no fallback to an unsandboxed run.
func Preflight(spec Spec, r Runner) (err error) {
	goos := spec.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" {
		return refuse(ReasonOS, "macbox is darwin-only (got %s)", goos)
	}
	account, aerr := AccountForRole(spec.Role)
	if aerr != nil {
		return refuse(ReasonUser, "%v", aerr)
	}
	for name, v := range map[string]string{
		"OwnerHome": spec.OwnerHome, "FactoryHome": spec.FactoryHome, "Launcher": spec.Launcher,
		"ProfilePath": spec.ProfilePath, "CloneDir": spec.CloneDir, "AgentDir": spec.AgentDir,
	} {
		if !path.IsAbs(v) {
			return refuse(ReasonUser, "spec %s must be an absolute path, got %q", name, v)
		}
	}
	if err := checkAccounts(r, AccountRO, AccountRW); err != nil {
		return err
	}
	// Every file the run creates carries a per-run nonce and is created
	// exclusively, so concurrent preflights never share or delete each
	// other's files.
	nonce, nerr := newNonce()
	if nerr != nil || nonce == "" {
		return refuse(ReasonSeatbelt, "self-test nonce: %v", nerr)
	}
	open := path.Join(spec.AgentDir, ".hivemill-preflight-open-"+nonce+".sb")
	if werr := r.CreateExclusive(open, []byte(openProfile), 0o644); werr != nil {
		return refuse(ReasonSudo, "write %s: %v (the agent dir must exist and be owner-writable)", open, werr)
	}
	defer func() {
		if rerr := removeIfPresent(r, open); rerr != nil && err == nil {
			err = refuse(ReasonSudo, "remove %s: %v", open, rerr)
		}
	}()
	accounts := []string{account}
	if account != AccountRO {
		accounts = append(accounts, AccountRO)
	}
	for _, a := range accounts {
		if err := checkSudo(spec, r, a, open); err != nil {
			return err
		}
	}
	if err := checkSeatbelt(spec, r, account, nonce); err != nil {
		return err
	}
	if err := checkView(spec, r, account, open); err != nil {
		return err
	}
	if err := checkACL(spec, r, open); err != nil {
		return err
	}
	return checkTokens(spec, r)
}

func checkAccounts(r Runner, accounts ...string) error {
	for _, u := range accounts {
		exit, _, stderr := r.Run("/usr/bin/dscl", ".", "-read", "/Users/"+u, "RecordType")
		if exit != 0 {
			return refuse(ReasonUser, "account %s missing (dscl exit %d: %s); run the M5 setup script", u, exit, strings.TrimSpace(stderr))
		}
	}
	return nil
}

// chain is the full launch chain: sudo -> launcher -> sandbox-exec -> prog.
func chain(spec Spec, account, profile string, prog ...string) []string {
	args := []string{"-n", "-u", account}
	args = append(args, DirsFor(spec.AgentDir).metaAssignments()...)
	args = append(args, spec.Launcher, "--", SandboxExec, "-f", profile)
	return append(args, prog...)
}

func checkSudo(spec Spec, r Runner, account, open string) error {
	exit, _, stderr := r.Run(DefaultSudo, chain(spec, account, open, "/usr/bin/true")...)
	if exit != 0 {
		return refuse(ReasonSudo, "sudo -n -u %s %s -- ... /usr/bin/true failed (exit %d: %s); check /etc/sudoers.d/hivemill", account, spec.Launcher, exit, strings.TrimSpace(stderr))
	}
	return nil
}

// probe runs one marker script through the chain and returns its stdout lines.
// It fails when the chain exits non-zero or the last line is not wantLast.
func probe(spec Spec, r Runner, account, profile, script string, wantLast []string, args ...string) ([]string, error) {
	prog := append([]string{"/bin/sh", "-c", script, "hivemill-probe"}, args...)
	exit, stdout, stderr := r.Run(DefaultSudo, chain(spec, account, profile, prog...)...)
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := lines[len(lines)-1]
	ok := exit == 0
	if ok {
		ok = false
		for _, w := range wantLast {
			ok = ok || last == w
		}
	}
	if !ok {
		return nil, fmt.Errorf("probe chain did not run (exit %d, stdout %q, stderr %q)", exit, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	}
	return lines, nil
}

func probeOne(spec Spec, r Runner, account string, kind ProbeKind, p string) (bool, error) {
	script := readScript
	if kind == ProbeWrite {
		script = writeScript
	}
	lines, err := probe(spec, r, account, spec.ProfilePath, script, []string{"ALLOWED", "DENIED"}, p)
	if err != nil {
		return false, err
	}
	if len(lines) != 1 {
		return false, fmt.Errorf("probe printed %d lines, want 1: %q", len(lines), lines)
	}
	return lines[0] == "ALLOWED", nil
}

func checkSeatbelt(spec Spec, r Runner, account, nonce string) error {
	for _, p := range []string{SandboxExec, spec.ProfilePath} {
		fi, err := r.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return refuse(ReasonSeatbelt, "%s is missing or not a regular file (%v)", p, err)
		}
	}
	// Positive control: the clone is readable under every role's profile. If
	// it is not, the chain is broken and the deny probes below prove nothing.
	ok, err := probeOne(spec, r, account, ProbeRead, spec.ProfileIn.CloneDir)
	if err != nil {
		return refuse(ReasonSeatbelt, "control probe: %v", err)
	}
	if !ok {
		return refuse(ReasonSeatbelt, "control probe: clone %s not readable under the profile", spec.ProfileIn.CloneDir)
	}
	for _, pr := range SelfTestPlan(spec.ProfileIn, nonce) {
		if err := runSelfTestProbe(spec, r, account, pr); err != nil {
			return err
		}
	}
	// The token files must be unreadable under the profile too: a macOS ACL
	// on them would pass the POSIX mode check in checkTokens.
	for _, p := range []string{spec.OAuthPath, spec.GhPath} {
		got, err := probeOne(spec, r, account, ProbeRead, p)
		if err != nil {
			return refuse(ReasonSeatbelt, "token probe %s: %v", p, err)
		}
		if got {
			return refuse(ReasonSeatbelt, "token file %s is readable by %s under the profile", p, account)
		}
	}
	return nil
}

func runSelfTestProbe(spec Spec, r Runner, account string, pr Probe) (err error) {
	if !pr.SetupAsOwner {
		defer func() {
			if rerr := removeIfPresent(r, pr.Path); rerr != nil && err == nil {
				err = refuse(ReasonSeatbelt, "probe %s: remove %s: %v", pr.Name, pr.Path, rerr)
			}
		}()
		return checkProbe(spec, r, account, pr, pr.WantAllowed)
	}
	// The canary must exist (and be readable by the filesystem) so that a
	// denial comes from the seatbelt, not from a missing file. It is created
	// exclusively: a file already at the path is foreign (or another run's),
	// so it is neither trusted nor removed.
	if werr := r.CreateExclusive(pr.Path, []byte(canaryBody), 0o644); werr != nil {
		return refuse(ReasonSeatbelt, "probe %s: create canary %s: %v (a leftover canary must be removed by hand)", pr.Name, pr.Path, werr)
	}
	defer func() {
		if rerr := removeIfPresent(r, pr.Path); rerr != nil && err == nil {
			err = refuse(ReasonSeatbelt, "probe %s: remove %s: %v", pr.Name, pr.Path, rerr)
		}
	}()
	id, ierr := canaryID(r, pr.Path)
	if ierr != nil {
		return refuse(ReasonSeatbelt, "probe %s: canary %s after create: %v", pr.Name, pr.Path, ierr)
	}
	if err := checkProbe(spec, r, account, pr, false); err != nil {
		return err
	}
	// A denial only counts if our canary was there for the whole probe: if
	// it vanished or was replaced, the DENIED proves nothing (fail closed).
	after, aerr := canaryID(r, pr.Path)
	if aerr != nil || after != id {
		return refuse(ReasonSeatbelt, "probe %s: canary %s vanished or was replaced during the probe (%v); the self-test proves nothing", pr.Name, pr.Path, aerr)
	}
	return nil
}

func canaryID(r Runner, p string) ([2]uint64, error) {
	fi, err := r.Stat(p)
	if err != nil {
		return [2]uint64{}, err
	}
	if !fi.Mode().IsRegular() {
		return [2]uint64{}, fmt.Errorf("not a regular file")
	}
	id, ok := fileID(fi)
	if !ok {
		return id, fmt.Errorf("cannot read its device and inode")
	}
	return id, nil
}

func checkProbe(spec Spec, r Runner, account string, pr Probe, want bool) error {
	got, perr := probeOne(spec, r, account, pr.Kind, pr.Path)
	if perr != nil {
		return refuse(ReasonSeatbelt, "probe %s: %v", pr.Name, perr)
	}
	if got != want {
		return refuse(ReasonSeatbelt, "probe %s (%s %s): allowed=%v, want %v", pr.Name, pr.Kind, pr.Path, got, want)
	}
	return nil
}

// viewAllowed reports whether entry (a direct child of the owner home) is on
// the worker's allow-list: equal to, an ancestor of, or inside an allowed path.
func viewAllowed(entry string, allowed []string) bool {
	for _, a := range allowed {
		if a == "" {
			continue
		}
		if entry == a || strings.HasPrefix(a, entry+"/") || strings.HasPrefix(entry, a+"/") {
			return true
		}
	}
	return false
}

func checkView(spec Spec, r Runner, account, open string) error {
	names, err := r.ReadDir(spec.OwnerHome)
	if err != nil {
		return refuse(ReasonView, "list owner home %s: %v", spec.OwnerHome, err)
	}
	allowed := append([]string{spec.FactoryHome, spec.AgentDir, spec.CloneDir, spec.ProfileIn.CloneDir}, spec.ProfileIn.ReadOnlyPaths...)
	targets := []string{spec.OwnerHome} // listing the home itself must be denied
	for _, n := range names {
		p := path.Join(spec.OwnerHome, n)
		if !viewAllowed(p, allowed) {
			targets = append(targets, p)
		}
	}
	sort.Strings(targets[1:])
	lines, err := probe(spec, r, account, open, viewScript, []string{"PROBE-DONE"}, append([]string{spec.FactoryHome}, targets...)...)
	if err != nil {
		return refuse(ReasonView, "view probe as %s: %v", account, err)
	}
	if lines[0] != "CONTROL-OK" {
		return refuse(ReasonView, "view probe as %s: control %s not readable; the probe cannot see the owner tree", account, spec.FactoryHome)
	}
	var leaks []string
	for _, l := range lines {
		if p, ok := strings.CutPrefix(l, "READABLE "); ok {
			leaks = append(leaks, p)
		}
	}
	if len(leaks) > 0 {
		return refuse(ReasonView, "%s can read outside the allow-list: %s", account, strings.Join(leaks, ", "))
	}
	return nil
}

func checkACL(spec Spec, r Runner, open string) error {
	targets := []string{spec.CloneDir, path.Join(spec.CloneDir, ".git", "hooks")}
	for _, d := range targets {
		fi, err := r.Stat(d)
		if err != nil || !fi.IsDir() {
			return refuse(ReasonACL, "%s is missing or not a directory (%v); the ACL probe needs it", d, err)
		}
	}
	lines, err := probe(spec, r, AccountRO, open, aclScript, []string{"PROBE-DONE"}, append([]string{spec.CloneDir}, targets...)...)
	for _, d := range targets {
		if rerr := removeIfPresent(r, path.Join(d, ".hivemill-acl-probe")); rerr != nil {
			return refuse(ReasonACL, "remove ACL probe file in %s: %v", d, rerr)
		}
	}
	if err != nil {
		return refuse(ReasonACL, "ACL probe as %s: %v", AccountRO, err)
	}
	if lines[0] != "CONTROL-OK" {
		return refuse(ReasonACL, "ACL probe as %s: control %s not readable; the probe cannot see the clone", AccountRO, spec.CloneDir)
	}
	for _, l := range lines {
		if d, ok := strings.CutPrefix(l, "WROTE "); ok {
			return refuse(ReasonACL, "%s created a file in %s without a profile", AccountRO, d)
		}
	}
	return nil
}

func checkTokens(spec Spec, r Runner) error {
	if spec.OwnerUID <= 0 {
		return refuse(ReasonACL, "owner uid %d is not a valid owner uid", spec.OwnerUID)
	}
	for _, p := range []string{spec.OAuthPath, spec.GhPath} {
		if !path.IsAbs(p) {
			return refuse(ReasonACL, "token path %q is not absolute", p)
		}
		fi, err := r.Stat(p)
		if err != nil {
			return refuse(ReasonACL, "token %s: %v", p, err)
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			return refuse(ReasonACL, "token %s is %v, want a regular file with mode 0600 (chmod 600 %s)", p, fi.Mode(), p)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return refuse(ReasonACL, "token %s: cannot read its owner uid", p)
		}
		if int(st.Uid) != spec.OwnerUID {
			return refuse(ReasonACL, "token %s is owned by uid %d, want the owner uid %d", p, st.Uid, spec.OwnerUID)
		}
	}
	return nil
}

func removeIfPresent(r Runner, p string) error {
	if err := r.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
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
