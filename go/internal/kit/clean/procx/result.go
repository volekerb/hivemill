// Package procx answers process questions for hm-clean: does any process have
// its cwd inside a directory, does any process hold a path open, and is the
// caller running under an AI agent. Every predicate fails closed: an answer
// that could not be read is Unknown, never Pass.
package procx

// Status is the tri-state outcome of a predicate. The zero value is Unknown so
// an unset Result can never be mistaken for a clean pass.
type Status int

const (
	// Unknown means the question could not be answered; callers must treat it
	// like Fail (do not delete).
	Unknown Status = iota
	// Pass means the predicate was fully evaluated and nothing was found.
	Pass
	// Fail means a process was found (cwd inside, file held, ...).
	Fail
)

func (s Status) String() string {
	switch s {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	default:
		return "unknown"
	}
}

// Result is a Status plus a human-readable reason.
type Result struct {
	Status Status
	Reason string
}

func pass() Result                 { return Result{Status: Pass} }
func fail(reason string) Result    { return Result{Status: Fail, Reason: reason} }
func unknown(reason string) Result { return Result{Status: Unknown, Reason: reason} }

// Process is one row of the process table.
type Process struct {
	PID   int
	PPID  int
	UID   int
	Start string // start time as reported by the OS (text, for display and identity)
	Comm  string
	Argv  []string
	Cwd   string // empty when unreadable
}
