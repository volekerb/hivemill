// Package data is the read-only data layer of the Go web UI: a port of web/db.py, web/dbpath.py, web/queries.py,
// web/context.py and the data parts of web/ci.py and web/advisor_audit.py (G-048 P3b).
//
// Every query function takes a *Conn opened by Connect (SQLite mode=ro plus query_only) and returns typed structs
// whose json tags and field order match the Python dicts, so Dumps reproduces json.dumps(...) of the /api/*.json
// bodies. Nothing here writes to the database. Time is injected (a zero now means time.Now()), never read from
// the clock inside a query.
//
// Differences from Python that callers should know:
//   - shell_status asks for the Cursor panel through a CursorFunc (web/cursor_limits.display needs web.views.tokens,
//     which the view tasks port); a nil CursorFunc yields "cursor": null.
//   - web/context.py main() is RunContextCLI; its argument errors are plain text, not argparse's.
package data
