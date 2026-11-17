package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Orgs (scout/orgs.py): the names in ORGS order, the default org and the HQ singleton roles.
const orgDefault = "claude"

var (
	orgNames       = []string{"claude", "cursor"}
	orgRuntimes    = map[string]string{"claude": "claude", "cursor": "cursor"}
	singletonRoles = []string{"boss", "director", "staff-engineer"}
	orgPrefixRE    = regexp.MustCompile(`^(claude|cursor)-`)
)

func inStrings(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func orgKnown(org string) bool { return inStrings(orgNames, org) }

// OrgPrefixOf is orgs.prefix_of: "cursor" for "cursor-coder-t-1", "" for an unprefixed name.
func OrgPrefixOf(name string) string {
	if m := orgPrefixRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// PyIsSpace is str.isspace for one character. Go's unicode.IsSpace misses U+001C-U+001F, which Python counts.
func PyIsSpace(r rune) bool {
	switch r {
	case 0x1c, 0x1d, 0x1e, 0x1f:
		return true
	}
	return unicode.IsSpace(r)
}

// PyStrip is str.strip() with no argument.
func PyStrip(s string) string { return strings.TrimFunc(s, PyIsSpace) }

// Store is the factory DB as the CLI and the tick use it. Clock stamps events and messages (Python now()); Zone is the
// zone of those stamps (nil: the process zone, as astimezone() does; tests pin one).
type Store struct {
	DB    *sql.DB
	Clock func() time.Time // nil is the real clock
	Zone  *time.Location
}

// TSLayout is the layout of every stored timestamp: ISO seconds with the offset.
const TSLayout = "2006-01-02T15:04:05-07:00"

// Now is Python now(): the clock converted to Zone (the process zone when nil), ISO seconds with the offset.
func (r *Store) Now() string {
	t := time.Now()
	if r.Clock != nil {
		t = r.Clock()
	}
	if r.Zone != nil {
		return t.In(r.Zone).Format(TSLayout)
	}
	return t.In(time.Now().Location()).Format(TSLayout) // the process zone
}

// Cfg is cfg(con, key): the config row, else the DEFAULT_CONFIG default. A key with neither is an error (Python
// KeyError).
func (r *Store) Cfg(key string) (string, error) {
	v, ok, err := r.CfgRow(key)
	if err != nil {
		return "", err
	}
	if ok {
		return v, nil
	}
	if d, found := DefaultMap()[key]; found {
		return d, nil
	}
	return "", fmt.Errorf("config key %q has no default: add it to DEFAULT_CONFIG and run go generate ./internal/store", key)
}

// CfgRow is the config row of key and whether it exists; no default is applied.
func (r *Store) CfgRow(key string) (string, bool, error) {
	var v string
	err := r.DB.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v)
	switch {
	case err == nil:
		return v, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	}
	return "", false, fmt.Errorf("read config %s: %w", key, err)
}

// EventAs is event_as: one events row. detail is nil (NULL), a string (as is) or anything PyDumps accepts (json.dumps).
// ts "" means Now().
func (r *Store) EventAs(who, entity, entityID, action string, detail any, ts string) error {
	var d any
	switch x := detail.(type) {
	case nil:
	case string:
		d = x
	default:
		s, err := PyDumps(detail)
		if err != nil {
			return fmt.Errorf("event %s: %w", action, err)
		}
		d = s
	}
	if ts == "" {
		ts = r.Now()
	}
	if _, err := r.DB.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		ts, who, entity, entityID, action, d); err != nil {
		return fmt.Errorf("write %s event: %w", action, err)
	}
	return nil
}

// GoalOrg is goal_org: the org of a goal, "" for no goal, an unknown goal or an unknown org. A DB without the goals.org
// column (pre G-028) also gives "".
func (r *Store) GoalOrg(goal string) (string, error) {
	if goal == "" {
		return "", nil
	}
	var org sql.NullString
	err := r.DB.QueryRow("SELECT org FROM goals WHERE id=?", goal).Scan(&org)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case strings.Contains(err.Error(), "no such column"):
		return "", nil
	default:
		return "", fmt.Errorf("read goal %s org: %w", goal, err)
	}
	if org.Valid && orgKnown(org.String) {
		return org.String, nil
	}
	return "", nil
}

// TaskOrg is task_org.
func (r *Store) TaskOrg(task string) (string, error) {
	if task == "" {
		return "", nil
	}
	var g sql.NullString
	err := r.DB.QueryRow("SELECT goal_id FROM tasks WHERE id=?", task).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read task %s goal: %w", task, err)
	}
	if !g.Valid {
		return "", nil
	}
	return r.GoalOrg(g.String)
}

// AgentOrg is agent_org: of_agent for a registered agent (the org column, else the name prefix, else the spawn_args
// runtime, else the default org), the name prefix for a non-agent, "" for no name.
func (r *Store) AgentOrg(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	var org, sa sql.NullString
	err := r.DB.QueryRow("SELECT org, spawn_args FROM agents WHERE name=?", name).Scan(&org, &sa)
	if errors.Is(err, sql.ErrNoRows) {
		return OrgPrefixOf(name), nil
	}
	if err != nil {
		return "", fmt.Errorf("read agent %s org: %w", name, err)
	}
	if org.Valid && orgKnown(org.String) {
		return org.String, nil
	}
	if p := OrgPrefixOf(name); p != "" {
		return p, nil
	}
	if rt := SpawnRuntime(sa.String); rt != "" {
		for _, o := range orgNames {
			if orgRuntimes[o] == rt {
				return o, nil
			}
		}
	}
	return orgDefault, nil
}

