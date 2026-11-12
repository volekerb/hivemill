package cli

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// msg and report, store-only (G-041 S2m). The one side effect beyond the INSERT is spawn_on_demand for the
// staff-engineer: every message to it (any kind, any sender) runs in Python before anything is written.

func init() {
	register("msg", "", handler{parse: parseMsg, run: runMsg})
	register("report", "", handler{parse: parseReport, run: runReport})
}

type msgCmd struct {
	to, subject string
	body        *string
	kind        string
	goal, task  *string
}

type reportCmd struct {
	headline string
	body     *string
	goal, to *string
}

// msgArgs splits rest like argparse for the shapes Go answers: positionals first (min..max of them), then
// "--flag value" / "--flag=value" for the names in flags, each at most once. Anything else (an abbreviation, an
// unknown or repeated flag, a positional after an option, a value starting with "-" other than exactly "-", an
// empty or non-UTF-8 value, "--") returns ok=false and the command runs in Python.
func msgArgs(rest []string, flags []string, minPos, maxPos int) (pos []string, vals map[string]string, ok bool) {
	vals = map[string]string{}
	val := func(v string) bool { return v != "" && (v == "-" || !strings.HasPrefix(v, "-")) }
	i := 0
	for ; i < len(rest) && (rest[i] == "-" || !strings.HasPrefix(rest[i], "-")); i++ {
		if !utf8.ValidString(rest[i]) {
			return nil, nil, false
		}
		pos = append(pos, rest[i])
	}
	for ; i < len(rest); i++ {
		a := rest[i]
		name, v, eq := strings.Cut(a, "=")
		if !inList(flags, name) {
			return nil, nil, false
		}
		if _, dup := vals[name]; dup {
			return nil, nil, false
		}
		if !eq {
			if i+1 >= len(rest) {
				return nil, nil, false
			}
			i++
			v = rest[i]
		}
		if !val(v) || !utf8.ValidString(v) {
			return nil, nil, false
		}
		vals[name] = v
	}
	if len(pos) < minPos || len(pos) > maxPos {
		return nil, nil, false
	}
	return pos, vals, true
}

func msgOpt(vals map[string]string, k string) *string {
	if v, ok := vals[k]; ok {
		return strp(v)
	}
	return nil
}

func msgPosOpt(pos []string, i int) *string {
	if i < len(pos) {
		return strp(pos[i])
	}
	return nil
}

func parseMsg(rest []string) (any, bool) {
	pos, vals, ok := msgArgs(rest, []string{"--kind", "--goal", "--task"}, 2, 3)
	if !ok {
		return nil, false
	}
	m := msgCmd{to: pos[0], subject: pos[1], body: msgPosOpt(pos, 2), kind: "msg",
		goal: msgOpt(vals, "--goal"), task: msgOpt(vals, "--task")}
	if k, set := vals["--kind"]; set {
		if !inList([]string{"msg", "request", "report", "escalation", "decision", "alert"}, k) {
			return nil, false
		}
		m.kind = k
	}
	return m, true
}

func parseReport(rest []string) (any, bool) {
	pos, vals, ok := msgArgs(rest, []string{"--goal", "--to"}, 1, 2)
	if !ok {
		return nil, false
	}
	return reportCmd{headline: pos[0], body: msgPosOpt(pos, 1), goal: msgOpt(vals, "--goal"), to: msgOpt(vals, "--to")}, true
}

func msgStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// runMsg is cmd_msg (bin/factory_cli.py 4104).
func runMsg(c *ctx, parsed any) (int, error) {
	a := parsed.(msgCmd)
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	if a.kind == "escalation" && !inList([]string{"owner", "director", "boss", "lead"}, role) {
		return 0, dieErr(3, "only leads, the director and the boss escalate; message your lead instead")
	}
	// refuse_on_demand: the staff-engineer wake path (caller check, spawn) stays Python until S4.
	first, err := orgFor(c, "", "", c.who, "")
	if err != nil {
		return 0, err
	}
	rcpt, err := msgResolveRecipient(c, a.to, first)
	if err != nil {
		return 0, err
	}
	if orgStripPrefix(rcpt) == msgOnDemand {
		return 0, errDelegate
	}
	// Python's warn_spec_by_msg regex is Unicode-aware; RE2's is not. The body of "-" (stdin) or "@file" is only known
	// after it is read, and a hand-over after that would lose stdin, so those run in Python too.
	if msgSpecWarnApplies(role, a.kind) {
		b := msgStr(a.body)
		if !msgIsASCII(a.subject) || !msgIsASCII(b) || b == "-" || strings.HasPrefix(b, "@") {
			return 0, errDelegate
		}
	}
	goal, task := msgStr(a.goal), msgStr(a.task)
	if err := msgRefuseCrossOrg(c, role, a.to, goal, task); err != nil {
		return 0, err
	}
	org, err := msgSenderOrg(c, role, a.to, goal, task)
	if err != nil {
		return 0, err
	}
	rcpt, err = msgResolveRecipient(c, a.to, org)
	if err != nil {
		return 0, err
	}
	if orgStripPrefix(rcpt) == msgOnDemand {
		return 0, errDelegate // another org's staff-engineer: still Python's spawn path (before stdin is read)
	}
	body, err := readText(c, a.body, false)
	if err != nil {
		return 0, err
	}
	mid, rcpt, err := msgSend(c, a.to, a.subject, body, a.kind, a.goal, a.task, org)
	if err != nil {
		return 0, err
	}
	c.out.WriteString("M" + strconv.FormatInt(mid, 10) + " → " + rcpt + "\n")
	return 0, msgWarnSpec(c, mid, role, rcpt, a.kind, a.subject, body)
}

// runReport is cmd_report (bin/factory_cli.py 4205).
func runReport(c *ctx, parsed any) (int, error) {
	a := parsed.(reportCmd)
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	var to string
	if a.to == nil {
		to = "director"
		if workerRoles[role] && role != "staff-engineer" {
			var team *string
			if err := msgScanOpt(c, "SELECT team_id FROM agents WHERE name=?", &team, c.who); err != nil {
				return 0, err
			}
			if team != nil && *team != "" {
				var lead *string
				if err := msgScanOpt(c, "SELECT lead_agent FROM teams WHERE id=?", &lead, *team); err != nil {
					return 0, err
				}
				if lead != nil && *lead != "" {
					to = "lead:" + *team
				} else {
					c.errb.WriteString("note: team " + *team + " has no lead; report sent to director\n")
				}
			}
		}
	} else {
		to = *a.to
		first, err := orgFor(c, "", "", c.who, "")
		if err != nil {
			return 0, err
		}
		rcpt, err := msgResolveRecipient(c, to, first)
		if err != nil {
			return 0, err
		}
		if orgStripPrefix(rcpt) == msgOnDemand {
			return 0, errDelegate
		}
	}
	body, err := readText(c, a.body, false)
	if err != nil {
		return 0, err
	}
	goal := msgStr(a.goal)
	org, err := msgSenderOrg(c, role, to, goal, "")
	if err != nil {
		return 0, err
	}
	subject := pyStrip("status: " + goal + " " + a.headline)
	mid, rcpt, err := msgSend(c, to, subject, body, "report", a.goal, nil, org)
	if err != nil {
		return 0, err
	}
	c.out.WriteString("M" + strconv.FormatInt(mid, 10) + " report → " + rcpt + "\n")
	return 0, nil
}

// msgScanOpt reads one nullable text column into dst (left nil when the row is missing or NULL).
func msgScanOpt(c *ctx, q string, dst **string, args ...any) error {
	var v sql.NullString
	err := c.db.QueryRow(q, args...).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if v.Valid {
		*dst = &v.String
	}
	return nil
}
