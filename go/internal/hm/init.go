package hm

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// InitDirs are the directories `hivemill init` creates under the factory home.
var InitDirs = []string{"db", "worktrees", "logs", "state/hivemill/locks", "state/hivemill/runs", "state/hivemill/settings",
	"state/hivemill/spawn", "state/hivemill/backups"}

// InitOptions are the factory config keys `hivemill init` may set (empty = leave as is).
type InitOptions struct {
	NoreplyEmail, ScrubTermsFile string
	// ObsListen sets the runner setting obs_listen, the host `hivemill obs` binds.
	ObsListen string
}

// InitHome makes a standalone factory home: the state dirs, and the factory DB with the tables the runner uses
// (FactorySchema plus the hm_* tables). It is idempotent: on an existing factory home it only adds what
// is missing and never alters a table. The hm_config keys keep their built-in defaults (`hivemill config` lists them);
// role prompts and settings come from the binary unless the home has its own roles/hivemill.
func InitHome(home string, o InitOptions) ([]string, error) {
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("home %q must be an absolute path", home)
	}
	var notes []string
	for _, d := range InitDirs {
		p := filepath.Join(home, d)
		if _, err := os.Stat(p); err != nil {
			if err := os.MkdirAll(p, 0o700); err != nil {
				return notes, err
			}
			notes = append(notes, "created "+p)
		}
	}
	// db/factory.db, unless the home only has the legacy <home>/factory.db (an old factory home): then that one
	dbp := filepath.Join(home, "db", "factory.db")
	if _, err := os.Stat(dbp); err != nil {
		if _, err := os.Stat(filepath.Join(home, "factory.db")); err == nil {
			dbp = filepath.Join(home, "factory.db")
		}
	}
	_, statErr := os.Stat(dbp)
	s, err := CreateSQLite(dbp)
	if err != nil {
		return notes, err
	}
	defer s.Close()
	if statErr != nil {
		notes = append(notes, "created "+dbp)
	} else {
		notes = append(notes, "checked "+dbp)
	}
	for _, k := range []string{"noreply_email", "scrub_terms_file"} {
		v := map[string]string{"noreply_email": o.NoreplyEmail, "scrub_terms_file": o.ScrubTermsFile}[k]
		if v == "" {
			continue
		}
		if err := SetFactoryKey(s, k, v); err != nil {
			return notes, err
		}
		notes = append(notes, "set "+k)
	}
	if o.ObsListen != "" {
		if err := s.SetCfg("obs_listen", o.ObsListen); err != nil {
			return notes, err
		}
		notes = append(notes, "set obs_listen")
	}
	if s.FactoryCfg("noreply_email") == "" { // as bin/factory: the git identity, when it is a noreply address
		if g := GitGlobalEmail(); g != "" && CheckFactoryKey("noreply_email", g) == nil {
			if err := s.SetFactoryCfg("noreply_email", g); err != nil {
				return notes, err
			}
			notes = append(notes, "set noreply_email from git config --global user.email")
		}
	}
	more, err := EnsureHomeFiles(home)
	notes = append(notes, more...)
	if err != nil {
		return notes, err
	}
	for _, k := range []string{"noreply_email", "scrub_terms_file"} {
		if s.FactoryCfg(k) == "" {
			notes = append(notes, "note: "+k+" is not set: "+FactoryKeys[k])
		}
	}
	return notes, nil
}

// PlistInput fills launchd/hivemill.daemon.plist.in.
type PlistInput struct {
	FactoryHome, Path, LogDir string
}

// DefaultLaunchdPath is the PATH of the daemon's LaunchAgent: launchd starts it with a bare PATH, and the runner calls
// claude, hc and hivemill (~/.local/bin), gh (Homebrew) and git and sh (the system).
func DefaultLaunchdPath(userHome string) string {
	return strings.Join([]string{filepath.Join(userHome, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin",
		"/usr/sbin", "/sbin"}, ":")
}

// RenderDaemonPlist renders the daemon's LaunchAgent (launchd/hivemill.daemon.plist.in, the home's copy or the
// built-in one). Values are XML-escaped and the result must parse.
func RenderDaemonPlist(e Env, in PlistInput) (string, error) { return RenderPlist(e, "daemon", in) }

// LaunchAgents are the LaunchAgents `hivemill launchd` renders: the pass loop, the morning digest, the dead-man
// check that runs `hivemill health --alert` every 5 minutes, and the dashboard (`hivemill obs`).
var LaunchAgents = []string{"daemon", "digest", "health", "obs"}

// RenderLaunchAgent is RenderPlist.
func RenderLaunchAgent(e Env, name string, in PlistInput) (string, error) {
	return RenderPlist(e, name, in)
}

// RenderPlist renders launchd/hivemill.<name>.plist.in (the home's copy or the built-in one).
func RenderPlist(e Env, name string, in PlistInput) (string, error) {
	known := false
	for _, n := range LaunchAgents {
		known = known || n == name
	}
	if !known {
		return "", fmt.Errorf("no LaunchAgent %q (known: %s)", name, strings.Join(LaunchAgents, ", "))
	}
	src, err := readRoleFile(filepath.Join(e.Home, "launchd", "hivemill."+name+".plist.in"))
	if err != nil {
		return "", err
	}
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	tpl, err := template.New("plist").Option("missingkey=error").Parse(string(src))
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, PlistInput{FactoryHome: esc(in.FactoryHome), Path: esc(in.Path), LogDir: esc(in.LogDir)}); err != nil {
		return "", err
	}
	d := xml.NewDecoder(bytes.NewReader(out.Bytes()))
	d.Strict = true
	for {
		if _, err := d.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("the rendered plist is not valid XML: %w", err)
		}
	}
	return out.String(), nil
}
