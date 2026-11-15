package macfacts

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ProbeTimeout is the per-probe limit.
const ProbeTimeout = 10 * time.Second

// MaxProbeOutput is the output budget of one probe, in bytes.
const MaxProbeOutput = 4096

// Step is one command (or stat) inside a probe.
type Step struct {
	Cmd      string
	Exit     int
	Out      string
	TimedOut bool
	Err      string
}

// Section is the recorded result of one probe.
type Section struct {
	Name  string
	Steps []Step
}

// Probe is one named, read-only fact probe.
type Probe struct {
	Name string
	Run  func(ctx context.Context, e *Env) []Step
}

// Env is everything a probe may touch; tests inject fakes.
type Env struct {
	Runner    Runner
	Home      string
	TempDir   string // the run's own temp dir (children's cwd)
	HerdrPath string
	Uid       int
	Timeout   time.Duration // per probe; 0 means ProbeTimeout
	Now       func() time.Time
	LookPath  func(string) (string, error)
	Stat      func(string) (exists bool, detail string)
	Fstype    func(string) (string, error)
}

// Collect runs every probe, each under its own timeout, and returns the
// sections. A failing or hanging probe is recorded and never aborts the run.
func Collect(ctx context.Context, e *Env, probes []Probe) []Section {
	timeout := e.Timeout
	if timeout == 0 {
		timeout = ProbeTimeout
	}
	var secs []Section
	for _, p := range probes {
		secs = append(secs, runProbe(ctx, e, p, timeout))
	}
	return secs
}

func runProbe(parent context.Context, e *Env, p Probe, timeout time.Duration) Section {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	done := make(chan []Step, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- []Step{{Cmd: "(probe " + p.Name + ")", Exit: -1, Err: fmt.Sprint("panic: ", r)}}
			}
		}()
		done <- p.Run(ctx, e)
	}()
	select {
	case steps := <-done:
		return Section{Name: p.Name, Steps: steps}
	case <-ctx.Done():
		return Section{Name: p.Name, Steps: []Step{{
			Cmd: "(probe " + p.Name + ")", Exit: -1, TimedOut: true,
			Err: fmt.Sprintf("timed out after %s", timeout),
		}}}
	}
}

// Render builds the Markdown report. Each probe's output is redacted and then
// cut to MaxProbeOutput bytes in total; the whole report gets a final
// redaction pass.
func Render(e *Env, secs []Section) string {
	var b strings.Builder
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	fmt.Fprintf(&b, "# macOS facts report\n\nGenerated: %s\n\n", now().UTC().Format(time.RFC3339))
	b.WriteString("Read-only probes for the ADR 0003 untested claims. Output is redacted and cut to 4 KB per probe.\n\n")
	for _, s := range secs {
		fmt.Fprintf(&b, "## %s\n\n", s.Name)
		budget := MaxProbeOutput
		for _, st := range s.Steps {
			out := Redact(st.Out)
			if len(out) > budget {
				out = out[:budget] + "\n[truncated]"
			}
			budget -= len(out)
			if budget < 0 {
				budget = 0
			}
			fmt.Fprintf(&b, "Command: `%s`\n\n", Redact(st.Cmd))
			status := fmt.Sprintf("Exit code: %d", st.Exit)
			if st.TimedOut {
				status += " (timed out)"
			}
			if st.Err != "" {
				status += " (error: " + Redact(st.Err) + ")"
			}
			fmt.Fprintf(&b, "%s\n\n```\n%s\n```\n\n", status, strings.TrimRight(out, "\n"))
		}
	}
	return Redact(b.String())
}
