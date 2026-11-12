package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Message helpers shared by msg and report (S2m): the Go ports of resolve_recipient, send, sender_org_for,
// refuse_cross_org, warn_spec_by_msg and the constants around them in bin/factory_cli.py. Frozen after merge.

const msgOnDemand = "staff-engineer" // ON_DEMAND

var msgCrossOrgRoles = []string{"owner", "boss", "director"} // CROSS_ORG_ROLES

// msgSpecWords is SPEC_WORDS. RE2's \b and (?i) are ASCII-only, Python's are Unicode, so callers delegate any text
// that holds a non-ASCII byte before they write.
var msgSpecWords = regexp.MustCompile(`(?i)\b(spec|acceptance|criteri\w*|requirement\w*|scope)\b`)

func msgIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func msgLive(c *ctx, name string) (bool, error) {
	var one int
	err := c.db.QueryRow("SELECT 1 FROM agents WHERE name=? AND status IN (?,?,?)", name, "live", "starting", "queued").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// msgResolveRecipient is resolve_recipient: an agent name, a role, "lead:<team>" or "owner"; a bare HQ role resolves
// inside org.
func msgResolveRecipient(c *ctx, to, org string) (string, error) {
	if !orgKnown(org) {
		org = orgDefault
	}
	if to == "owner" {
		to = "boss"
	}
	if inList(orgSingletonRoles, to) {
		cand, err := orgAgentName(c, org, to)
		if err != nil {
			return "", err
		}
		if ok, err := msgLive(c, cand); err != nil || ok {
			return cand, err
		}
		rows, err := c.query("SELECT name FROM agents WHERE role=? AND status IN (?,?,?) ORDER BY started DESC",
			to, "live", "starting", "queued")
		if err != nil {
			return "", err
		}
		var names []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return "", err
			}
			names = append(names, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		for _, n := range names {
			if o, err := agentOrg(c, n); err != nil {
				return "", err
			} else if o == org {
				return n, nil
			}
		}
		pfx, err := cfgText(c, "org_prefix_default")
		if err != nil {
			return "", err
		}
		if org == orgDefault && pyStrip(pfx) == "1" && orgPrefixOf(to) == "" {
			if ok, err := msgLive(c, to); err != nil {
				return "", err
			} else if ok {
				if o, err := agentOrg(c, to); err != nil || o == org {
					return to, err
				}
			}
		}
		return cand, nil // stored for the org's (future) HQ agent
	}
	if strings.HasPrefix(to, "lead:") {
		var lead sql.NullString
		err := c.db.QueryRow("SELECT lead_agent FROM teams WHERE id=?", to[5:]).Scan(&lead)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if err != nil || lead.String == "" {
			return "", dieErr(1, fmt.Sprintf("team %s has no lead", to[5:]))
		}
		return lead.String, nil
	}
	var one int
	err := c.db.QueryRow("SELECT 1 FROM agents WHERE name=?", to).Scan(&one)
	if err == nil {
		return to, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var name string
	err = c.db.QueryRow("SELECT name FROM agents WHERE role=? AND status IN ('live','starting','queued') ORDER BY started DESC LIMIT 1", to).Scan(&name)
	if err == nil {
		return name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return "", dieErr(1, "unknown recipient "+to)
}

// msgSenderOrg is sender_org_for: a lead or worker addressing a bare singleton reaches its own org's HQ; everyone
// else resolves through goal, task and sender.
func msgSenderOrg(c *ctx, role, to, goal, task string) (string, error) {
	if !inList(msgCrossOrgRoles, role) && (to == "owner" || inList(orgSingletonRoles, to)) {
		o, err := agentOrg(c, c.who)
		if err != nil || o != "" {
			return o, err
		}
		return orgDefault, nil
	}
	return orgFor(c, goal, task, c.who, "")
}

// msgRefuseCrossOrg is refuse_cross_org.
func msgRefuseCrossOrg(c *ctx, role, to, goal, task string) error {
	if inList(msgCrossOrgRoles, role) {
		return nil
	}
	mine, err := agentOrg(c, c.who)
	if err != nil {
		return err
	}
	if mine == "" {
		mine = orgDefault
	}
	org, err := msgSenderOrg(c, role, to, goal, task)
	if err != nil {
		return err
	}
	rcpt, err := msgResolveRecipient(c, to, org)
	if err != nil {
		return err
	}
	theirs, err := agentOrg(c, rcpt)
	if err != nil {
		return err
	}
	if theirs == "" {
		theirs = orgPrefixOf(rcpt)
	}
	if theirs != "" && theirs != mine {
		return dieErr(3, fmt.Sprintf("%s (%s org) may not message %s (%s org); only the boss and director talk across "+
			"orgs. Ask your lead.", c.who, mine, rcpt, theirs))
	}
	return nil
}

// msgSend is send(): resolve the recipient in org, INSERT the message. goal/task/body nil store NULL.
func msgSend(c *ctx, to, subject string, body *string, kind string, goal, task *string, org string) (int64, string, error) {
	g, t := "", ""
	if goal != nil {
		g = *goal
	}
	if task != nil {
		t = *task
	}
	o, err := orgFor(c, g, t, c.who, org)
	if err != nil {
		return 0, "", err
	}
	rcpt, err := msgResolveRecipient(c, to, o)
	if err != nil {
		return 0, "", err
	}
	return msgInsert(c, rcpt, subject, body, kind, goal, task)
}

func msgInsert(c *ctx, rcpt, subject string, body *string, kind string, goal, task *string) (int64, string, error) {
	val := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	res, err := c.exec("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
		c.who, rcpt, kind, subject, val(body), val(goal), val(task), eventTS(c))
	if err != nil {
		return 0, "", err
	}
	id, err := res.LastInsertId()
	return id, rcpt, err
}

// msgSpecWarnApplies reports whether warn_spec_by_msg can look at this message at all (sender role and kind).
func msgSpecWarnApplies(role, kind string) bool {
	return (role == "lead" || role == "director" || role == "boss" || role == "owner") && kind != "decision"
}

// msgWarnSpec is warn_spec_by_msg: warn (stderr) and log an events row when a running worker is told a spec change.
func msgWarnSpec(c *ctx, mid int64, role, rcpt, kind, subject string, body *string) error {
	if !msgSpecWarnApplies(role, kind) {
		return nil
	}
	roles := make([]string, 0, len(workerRoles))
	args := []any{rcpt}
	for r := range workerRoles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		args = append(args, r)
	}
	q := "SELECT task_id FROM agents WHERE name=? AND status='live' AND task_id IS NOT NULL AND role IN (" +
		strings.TrimSuffix(strings.Repeat("?,", len(roles)), ",") + ")"
	var task string
	err := c.db.QueryRow(q, args...).Scan(&task)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	text := subject + " "
	if body != nil {
		text += *body
	}
	if !msgSpecWords.MatchString(text) {
		return nil
	}
	fmt.Fprintf(c.errb, "warning: %s is running %s; a running worker does not re-read its task. "+
		"For spec changes: factory stop, task update, respawn\n", rcpt, task)
	return event(c, "task", task, "spec-by-msg", strp(fmt.Sprintf("M%d %s->%s", mid, c.who, rcpt)))
}
