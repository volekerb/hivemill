package cli

import (
	"database/sql"
	"regexp"
	"strings"
)

// Stmt is the (verb, table) of one write statement a handler ran, for the statement-order tests (S2 rule W5).
// BEGIN and COMMIT have no table.
type Stmt struct{ Verb, Table string }

var stmtRe = regexp.MustCompile(`(?is)^\s*(?:(INSERT)\s+(?:OR\s+\w+\s+)?INTO|(UPDATE)(?:\s+OR\s+\w+)?|(DELETE)\s+FROM)\s+["` + "`" + `\[]?(\w+)`)

// parseStmt reads the verb and table off an SQL statement; an unrecognised statement has an empty verb.
func parseStmt(q string) Stmt {
	if m := stmtRe.FindStringSubmatch(q); m != nil {
		return Stmt{Verb: strings.ToUpper(m[1] + m[2] + m[3]), Table: strings.ToLower(m[4])}
	}
	f := strings.Fields(strings.ToUpper(q))
	if len(f) > 0 && (f[0] == "BEGIN" || f[0] == "COMMIT" || f[0] == "ROLLBACK") {
		return Stmt{Verb: f[0]}
	}
	return Stmt{}
}

// exec runs a write statement. Write handlers must use it (never c.db.Exec) so the trace sees every statement.
func (c *ctx) exec(query string, args ...any) (sql.Result, error) {
	if c.trace != nil {
		*c.trace = append(*c.trace, parseStmt(query))
	}
	return c.db.Exec(query, args...)
}

// query runs a read statement; it exists so a handler has one way in and a future change (a read trace) has one place.
func (c *ctx) query(query string, args ...any) (*sql.Rows, error) {
	return c.db.Query(query, args...)
}
