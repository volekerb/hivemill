package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Owner alerts: the dead-man alert of `hivemill health --alert`, and every other alert that opens an owner alert feed
// row (hivemill-idle, hivemill-asks, hivemill-planner, hivemill-digest, hivemill-ci). Every alert is appended to <home>/logs/alerts.log.
// Then alert_cmd, when set, runs as `sh -c "$alert_cmd" hivemill-alert TITLE TEXT` (also in the environment as
// HIVEMILL_ALERT_TITLE and HIVEMILL_ALERT_TEXT; the text is never pasted into the command). Without alert_cmd, macOS
// shows a notification (osascript); other systems only log. On a factory checkout, alert_cmd can keep the old
// message to the planner: `factory msg --kind alert claude-boss "$1" "$2"`.

// GOOS is runtime.GOOS (tests replace it).
var GOOS = runtime.GOOS

// AlertLog is the file every alert is appended to.
func (e Env) AlertLog() string { return filepath.Join(e.Home, "logs", "alerts.log") }

// ownerAlert sends a feed row that was just opened (hivemill-idle, -asks, -planner, -digest, -ci) through Alert. A delivery
// failure is printed; the feed row stays open either way.
func (r *Runner) ownerAlert(kind, text string) {
	if _, err := r.Alert(kind, text); err != nil {
		fmt.Fprintf(os.Stderr, "hivemill: alert %s: %v\n", kind, err)
	}
}

// Alert tells the owner: the log line always, then alert_cmd or the platform notification. It returns how it was sent.
func (r *Runner) Alert(title, text string) (string, error) {
	line := fmt.Sprintf("%s %s: %s\n", time.Now().Format(time.RFC3339), title, strings.ReplaceAll(text, "\n", " "))
	logErr := os.MkdirAll(filepath.Dir(r.Env.AlertLog()), 0o700)
	if logErr == nil {
		var f *os.File
		if f, logErr = os.OpenFile(r.Env.AlertLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); logErr == nil {
			_, logErr = f.WriteString(line)
			if cerr := f.Close(); logErr == nil {
				logErr = cerr
			}
		}
	}
	if r.Tools.Sh == nil {
		return "log", logErr
	}
	env := append(os.Environ(), "FACTORY_AGENT=owner", "HIVEMILL_ALERT_TITLE="+title, "HIVEMILL_ALERT_TEXT="+text)
	if cmd := strings.TrimSpace(r.Store.Cfg("alert_cmd")); cmd != "" {
		if rc, out := r.Tools.Sh(r.Env.Home, time.Minute, env, "sh", "-c", cmd, "hivemill-alert", title, text); rc != 0 {
			return "alert_cmd", fmt.Errorf("alert_cmd exited %d: %s", rc, firstLine(lastLines(out, 2)))
		}
		return "alert_cmd", logErr
	}
	if GOOS == "darwin" {
		// AppleScript gets the strings as arguments, never as script text
		if rc, out := r.Tools.Sh(r.Env.Home, time.Minute, env, "osascript", "-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, text); rc != 0 {
			return "osascript", fmt.Errorf("osascript exited %d: %s", rc, firstLine(out))
		}
		return "osascript", logErr
	}
	return "log", logErr
}
