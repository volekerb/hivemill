package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// hostFS exposes the files of the repo clone through Host.ReadFile as an fs.FS for ldTemplates.
type hostFS struct {
	h   Host
	dir string
}

func (f hostFS) Open(name string) (fs.File, error) { return nil, fs.ErrInvalid }

// ReadFile makes fs.ReadFile use the host (fs.ReadFile prefers ReadFileFS).
func (f hostFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	return f.h.ReadFile(filepath.Join(f.dir, path.Clean(name)))
}

// hostRunner runs launchctl through Host.Run, so a fake host sees the argv.
type hostRunner struct{ h Host }

func (r hostRunner) run(args ...string) (int, string) {
	return r.h.Run(append([]string{"launchctl"}, args...), "", 120*time.Second, true)
}

func (r hostRunner) Bootstrap(uid int, plist string) error {
	argv := []string{"bootstrap", fmt.Sprintf("gui/%d", uid), plist}
	if rc, out := r.run(argv...); rc != 0 {
		return fmt.Errorf("launchctl %s failed (rc=%d): %s", strings.Join(argv, " "), rc, strings.TrimSpace(out))
	}
	return nil
}

func (r hostRunner) Bootout(uid int, label string) error {
	argv := []string{"bootout", fmt.Sprintf("gui/%d/%s", uid, label)}
	rc, out := r.run(argv...)
	switch {
	case rc == 0:
		return nil
	case rc == 3 || rc == 113: // launchctl: No such process / Could not find service
		return fmt.Errorf("%w: %s", errLdNotLoaded, strings.TrimSpace(out))
	}
	return fmt.Errorf("launchctl %s failed (rc=%d): %s", strings.Join(argv, " "), rc, strings.TrimSpace(out))
}

func (r hostRunner) Kickstart(uid int, label string) error {
	if rc, out := r.run("kickstart", "-k", fmt.Sprintf("gui/%d/%s", uid, label)); rc != 0 {
		return fmt.Errorf("launchctl kickstart %s failed (rc=%d): %s", label, rc, strings.TrimSpace(out))
	}
	return nil
}

func (c *Ctx) agentsDir() string { return filepath.Join(c.home, "Library", "LaunchAgents") }

// bootJobs are the launchd jobs an install writes, in bootstrap order.
var bootJobs = []string{"hivemill.daemon", "hivemill.digest", "hivemill.health"}

// The renderer accepts, and uninstall removes, every label in ldLabels: add the installed jobs to the older ones,
// so that --uninstall-boot-units still cleans a machine an earlier installer set up.
func init() { ldLabels = append(append([]string(nil), bootJobs...), ldLabels...) }

func (c *Ctx) launchdVars() ldVars {
	// agent bin dirs first (the claude installer and our tools), then the Homebrew and system dirs
	pathParts := []string{c.bindir, "/opt/homebrew/bin", "/usr/bin", "/bin"}
	return ldVars{
		FactoryHome: c.fh,
		Path:        strings.Join(pathParts, ":"),
		HerdrBin:    filepath.Join(c.bindir, "herdr"),
		FactoryBin:  c.factory,
	}
}

// stepBootUnits installs the launchd user agents on macOS (ADR 0003 4.2): render, write, bootout, bootstrap.
func stepBootUnits(c *Ctx) (string, error) {
	uid := c.host.Euid()
	dir := c.agentsDir()
	labels := bootJobs
	tdir := filepath.Join(c.fh, "launchd")
	tm := ldNewTemplates(hostFS{c.host, tdir})
	vars := c.launchdVars()
	plists := map[string][]byte{}
	for _, l := range labels {
		b, err := tm.Render(l, vars)
		if err != nil {
			if c.plan && errors.Is(err, fs.ErrNotExist) {
				// a first install has no clone yet in a dry run: show the plan without the content
				c.emit(fmt.Sprintf("    render %s/%s.plist.in -> %s (after the clone)", tdir, l, filepath.Join(dir, ldPlistName(l))))
				continue
			}
			return "", stepWrap(err, "render "+l)
		}
		plists[l] = b
	}
	if c.plan {
		c.emit("    $ mkdir -p " + dir)
		c.emit("    $ mkdir -p " + vars.FactoryHome + "/logs")
		for _, l := range labels {
			p := filepath.Join(dir, ldPlistName(l))
			if b, ok := plists[l]; ok {
				c.emit("    write " + p + " (mode 0644):")
				for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
					c.emit("      | " + ln)
				}
			}
			c.emit(fmt.Sprintf("    $ launchctl bootout gui/%d/%s   (a job that is not loaded is fine)", uid, l))
			c.emit(fmt.Sprintf("    $ launchctl bootstrap gui/%d %s", uid, p))
		}
		return "plan", nil
	}
	// 0755 not Mkdirs' 0777: launchd ignores agents in a group-writable directory (a 002 umask would make one)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := c.host.Mkdirs(filepath.Join(c.fh, "logs")); err != nil {
		return "", fmt.Errorf("create the log directory: %w", err)
	}
	if err := ldInstall(dir, plists); err != nil {
		return "", stepWrap(err, "write plists")
	}
	r := hostRunner{c.host}
	for _, l := range labels {
		// idempotent: an already-loaded job makes bootstrap fail, so boot it out first. A bootout failure is not
		// fatal (newer macOS answers rc 5 for a job that is not loaded): bootstrap below reports a real problem.
		_ = r.Bootout(uid, l)
		p := filepath.Join(dir, ldPlistName(l))
		var err error
		for try := 0; try < 3; try++ { // bootout is asynchronous: the first bootstrap can lose the race
			if err = r.Bootstrap(uid, p); err == nil {
				break
			}
			if try < 2 {
				c.host.Sleep(time.Second)
			}
		}
		if err != nil {
			return "", stepWrap(err, "bootstrap "+l)
		}
	}
	return fmt.Sprintf("launchd agents %s installed in %s and bootstrapped", strings.Join(labels, ", "), dir), nil
}

// runUninstallBootUnits is --uninstall-boot-units on macOS: boot out every hivemill.* job, remove the plists.
func runUninstallBootUnits(host Host, a *Args, out io.Writer) int {
	c := NewCtx(host, a, out, a.DryRun)
	uid, dir := host.Euid(), c.agentsDir()
	if a.DryRun {
		for _, l := range ldLabels {
			c.emit(fmt.Sprintf("    $ launchctl bootout gui/%d/%s", uid, l))
			c.emit("    rm " + filepath.Join(dir, ldPlistName(l)))
		}
		c.emit("plan only: nothing was changed")
		return 0
	}
	res, err := ldUninstall(dir, uid, hostRunner{host})
	for _, r := range res {
		c.emit(fmt.Sprintf("[ok] %s: not loaded=%t, plist removed=%t", r.Label, r.NotLoaded, r.Removed))
	}
	if err != nil {
		c.emit("[FAIL] uninstall-boot-units: " + err.Error())
		c.emit("RESULT FAIL step=uninstall-boot-units")
		return 1
	}
	c.emit("RESULT PASS boot units removed")
	return 0
}
