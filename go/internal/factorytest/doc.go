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
// The herdr stub has two modes. The default is scripted (Env.Script): answers come from rules, nothing is kept. The
// stateful mode is the Go port of tests/stubs/herdr and is switched on per test with Env.HerdrStateful (FAKE_HERDR_MODE=stateful):
// the JSON state file Env.HerdrStatePath ($FAKE_HERDR_STATE) holds workspaces, tabs, panes and agents with new ids
// (w, t, p; one counter), so `tab create` and `agent start` add rows that `tab list` and `agent list` return, and
// `pane close` removes a pane and its agents. A missing or empty state file is an empty state. The knobs of the
// Python stub are keys of the state file, set with Env.SetHerdr(map) or Env.HerdrFail(bool): fail, slow_list,
// start_fail, start_sleep, wait_sleep, pane_close_fail, pane_close_noop, fail_cmds, read_text, pane_read_text,
// agent_list_ok. Env.HerdrHook(cmd, at) sets FAKE_HERDR_HOOK / FAKE_HERDR_HOOK_AT: a shell command the stub runs
// synchronously, once (marker <state>.hook-ran, written before the command runs, so herdr calls the hook makes do
// not re-run it), on the first call or the first call whose argv starts with the words of at, before the call is
// handled; Env.HerdrHookAfter(cmd, at, n) runs it on the call after the first n matches (a Go-only extension, to
// reach a point inside one CLI run). Env.HerdrStarts lists the `agent start` calls, Env.HerdrState parses the state
// file. Answers, state file and calls.jsonl are byte-for-byte those of the Python stub (TestHerdrStubParity). One
// difference by design: the Python stub has no `agent stop` handling (it answers {"text": ""}), and neither has this one.
// Env.Timeout (default 60 s) ends a CLI run, or a stub whose hook hangs, with exit -1 and a timeout line on stderr.
//
// Porting a Python module: write Test<Name>... functions in the consumer package, add a line to a ported_*.txt file in
// this directory (see ported.go), and keep the Python module until the code it covers is removed.
//
// testdata/init.sql is generated from the Python harness (tests/harness.py FactoryEnv after `factory init` and its
// config seeds); regenerate it when SCHEMA or DEFAULT_CONFIG in bin/factory_cli.py change.
package factorytest
