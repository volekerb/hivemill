// Package launchd renders, installs and (un)loads the macOS launchd user agents that replace the systemd user
// units (ADR 0003 section 4.2). Pure Go; launchctl is only exec'd by ExecRunner.
package launchd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// Labels are the four launchd jobs, in bootstrap order. Each has a template <label>.plist.in.
var Labels = []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick", "hivemill.serve"}

// TickDeadline is the wall-clock limit of one tick. launchd has no TimeoutStartSec; this equals the systemd
// TimeoutStartSec=240. The tick job's ProgramArguments are `<FactoryBin> tick --deadline 240s`; the --deadline
// flag itself lands with M2b, this package only fixes the value and the argv.
const TickDeadline = 240 * time.Second

// Deadline formats d as the value of the tick --deadline argument (240s for TickDeadline).
func Deadline(d time.Duration) string { return fmt.Sprintf("%ds", int(d/time.Second)) }

// Vars are the template inputs. Every path must be absolute.
type Vars struct {
	FactoryHome string
	Path        string // PATH of the job
	LogDir      string // default FactoryHome/logs
	HerdrBin    string
	FactoryBin  string
	// TickDeadline is the --deadline value of the tick job; default Deadline(TickDeadline).
	TickDeadline string
	// Caffeinate prefixes the herdr-server job with `/usr/bin/caffeinate -s`. Default false: the sleep decision
	// is made in stage M2 on a Mac.
	Caffeinate bool
}

// Templates renders plists from <label>.plist.in files found in fsys (the repo's launchd/ directory).
type Templates struct{ fsys fs.FS }

// NewTemplates returns a Templates reading from fsys.
func NewTemplates(fsys fs.FS) *Templates { return &Templates{fsys: fsys} }

func known(label string) bool {
	for _, l := range Labels {
		if l == label {
			return true
		}
	}
	return false
}

func escape(s string) (string, error) {
	for _, r := range s {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return "", fmt.Errorf("launchd: control character %q in value %q", r, s)
		}
	}
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Render fills the template of label with vars. Every value is XML-escaped; relative paths, empty required
// values and unknown labels are errors.
func (t *Templates) Render(label string, v Vars) ([]byte, error) {
	if !known(label) {
		return nil, fmt.Errorf("launchd: unknown label %q (want one of %v)", label, Labels)
	}
	if v.LogDir == "" && v.FactoryHome != "" {
		v.LogDir = filepath.Join(v.FactoryHome, "logs")
	}
	if v.TickDeadline == "" {
		v.TickDeadline = Deadline(TickDeadline)
	}
	for name, p := range map[string]string{"FactoryHome": v.FactoryHome, "LogDir": v.LogDir, "HerdrBin": v.HerdrBin, "FactoryBin": v.FactoryBin} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("launchd: %s %q must be an absolute path (no ~ or relative)", name, p)
		}
	}
	if v.Path == "" {
		return nil, errors.New("launchd: Path must not be empty")
	}
	var err error
	for _, f := range []*string{&v.FactoryHome, &v.Path, &v.LogDir, &v.HerdrBin, &v.FactoryBin, &v.TickDeadline} {
		if *f, err = escape(*f); err != nil {
			return nil, err
		}
	}
	src, err := fs.ReadFile(t.fsys, label+".plist.in")
	if err != nil {
		return nil, fmt.Errorf("launchd: read template: %w", err)
	}
	tm, err := template.New(label).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("launchd: parse template %s: %w", label, err)
	}
	var out bytes.Buffer
	if err := tm.Execute(&out, v); err != nil {
		return nil, fmt.Errorf("launchd: render %s: %w", label, err)
	}
	return out.Bytes(), nil
}

// RenderAll renders every label.
func (t *Templates) RenderAll(v Vars) (map[string][]byte, error) {
	m := map[string][]byte{}
	for _, l := range Labels {
		b, err := t.Render(l, v)
		if err != nil {
			return nil, err
		}
		m[l] = b
	}
	return m, nil
}

// PlistName is the file name of a label's plist.
func PlistName(label string) string { return label + ".plist" }

