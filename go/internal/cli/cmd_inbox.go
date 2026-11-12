package cli

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"factory/go/internal/identity"
)

// The S2e inbox read forms. Python marks the shown messages read unless --peek is given or --agent names someone
// else, so those two are the only list shapes answered here; every form that would write read_at, and every shape
// argparse might read differently (abbreviated flags, --opt=value, repeated flags), runs in Python.

func init() {
	register("inbox", "", handler{parse: parseInbox, run: runInbox})
}

type inboxCmd struct {
	show            string
	hasShow         bool
	peek, all, full bool
	agent           string
	limit           int
}

const inboxDefaultLimit = 50

// inboxValue reports whether v is safe as an option value: argparse would take a leading "-" for another option.
func inboxValue(v string) bool { return v != "" && !strings.HasPrefix(v, "-") && utf8.ValidString(v) }

func parseInbox(rest []string) (any, bool) {
	c := inboxCmd{limit: inboxDefaultLimit}
	if len(rest) == 2 && rest[0] == "--show" { // --show alone
		if rest[1] == "" || strings.HasPrefix(rest[1], "-") || !utf8.ValidString(rest[1]) { // "" is falsy to Python: the list (write) path
			return nil, false
		}
		c.show, c.hasShow = rest[1], true
		return c, true
	}
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if seen[a] {
			return nil, false // a repeated flag: leave argparse's last-wins rule to Python
		}
		switch a {
		case "--peek", "--all", "--full":
			seen[a] = true
			switch a {
			case "--peek":
				c.peek = true
			case "--all":
				c.all = true
			default:
				c.full = true
			}
		case "--limit", "--agent":
			if i+1 >= len(rest) || !inboxValue(rest[i+1]) {
				return nil, false
			}
			seen[a] = true
			v := rest[i+1]
			i++
			if a == "--agent" {
				c.agent = v
				break
			}
			n, ok := parseDecimal(v, 9)
			if !ok || n < 1 {
				return nil, false
			}
			c.limit = n
		default:
			return nil, false
		}
	}
	if !c.peek && c.agent == "" { // the write path: marks the shown messages read
		return nil, false
	}
	return c, true
}

