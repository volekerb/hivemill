package hm

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// The obs service next to the daemon: `hivemill init` puts it where this home already has a daemon service
// (hivemill-install does the same for a new machine).

// ServiceHost is the service manager of this machine.
type ServiceHost struct {
	GOOS, UserHome string
	ConfigDir      string // XDG config dir, where systemd/user is
	UID            int
	Run            func(argv ...string) (int, string)
}

// LocalServiceHost is the real systemctl / launchctl of the invoking user.
func LocalServiceHost(userHome string) ServiceHost {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(userHome, ".config")
	}
	return ServiceHost{GOOS: runtime.GOOS, UserHome: userHome, ConfigDir: config, UID: os.Getuid(),
		Run: func(argv ...string) (int, string) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
			if err != nil {
				return 1, strings.TrimSpace(string(out) + " " + err.Error())
			}
			return 0, string(out)
		}}
}

// InstallObsService installs and starts the obs service next to the daemon service of e.Home, with that service's
// FACTORY_HOME and PATH. A machine without a daemon service for this home is left alone. An obs service file that
// exists is kept, and without restart so is its state. restart restarts obs, so a new obs_listen applies. It returns what it did; a failure is
// a "warning:" note, because the home itself is ready.
func InstallObsService(e Env, h ServiceHost, restart bool) []string {
	var notes []string
	var err error
	switch h.GOOS {
	case "linux":
		notes, err = installObsUnit(e, h, restart)
	case "darwin":
		notes, err = installObsAgent(e, h, restart)
	}
	if err != nil {
		notes = append(notes, "warning: the obs service is not running: "+err.Error())
	}
	return notes
}

const noObsService = "note: no obs service installed (%s); run `hivemill obs` by hand, or hivemill-install"

func (h ServiceHost) must(argv ...string) error {
	if rc, out := h.Run(argv...); rc != 0 {
		return fmt.Errorf("%s: %s", strings.Join(argv, " "), strings.TrimSpace(out))
	}
	return nil
}

// sameHome reports whether a daemon whose FACTORY_HOME is set to v ("" = the default ~/factory) serves home.
func sameHome(v, userHome, home string) bool {
	if v == "" {
		v = filepath.Join(userHome, "factory")
	}
	return filepath.Clean(v) == filepath.Clean(home)
}

func installObsUnit(e Env, h ServiceHost, restart bool) ([]string, error) {
	dir := filepath.Join(h.ConfigDir, "systemd", "user")
	daemon := filepath.Join(dir, "hivemill.service")
	b, err := os.ReadFile(daemon)
	if err != nil {
		return []string{fmt.Sprintf(noObsService, "no hivemill.service in "+dir)}, nil
	}
	texts := []string{string(b)}
	dropins, _ := filepath.Glob(daemon + ".d/*.conf")
	for _, p := range dropins {
		d, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		texts = append(texts, string(d))
	}
	env := map[string]string{} // only these two go into the obs unit: a drop-in may hold secrets
	for _, ln := range strings.Split(strings.Join(texts, "\n"), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(ln), "Environment=")
		if !ok {
			continue
		}
		for _, kv := range unitWords(v) {
			if k, x, ok := strings.Cut(kv, "="); ok && (k == "PATH" || k == "FACTORY_HOME") {
				env[k] = x
			}
		}
	}
	factoryHome := strings.ReplaceAll(env["FACTORY_HOME"], "%h", h.UserHome)
	if !sameHome(factoryHome, h.UserHome, e.Home) {
		return []string{fmt.Sprintf(noObsService, "hivemill.service serves another factory home")}, nil
	}
	var notes []string
	unit := filepath.Join(dir, "hivemill-obs.service")
	_, statErr := os.Stat(unit)
	if statErr == nil && !restart { // a dashboard the owner stopped or disabled stays so
		return []string{"note: " + unit + " is kept; `systemctl --user enable --now hivemill-obs.service` starts it"}, nil
	}
	if statErr != nil {
		src, err := readRoleFile(filepath.Join(e.Home, "systemd", "hivemill-obs.service"))
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(unit, []byte(withEnvironment(string(src), env)), 0o644); err != nil {
			return nil, err
		}
		notes = append(notes, "created "+unit)
	}
	cmds := [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "hivemill-obs.service"}}
	if restart {
		cmds = append(cmds, []string{"systemctl", "--user", "restart", "hivemill-obs.service"})
	}
	for _, c := range cmds {
		if err := h.must(c...); err != nil {
			if statErr != nil { // a kept unit is never started again: remove ours so the next init retries
				_ = os.Remove(unit)
			}
			return notes, err
		}
	}
	return append(notes, "hivemill-obs.service enabled and started"), nil
}

