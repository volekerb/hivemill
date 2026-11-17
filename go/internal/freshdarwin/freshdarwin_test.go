package freshdarwin

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

const pw = "S3cretPassw0rdXYZ"

// fake records every command; failOn fails the first command whose joined argv contains the substring.
type fake struct {
	calls    []string
	failOn   string
	ticks    int // tick events the grep reports
	userGone bool
}

func (f *fake) Run(c Cmd) (string, int, error) {
	j := strings.Join(c.Argv, " ")
	f.calls = append(f.calls, j)
	if f.failOn != "" && strings.Contains(j, f.failOn) {
		return "boom " + pw, 1, nil
	}
	switch {
	case c.Argv[0] == "id":
		return "501\n", 0, nil
	case strings.Contains(j, "grep -c"):
		return strings.TrimSpace(strings.Repeat(" ", 0)) + itoa(f.ticks), 0, nil
	case c.Argv[0] == "dscl":
		return "", 56, nil // user is gone
	}
	return "ok", 0, nil
}

func itoa(n int) string { return string(rune('0' + n)) }

func cfgFor(f *fake, log *bytes.Buffer) Config {
	now := time.Unix(1000, 0)
	return Config{GOOS: "darwin", EUID: 0, Runner: f, Log: log, Shim: "/shim/install", Name: "hmfreshab12",
		Password: pw, Sleep: func(d time.Duration) { now = now.Add(d) }, Now: func() time.Time { return now }}
}

func lastIndex(calls []string, sub string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if strings.Contains(calls[i], sub) {
			return i
		}
	}
	return -1
}

func index(calls []string, sub string) int {
	for i, c := range calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func TestOrderOfSteps(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	code, err := Run(cfgFor(f, &log))
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v\n%s", code, err, log.String())
	}
	want := []string{"sysadminctl -addUser", "/shim/install --dry-run", "/shim/install --yes", "/shim/install --check",
		"factory up", "grep -c", "factory res", "bootout", "sysadminctl -deleteUser"}
	last := -1
	for _, w := range want {
		i := index(f.calls, w)
		if i < 0 || i < last {
			t.Fatalf("%q at %d, previous at %d\ncalls: %v", w, i, last, f.calls)
		}
		last = i
	}
	for _, n := range StepNames {
		if !strings.Contains(log.String(), n+strings.Repeat(" ", 18-len(n))+" PASS") {
			t.Errorf("summary lacks PASS for %s:\n%s", n, log.String())
		}
	}
	if strings.Contains(strings.Join(f.calls, "\n"), "launchctl asuser 501 sudo -u hmfreshab12 -H env HOME=/Users/hmfreshab12 PATH=/Users/hmfreshab12/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin /shim") == false {
		t.Errorf("installer must run via launchctl asuser + sudo -u: %v", f.calls)
	}
	if !strings.Contains(f.calls[1], "sysadminctl -addUser hmfreshab12") || strings.Contains(f.calls[1], " -admin") {
		t.Errorf("create must not grant admin: %s", f.calls[1])
	}
}

func TestCleanupOnFailureAtEachStep(t *testing.T) {
	for _, step := range []string{"-addUser", "--dry-run", "--yes", "--check", "factory up", "grep -c", "factory res", "bootout"} {
		t.Run(step, func(t *testing.T) {
			f := &fake{ticks: 2, failOn: step}
			if step == "grep -c" {
				f.failOn = ""
				f.ticks = 0 // never reaches 2: times out
			}
			var log bytes.Buffer
			code, err := Run(cfgFor(f, &log))
			if err != nil || code != 1 {
				t.Fatalf("code %d err %v", code, err)
			}
			if index(f.calls, "sysadminctl -deleteUser hmfreshab12") < 0 {
				t.Fatalf("no deleteUser after failure at %s: %v", step, f.calls)
			}
			if d := lastIndex(f.calls, "dscl . -read /Users/hmfreshab12"); d < index(f.calls, "-deleteUser") {
				t.Fatalf("dscl read-fails check missing or early: %v", f.calls)
			}
			if !strings.Contains(log.String(), "FAIL") {
				t.Errorf("summary shows no FAIL:\n%s", log.String())
			}
		})
	}
}

func TestCleanupFailsWhenUserStillExists(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.Runner = runnerFunc(func(cmd Cmd) (string, int, error) {
		if cmd.Argv[0] == "dscl" && index(f.calls, "-deleteUser") >= 0 {
			f.calls = append(f.calls, "dscl")
			return "RecordName: x", 0, nil
		}
		return f.Run(cmd)
	})
	if code, _ := Run(c); code != 1 {
		t.Fatalf("code %d, want 1 when the user is still present", code)
	}
}

type runnerFunc func(Cmd) (string, int, error)

func (r runnerFunc) Run(c Cmd) (string, int, error) { return r(c) }

