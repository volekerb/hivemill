// Package store is the leaf package of the factory DB helpers that Python's bin/factory_cli.py shares between the CLI
// and the tick: config with its DEFAULT_CONFIG defaults, event_as, send with resolve_recipient/org_for, and the exact
// text of Python's json.dumps. It imports only the standard library (database/sql); cli and tick both import it,
// it imports neither.
package store

//go:generate go run ./gen