// withEnvironment gives the unit the daemon's PATH and FACTORY_HOME; without a daemon PATH the unit keeps its own.
func withEnvironment(unit string, env map[string]string) string {
	var set, out []string
	for _, k := range []string{"PATH", "FACTORY_HOME"} {
		if v := env[k]; v != "" {
			set = append(set, `Environment="`+k+"="+v+`"`)
		}
	}
	for _, ln := range strings.Split(unit, "\n") {
		if env["PATH"] != "" && strings.HasPrefix(ln, "Environment=PATH=") {
			continue
		}
		if strings.HasPrefix(ln, "ExecStart=") {
			out = append(out, set...)
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// unitWords splits the value of an Environment= line: blanks separate, quotes group.
func unitWords(s string) []string {
	var words []string
	var cur strings.Builder
	quote := rune(0)
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

func installObsAgent(e Env, h ServiceHost, restart bool) ([]string, error) {
	dir := filepath.Join(h.UserHome, "Library", "LaunchAgents")
	b, err := os.ReadFile(filepath.Join(dir, "hivemill.daemon.plist"))
	if err != nil {
		return []string{fmt.Sprintf(noObsService, "no hivemill.daemon.plist in "+dir)}, nil
	}
	env := plistStrings(string(b))
	if !sameHome(env["FACTORY_HOME"], h.UserHome, e.Home) {
		return []string{fmt.Sprintf(noObsService, "hivemill.daemon serves another factory home")}, nil
	}
	var notes []string
	plist := filepath.Join(dir, "hivemill.obs.plist")
	_, statErr := os.Stat(plist)
	if statErr == nil && !restart { // a dashboard the owner unloaded stays so
		return []string{fmt.Sprintf("note: %s is kept; `launchctl bootstrap gui/%d %s` loads it", plist, h.UID, plist)}, nil
	}
	if statErr != nil {
		path := env["PATH"]
		if path == "" {
			path = DefaultLaunchdPath(h.UserHome)
		}
		logs := filepath.Join(e.Home, "logs")
		p, err := RenderPlist(e, "obs", PlistInput{FactoryHome: e.Home, Path: path, LogDir: logs})
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(plist, []byte(p), 0o644); err != nil {
			return nil, err
		}
		notes = append(notes, "created "+plist)
	}
	// bootstrap starts a job that is not loaded; it fails for a loaded one, which kickstart then (re)starts
	target := fmt.Sprintf("gui/%d", h.UID)
	if rc, out := h.Run("launchctl", "bootstrap", target, plist); rc != 0 {
		kick := []string{"launchctl", "kickstart", target + "/hivemill.obs"}
		if restart {
			kick = []string{"launchctl", "kickstart", "-k", target + "/hivemill.obs"}
		}
		if err := h.must(kick...); err != nil {
			if statErr != nil { // as on Linux: the next init retries
				_ = os.Remove(plist)
			}
			return notes, fmt.Errorf("launchctl bootstrap: %s; %w", strings.TrimSpace(out), err)
		}
	}
	return append(notes, "LaunchAgent hivemill.obs loaded and started"), nil
}

var plistPair = regexp.MustCompile(`<key>([^<]*)</key>\s*<string>([^<]*)</string>`)

// plistStrings maps each <key> of a plist that a <string> follows to that string.
func plistStrings(s string) map[string]string {
	m := map[string]string{}
	for _, p := range plistPair.FindAllStringSubmatch(s, -1) {
		m[html.UnescapeString(p[1])] = html.UnescapeString(p[2])
	}
	return m
}