func TestCleanupKeepSkipsDelete(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.Keep = true
	if code, _ := Run(c); code != 0 {
		t.Fatalf("code %d", code)
	}
	if index(f.calls, "-deleteUser") >= 0 {
		t.Fatalf("--keep must not delete: %v", f.calls)
	}
}

func TestCleanupAfterSignal(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.Stop = func() bool { return index(f.calls, "--dry-run") >= 0 }
	code, _ := Run(c)
	if code != 1 || index(f.calls, "--yes") >= 0 || index(f.calls, "-deleteUser") < 0 {
		t.Fatalf("code %d calls %v", code, f.calls)
	}
}

func TestCleanupAfterPanic(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.Runner = runnerFunc(func(cmd Cmd) (string, int, error) {
		if strings.Contains(strings.Join(cmd.Argv, " "), "factory up") {
			panic("step exploded")
		}
		return f.Run(cmd)
	})
	func() {
		defer func() { _ = recover() }()
		Run(c)
	}()
	if index(f.calls, "-deleteUser") < 0 {
		t.Fatalf("no cleanup after panic: %v", f.calls)
	}
}

func TestNoPasswordInLogs(t *testing.T) {
	for _, fail := range []string{"", "-addUser", "factory up"} {
		f := &fake{ticks: 2, failOn: fail}
		var log bytes.Buffer
		Run(cfgFor(f, &log))
		if strings.Contains(log.String(), pw) {
			t.Fatalf("password in log (fail=%q):\n%s", fail, log.String())
		}
	}
	// A random password is never printed either.
	f := &fake{ticks: 2}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.Password = ""
	Run(c)
	i := index(f.calls, "-addUser")
	fields := strings.Fields(f.calls[i])
	var rnd string
	for k, x := range fields {
		if x == "-password" {
			rnd = fields[k+1]
		}
	}
	if len(rnd) < 20 || strings.Contains(log.String(), rnd) {
		t.Fatalf("random password %q missing or logged", rnd)
	}
}

func TestRefusals(t *testing.T) {
	err := Check("linux", 0)
	var r *Refusal
	if !errors.As(err, &r) || r.Msg != "ci-freshbox-darwin: macOS only" || r.Exit != 2 {
		t.Fatalf("non-darwin: %v", err)
	}
	if !strings.Contains(r.Msg, "macOS only") {
		t.Fatal("message")
	}
}

func TestNonRootRefused(t *testing.T) {
	f := &fake{}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.EUID = 501
	code, err := Run(c)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "must run as root") || len(f.calls) != 0 {
		t.Fatalf("code %d err %v calls %v", code, err, f.calls)
	}
}

func TestRandomName(t *testing.T) {
	n, err := RandomName()
	if err != nil || len(n) != len("hmfresh")+4 || !strings.HasPrefix(n, "hmfresh") {
		t.Fatalf("%q %v", n, err)
	}
}

func TestSignalStillReportsConfirmedDeletionAsPass(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.Stop = func() bool { return index(f.calls, "--dry-run") >= 0 }
	if code, _ := Run(c); code != 1 {
		t.Fatalf("code %d, want 1 (interrupted)", code)
	}
	out := log.String()
	if !strings.Contains(out, "delete-user") || !regexpLine(out, "delete-user", "PASS") {
		t.Errorf("delete-user not PASS after signal:\n%s", out)
	}
	if !regexpLine(out, "installer-dry-run", "FAIL") {
		t.Errorf("interrupted step not FAIL:\n%s", out)
	}
}

func regexpLine(out, name, res string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), name) && strings.Contains(l, res) {
			return true
		}
	}
	return false
}

func TestUserStepsSetPathAndUseShimForCheck(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	if code, _ := Run(cfgFor(f, &log)); code != 0 {
		t.Fatalf("code %d\n%s", code, log.String())
	}
	const env = "env HOME=/Users/hmfreshab12 PATH=/Users/hmfreshab12/.local/bin:"
	for _, want := range []string{env + "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin /shim/install --check", "/sbin factory up", "/sbin factory res"} {
		if index(f.calls, want) < 0 {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
	for _, c := range f.calls {
		if strings.Contains(c, " hivemill-install ") {
			t.Errorf("bare hivemill-install on PATH-less root: %s", c)
		}
	}
}

func TestExistingUserIsNeverTouched(t *testing.T) {
	f := &fake{ticks: 2}
	var log bytes.Buffer
	c := cfgFor(f, &log)
	c.Runner = runnerFunc(func(cmd Cmd) (string, int, error) {
		if cmd.Argv[0] == "dscl" {
			f.calls = append(f.calls, "dscl")
			return "RecordName: x", 0, nil
		}
		return f.Run(cmd)
	})
	if code, _ := Run(c); code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	if index(f.calls, "sysadminctl") >= 0 || index(f.calls, "rm -rf") >= 0 {
		t.Fatalf("touched a pre-existing user: %v", f.calls)
	}
}
