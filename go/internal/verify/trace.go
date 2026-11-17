// Package verify is the harness behind go/cmd/verify: it runs a change in a throwaway FACTORY_HOME (cli mode) or
// checks the dashboard mod (mod mode) and prints a step trace whose line format is a contract (docs/verify.md).
package verify

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// trace writes trace lines to the live writer and keeps the same bytes for OUT/trace.txt.
type trace struct {
	w     io.Writer
	buf   bytes.Buffer
	steps int
	werr  bool
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func (t *trace) line(s string) {
	s += "\n"
	t.buf.WriteString(s)
	if _, err := io.WriteString(t.w, s); err != nil && !t.werr {
		t.werr = true
		fmt.Fprintln(os.Stderr, "verify: cannot write trace to stdout:", err)
	}
}

func (t *trace) step(name string, ok bool, d time.Duration, detail string) {
	t.steps++
	status := "ok"
	if !ok {
		status = "fail"
	}
	l := fmt.Sprintf("verify: step %d %s %s %dms", t.steps, name, status, d.Milliseconds())
	if detail = oneLine(detail); detail != "" {
		l += " " + detail
	}
	t.line(l)
}

func (t *trace) result(mode string, reason string, d time.Duration) {
	verdict := "PASS"
	if reason != "" {
		verdict = "FAIL"
	}
	l := fmt.Sprintf("verify: result %s mode=%s steps=%d ms=%d", verdict, mode, t.steps, d.Milliseconds())
	if reason != "" {
		l += " reason=" + oneLine(reason)
	}
	t.line(l)
}
