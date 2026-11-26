package hm

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GET /api/obs/v1/board: every task of the factory in the column of where it is now (queued, working, in CI, to
// merge, held, done today), grouped inside a column by why it is not moving. GET /api/obs/v1/task: one task's trace
// with its title and the text of every prompt the runner sent. Both read only; design: docs/design/multi-factory.md.

// BoardCard is one task on the board.
type BoardCard struct {
	Key     string `json:"key,omitempty"` // the trace's key; empty for a task that never ran
	Task    string `json:"task"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
	Stage   string `json:"stage,omitempty"`
	AgeSec  int64  `json:"age_sec,omitempty"` // in the stage, or since it was filed for a queued task
	Note    string `json:"note,omitempty"`    // one detail: what it waits for, how it ended
	At      string `json:"at,omitempty"`      // when it ended (done cards)
	PR      string `json:"pr,omitempty"`
}

// BoardGroup is the cards of one column that share a cause. Cause "" is "moving": nothing to say.
type BoardGroup struct {
	Cause string      `json:"cause"`
	Bad   bool        `json:"bad"` // the cause is trouble, not an ordinary wait
	Count int         `json:"count"`
	Cards []BoardCard `json:"cards"` // at most boardCards, longest waiting first
}

// BoardColumn is one column.
type BoardColumn struct {
	ID     string       `json:"id"` // queued | working | ci | merge | held | done
	Name   string       `json:"name"`
	Count  int          `json:"count"`
	Groups []BoardGroup `json:"groups"`
}

// BoardDone counts today's finished tasks.
type BoardDone struct {
	Merged        int `json:"merged"`
	FailedRefiled int `json:"failed_refiled"`
	Failed        int `json:"failed"` // not re-filed
	Cancelled     int `json:"cancelled"`
}

// Board is the answer of /api/obs/v1/board.
type Board struct {
	API      int           `json:"api"`
	Now      time.Time     `json:"now"`
	Runner   RunnerInfo    `json:"runner"`
	Project  string        `json:"project"` // the filter ("" = all)
	Projects []string      `json:"projects"`
	Columns  []BoardColumn `json:"columns"`
	Done     BoardDone     `json:"done"`
}

const boardCards = 30

// mergeCause turns the runner's merge-hold text into a short cause; bad: it will not clear by waiting alone.
func mergeCause(hold string) (string, bool) {
	h := strings.ToLower(hold)
	switch {
	case h == "":
		return "Merging", false
	case strings.Contains(h, "main is red"), strings.Contains(h, "push run for its head failed"):
		return "Main is red", true
	case strings.Contains(h, "main's push run"):
		return "Main's checks still running", false
	case strings.Contains(h, "does not contain the base branch"):
		return "Behind main, refreshing", false
	case strings.Contains(h, "conflicts with the base"):
		return "Conflicts with main", true
	case strings.Contains(h, "required checks missing"):
		return "Required checks missing", true
	case strings.Contains(h, "ci is red"):
		return "CI red, repairing", true
	case strings.Contains(h, "pending"), strings.Contains(h, "ci"):
		return "CI still running", false
	case strings.Contains(h, "no judge merge"):
		return "Head moved, judging again", false
	}
	return truncate(strings.TrimSpace(hold), 80), true
}

// lastEvent is the task's newest event of one of these names ("" when none).
func lastEvent(t *ObsTask, names ...string) string {
	last, at := "", time.Time{}
	for _, e := range t.Events {
		for _, n := range names {
			if e.Event == n && !evTime(e).Before(at) {
				last, at = e.Event, evTime(e)
			}
		}
	}
	return last
}

func (s *obsServer) buildBoard(o *Obs, project string) *Board {
	r, me := s.r, s.r.Machine()
	b := &Board{API: ObsAPI, Now: o.Now, Project: project, Projects: []string{}}
	b.Runner = RunnerInfo{ID: r.RunnerID(), Label: strings.TrimSpace(r.Store.Cfg("obs_label")), Version: buildVersion(), Projects: []string{}, Hub: strings.TrimSpace(r.Store.Cfg("obs_hub_url"))}
	titles := map[string]string{}
	title := func(task string) string {
		if v, ok := titles[task]; ok {
			return v
		}
		v := ""
		if x, err := r.Store.Task(task); err == nil && x != nil {
			v = x.Title
		}
		titles[task] = v
		return v
	}
	runs := map[string]Run{}
	if all, err := r.Store.Runs(); err == nil {
		for _, x := range all {
			runs[x.Task] = x
		}
	}
	limits := StuckLimits(r.Store)
	groups := map[string]map[string]*BoardGroup{}
	add := func(col, cause string, bad bool, c BoardCard) {
		if groups[col] == nil {
			groups[col] = map[string]*BoardGroup{}
		}
		g := groups[col][cause]
		if g == nil {
			g = &BoardGroup{Cause: cause, Bad: bad}
			groups[col][cause] = g
		}
		g.Count++
		g.Cards = append(g.Cards, c)
	}
	pset := map[string]bool{}
	day := windowStart(o.Now, 1)
	refiled := r.refiled()
	for _, t := range o.Tasks {
		if t.Row.Eval {
			continue
		}
		if t.Project != "" {
			pset[t.Project] = true
		}
		if !projectMatches(project, t.Project) {
			continue
		}
		c := BoardCard{Key: t.Key, Task: t.Task, Project: t.Project, PR: httpsURL(t.PR)}
		if t.Origin == me {
			c.Title = title(t.Task)
		}
		if t.Terminal != "" { // finished: only today's
			at := parseTS(t.Terminal)
			if !at.After(day) {
				continue
			}
			c.At = t.Terminal
			switch out := t.Row.Outcome; {
			case out == "merged":
				b.Done.Merged++
				add("done", "Last merged", false, c)
			case strings.HasPrefix(out, "FAIL"):
				c.Note = truncate(strings.SplitN(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(out, "FAIL"), ":")), ":", 2)[0], 60)
				if (t.Origin == me && refiled[t.Task]) || strings.Contains(out, "the owner closed") {
					b.Done.FailedRefiled++
				} else {
					b.Done.Failed++
					add("done", "Failed, not re-filed", true, c)
				}
			default:
				b.Done.Cancelled++
			}
			continue
		}
		f := flowOf(t, o.Now)
		c.Stage, c.AgeSec = f.Stage, f.AgeSec
		run := runs[t.Task]
		lim, has := limits[f.Stage]
		over := has && time.Duration(f.AgeSec)*time.Second > lim
		switch {
		case strings.HasPrefix(t.Row.Outcome, "held") || f.Stage == "held":
			why, pr := heldWhy(t)
			c.Note, c.PR = truncate(why, 200), httpsURL(pr)
			add("held", "", true, c)
		case f.Stage == "merge":
			cause, bad := mergeCause(run.MergeHold)
			add("merge", cause, bad || over, c)
		case f.Stage == "ci":
			if lastEvent(t, "ci.red", "gate.passed") == "ci.red" {
				add("ci", "CI red, repairing", true, c)
			} else {
				add("ci", "", over, c)
			}
		default:
			switch {
			case t.Origin == me && run.IdleSince != "" && run.IdleNudges > 0:
				add("working", "Idle after a nudge", true, c)
			case t.Origin == me && run.IdleSince != "":
				add("working", "Idle", true, c)
			case f.Stage == "gate":
				add("working", "Fixing what the gate blocked", over, c)
			case f.Stage == "judge":
				add("working", "With the judge", over, c)
			default:
				add("working", "", over, c)
			}
		}
	}
	// queued: the todo tasks of the dispatched projects that never ran
	if ready, blocked, err := r.Queue(csv(r.Store.Cfg("projects"))); err == nil {
		readyCause := "Ready, waiting for a free session"
		caps := r.ReadCaps(s.now())
		if why := caps.DispatchBlock(r.Store, ""); why != "" {
			readyCause = "Ready, dispatch held: " + truncate(why, 60)
		}
		for _, t := range ready {
			pset[t.Project] = true
			if projectMatches(project, t.Project) {
				add("queued", readyCause, false, BoardCard{Task: t.ID, Title: t.Title, Project: t.Project})
			}
		}
		for _, x := range blocked {
			pset[x.Task.Project] = true
			if x.Kind == BlockedRun || !projectMatches(project, x.Task.Project) {
				continue
			}
			cause := "Waiting for another task"
			if x.Kind == BlockedLint {
				cause = "Spec does not pass the lint"
			}
			add("queued", cause, x.Kind == BlockedLint, BoardCard{Task: x.Task.ID, Title: x.Task.Title, Project: x.Task.Project, Note: truncate(x.Reason, 80)})
		}
	}
	for _, def := range [][2]string{{"queued", "Queued"}, {"working", "Working"}, {"ci", "In CI"}, {"merge", "To merge"}, {"held", "Held for you"}, {"done", "Done today"}} {
		col := BoardColumn{ID: def[0], Name: def[1], Groups: []BoardGroup{}}
		for _, g := range groups[def[0]] {
			if def[0] == "done" {
				sort.SliceStable(g.Cards, func(i, j int) bool { return g.Cards[i].At > g.Cards[j].At })
			} else {
				sort.SliceStable(g.Cards, func(i, j int) bool { return g.Cards[i].AgeSec > g.Cards[j].AgeSec })
			}
			if len(g.Cards) > boardCards {
				g.Cards = g.Cards[:boardCards]
			}
			col.Count += g.Count
			col.Groups = append(col.Groups, *g)
		}
		// trouble first, then the larger group, then the name: a stable order
		sort.SliceStable(col.Groups, func(i, j int) bool {
			a, c := col.Groups[i], col.Groups[j]
			if a.Bad != c.Bad {
				return a.Bad
			}
			if a.Count != c.Count {
				return a.Count > c.Count
			}
			return a.Cause < c.Cause
		})
		if def[0] == "done" {
			col.Count = b.Done.Merged + b.Done.Failed + b.Done.FailedRefiled + b.Done.Cancelled
		}
		b.Columns = append(b.Columns, col)
	}
	b.Projects = setKeys(pset)
	b.Runner.Projects = b.Projects
	return b
}

// TracePrompt is the text of one prompt the runner sent.
type TracePrompt struct {
	Kind      string `json:"kind"`
	Chars     int    `json:"chars"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// TraceV1 is the answer of /api/obs/v1/task: the trace, the task's title, and each prompt step's text.
type TraceV1 struct {
	API     int                  `json:"api"`
	Key     string               `json:"key"`
	Title   string               `json:"title,omitempty"`
	PR      string               `json:"pr,omitempty"`
	Outcome string               `json:"outcome"`
	Stage   string               `json:"stage,omitempty"`
	Trace                        // task, goal, project, steps, total_usd
	Prompts map[int]*TracePrompt `json:"prompts"` // step index -> its prompt
}

const promptMaxBytes = 64 << 10

// promptText reads a recorded prompt. rel is the path the runner logged, relative to the state dir; only a file
// under <state>/prompts/ is read.
func (r *Runner) promptText(rel string) (string, bool, bool) {
	root := filepath.Join(r.Env.StateDir(), "prompts")
	p := filepath.Clean(filepath.Join(r.Env.StateDir(), rel))
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", false, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false, false
	}
	if len(b) > promptMaxBytes {
		return strings.ToValidUTF8(string(b[:promptMaxBytes]), ""), true, true
	}
	return string(b), false, true
}

func (s *obsServer) traceV1(o *Obs, t *ObsTask) *TraceV1 {
	f := flowOf(t, o.Now)
	out := &TraceV1{API: ObsAPI, Key: t.Key, PR: httpsURL(t.PR), Outcome: t.Row.Outcome, Stage: f.Stage, Trace: buildTrace(t), Prompts: map[int]*TracePrompt{}}
	if t.Origin != s.r.Machine() {
		return out // titles and prompt files are this factory's own
	}
	if x, err := s.r.Store.Task(t.Task); err == nil && x != nil {
		out.Title = x.Title
	}
	for i, st := range out.Steps {
		if st.Kind != "prompt.sent" {
			continue
		}
		parts := strings.Fields(strings.TrimPrefix(st.Summary, "prompt sent:"))
		if len(parts) < 2 {
			continue
		}
		if text, cut, ok := s.r.promptText(parts[len(parts)-1]); ok {
			out.Prompts[i] = &TracePrompt{Kind: parts[0], Chars: len(text), Text: text, Truncated: cut}
		}
	}
	return out
}
