package hm

import (
	"fmt"
	"sort"
	"strings"
)

// The Herdr app shows an hc terminal only when it carries the labels herdr.space and herdr.tab
// (docs/hivemill-planner.md#herdr-app-spaces). The planner has its own space; task and judge sessions share one.
const (
	herdrPlannerSpace = "hivemill"
	herdrWorkerSpace  = "workers"
	machineLabelKey   = "machine_label." // + hc machine: the name the app shows for it
)

func init() { Defaults["herdr_spaces"] = "on" } // off: sessions start without the Herdr app labels

func checkHerdrCfg(key, value string) error {
	if key == "herdr_spaces" && value != "on" && value != "off" {
		return fmt.Errorf("herdr_spaces %q refused: use on or off", value)
	}
	return nil
}

// machineLabelCfg reports whether key is machine_label.<hc machine>.
func machineLabelCfg(key string) bool {
	name, ok := strings.CutPrefix(key, machineLabelKey)
	return ok && validMachine(name)
}

// herdrMachine is the hc machine a session is on and the name shown for it: machine_label.<machine>, else the hc name,
// else (hc cannot name its core) this machine's own name.
func (r *Runner) herdrMachine(m, core string) (string, string) {
	m = or(m, or(core, r.Machine()))
	return m, or(strings.TrimSpace(r.Store.Cfg(machineLabelKey+m)), m)
}

// herdrTab is the space of a session and its tab title.
func (r *Runner) herdrTab(role, task string) (space, title string) {
	if role == "planner" {
		return herdrPlannerSpace, PlannerName
	}
	title = task
	if t, err := r.Store.Task(task); err == nil && t != nil {
		if name := strings.Join(strings.Fields(t.Title), " "); name != "" {
			title += " · " + name
		}
	}
	if role == "task-judge" {
		title += " (judge)" // the task session's tab has the same task
	}
	return herdrWorkerSpace, title
}

// herdrLabels are the `hc run` flags of a session's terminal (nil while herdr_spaces is off).
func (r *Runner) herdrLabels(l Launch, task, core string) []string {
	if r.Store.Cfg("herdr_spaces") == "off" {
		return nil
	}
	space, title := r.herdrTab(l.Role, task)
	_, machine := r.herdrMachine(l.Machine, core)
	return []string{"--label", "herdr.space=" + space, "--label", "herdr.tab=tab-" + l.Name,
		"--label", "where=" + machine + " · " + space + " / " + title}
}

// HerdrSpace is one space the app should show: the space label of one machine and its tabs.
type HerdrSpace struct {
	ID      string     `json:"id"` // the herdr.space label
	Name    string     `json:"name"`
	Machine string     `json:"machine"` // the hc machine
	Tabs    []HerdrTab `json:"tabs"`
}

// HerdrTab is one terminal of a space.
type HerdrTab struct {
	ID       string `json:"id"` // the herdr.tab label
	Terminal string `json:"terminal"`
	Name     string `json:"name"`
}

// HerdrNames lists the spaces and tabs of the live runner sessions, and the planner while it is on.
func (r *Runner) HerdrNames() ([]HerdrSpace, error) {
	s, err := r.sq()
	if err != nil {
		return nil, err
	}
	sessions, err := s.liveSessions()
	if err != nil {
		return nil, err
	}
	hasPlanner := false
	for _, x := range sessions {
		hasPlanner = hasPlanner || x.name == PlannerName
	}
	if !hasPlanner && r.Store.Meta("planner") == "on" {
		sessions = append(sessions, liveSession{name: PlannerName, role: "planner"})
	}
	core := r.coreName()
	spaces := map[[2]string]*HerdrSpace{}
	for _, x := range sessions {
		placed := ""
		if x.role == "task" { // the planner and the judges stay on the local core
			run, err := r.Store.Run(x.task)
			if err != nil {
				return nil, err
			}
			if run != nil {
				placed = run.Machine
			}
		}
		id, title := r.herdrTab(x.role, x.task)
		m, label := r.herdrMachine(placed, core)
		k := [2]string{id, m}
		if spaces[k] == nil {
			spaces[k] = &HerdrSpace{ID: id, Name: label + " · " + id, Machine: m}
		}
		spaces[k].Tabs = append(spaces[k].Tabs, HerdrTab{ID: "tab-" + x.name, Terminal: x.name, Name: title})
	}
	out := []HerdrSpace{}
	for _, sp := range spaces {
		sort.Slice(sp.Tabs, func(i, j int) bool { return sp.Tabs[i].Terminal < sp.Tabs[j].Terminal })
		out = append(out, *sp)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID || (out[i].ID == out[j].ID && out[i].Machine < out[j].Machine)
	})
	return out, nil
}

type liveSession struct{ name, role, task string }

// liveSessions are the agents rows of the runner's own live sessions.
func (s *SQLite) liveSessions() ([]liveSession, error) {
	rows, err := s.db.Query(`SELECT name, role, COALESCE(task_id,'') FROM agents WHERE status='live'
		AND role IN ('planner','task','task-judge')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []liveSession
	for rows.Next() {
		var x liveSession
		if err := rows.Scan(&x.name, &x.role, &x.task); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
