package tick

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Shared support for the reap units (20-26): the DB helpers every one of them calls in Python (cfg, event_as, send with
// resolve_recipient/org_for) and the adapter that lets the herdr client read the same DB. No unit is registered here.

// reapActor is the actor of every row the reap units write: Python passes sender="tick" and event_as(con, "tick", ...).
const reapActor = "tick"

// reapDefaults are the DEFAULT_CONFIG values of the keys the reap units read. TestReapDefaultsMatchPython pins every
// entry to the literal in bin/factory_cli.py; a key that is not listed here is an error, never a silent "".
var reapDefaults = map[string]string{
	"exited_pane_reap_mode": "dry-run",
	"idle_coder_reap":       "off",
	"idle_coder_reap_s":     "900",
	"blocked_alert_s":       "600",
	"hq_unblock_s":          "900",
	"director_cycle_s":      "900",
	"orgs_enabled":          "claude",
	"org_prefix_default":    "0",
	"terminal_backend":      "auto",
	"herdr_socket":          "~/.config/herdr/herdr.sock",
}

// Orgs (scout/orgs.py): the names in ORGS order, the default org and the HQ singleton roles.
const reapOrgDefault = "claude"

var (
	reapOrgNames       = []string{"claude", "cursor"}
	reapOrgRuntimes    = map[string]string{"claude": "claude", "cursor": "cursor"}
	reapSingletonRoles = []string{"boss", "director", "staff-engineer"}
	reapOrgPrefixRE    = regexp.MustCompile(`^(claude|cursor)-`)
)

func inStrings(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func reapOrgKnown(org string) bool { return inStrings(reapOrgNames, org) }

// reapOrgPrefixOf is orgs.prefix_of: "cursor" for "cursor-coder-t-1", "" for an unprefixed name.
func reapOrgPrefixOf(name string) string {
	if m := reapOrgPrefixRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// pyIsSpace is str.isspace for one character: bidi type WS, B or S, or category Zs. Go's unicode.IsSpace misses U+001C-
// U+001F, which Python counts.
func pyIsSpace(r rune) bool {
	switch r {
	case 0x1c, 0x1d, 0x1e, 0x1f:
		return true
	}
	return unicode.IsSpace(r)
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// reapDB is the factory DB as the reap units use it. clock stamps events (Python now()).
type reapDB struct {
	db    *sql.DB
	clock func() time.Time
}

// newReapDB returns the helper over db. The event clock is the S2 clock the prune unit uses: FACTORY_TEST_NOW on a
// non-live home, else the real clock; an invalid FACTORY_TEST_NOW is an error, as before any Python write.
func newReapDB(db *sql.DB) (*reapDB, error) {
	now, err := pruneEventClock(pruneIdentity())
	if err != nil {
		return nil, fmt.Errorf("event clock: %w", err)
	}
	return &reapDB{db: db, clock: now}, nil
}

const reapTSLayout = "2006-01-02T15:04:05-07:00"

// now is Python now(): ISO seconds with the offset.
func (r *reapDB) now() string { return r.clock().Format(reapTSLayout) }

// cfg is cfg(con, key): the config row, else the default. A key with neither is an error (Python KeyError).
func (r *reapDB) cfg(key string) (string, error) {
	var v string
	err := r.db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v)
	switch {
	case err == nil:
		return v, nil
	case errors.Is(err, sql.ErrNoRows):
		if d, ok := reapDefaults[key]; ok {
			return d, nil
		}
		return "", fmt.Errorf("config key %q has no default in the reap units: add it to reapDefaults", key)
	}
	return "", fmt.Errorf("read config %s: %w", key, err)
}

// cfgRow is the config row of key and whether it exists (herdr.Store.Config: no default applied).
func (r *reapDB) cfgRow(key string) (string, bool, error) {
	var v string
	err := r.db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v)
	switch {
	case err == nil:
		return v, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	}
	return "", false, fmt.Errorf("read config %s: %w", key, err)
}

// eventAs is event_as: one events row. detail is nil (NULL), a string (as is) or anything pyDumps accepts (json.dumps).
// ts "" means now().
func (r *reapDB) eventAs(who, entity, entityID, action string, detail any, ts string) error {
	d, err := pyDetail(detail)
	if err != nil {
		return fmt.Errorf("event %s: %w", action, err)
	}
	if ts == "" {
		ts = r.now()
	}
	if _, err := r.db.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		ts, who, entity, entityID, action, d); err != nil {
		return fmt.Errorf("write %s event: %w", action, err)
	}
	return nil
}

// event is event_as with the tick as actor.
func (r *reapDB) event(entity, entityID, action string, detail any) error {
	return r.eventAs(reapActor, entity, entityID, action, detail, "")
}

func pyDetail(detail any) (any, error) {
	switch d := detail.(type) {
	case nil:
		return nil, nil
	case string:
		return d, nil
	}
	s, err := pyDumps(detail)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// goalOrg is goal_org: the org of a goal, "" for no goal, an unknown goal or an unknown org. A DB without the goals.org
// column (pre G-028) also gives "".
func (r *reapDB) goalOrg(goal string) (string, error) {
	if goal == "" {
		return "", nil
	}
	var org sql.NullString
	err := r.db.QueryRow("SELECT org FROM goals WHERE id=?", goal).Scan(&org)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case strings.Contains(err.Error(), "no such column"):
		return "", nil
	default:
		return "", fmt.Errorf("read goal %s org: %w", goal, err)
	}
	if org.Valid && reapOrgKnown(org.String) {
		return org.String, nil
	}
	return "", nil
}

// taskOrg is task_org.
func (r *reapDB) taskOrg(task string) (string, error) {
	if task == "" {
		return "", nil
	}
	var g sql.NullString
	err := r.db.QueryRow("SELECT goal_id FROM tasks WHERE id=?", task).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read task %s goal: %w", task, err)
	}
	if !g.Valid {
		return "", nil
	}
	return r.goalOrg(g.String)
}

