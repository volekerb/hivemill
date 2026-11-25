package hm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code's workspace trust. A session starts with `--add-dir <home>` in its clone under <home>/worktrees; a folder
// the owner never trusted shows the "trust this folder?" prompt and the session waits there forever. Claude Code has
// no documented command or flag that grants trust (it is skipped only in -p mode, which the runner never uses), so
// `hivemill init` only reads ~/.claude.json and, for a folder that is not trusted, prints what the owner runs once.
// Neither init nor the runner ever answers the prompt.

// ErrTrustPrompt is a session stopped at the workspace trust prompt.
var ErrTrustPrompt = errors.New("the session is waiting at Claude Code's workspace trust prompt")

// trustPromptMarks are texts of the trust prompt (lower case); one on the screen means the session is stuck there.
var trustPromptMarks = []string{"do you trust the files in this folder", "trust this folder", "yes, i trust this folder",
	"is this a project you created or one you trust"}

// ShowsTrustPrompt reports whether a terminal screen shows the workspace trust prompt.
func ShowsTrustPrompt(screen string) bool {
	s := strings.ToLower(screen)
	for _, m := range trustPromptMarks {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// stuckAtTrust reads the session's screen (`hc read`) and returns ErrTrustPrompt, with what the owner does, when it
// shows the trust prompt. A screen that cannot be read is no evidence either way.
func (r *Runner) stuckAtTrust(name string) error {
	rc, screen, _ := r.Tools.HC("read", name)
	if rc != 0 || !ShowsTrustPrompt(screen) {
		return nil
	}
	return fmt.Errorf("%w (%s): the folder is not trusted; the runner does not answer it. %s", ErrTrustPrompt, name,
		TrustInstruction(r.Env.Home))
}

// TrustInstruction is what the owner runs once so sessions of this home never stop at the trust prompt.
func TrustInstruction(home string) string {
	return fmt.Sprintf("As the owner, run `cd %s && claude` once, accept \"trust this folder\", and quit; then do the same in %s",
		home, filepath.Join(home, "worktrees"))
}

// Trusted reports whether ~/.claude.json records an accepted trust dialog for dir or one of its parents. It only reads
// the file. An unreadable file counts as not trusted.
func Trusted(userHome, dir string) bool {
	b, err := os.ReadFile(filepath.Join(userHome, ".claude.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return false
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if p, ok := cfg.Projects[d]; ok && p.HasTrustDialogAccepted {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

// TrustNotes are the init lines for the folders the sessions use: the home (--add-dir) and the worktree root.
func TrustNotes(userHome, home string) []string {
	var untrusted []string
	for _, d := range []string{home, filepath.Join(home, "worktrees")} {
		if !Trusted(userHome, d) {
			untrusted = append(untrusted, d)
		}
	}
	if len(untrusted) == 0 {
		return []string{"trust: " + home + " and its worktrees are trusted in ~/.claude.json"}
	}
	return []string{"trust: not trusted yet: " + strings.Join(untrusted, ", ") + ". Sessions would stop at Claude Code's " +
		"\"trust this folder?\" prompt (the runner fails them fast and never answers it). " + TrustInstruction(home) + "."}
}
