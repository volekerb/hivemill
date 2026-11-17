// Package freshdarwin drives the macOS fresh-install test (ADR 0003 stage M8a): a throwaway user installs
// Hivemill, runs `factory up`, waits for ticks, boots the jobs out and is deleted again. Every machine
// effect goes through Runner so Linux unit tests use a fake.
package freshdarwin

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"
)

// Cmd is one command to run. Secret values in Args are replaced in logs by redaction (see Config.Secrets).
type Cmd struct {
	Argv []string
}

// Runner executes commands. A non-zero exit is reported as code, not as err; err means it could not run.
type Runner interface {
	Run(c Cmd) (out string, code int, err error)
}

// Config is everything Run needs.
type Config struct {
	GOOS     string
	EUID     int
	Runner   Runner
	Log      io.Writer
	Keep     bool
	Shim     string        // path of the release installer shim
	Name     string        // throwaway user name; empty = random hmfresh<4>
	Password string        // empty = random; never logged
	Ticks    int           // tick events to wait for (default 2)
	Timeout  time.Duration // tick wait timeout (default 180 s)
	Poll     time.Duration // poll interval (default 5 s)
	Sleep    func(time.Duration)
	Stop     func() bool // true once a signal arrived: no further step starts, cleanup still runs
	Now      func() time.Time
	// EventsLog is the user's events log path, relative to the user's home.
	EventsLog string
}

// Step is the outcome of one step.
type Step struct {
	Name string
	Pass bool
	Note string
}

// Refusal is returned when the test must not start. Exit is the process exit code.
type Refusal struct {
	Msg  string
	Exit int
}

func (r *Refusal) Error() string { return r.Msg }

const (
	prefix       = "ci-freshbox-darwin: "
	defaultEvLog = "factory/state/events.jsonl"
)

// Step names in order.
var StepNames = []string{"create-user", "installer-dry-run", "install", "install-check", "factory-up",
	"wait-ticks", "factory-res", "bootout", "delete-user"}

// RandomName returns hmfresh plus four random lowercase letters or digits.
func RandomName() (string, error) { return randomString("hmfresh", 4) }

func randomString(p string, n int) (string, error) {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := []byte(p)
	for i := 0; i < n; i++ {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(alpha))))
		if err != nil {
			return "", fmt.Errorf("random source failed: %w", err)
		}
		b = append(b, alpha[x.Int64()])
	}
	return string(b), nil
}

func randomPassword() (string, error) { return randomString("", 24) }

// Check refuses off macOS and for non-root.
func Check(goos string, euid int) error {
	if goos != "darwin" {
		return &Refusal{Msg: prefix + "macOS only", Exit: 2}
	}
	if euid != 0 {
		return &Refusal{Msg: prefix + "must run as root (sudo ci-freshbox-darwin)", Exit: 2}
	}
	return nil
}

type runState struct {
	cfg     Config
	name    string
	secret  string
	uid     string
	home    string
	created bool
	steps   []Step
}

func (s *runState) logf(format string, a ...any) {
	fmt.Fprintln(s.cfg.Log, s.redact(fmt.Sprintf(format, a...)))
}

func (s *runState) redact(t string) string {
	if s.secret != "" {
		t = strings.ReplaceAll(t, s.secret, "[REDACTED]")
	}
	return t
}

func (s *runState) exec(argv ...string) (string, int, error) {
	s.logf("$ %s", strings.Join(argv, " "))
	out, code, err := s.cfg.Runner.Run(Cmd{Argv: argv})
	if out = strings.TrimSpace(out); out != "" {
		s.logf("%s", out)
	}
	if err != nil {
		s.logf("error: %v", err)
	} else {
		s.logf("exit %d", code)
	}
	return out, code, err
}

