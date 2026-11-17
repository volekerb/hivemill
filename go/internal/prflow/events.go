package prflow

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxLineBytes is the longest events line that is parsed. A longer line is counted as skipped and never held in
// memory: the reader drops it chunk by chunk.
const MaxLineBytes = 4 << 20

// Normalised actions of agent events (entity "agent"); task events keep the action name factory_cli.py wrote.
const (
	actCoderSpawn = "coder-spawn"
	actCoderDone  = "coder-done"
)

// TaskEvent is one event of a task, reduced to what the stage walk needs.
type TaskEvent struct {
	At     time.Time
	Action string
	// Arg is the part of the detail the stage map reads: the "kind" of review-autospawn-wait, the "sha" of
	// review-autospawn, "stale" for a judge-blocked that asks for a refresh, "blocked" for an update to status=blocked.
	Arg string
}

// Interval is a half-open time range [From, To).
type Interval struct {
	From, To time.Time
}

// Index is what one streamed pass over events.jsonl keeps.
type Index struct {
	// PRTask maps a PR number of the repo to the task whose update event carried pr=<url>.
	PRTask map[int]string
	// BranchTask maps a head branch to the task of its pr-create event.
	BranchTask map[string]string
	// TaskOrg is the org prefix (claude, cursor) of the task's latest coder agent.
	TaskOrg map[string]string
	// Events holds the stage events per task, in file order, from NotBefore on.
	Events map[string][]TaskEvent
	// Red holds the red-main windows of the repo. An open window has a zero To.
	Red []Interval
	// RedRepos holds the red-main events and windows of every repo in the log, for the red main section.
	RedRepos map[string]*RedState
	// Lines and Skipped count the lines read and the lines that were not a usable JSON event.
	Lines, Skipped int
}

type rawEvent struct {
	TS       string  `json:"ts"`
	Actor    string  `json:"actor"`
	Entity   string  `json:"entity"`
	EntityID string  `json:"entity_id"`
	Action   string  `json:"action"`
	Detail   *string `json:"detail"`
}

var coderRe = regexp.MustCompile(`^(?:([a-z0-9]+)-)?coder-t-(\d+)`)

// stageActions are the task actions the stage walk reads. Every other action is ignored, so a renamed or new action
// in bin/factory_cli.py never fails a run.
var stageActions = map[string]bool{
	"pr-create": true, "pr-ready": true, "review-autospawn-wait": true, "review-autospawn": true,
	"reviewer-pass": true, "reviewer-fail": true, "qa-pass": true, "qa-fail": true,
	"coder-ci-resume": true, "judge-reject": true, "red-pr-unowned": true, "judge-autospawn": true,
	"mech-blocked": true, "judge-hold-clear": true, "judge-hold-set": true, "judge-hold": true,
	"judge-blocked": true, "refreshed": true, "verdict-carried": true, "update": true,
}

// scanLines calls fn for every line of r that is at most maxLine bytes long (without its line end). It returns the
// number of lines seen and the number dropped for being longer. Memory use is bounded by maxLine, not by the file.
func scanLines(r io.Reader, maxLine int, fn func(line []byte)) (lines, tooLong int, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	buf := make([]byte, 0, 64<<10)
	for {
		buf = buf[:0]
		over := false
		var rerr error
		for {
			var chunk []byte
			chunk, rerr = br.ReadSlice('\n')
			if !over {
				if len(buf)+len(chunk) > maxLine+1 {
					over = true
					buf = buf[:0]
				} else {
					buf = append(buf, chunk...)
				}
			}
			if !errors.Is(rerr, bufio.ErrBufferFull) {
				break
			}
		}
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return lines, tooLong, fmt.Errorf("read events: %w", rerr)
		}
		if over || len(buf) > 0 {
			lines++
			if over {
				tooLong++
			} else {
				fn(buf)
			}
		}
		if rerr != nil {
			return lines, tooLong, nil
		}
	}
}