// parseDecimal accepts plain ASCII digits only (no sign, underscore or other-script digit) of at most maxLen digits.
func parseDecimal(s string, maxLen int) (int, bool) {
	if s == "" || len(s) > maxLen {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

type inboxMsg struct {
	id                                     int64
	sender, recipient, kind, subject, body string
	goal, task, created                    string
	unread                                 bool
}

func scanInbox(rows *sql.Rows) ([]inboxMsg, error) {
	defer rows.Close()
	var out []inboxMsg
	for rows.Next() {
		var id, sender, rcpt, kind, subj, body, goal, task, created, readAt any
		if err := rows.Scan(&id, &sender, &rcpt, &kind, &subj, &body, &goal, &task, &created, &readAt); err != nil {
			return nil, errDelegate
		}
		m := inboxMsg{unread: readAt == nil}
		n, ok := id.(int64)
		if !ok {
			return nil, errDelegate
		}
		m.id = n
		for _, f := range []struct {
			dst *string
			src any
		}{{&m.sender, sender}, {&m.recipient, rcpt}, {&m.kind, kind}, {&m.subject, subj}, {&m.body, body},
			{&m.goal, goal}, {&m.task, task}, {&m.created, created}} {
			s, err := cell(f.src)
			if err != nil {
				return nil, err
			}
			*f.dst = s
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errDelegate
	}
	return out, nil
}

const inboxCols = "id,sender,recipient,kind,subject,body,goal_id,task_id,created,read_at"

func inboxHeader(m inboxMsg) string {
	s := fmt.Sprintf("── M%d [%s] from %s @ %s", m.id, m.kind, m.sender, m.created)
	if m.goal != "" {
		s += " goal=" + m.goal
	}
	if m.task != "" {
		s += " task=" + m.task
	}
	return s
}

// hasDailyDigestPrefix is subject.lower().startswith("daily digest"). Only ASCII folds are applied: no non-ASCII
// character lower-cases to one of these letters, and Go's ToLower would wrongly fold U+0130 to "i".
func hasDailyDigestPrefix(subject string) bool {
	const p = "daily digest"
	if len(subject) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		b := subject[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if b != p[i] {
			return false
		}
	}
	return true
}

func inboxCollapsed(m inboxMsg, hq, full bool) bool {
	if full || !hq || m.kind != "report" || hasDailyDigestPrefix(m.subject) {
		return false
	}
	return len(pySplitlines(m.body)) > 3 || utf8.RuneCountInString(m.body) > 400
}

func runInbox(c *ctx, p any) (int, error) {
	if c.who == identity.Unknown {
		return 0, nil // no identity: print nothing, mark nothing read
	}
	a := p.(inboxCmd)
	if a.hasShow {
		return 0, inboxShow(c, a.show)
	}
	if a.agent == c.who && !a.peek { // Python marks the shown messages read here
		return 0, errDelegate
	}
	me := a.agent
	if me == "" {
		me = c.who
	}
	hq := false
	var role string
	switch err := c.db.QueryRow("SELECT role FROM agents WHERE name=?", me).Scan(&role); {
	case err == nil:
		hq = role == "boss" || role == "director" || role == "lead"
	case err == sql.ErrNoRows:
		hq = me == "boss"
	default:
		return 0, errDelegate
	}
	q := "SELECT " + inboxCols + " FROM messages WHERE recipient=?"
	if !a.all {
		q += " AND read_at IS NULL"
	}
	r, err := c.db.Query(q+" ORDER BY id", me)
	if err != nil {
		return 0, errDelegate
	}
	rows, err := scanInbox(r)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.out, "inbox empty")
	}
	cut := 0
	if len(rows) > a.limit {
		cut = len(rows) - a.limit
	}
	hidden := 0
	for _, m := range rows[:cut] {
		if m.unread {
			hidden++
		}
	}
	if hidden > 0 {
		fmt.Fprintf(c.out, "(%d older unread not shown; run factory inbox again or --all)\n", hidden)
	}
	for _, m := range rows[cut:] {
		fmt.Fprintln(c.out, inboxHeader(m))
		fmt.Fprintf(c.out, "   %s\n", m.subject)
		if inboxCollapsed(m, hq, a.full) {
			lines := pySplitlines(m.body)
			first := ""
			for _, l := range lines {
				if s := pyStrip(l); s != "" {
					first = s
					break
				}
			}
			more := "truncated"
			if len(lines) > 1 {
				more = fmt.Sprintf("+%d lines", len(lines)-1)
			}
			fmt.Fprintf(c.out, "   %s\n   (%s: factory inbox --show M%d)\n", pyTrunc(first, 200), more, m.id)
		} else if m.body != "" {
			fmt.Fprintln(c.out, pyIndent(m.body, "   "))
		}
	}
	return 0, nil
}

func inboxShow(c *ctx, raw string) error {
	arg := pyStrip(raw)
	if strings.HasPrefix(arg, "M") || strings.HasPrefix(arg, "m") {
		arg = arg[1:]
	}
	id, ok := parseDecimal(arg, 18)
	if !ok {
		for i := 0; i < len(arg); i++ {
			if arg[i] >= 0x80 { // str.isdigit accepts other scripts' digits; Python decides
				return errDelegate
			}
		}
		if arg != "" && allASCIIDigits(arg) { // more than 18 digits: sqlite integer overflow is Python's to report
			return errDelegate
		}
		return &identity.ExitError{Code: 1, Msg: fmt.Sprintf("bad message id '%s'; use M123 or 123", raw)}
	}
	r, err := c.db.Query("SELECT "+inboxCols+" FROM messages WHERE id=?", id)
	if err != nil {
		return errDelegate
	}
	ms, err := scanInbox(r)
	if err != nil {
		return err
	}
	if len(ms) == 0 {
		return &identity.ExitError{Code: 1, Msg: "no message M" + arg}
	}
	m := ms[0]
	if c.who != m.recipient && c.who != m.sender {
		role := "unknown"
		if c.who == "owner" {
			role = "owner"
		} else {
			var r string
			switch err := c.db.QueryRow("SELECT role FROM agents WHERE name=?", c.who).Scan(&r); {
			case err == nil:
				role = r
			case err != sql.ErrNoRows:
				return errDelegate
			}
		}
		if role != "director" && role != "boss" && role != "owner" {
			return &rawExit{Code: 3, Stderr: "not your message: M" + arg + "\n"}
		}
	}
	fmt.Fprintln(c.out, inboxHeader(m))
	fmt.Fprintf(c.out, "   %s\n", m.subject)
	if m.body != "" {
		fmt.Fprintln(c.out, pyIndent(m.body, "   "))
	}
	return nil
}

func allASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
