// Package merge is the Go port of the judge merge gate in bin/factory_cli.py (cmd_judge, _judge_gate_and_merge,
// verdict_pass_holds_for_pr_head, verdict_carries, head_has_main, checks_ok, latest_checks, missing_checks,
// red_main_refusal, main_run_refusal, _push_policy, main_landing). Python stays the reference; every decision here is
// the same decision or a stricter one. Whatever the port cannot decide with Python's exact text is reported as
// ErrUnsupported, and the caller hands the command to Python before it has written anything.
//
// The package does no database and no process I/O of its own: git and gh answers arrive through Runner and plain
// values, so the gate table can be driven both from unit tests and from the real `factory judge` entry point.
package merge

import "errors"

// ErrUnsupported means the port cannot reproduce Python's answer (an unexpected JSON shape, a value whose Python
// text differs): the caller delegates to Python without having written anything.
var ErrUnsupported = errors.New("merge: not decided here, hand to python")