// Install writes each plist (keyed by label) into dir as <label>.plist with mode 0644, atomically (temp file in
// dir, then rename). launchd ignores agents that are group or world writable (ADR D9), so a dir with those bits
// is refused.
func Install(dir string, plists map[string][]byte) error {
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("launchd: install dir: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("launchd: %s is not a directory", dir)
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("launchd: refusing %s: mode %o is group or world writable; chmod go-w it", dir, st.Mode().Perm())
	}
	for label, data := range plists {
		if !known(label) {
			return fmt.Errorf("launchd: unknown label %q", label)
		}
		if err := writeAtomic(filepath.Join(dir, PlistName(label)), data); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".hivemill-*.tmp")
	if err != nil {
		return fmt.Errorf("launchd: temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("launchd: write %s: %w", path, err)
	}
	if err = f.Chmod(0o644); err != nil {
		_ = f.Close()
		return fmt.Errorf("launchd: chmod %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("launchd: close %s: %w", path, err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("launchd: rename to %s: %w", path, err)
	}
	return nil
}

// ErrNotLoaded is what a Runner returns from Bootout when the job was not loaded.
var ErrNotLoaded = errors.New("launchd: job not loaded")

// Runner drives launchctl. Implementations never parse `launchctl print` (ADR D10: unstable format).
type Runner interface {
	Bootstrap(uid int, plistPath string) error
	Bootout(uid int, label string) error
	Kickstart(uid int, label string) error
}

// ExecRunner runs the real launchctl.
type ExecRunner struct{ Launchctl string }

func (r ExecRunner) run(args ...string) error {
	bin := r.Launchctl
	if bin == "" {
		bin = "launchctl"
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(args) > 0 && args[0] == "bootout" && notLoadedExit(ee.ExitCode()) {
			return fmt.Errorf("%w: %s", ErrNotLoaded, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// notLoadedExit: launchctl bootout exits 3 (No such process) or 113 (Could not find service) for an unloaded job.
func notLoadedExit(code int) bool { return code == 3 || code == 113 }

func (r ExecRunner) Bootstrap(uid int, plistPath string) error {
	return r.run("bootstrap", fmt.Sprintf("gui/%d", uid), plistPath)
}
func (r ExecRunner) Bootout(uid int, label string) error {
	return r.run("bootout", fmt.Sprintf("gui/%d/%s", uid, label))
}
func (r ExecRunner) Kickstart(uid int, label string) error {
	return r.run("kickstart", "-k", fmt.Sprintf("gui/%d/%s", uid, label))
}

// FakeRunner records the argv launchctl would get. Err maps an argv-joined command to the error to return.
type FakeRunner struct {
	Calls [][]string
	Err   map[string]error
}

func (f *FakeRunner) record(args ...string) error {
	f.Calls = append(f.Calls, append([]string{"launchctl"}, args...))
	return f.Err[strings.Join(f.Calls[len(f.Calls)-1], " ")]
}
func (f *FakeRunner) Bootstrap(uid int, plistPath string) error {
	return f.record("bootstrap", fmt.Sprintf("gui/%d", uid), plistPath)
}
func (f *FakeRunner) Bootout(uid int, label string) error {
	return f.record("bootout", fmt.Sprintf("gui/%d/%s", uid, label))
}
func (f *FakeRunner) Kickstart(uid int, label string) error {
	return f.record("kickstart", "-k", fmt.Sprintf("gui/%d/%s", uid, label))
}

// UninstallResult records one label of Uninstall.
type UninstallResult struct {
	Label     string
	NotLoaded bool // bootout reported the job was not loaded (tolerated)
	Removed   bool // the plist file existed and was removed
}

// Uninstall boots out every hivemill.* label, then removes the plist files in dir. A job that is not loaded or a
// file that is already gone is not an error; any other failure is returned after all labels were tried.
func Uninstall(dir string, uid int, r Runner) ([]UninstallResult, error) {
	var res []UninstallResult
	var errs []error
	for i := len(Labels) - 1; i >= 0; i-- {
		l := Labels[i]
		ur := UninstallResult{Label: l}
		if err := r.Bootout(uid, l); err != nil {
			if errors.Is(err, ErrNotLoaded) {
				ur.NotLoaded = true
			} else {
				errs = append(errs, fmt.Errorf("bootout %s: %w", l, err))
			}
		}
		switch err := os.Remove(filepath.Join(dir, PlistName(l))); {
		case err == nil:
			ur.Removed = true
		case errors.Is(err, fs.ErrNotExist):
		default:
			errs = append(errs, err)
		}
		res = append(res, ur)
	}
	return res, errors.Join(errs...)
}
