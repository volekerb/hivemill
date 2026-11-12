package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"factory/go/internal/identity"
)

// Write helpers shared by the S2 write commands: the Go ports of die, event, event_as, next_id, read_text, slug,
// require_role, pm_own_goal and cfg in bin/factory_cli.py. Frozen after merge: later tasks reuse, never edit.

// dieErr is die(msg, code): the command ends with "factory: <msg>" on stderr and exit code code.
func dieErr(code int, msg string) error { return &identity.ExitError{Code: code, Msg: msg} }

// eventTS formats the clock like now() and the identity layer's events rows.
func eventTS(c *ctx) string { return c.env.Now().Format("2006-01-02T15:04:05-07:00") }

// eventAs is event_as: one events row for who. detail nil stores NULL.
func eventAs(c *ctx, who, entity, entityID, action string, detail *string) error {
	var d any
	if detail != nil {
		d = *detail
	}
	_, err := c.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		eventTS(c), who, entity, entityID, action, d)
	return err
}

// event is event(): one events row for the caller.
func event(c *ctx, entity, entityID, action string, detail *string) error {
	return eventAs(c, c.who, entity, entityID, action, detail)
}

// strp is the address of s, for event details.
func strp(s string) *string { return &s }

// nextID is next_id: prefix-N with N = highest numeric suffix + 1, zero-padded to width (a minimum). Python's
// int() raises on a suffix it cannot parse; Go hands the command over then.
func nextID(c *ctx, table, prefix string, width int) (string, error) {
	rows, err := c.query("SELECT id FROM "+table+" WHERE id LIKE ?", prefix+"-%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var max int64
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		parts := strings.Split(id, "-")
		if len(parts) < 2 {
			return "", errDelegate
		}
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n < 0 {
			return "", errDelegate
		}
		if n > max {
			max = n
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%0*d", prefix, width, max+1), nil
}

// stdinUsed is _STDIN_USED, kept per command run (one ctx per Run).
var stdinUsed sync.Map // *ctx -> bool

// pyNewlines is universal-newline translation of Python text mode.
func pyNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// pyPathNorm is how pathlib spells a path before Path.exists: "." components, empty components and a trailing "/" are
// dropped ("" is "."); ".." stays.
func pyPathNorm(p string) string {
	var keep []string
	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			keep = append(keep, part)
		}
	}
	out := strings.Join(keep, "/")
	if strings.HasPrefix(p, "/") {
		out = "/" + out
	}
	if out == "" {
		return "."
	}
	return out
}

// readText is read_text: "-" reads stdin once (second "-": exit 2), "@path" of an existing file reads it, anything
// else is literal. nil in, nil out. Result is stripped. withinCwd: an @path must resolve inside the cwd (exit 2).
// Cases Python would crash on (a directory, invalid UTF-8, an unreadable file) delegate.
func readText(c *ctx, val *string, withinCwd bool) (*string, error) {
	if val == nil {
		return nil, nil
	}
	v := *val
	if v == "-" {
		if _, used := stdinUsed.LoadOrStore(c, true); used {
			return nil, dieErr(2, "only one argument per call can be '-' (stdin). Set the other field with a second call "+
				"(e.g. `factory goal edit`/`factory task update`), or pass it inline.")
		}
		b, err := io.ReadAll(c.deps.stdin())
		if err != nil || !utf8.Valid(b) {
			return nil, errDelegate
		}
		return strp(pyStrip(pyNewlines(string(b)))), nil
	}
	if strings.HasPrefix(v, "@") {
		p := pyPathNorm(v[1:])
		if _, err := os.Stat(p); err == nil {
			f := identity.Realpath(p)
			if withinCwd {
				cwd, err := c.deps.getwd()
				if err != nil {
					return nil, errDelegate
				}
				root := identity.Realpath(cwd)
				if f != root && !strings.HasPrefix(f, strings.TrimRight(root, "/")+"/") {
					return nil, dieErr(2, fmt.Sprintf("%s: @path must be inside the current directory (%s); copy the file there or pass the text inline", v, cwd))
				}
			}
			fi, err := os.Stat(f)
			if err != nil || fi.IsDir() {
				return nil, errDelegate
			}
			b, err := os.ReadFile(f)
			if err != nil || !utf8.Valid(b) {
				return nil, errDelegate
			}
			return strp(pyStrip(pyNewlines(string(b)))), nil
		}
	}
	return val, nil
}

var slugBadRE = regexp.MustCompile(`[^a-z0-9-]+`)

// slug is slug(s, n): lower-case, runs of other characters become "-", trimmed, "a-" prefix when empty or not
// starting with a letter, cut at n then rstripped of "-".
func slug(s string, n int) string {
	s = strings.ToLower(strings.ReplaceAll(s, "İ", "i̇")) // Python lower() of U+0130 is two code points
	s = strings.Trim(slugBadRE.ReplaceAllString(s, "-"), "-")
	if s == "" || !(s[0] >= 'a' && s[0] <= 'z') {
		s = "a-" + s
	}
	switch { // s is ASCII here, so byte and character slicing agree
	case n < 0:
		if -n >= len(s) {
			s = ""
		} else {
			s = s[:len(s)+n]
		}
	case n < len(s):
		s = s[:n]
	}
	return strings.TrimRight(s, "-")
}

// requireRole is require_role: the owner or one of allowed passes; otherwise exit 3. It returns the caller's role.
func requireRole(c *ctx, allowed ...string) (string, error) {
	role, err := actorRole(c)
	if err != nil {
		return "", err
	}
	if role == "owner" {
		return role, nil
	}
	for _, a := range allowed {
		if role == a {
			return role, nil
		}
	}
	return "", dieErr(3, fmt.Sprintf("role '%s' (%s) may not do this; allowed: %s", role, c.who, strings.Join(allowed, ", ")))
}

// pmOwnGoal is pm_own_goal: a pm may only act on the goal it works on (agents.goal_id).
func pmOwnGoal(c *ctx, goalID, what string) error {
	role, err := actorRole(c)
	if err != nil {
		return err
	}
	if role != "pm" {
		return nil
	}
	var g sql.NullString
	if err := c.db.QueryRow("SELECT goal_id FROM agents WHERE name=?", c.who).Scan(&g); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errDelegate // Python crashes on a missing row
		}
		return err
	}
	if !g.Valid || g.String == "" || g.String != goalID {
		works := g.String
		if works == "" {
			works = "no goal"
		}
		return dieErr(3, fmt.Sprintf("%s works on %s; %s is in %s", c.who, works, what, goalID))
	}
	return nil
}

// cfgText is cfg(con, key): the config row, else the built-in default; an unknown key delegates (Python KeyError).
func cfgText(c *ctx, key string) (string, error) {
	var v sql.NullString
	err := c.db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v)
	if err == nil {
		return v.String, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if d, ok := defaultMap()[key]; ok {
		return d, nil
	}
	return "", errDelegate
}
