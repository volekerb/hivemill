// Package views renders the pages of the Go web UI: a port of web/html.py, web/components.py, web/charts.py and
// web/views/{home,goals,tasks}.py (G-048 P3c). It reads through go/internal/web/data and go/internal/tokens and
// returns HTML strings; serving them is P3e.
//
// Escaping: every interpolated value goes through html/template.HTMLEscapeString unless it is a Safe (the output of
// these helpers). Views never build markup by concatenating raw data. No inline script or style is emitted.
//
// Differences from Python that callers should know:
//   - The page title and body come back from View.Render instead of the module attributes TITLE / data() / render().
//   - A Ctx carries injected functions (Tokens, Usage, Mem, Machine, Cursor) where the Python Ctx carries the
//     same callables; a nil function means "not available" exactly as a None does in Python.
//   - El panics on a tag or attribute name that is not [a-z][a-z0-9]* / [a-z-]+ (Python raises ValueError): names
//     are constants in the code, never data.
package views
