package tokens

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cursor plan limits recorded by the tick from in-pane usage-limit errors (port of web/cursor_limits.py).

// ConfigKey is the config row that holds the per-model limit state (JSON, shared with Python).
const ConfigKey = "cursor_model_limits"

// PlanTier is PLAN_TIER.
const PlanTier = "Pro"

// cursorOrgModels is the "cursor" entry of scout/orgs.py ORGS[...]["models"] values; TestCursorModelSlugs compares it
// with the Python source.
var cursorOrgModels = []string{"grok-4.7-high", "composer-2.5", "claude-sonnet-5-5"}

var (
	limitRE = regexp.MustCompile(`(?i)usage limit for\s+(.+?)(?:\.|\n|\z)`)
	resetRE = regexp.MustCompile(`(?i)(?:resets?(?:\s+on)?\s+)(?:(\d{4}-\d{2}-\d{2})|` +
		`((?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|` +
		`Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)\s+\d{1,2},?\s+\d{4}))`)
	switchRE = regexp.MustCompile(`(?i)otherModelsLimitSwitchTarget["'\s:=]+([^\s"'\n,]+)`)
	isoDay   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// CursorModelSlugs is cursor_model_slugs: per-model slugs of the Cursor org (not the Claude evaluator model), sorted.
func CursorModelSlugs() []string {
	set := map[string]bool{}
	for _, m := range cursorOrgModels {
		if !strings.HasPrefix(m, "claude-") {
			set[m] = true
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// LimitError is what parse_limit_error returns; Reset and SwitchTarget are "" for Python None.
type LimitError struct{ Model, Reset, SwitchTarget string }

// ParseLimitError is parse_limit_error: ok=false when the pane text shows no usage-limit error.
func ParseLimitError(text string) (LimitError, bool) {
	if text == "" || !strings.Contains(strings.ToLower(text), "usage limit") {
		return LimitError{}, false
	}
	m := limitRE.FindStringSubmatch(text)
	if m == nil {
		return LimitError{}, false
	}
	out := LimitError{Model: strings.Trim(strings.Trim(strings.TrimSpace(m[1]), `"`), `'`)}
	if rm := resetRE.FindStringSubmatch(text); rm != nil {
		if rm[1] != "" {
			out.Reset = rm[1]
		} else {
			raw := strings.Join(strings.Fields(strings.ReplaceAll(rm[2], ",", "")), " ")
			if t, err := time.Parse("January 2 2006", raw); err == nil {
				out.Reset = t.Format("2006-01-02")
			} else if t, err := time.Parse("Jan 2 2006", raw); err == nil {
				out.Reset = t.Format("2006-01-02")
			} else {
				out.Reset = strings.TrimSpace(rm[2])
			}
		}
	}
	if sw := switchRE.FindStringSubmatch(text); sw != nil {
		out.SwitchTarget = strings.Trim(strings.Trim(strings.TrimSpace(sw[1]), `"`), `'`)
	}
	return out, true
}

// LimitState is the config value: model slug -> entry (status, reset, switch_target, agent, ...). Entries are kept as
// generic maps so a field this port does not know survives a Go write that Python reads, and the reverse.
type LimitState map[string]map[string]any

// LoadState is load_state: {} when the row is missing, not JSON, or not an object.
func LoadState(db *sql.DB) (LimitState, error) {
	var v string
	err := db.QueryRow("SELECT value FROM config WHERE key=?", ConfigKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return LimitState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tokens: read config %s: %w", ConfigKey, err)
	}
	return parseState(v), nil
}

func parseState(v string) LimitState {
	dec := json.NewDecoder(strings.NewReader(v))
	dec.UseNumber()
	var raw map[string]any
	if dec.Decode(&raw) != nil || raw == nil {
		return LimitState{}
	}
	out := LimitState{}
	for k, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out[k] = m
		} else {
			out[k] = nil // Python keeps the odd value; callers only .get() on dict entries
		}
	}
	return out
}

// SaveState is save_state: json.dumps(state, sort_keys=True) as Python writes it.
func SaveState(db *sql.DB, state LimitState) error {
	m := map[string]any{}
	for k, e := range state {
		if e == nil {
			m[k] = nil
		} else {
			m[k] = e
		}
	}
	_, err := db.Exec("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		ConfigKey, pyDumps(m, true))
	if err != nil {
		return fmt.Errorf("tokens: write config %s: %w", ConfigKey, err)
	}
	return nil
}

// ModelRow is one per-model row of Display.
type ModelRow struct {
	Model        string  `json:"model"`
	Status       string  `json:"status"`
	Reset        *string `json:"reset"`
	ResetDisplay *string `json:"reset_display"`
	SwitchTarget *string `json:"switch_target"`
}

func entStr(ent map[string]any, k string) string {
	s, _ := ent[k].(string)
	return s
}

// ModelRows is _model_rows.
func ModelRows(state LimitState) []ModelRow {
	rows := []ModelRow{}
	for _, slug := range CursorModelSlugs() {
		ent := state[slug]
		if ent != nil && entStr(ent, "status") == "limited" {
			reset := entStr(ent, "reset")
			disp := FmtReset(reset)
			r := ModelRow{Model: slug, Status: "limited", Reset: &reset, ResetDisplay: &disp}
			if s, ok := ent["switch_target"].(string); ok {
				r.SwitchTarget = &s
			}
			rows = append(rows, r)
		} else {
			rows = append(rows, ModelRow{Model: slug, Status: "ok"})
		}
	}
	return rows
}

// FmtReset is _fmt_reset: YYYY-MM-DD as MM-DD, anything else unchanged.
func FmtReset(reset string) string {
	if reset == "" {
		return ""
	}
	if isoDay.MatchString(reset) {
		if t, err := time.Parse("2006-01-02", reset); err == nil {
			return t.Format("01-02")
		}
	}
	return reset
}

// DisplayData is what display() returns.
type DisplayData struct {
	Tier   string     `json:"tier"`
	Models []ModelRow `json:"models"`
	Hours  float64    `json:"hours"`
}

// Display is display(): tier, per-model limit state and today's agent-hours. Python derives the hours from
// web.queries/web.views.tokens.cursor_activity, which are not ported yet, so the caller passes them in.
func Display(db *sql.DB, hours float64) (*DisplayData, error) {
	st, err := LoadState(db)
	if err != nil {
		return nil, err
	}
	return &DisplayData{Tier: PlanTier, Models: ModelRows(st), Hours: hours}, nil
}

// PlanLine is plan_line: "Pro · ok" or "Pro · limited until <date>" when any model is limited.
func PlanLine(d *DisplayData) string {
	for _, m := range d.Models {
		if m.Status == "limited" {
			until := ""
			if m.ResetDisplay != nil {
				until = *m.ResetDisplay
			}
			if until == "" && m.Reset != nil {
				until = *m.Reset
			}
			if until == "" {
				until = "unknown"
			}
			return d.Tier + " · limited until " + until
		}
	}
	return d.Tier + " · ok"
}

// HeaderPill is header_pill: today's agent-hours and plan/limit; hours print like Python's {:g}.
func HeaderPill(d *DisplayData) string {
	return "Cursor usage · " + strconv.FormatFloat(d.Hours, 'g', 6, 64) + " h · " + PlanLine(d)
}

// LiveAgent is a live agents row as tick_record reads it.
type LiveAgent struct{ Name, Model, GoalID, TaskID, Org, SpawnArgs string }

// TickDeps are the process-boundary functions tick_record is given.
type TickDeps struct {
	RuntimeOf func(LiveAgent) string   // agent_runtime_of
	ReadPane  func(name string) string // herdr agent read ... text (text, else raw)
	OrgOf     func(name string) string // agent_org_fn
	Send      func(to, subject, body, kind, goalID, taskID, sender, org string) error
	Event     func(entity, id, action, detail string) error
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TickRecord is tick_record: scan live Cursor panes, update the config row, alert the director on a new limit.
func TickRecord(db *sql.DB, d TickDeps) error {
	rows, err := db.Query("SELECT name, model, goal_id, task_id, org, spawn_args FROM agents WHERE status='live'")
	if err != nil {
		return fmt.Errorf("tokens: read live agents: %w", err)
	}
	var live []LiveAgent
	for rows.Next() {
		var n, m, g, t, o, sa sql.NullString
		if err := rows.Scan(&n, &m, &g, &t, &o, &sa); err != nil {
			rows.Close()
			return fmt.Errorf("tokens: scan live agents: %w", err)
		}
		live = append(live, LiveAgent{n.String, m.String, g.String, t.String, o.String, sa.String})
	}
	rows.Close()
	slugs := CursorModelSlugs()
	isSlug := func(s string) bool {
		for _, x := range slugs {
			if x == s {
				return true
			}
		}
		return false
	}
	limited := map[string]map[string]any{}
	for _, r := range live {
		if d.RuntimeOf(r) != "cursor" {
			continue
		}
		parsed, ok := ParseLimitError(d.ReadPane(r.Name))
		if !ok {
			continue
		}
		slug := r.Model
		if slug == "" {
			slug = parsed.Model
		}
		if !isSlug(slug) {
			slug = parsed.Model
		}
		limited[slug] = map[string]any{"status": "limited", "reset": nullable(parsed.Reset),
			"switch_target": nullable(parsed.SwitchTarget), "agent": r.Name}
		key := "cursor-limit:" + slug
		sig := pyDumps(map[string]any{"reset": nullable(parsed.Reset), "switch": nullable(parsed.SwitchTarget)}, true)
		var old sql.NullString
		err := db.QueryRow("SELECT ts FROM throttle WHERE key=?", key).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tokens: read throttle: %w", err)
		}
		if old.String == sig && err == nil {
			continue
		}
		if _, err := db.Exec("INSERT INTO throttle(key,ts) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts", key, sig); err != nil {
			return fmt.Errorf("tokens: write throttle: %w", err)
		}
		org := d.OrgOf(r.Name)
		reset := parsed.Reset
		if reset == "" {
			reset = "unknown date"
		}
		model := parsed.Model
		if model == "" {
			model = slug
		}
		body := fmt.Sprintf("%s hit the Cursor usage limit for %s; limited until %s", r.Name, model, reset)
		if parsed.SwitchTarget != "" {
			body += "; switch target " + parsed.SwitchTarget
		}
		body += "."
		if err := d.Send("director", "Cursor limit: "+slug, body, "alert", r.GoalID, r.TaskID, "tick", org); err != nil {
			return err
		}
		detail := pyDumps(pyObj{{"model", slug}, {"reset", nullable(parsed.Reset)}, {"switch_target", nullable(parsed.SwitchTarget)}}, false)
		if err := d.Event("agent", r.Name, "cursor-limit", detail); err != nil {
			return err
		}
	}
	prior, err := LoadState(db)
	if err != nil {
		return err
	}
	if len(limited) == 0 && len(prior) == 0 {
		return nil
	}
	state := LimitState{}
	for _, slug := range slugs {
		if e, ok := limited[slug]; ok {
			state[slug] = e
		} else {
			state[slug] = map[string]any{"status": "ok"}
		}
	}
	return SaveState(db, state)
}