// agentOrg is agent_org: of_agent for a registered agent (the org column, else the name prefix, else spawn_args runtime,
// else the default org), the name prefix for a non-agent, "" for no name.
func (r *reapDB) agentOrg(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	var org, sa sql.NullString
	err := r.db.QueryRow("SELECT org, spawn_args FROM agents WHERE name=?", name).Scan(&org, &sa)
	if errors.Is(err, sql.ErrNoRows) {
		return reapOrgPrefixOf(name), nil
	}
	if err != nil {
		return "", fmt.Errorf("read agent %s org: %w", name, err)
	}
	if org.Valid && reapOrgKnown(org.String) {
		return org.String, nil
	}
	if p := reapOrgPrefixOf(name); p != "" {
		return p, nil
	}
	rt := spawnRuntime(sa.String)
	if rt != "" {
		for _, o := range reapOrgNames {
			if reapOrgRuntimes[o] == rt {
				return o, nil
			}
		}
	}
	return reapOrgDefault, nil
}

// orgFor is org_for: explicit, else the goal's, else the task's goal's, else the sender's, else the default org.
func (r *reapDB) orgFor(goal, task, sender, org string) (string, error) {
	if reapOrgKnown(org) {
		return org, nil
	}
	if o, err := r.goalOrg(goal); err != nil || o != "" {
		return o, err
	}
	if o, err := r.taskOrg(task); err != nil || o != "" {
		return o, err
	}
	if o, err := r.agentOrg(sender); err != nil || o != "" {
		return o, err
	}
	return reapOrgDefault, nil
}

// oname is oname: <org>-<base>; the default org's prefix is behind config org_prefix_default; a prefixed base is kept.
func (r *reapDB) oname(org, base string) (string, error) {
	if !reapOrgKnown(org) {
		org = reapOrgDefault
	}
	if reapOrgPrefixOf(base) != "" {
		return base, nil
	}
	if org == reapOrgDefault {
		v, err := r.cfg("org_prefix_default")
		if err != nil {
			return "", err
		}
		if pyStrip(v) != "1" {
			return base, nil
		}
	}
	return org + "-" + base, nil
}

func (r *reapDB) agentLive(name string) (bool, error) {
	var one int
	err := r.db.QueryRow("SELECT 1 FROM agents WHERE name=? AND status IN ('live','starting','queued')", name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read agent %s: %w", name, err)
	}
	return true, nil
}

