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
	for k, v := range map[string]string{"noreply_email": o.NoreplyEmail, "scrub_terms_file": o.ScrubTermsFile} {
		if v == "" {
			continue
		}
		if err := s.SetFactoryCfg(k, v); err != nil {
			return notes, err
		}
		notes = append(notes, "set "+k)
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
func RenderDaemonPlist(e Env, in PlistInput) (string, error) {
	src, err := readRoleFile(filepath.Join(e.Home, "launchd", "hivemill.daemon.plist.in"))
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
