// This file renders, installs and (un)loads the macOS launchd user agents that replace the systemd user
// units (ADR 0003 section 4.2). Stdlib only; kept inside the installer package so the installer stays self-contained.
package install

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"
	"time"
)

// ldLabels are the four launchd jobs, in bootstrap order. Each has a template <label>.plist.in.
var ldLabels = []string{"hivemill.herdr-server", "hivemill.up", "hivemill.tick", "hivemill.serve"}

// ldTickDeadline is the wall-clock limit of one tick. launchd has no TimeoutStartSec; this equals the systemd
// TimeoutStartSec=240. The tick job's ProgramArguments are `<FactoryBin> tick --deadline 240s`; the --deadline
// flag itself lands with M2b, this package only fixes the value and the argv.
const ldTickDeadline = 240 * time.Second

// ldDeadline formats d as the value of the tick --deadline argument (240s for ldTickDeadline).
func ldDeadline(d time.Duration) string { return fmt.Sprintf("%ds", int(d/time.Second)) }

// ldVars are the template inputs. Every path must be absolute.
type ldVars struct {
	FactoryHome string
	Path        string // PATH of the job
	LogDir      string // default FactoryHome/logs
	HerdrBin    string
	FactoryBin  string
	// TickDeadline is the --deadline value of the tick job; default ldDeadline(ldTickDeadline).
	TickDeadline string
	// Caffeinate prefixes the herdr-server job with `/usr/bin/caffeinate -s`. Default false: the sleep decision
	// is made in stage M2 on a Mac.
	Caffeinate bool
}

// ldTemplates renders plists from <label>.plist.in files found in fsys (the repo's launchd/ directory).
type ldTemplates struct{ fsys fs.FS }

// ldNewTemplates returns a ldTemplates reading from fsys.
func ldNewTemplates(fsys fs.FS) *ldTemplates { return &ldTemplates{fsys: fsys} }

func ldKnown(label string) bool {
	for _, l := range ldLabels {
		if l == label {
			return true
		}
	}
	return false
}

func ldEscape(s string) (string, error) {
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
func (t *ldTemplates) Render(label string, v ldVars) ([]byte, error) {
	if !ldKnown(label) {
		return nil, fmt.Errorf("launchd: unknown label %q (want one of %v)", label, ldLabels)
	}
	if v.LogDir == "" && v.FactoryHome != "" {
		v.LogDir = filepath.Join(v.FactoryHome, "logs")
	}
	if v.TickDeadline == "" {
		v.TickDeadline = ldDeadline(ldTickDeadline)
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
		if *f, err = ldEscape(*f); err != nil {
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
func (t *ldTemplates) RenderAll(v ldVars) (map[string][]byte, error) {
	m := map[string][]byte{}
	for _, l := range ldLabels {
		b, err := t.Render(l, v)
		if err != nil {
			return nil, err
		}
		m[l] = b
	}
	return m, nil
}

// ldPlistName is the file name of a label's plist.
func ldPlistName(label string) string { return label + ".plist" }

// ldInstall writes each plist (keyed by label) into dir as <label>.plist with mode 0644, atomically (temp file in
// dir, then rename). launchd ignores agents that are group or world writable (ADR D9), so a dir with those bits
// is refused.
func ldInstall(dir string, plists map[string][]byte) error {
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
		if !ldKnown(label) {
			return fmt.Errorf("launchd: unknown label %q", label)
		}
		if err := ldWriteAtomic(filepath.Join(dir, ldPlistName(label)), data); err != nil {
			return err
		}
	}
	return nil
}

func ldWriteAtomic(path string, data []byte) (err error) {
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

// errLdNotLoaded is what a ldRunner returns from Bootout when the job was not loaded.
var errLdNotLoaded = errors.New("launchd: job not loaded")

// ldRunner drives launchctl. Implementations never parse `launchctl print` (ADR D10: unstable format).
type ldRunner interface {
	Bootstrap(uid int, plistPath string) error
	Bootout(uid int, label string) error
}

// ldUninstallResult records one label of ldUninstall.
type ldUninstallResult struct {
	Label     string
	NotLoaded bool // bootout reported the job was not loaded (tolerated)
	Removed   bool // the plist file existed and was removed
}

// ldUninstall boots out every hivemill.* label, then removes the plist files in dir. A job that is not loaded or a
// file that is already gone is not an error; any other failure is returned after all labels were tried.
func ldUninstall(dir string, uid int, r ldRunner) ([]ldUninstallResult, error) {
	var res []ldUninstallResult
	var errs []error
	for i := len(ldLabels) - 1; i >= 0; i-- {
		l := ldLabels[i]
		ur := ldUninstallResult{Label: l}
		if err := r.Bootout(uid, l); err != nil {
			if errors.Is(err, errLdNotLoaded) {
				ur.NotLoaded = true
			} else {
				errs = append(errs, fmt.Errorf("bootout %s: %w", l, err))
			}
		}
		switch err := os.Remove(filepath.Join(dir, ldPlistName(l))); {
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
