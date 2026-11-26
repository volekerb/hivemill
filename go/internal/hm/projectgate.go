package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A project's own gate command (`hivemill project add --gate CMD`): the gate's "affected" check runs it in the task
// clone instead of ci/affected.py, so a non-Python project is tested with its own tools (pnpm, go, cargo, make ...).

// projectGate is the gate command and its timeout of a project ("" when it has none).
func (r *Runner) projectGate(project string) (string, time.Duration) {
	s, ok := r.Store.(*SQLite)
	if !ok || project == "" {
		return "", 0
	}
	var cmd string
	var secs int
	if err := s.db.QueryRow(`SELECT gate_cmd, gate_timeout_s FROM projects WHERE id=?`, project).Scan(&cmd, &secs); err != nil {
		return "", 0
	}
	if secs <= 0 {
		secs = 3600
	}
	return cmd, time.Duration(secs) * time.Second
}

// runProjectGate runs the gate command with bash in the clone (the environment without Claude or GitHub tokens) and
// passes on exit 0. The output goes to gate-cmd.log in the round's log dir.
func (r *Runner) runProjectGate(wt, cmd string, limit time.Duration, logdir string) (bool, string) {
	rc, _, note := r.execProjectGate(wt, cmd, limit, logdir)
	return rc == 0, note
}

// execProjectGate is runProjectGate with the exit code and the whole output.
func (r *Runner) execProjectGate(wt, cmd string, limit time.Duration, logdir string) (int, string, string) {
	rc, text := r.Tools.Sh(wt, limit, cleanEnv(), "bash", "-c", cmd)
	log := filepath.Join(logdir, "gate-cmd.log")
	_ = os.WriteFile(log, []byte(text), 0o600)
	switch {
	case rc == 0:
		return rc, text, fmt.Sprintf("project gate `%s` ok", cmd)
	case rc == 124:
		return rc, text, fmt.Sprintf("project gate `%s` timed out after %s (log %s)", cmd, limit, log)
	}
	return rc, text, fmt.Sprintf("project gate `%s` exited %d (log %s):\n%s", cmd, rc, log, lastLines(text, 8))
}