// asUser wraps argv to run as the throwaway user inside its launchd gui domain.
func (s *runState) asUser(argv ...string) []string {
	// Root's PATH lacks the user's ~/.local/bin (where the installer puts factory), so set it explicitly.
	home := s.home
	if home == "" {
		home = "/Users/" + s.name
	}
	env := []string{"env", "HOME=" + home, "PATH=" + home + "/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"}
	return append(append([]string{"launchctl", "asuser", s.uid, "sudo", "-u", s.name, "-H"}, env...), argv...)
}

func (s *runState) must(argv ...string) (string, error) {
	out, code, err := s.exec(argv...)
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("%s exited %d", argv[0], code)
	}
	return out, nil
}

// Run executes the test and returns the exit code (0 only when every step passed). The returned error is a
// *Refusal when it refused to start.
func Run(cfg Config) (int, error) {
	if err := Check(cfg.GOOS, cfg.EUID); err != nil {
		return err.(*Refusal).Exit, err
	}
	if cfg.Ticks == 0 {
		cfg.Ticks = 2
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 180 * time.Second
	}
	if cfg.Poll == 0 {
		cfg.Poll = 5 * time.Second
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.EventsLog == "" {
		cfg.EventsLog = defaultEvLog
	}
	if cfg.Name != "" && !strings.HasPrefix(cfg.Name, "hmfresh") || strings.ContainsAny(cfg.Name, "/.") {
		return 2, &Refusal{Msg: prefix + "user name must start with hmfresh and contain no / or .", Exit: 2}
	}
	s := &runState{cfg: cfg, name: cfg.Name, secret: cfg.Password}
	var err error
	if s.name == "" {
		if s.name, err = RandomName(); err != nil {
			return 1, err
		}
	}
	if s.secret == "" {
		if s.secret, err = randomPassword(); err != nil {
			return 1, err
		}
	}
	// Cleanup runs on every exit path, including a panic in a step.
	defer func() {
		if r := recover(); r != nil {
			s.cleanup()
			s.summary()
			panic(r)
		}
	}()
	s.runSteps()
	s.cleanup()
	return s.summary(), nil
}

// step records one step; a signal that arrived while it ran turns a pass into an interruption.
func (s *runState) step(name string, err error) bool {
	if err == nil && s.cfg.Stop != nil && s.cfg.Stop() {
		err = errors.New("interrupted by signal")
	}
	return s.record(name, err)
}

func (s *runState) record(name string, err error) bool {
	st := Step{Name: name, Pass: err == nil}
	if err != nil {
		st.Note = s.redact(err.Error())
	}
	s.steps = append(s.steps, st)
	return err == nil
}

func (s *runState) runSteps() {
	// 1. create user: no admin, password never printed.
	if !s.step("create-user", s.createUser()) {
		return
	}
	shim := s.cfg.Shim
	if !s.step("installer-dry-run", s.userStep(shim, "--dry-run")) {
		return
	}
	if !s.step("install", s.userStep(shim, "--yes")) {
		return
	}
	if !s.step("install-check", s.userStep(shim, "--check")) {
		return
	}
	if !s.step("factory-up", s.userStep("factory", "up")) {
		return
	}
	if !s.step("wait-ticks", s.waitTicks()) {
		return
	}
	if !s.step("factory-res", s.userStep("factory", "res")) {
		return
	}
	if !s.step("bootout", s.bootout()) {
		return
	}
}

func (s *runState) createUser() error {
	// The user is created before the command runs to completion, so a half-created user is still cleaned up.
	// Never touch a user that already exists: cleanup would delete someone else's account.
	if _, code, err := s.exec("dscl", ".", "-read", "/Users/"+s.name); err != nil || code == 0 {
		return fmt.Errorf("user %s already exists or could not be checked; refusing to touch it", s.name)
	}
	s.created = true
	if _, err := s.must("sysadminctl", "-addUser", s.name, "-password", s.secret, "-home", "/Users/"+s.name); err != nil {
		return err
	}
	out, err := s.must("id", "-u", s.name)
	if err != nil {
		return err
	}
	s.uid = strings.TrimSpace(out)
	if s.uid == "" {
		return errors.New("id -u printed nothing")
	}
	s.home = "/Users/" + s.name
	return nil
}

func (s *runState) userStep(argv ...string) error {
	_, err := s.must(s.asUser(argv...)...)
	return err
}

func (s *runState) waitTicks() error {
	log := s.home + "/" + s.cfg.EventsLog
	deadline := s.cfg.Now().Add(s.cfg.Timeout)
	for {
		// Count lines whose actor is the tick, read as the user so file permissions are the real ones.
		out, code, err := s.exec(s.asUser("grep", "-c", `"actor": "tick"`, log)...)
		if err != nil {
			return err
		}
		n := 0
		if code == 0 || code == 1 { // grep -c: 1 means zero matches
			fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
		}
		if n >= s.cfg.Ticks {
			s.logf("%d tick events seen (need %d)", n, s.cfg.Ticks)
			return nil
		}
		if s.cfg.Stop != nil && s.cfg.Stop() {
			return errors.New("interrupted by signal")
		}
		if !s.cfg.Now().Before(deadline) {
			return fmt.Errorf("only %d of %d tick events in %s", n, s.cfg.Ticks, s.cfg.Timeout)
		}
		s.cfg.Sleep(s.cfg.Poll)
	}
}

func (s *runState) bootout() error {
	var errs []error
	for _, l := range []string{"hivemill.serve", "hivemill.tick", "hivemill.up", "hivemill.herdr-server"} {
		_, code, err := s.exec("launchctl", "asuser", s.uid, "sudo", "-u", s.name, "-H", "launchctl", "bootout",
			fmt.Sprintf("gui/%s/%s", s.uid, l))
		// 3 = No such process, 113 = Could not find service: not loaded is fine.
		if err != nil {
			errs = append(errs, err)
		} else if code != 0 && code != 3 && code != 113 {
			errs = append(errs, fmt.Errorf("bootout %s exited %d", l, code))
		}
	}
	return errors.Join(errs...)
}

// deleteUser deletes the user and its home and confirms dscl no longer knows it.
func (s *runState) deleteUser() bool {
	ok := true
	if _, err := s.must("sysadminctl", "-deleteUser", s.name); err != nil {
		ok = false
	}
	// Belt and braces: the home must go even when sysadminctl keeps it.
	if _, err := s.must("rm", "-rf", "/Users/"+s.name); err != nil {
		ok = false
	}
	_, code, err := s.exec("dscl", ".", "-read", "/Users/"+s.name)
	if err != nil || code == 0 {
		s.logf("user %s still exists after delete", s.name)
		ok = false
	}
	return ok
}

func (s *runState) cleanup() {
	if !s.created {
		return
	}
	if s.cfg.Keep {
		s.logf("--keep: user %s and its home are left in place; delete with: sudo sysadminctl -deleteUser %s", s.name, s.name)
		s.steps = append(s.steps, Step{Name: "delete-user", Pass: true, Note: "skipped (--keep)"})
		return
	}
	// A killed bootout may not have run; stop the user's jobs before deleting.
	if s.uid != "" && !s.has("bootout") {
		s.bootout()
	}
	s.record("delete-user", func() error {
		if !s.deleteUser() {
			return errors.New("user deletion not confirmed")
		}
		return nil
	}())
}

func (s *runState) has(name string) bool {
	for _, st := range s.steps {
		if st.Name == name {
			return true
		}
	}
	return false
}

func (s *runState) summary() int {
	code := 0
	fmt.Fprintln(s.cfg.Log, "summary:")
	for _, n := range StepNames {
		st := Step{Name: n, Note: "not run"}
		for _, x := range s.steps {
			if x.Name == n {
				st = x
			}
		}
		res := "FAIL"
		if st.Pass {
			res = "PASS"
		} else {
			code = 1
		}
		line := fmt.Sprintf("  %-18s %s", n, res)
		if st.Note != "" {
			line += "  " + st.Note
		}
		fmt.Fprintln(s.cfg.Log, line)
	}
	return code
}
