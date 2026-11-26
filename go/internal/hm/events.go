package hm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Harness names the agent runtime in every event; Claude Code is the only adapter today.
const Harness = "claude-code"

// Event is one line of state/hivemill/events.jsonl (docs/option-b-runner.md#event-log). Known kinds:
// task.dispatched, session.started, session.ended, session.closed, gate.blocked, gate.passed, review.findings, ci.red,
// pr.refreshed, judge.verdict, pr.merged, pr.reverted, task.failed, task.cancelled, task.depends_repointed, goal.checked, prompt.sent, prompt.record_failed, dispatch.paused_waiting, task.unstartable.
type Event struct {
	TS      string         `json:"ts"`
	Event   string         `json:"event"`
	TaskID  string         `json:"task_id,omitempty"`
	GoalID  string         `json:"goal_id,omitempty"`
	Project string         `json:"project,omitempty"`
	Session string         `json:"session_id,omitempty"`
	Role    string         `json:"role,omitempty"` // task | judge | review
	Harness string         `json:"harness"`
	Machine string         `json:"machine,omitempty"` // the machine that wrote the line (config machine_name, else the hostname)
	Model   string         `json:"model,omitempty"`
	Effort  string         `json:"effort,omitempty"`
	CostUSD *float64       `json:"cost_usd,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// Log appends events to a JSONL file under an exclusive lock. Machine stamps every line (empty = the hostname).
type Log struct {
	Path    string
	Machine string
}

func (l Log) Emit(kind string, r *Run, e Event) error {
	e.TS = time.Now().Format(time.RFC3339)
	e.Event = kind
	if e.Harness == "" { // a future adapter names its own harness; Claude Code is the default
		e.Harness = Harness
	}
	if e.Machine == "" {
		e.Machine = MachineName(l.Machine)
	}
	if r != nil {
		e.TaskID, e.GoalID, e.Project = r.Task, r.Goal, r.Project
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Read returns every parseable event (bad lines are skipped).
func (l Log) Read() ([]Event, error) {
	f, err := os.Open(l.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
