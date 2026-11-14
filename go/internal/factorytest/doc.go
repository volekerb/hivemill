// Package factorytest is the Go test harness for the factory CLI: the counterpart of tests/harness.py FactoryEnv.
//
// An Env is a throwaway FACTORY_HOME under t.TempDir() with the same layout FactoryEnv builds (git repo, db/factory.db
// created from testdata/init.sql, the config rows `factory init` plus the harness seeds write, roles/ and run/), a
// fake /proc, a temp HOME, and gh, herdr and hc stubs first on PATH. Env.Run drives a factory-go built once per
// test binary (cached across binaries by a hash of the Go sources). Stub calls are logged to calls.jsonl in the byte
// format of the Python stubs, so Env.Calls reads the same records as FactoryEnv.calls.
//
// A command the Go CLI has not ported yet is handed to bin/factory (Python) by the CLI itself; tests of such
// commands call Env.NeedPython, which skips when python3 is not on PATH. Everything the Go CLI answers itself
// runs without Python.
//
// Porting a Python module: write Test<Name>... functions in the consumer package, add a line to a ported_*.txt file in
// this directory (see ported.go), and keep the Python module until the code it covers is removed.
//
// testdata/init.sql is generated from the Python harness (tests/harness.py FactoryEnv after `factory init` and its
// config seeds); regenerate it when SCHEMA or DEFAULT_CONFIG in bin/factory_cli.py change.
package factorytest
