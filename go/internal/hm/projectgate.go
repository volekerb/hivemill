package hm

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A project's own gate command (`hivemill project add --gate CMD`): the gate's "affected" check runs it in the task
// clone instead of ci/affected.py, so a non-Python project is tested with its own tools (pnpm, go, cargo, make ...).

// projectGate is the gate command and its timeout of a project ("" when it has none).
func (r *Runner) projectGate(project string) (string, time.Duration) {
	cmd, limit, _ := r.readProjectGate(project)
	return cmd, limit
}

// readProjectGate is projectGate that tells a failed read from a project without a gate command.
func (r *Runner) readProjectGate(project string) (string, time.Duration, error) {
	s, ok := r.Store.(*SQLite)
	if !ok || project == "" {
		return "", 0, nil
	}
	var cmd string
	var secs int
	if err := s.db.QueryRow(`SELECT gate_cmd, gate_timeout_s FROM projects WHERE id=?`, project).Scan(&cmd, &secs); errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	} else if err != nil {
		return "", 0, err
	}
	if secs <= 0 {
		secs = 3600
	}
	return cmd, time.Duration(secs) * time.Second, nil
}

// runProjectGate runs the gate command with bash in the clone (the environment without Claude or GitHub tokens) and
// passes on exit 0. The output goes to gate-cmd.log in the round's log dir.
func (r *Runner) runProjectGate(wt, cmd string, limit time.Duration, logdir string) (bool, string) {
	return r.runProjectGateAt(place{r: r}, wt, cmd, limit, logdir)
}

// runProjectGateAt is runProjectGate where the worktree is; the log is written here.
func (r *Runner) runProjectGateAt(p place, wt, cmd string, limit time.Duration, logdir string) (bool, string) {
	rc, _, note := r.execProjectGateAt(p, wt, cmd, limit, logdir)
	return rc == 0, note
}

// execProjectGate is runProjectGate with the exit code and the whole output.
func (r *Runner) execProjectGate(wt, cmd string, limit time.Duration, logdir string) (int, string, string) {
	return r.execProjectGateAt(place{r: r}, wt, cmd, limit, logdir)
}

func (r *Runner) execProjectGateAt(p place, wt, cmd string, limit time.Duration, logdir string) (int, string, string) {
	rc, text := p.sh(wt, limit, cleanEnv(), "bash", "-c", cmd)
	log := filepath.Join(logdir, "gate-cmd.log")
	_ = os.WriteFile(log, []byte(text), 0o600)
	return rc, text, gateNote(cmd, rc, limit, log, text)
}

// gateNote is the gate's line about one gate_cmd result.
func gateNote(cmd string, rc int, limit time.Duration, log, text string) string {
	switch {
	case rc == 0:
		return fmt.Sprintf("project gate `%s` ok", cmd)
	case rc == 124:
		return fmt.Sprintf("project gate `%s` timed out after %s (log %s)", cmd, limit, log)
	}
	return fmt.Sprintf("project gate `%s` exited %d (log %s):\n%s", cmd, rc, log, lastLines(text, 8))
}