// OrgFor is org_for: explicit, else the goal's, else the task's goal's, else the sender's, else the default org.
func (r *Store) OrgFor(goal, task, sender, org string) (string, error) {
	if orgKnown(org) {
		return org, nil
	}
	if o, err := r.GoalOrg(goal); err != nil || o != "" {
		return o, err
	}
	if o, err := r.TaskOrg(task); err != nil || o != "" {
		return o, err
	}
	if o, err := r.AgentOrg(sender); err != nil || o != "" {
		return o, err
	}
	return orgDefault, nil
}

// Oname is oname: <org>-<base>; the default org's prefix is behind config org_prefix_default; a prefixed base is kept.
func (r *Store) Oname(org, base string) (string, error) {
	if !orgKnown(org) {
		org = orgDefault
	}
	if OrgPrefixOf(base) != "" {
		return base, nil
	}
	if org == orgDefault {
		v, err := r.Cfg("org_prefix_default")
		if err != nil {
			return "", err
		}
		if PyStrip(v) != "1" {
			return base, nil
		}
	}
	return org + "-" + base, nil
}

// AgentLive reports whether an agent of that name is live, starting or queued.
func (r *Store) AgentLive(name string) (bool, error) {
	var one int
	err := r.DB.QueryRow("SELECT 1 FROM agents WHERE name=? AND status IN ('live','starting','queued')", name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read agent %s: %w", name, err)
	}
	return true, nil
}

// ResolveRecipient is resolve_recipient: an agent name, a role, "lead:<team>" or "owner". A bare HQ role resolves
// inside org to <org>-<role>. The errors carry Python's die() texts.
func (r *Store) ResolveRecipient(to, org string) (string, error) {
	if !orgKnown(org) {
		org = orgDefault
	}
	if to == "owner" {
		to = "boss" // the boss is the owner's interface; it relays
	}
	if inStrings(singletonRoles, to) {
		cand, err := r.Oname(org, to)
		if err != nil {
			return "", err
		}
		if ok, err := r.AgentLive(cand); err != nil || ok {
			return cand, err
		}
		names, err := r.liveNamesOfRole(to)
		if err != nil {
			return "", err
		}
		for _, n := range names {
			o, err := r.AgentOrg(n)
			if err != nil {
				return "", err
			}
			if o == org {
				return n, nil
			}
		}
		pfx, err := r.Cfg("org_prefix_default")
		if err != nil {
			return "", err
		}
		if org == orgDefault && PyStrip(pfx) == "1" && OrgPrefixOf(to) == "" {
			if ok, err := r.AgentLive(to); err != nil {
				return "", err
			} else if ok {
				o, err := r.AgentOrg(to)
				if err != nil {
					return "", err
				}
				if o == org {
					return to, nil
				}
			}
		}
		return cand, nil // stored for the org's (future) HQ agent; `factory up` starts it
	}
	if strings.HasPrefix(to, "lead:") {
		var lead sql.NullString
		err := r.DB.QueryRow("SELECT lead_agent FROM teams WHERE id=?", to[5:]).Scan(&lead)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("read team %s: %w", to[5:], err)
		}
		if err != nil || lead.String == "" {
			return "", fmt.Errorf("team %s has no lead", to[5:])
		}
		return lead.String, nil
	}
	var one int
	err := r.DB.QueryRow("SELECT 1 FROM agents WHERE name=?", to).Scan(&one)
	if err == nil {
		return to, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read agent %s: %w", to, err)
	}
	var name string
	err = r.DB.QueryRow("SELECT name FROM agents WHERE role=? AND status IN ('live','starting','queued') ORDER BY started DESC LIMIT 1", to).Scan(&name)
	if err == nil {
		return name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read agents of role %s: %w", to, err)
	}
	return "", fmt.Errorf("unknown recipient %s", to)
}

func (r *Store) liveNamesOfRole(role string) ([]string, error) {
	rows, err := r.DB.Query("SELECT name FROM agents WHERE role=? AND status IN ('live','starting','queued') ORDER BY started DESC", role)
	if err != nil {
		return nil, fmt.Errorf("read agents of role %s: %w", role, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("read agents of role %s: %w", role, err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read agents of role %s: %w", role, err)
	}
	return out, nil
}

// Msg is one message for Send. Goal, Task and Body are optional (nil stores NULL, as Python's None).
type Msg struct {
	To, Subject string
	Body        *string
	Kind        string // "" means "msg"
	Goal, Task  *string
	Sender      string // required: the caller names the actor (Python's sender or actor())
	Org         string
}

// Send is send(): resolve the recipient inside the message's org, INSERT the row. Returns the message id and the
// resolved recipient.
func (r *Store) Send(m Msg) (int64, string, error) {
	sender, kind := m.Sender, m.Kind
	if sender == "" {
		return 0, "", fmt.Errorf("send: Msg.Sender is required (pass the actor, \"owner\" for the tick)")
	}
	if kind == "" {
		kind = "msg"
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	val := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	org, err := r.OrgFor(deref(m.Goal), deref(m.Task), sender, m.Org)
	if err != nil {
		return 0, "", err
	}
	rcpt, err := r.ResolveRecipient(m.To, org)
	if err != nil {
		return 0, "", err
	}
	res, err := r.DB.Exec("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
		sender, rcpt, kind, m.Subject, val(m.Body), val(m.Goal), val(m.Task), r.Now())
	if err != nil {
		return 0, "", fmt.Errorf("send %q to %s: %w", m.Subject, rcpt, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("send %q to %s: %w", m.Subject, rcpt, err)
	}
	return id, rcpt, nil
}

// SpawnRuntime is the "runtime" string of an agents.spawn_args document, "" when absent, not a string or not JSON.
func SpawnRuntime(raw string) string {
	m, ok := SpawnArgsMap(raw)
	if !ok {
		return ""
	}
	s, _ := m["runtime"].(string)
	return s
}