// resolveRecipient is resolve_recipient: an agent name, a role, "lead:<team>" or "owner". A bare HQ role resolves inside
// org to <org>-<role>. The errors are Python's die() texts (the tick turns them into a failed unit).
func (r *reapDB) resolveRecipient(to, org string) (string, error) {
	if !reapOrgKnown(org) {
		org = reapOrgDefault
	}
	if to == "owner" {
		to = "boss" // the boss is the owner's interface; it relays
	}
	if inStrings(reapSingletonRoles, to) {
		cand, err := r.oname(org, to)
		if err != nil {
			return "", err
		}
		if ok, err := r.agentLive(cand); err != nil || ok {
			return cand, err
		}
		names, err := r.liveNamesOfRole(to)
		if err != nil {
			return "", err
		}
		for _, n := range names {
			o, err := r.agentOrg(n)
			if err != nil {
				return "", err
			}
			if o == org {
				return n, nil
			}
		}
		pfx, err := r.cfg("org_prefix_default")
		if err != nil {
			return "", err
		}
		if org == reapOrgDefault && pyStrip(pfx) == "1" && reapOrgPrefixOf(to) == "" {
			if ok, err := r.agentLive(to); err != nil {
				return "", err
			} else if ok {
				o, err := r.agentOrg(to)
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
		err := r.db.QueryRow("SELECT lead_agent FROM teams WHERE id=?", to[5:]).Scan(&lead)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("read team %s: %w", to[5:], err)
		}
		if err != nil || lead.String == "" {
			return "", fmt.Errorf("team %s has no lead", to[5:])
		}
		return lead.String, nil
	}
	var one int
	err := r.db.QueryRow("SELECT 1 FROM agents WHERE name=?", to).Scan(&one)
	if err == nil {
		return to, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read agent %s: %w", to, err)
	}
	var name string
	err = r.db.QueryRow("SELECT name FROM agents WHERE role=? AND status IN ('live','starting','queued') ORDER BY started DESC LIMIT 1", to).Scan(&name)
	if err == nil {
		return name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read agents of role %s: %w", to, err)
	}
	return "", fmt.Errorf("unknown recipient %s", to)
}

func (r *reapDB) liveNamesOfRole(role string) ([]string, error) {
	rows, err := r.db.Query("SELECT name FROM agents WHERE role=? AND status IN ('live','starting','queued') ORDER BY started DESC", role)
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

// Msg is one message for send. Goal, Task and Body are optional (nil stores NULL, as Python's None).
type Msg struct {
	To, Subject string
	Body        *string
	Kind        string // "" means "msg"
	Goal, Task  *string
	Sender      string // "" means the owner (the Go tick runs as the owner; every reap call passes "tick")
	Org         string
}

// send is send(): resolve the recipient inside the message's org, INSERT the row. Returns the message id and the resolved
// recipient.
func (r *reapDB) send(m Msg) (int64, string, error) {
	sender, kind := m.Sender, m.Kind
	if sender == "" {
		sender = "owner"
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
	org, err := r.orgFor(deref(m.Goal), deref(m.Task), sender, m.Org)
	if err != nil {
		return 0, "", err
	}
	rcpt, err := r.resolveRecipient(m.To, org)
	if err != nil {
		return 0, "", err
	}
	res, err := r.db.Exec("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
		sender, rcpt, kind, m.Subject, val(m.Body), val(m.Goal), val(m.Task), r.now())
	if err != nil {
		return 0, "", fmt.Errorf("send %q to %s: %w", m.Subject, rcpt, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("send %q to %s: %w", m.Subject, rcpt, err)
	}
	return id, rcpt, nil
}

// strp is a *string for Msg fields.
func strp(s string) *string { return &s }

// spawnRuntime is the "runtime" string of an agents.spawn_args document, "" when absent, not a string or not JSON.
func spawnRuntime(raw string) string {
	m, ok := spawnArgsMap(raw)
	if !ok {
		return ""
	}
	s, _ := m["runtime"].(string)
	return s
}
