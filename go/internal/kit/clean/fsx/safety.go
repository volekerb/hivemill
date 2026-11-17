package fsx

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var geteuid = os.Geteuid

// NeverRoot refuses to run as root.
func NeverRoot() error {
	if geteuid() == 0 {
		return errors.New("hm-clean: refusing to run as root")
	}
	return nil
}

// ErrCap means a reservation would exceed a byte cap.
var ErrCap = errors.New("fsx: byte cap exceeded")

// Ledger enforces a per-run and per-rule byte cap.
type Ledger struct {
	mu       sync.Mutex
	runCap   int64
	ruleCaps map[string]int64
	total    int64
	rules    map[string]int64
}

// NewLedger builds a ledger. A rule without an entry in ruleCaps is bound by
// the run cap only.
func NewLedger(runCap int64, ruleCaps map[string]int64) *Ledger {
	rc := make(map[string]int64, len(ruleCaps))
	for k, v := range ruleCaps {
		rc[k] = v
	}
	return &Ledger{runCap: runCap, ruleCaps: rc, rules: map[string]int64{}}
}

// Reserve books bytes for rule, or returns ErrCap and books nothing.
func (l *Ledger) Reserve(rule string, bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("fsx: negative reservation %d", bytes)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if bytes > l.runCap-l.total {
		return fmt.Errorf("%w: run cap %d", ErrCap, l.runCap)
	}
	if c, ok := l.ruleCaps[rule]; ok && bytes > c-l.rules[rule] {
		return fmt.Errorf("%w: rule %q cap %d", ErrCap, rule, c)
	}
	l.total += bytes
	l.rules[rule] += bytes
	return nil
}

// Total is the bytes reserved so far.
func (l *Ledger) Total() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}