// ReadEvents streams an events.jsonl and keeps the PR-to-task hints, the coder org per task, the red-main windows of
// repo and the stage events at or after notBefore. Lines that are not JSON, have no parsable ts or are longer than
// MaxLineBytes are skipped and counted.
func ReadEvents(r io.Reader, repo string, notBefore time.Time) (*Index, error) {
	ix := &Index{
		PRTask:     map[int]string{},
		BranchTask: map[string]string{},
		TaskOrg:    map[string]string{},
		Events:     map[string][]TaskEvent{},
		RedRepos:   map[string]*RedState{},
	}
	lines, tooLong, err := scanLines(r, MaxLineBytes, func(line []byte) {
		if len(strings.TrimSpace(string(line))) == 0 {
			return
		}
		var ev rawEvent
		if json.Unmarshal(line, &ev) != nil {
			ix.Skipped++
			return
		}
		at, perr := time.Parse(time.RFC3339, ev.TS)
		if perr != nil {
			ix.Skipped++
			return
		}
		detail := ""
		if ev.Detail != nil {
			detail = *ev.Detail
		}
		switch ev.Entity {
		case "redmain":
			st := ix.RedRepos[ev.EntityID]
			if st == nil {
				st = &RedState{}
				ix.RedRepos[ev.EntityID] = st
			}
			switch ev.Action {
			case "red-main":
				// The day is the date in the ts's own offset, so it does not depend on the machine time zone.
				st.Events = append(st.Events, RedEvent{At: at, Day: at.Format("2006-01-02")})
				if !st.open {
					st.Windows = append(st.Windows, Interval{From: at})
					st.open = true
				}
			case "red-main-clear":
				if st.open {
					st.Windows[len(st.Windows)-1].To = at
					st.open = false
				}
			}
		case "agent":
			if ev.Action != "spawn" && ev.Action != "done" {
				return
			}
			org, task, ok := coderAgent(ev.EntityID)
			if !ok {
				return
			}
			if org != "" {
				ix.TaskOrg[task] = org
			}
			if at.Before(notBefore) {
				return
			}
			act := actCoderSpawn
			if ev.Action == "done" {
				act = actCoderDone
			}
			ix.Events[task] = append(ix.Events[task], TaskEvent{At: at, Action: act})
		case "task":
			ix.taskEvent(ev, at, detail, repo, notBefore)
		}
	})
	if err != nil {
		return nil, err
	}
	if st := ix.RedRepos[repo]; st != nil {
		ix.Red = st.Windows
	}
	ix.Lines = lines
	ix.Skipped += tooLong
	return ix, nil
}

func (ix *Index) taskEvent(ev rawEvent, at time.Time, detail, repo string, notBefore time.Time) {
	task := ev.EntityID
	if org, _, ok := coderAgent(ev.Actor); ok && org != "" {
		ix.TaskOrg[task] = org
	}
	if !stageActions[ev.Action] {
		return
	}
	te := TaskEvent{At: at, Action: ev.Action}
	switch ev.Action {
	case "update":
		if n, ok := prNumber(detail, repo); ok {
			ix.PRTask[n] = task
		}
		if !hasField(detail, "status=blocked") {
			return
		}
		te.Arg = "blocked"
	case "pr-create":
		if b, ok := prCreateBranch(detail, repo); ok {
			ix.BranchTask[b] = task
		}
	case "review-autospawn-wait":
		te.Arg = jsonString(detail, "kind")
	case "review-autospawn":
		te.Arg = jsonString(detail, "sha")
	case "judge-blocked":
		if strings.Contains(detail, "STALE") || strings.Contains(detail, "head is now") {
			te.Arg = "stale"
		}
	}
	if at.Before(notBefore) {
		return
	}
	ix.Events[task] = append(ix.Events[task], te)
}

// coderAgent reads an agent name such as claude-coder-t-1591 or cursor-coder-t-0330-fix. org is "" unless the prefix is
// claude or cursor.
func coderAgent(name string) (org, task string, ok bool) {
	m := coderRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	if m[1] == "claude" || m[1] == "cursor" {
		org = m[1]
	}
	return org, "T-" + m[2], true
}

// prNumber finds pr=https://github.com/<repo>/pull/N in an update detail.
func prNumber(detail, repo string) (int, bool) {
	prefix := "pr=https://github.com/" + repo + "/pull/"
	for _, f := range strings.Fields(detail) {
		rest, ok := strings.CutPrefix(f, prefix)
		if !ok {
			continue
		}
		rest = strings.TrimSuffix(rest, "/")
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 {
			continue
		}
		return n, true
	}
	return 0, false
}

// prCreateBranch reads a pr-create detail "<repo> <branch>". A detail that names another repo does not match.
func prCreateBranch(detail, repo string) (string, bool) {
	f := strings.Fields(detail)
	switch {
	case len(f) == 1:
		return f[0], true
	case len(f) >= 2 && f[0] == repo:
		return f[len(f)-1], true
	}
	return "", false
}

func hasField(detail, want string) bool {
	for _, f := range strings.Fields(detail) {
		if f == want {
			return true
		}
	}
	return false
}

// jsonString returns the string value of key in a JSON object detail, or "" when the detail is not such an object.
func jsonString(detail, key string) string {
	if !strings.HasPrefix(detail, "{") {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(detail), &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m[key], &s) != nil {
		return ""
	}
	return s
}
