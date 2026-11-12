package tick

import (
	"io"
	"os/exec"
	"strings"
	"syscall"
)

// childResult is how a Python child ended.
type childResult struct {
	code   int
	signal string // non-empty: killed by this signal
	err    error  // could not start
}

// runChild is contract H3: python3 <entry> tick --phases <units> with FACTORY_CLI_IMPL=python and
// FACTORY_TICK_HANDOFF, stdin /dev/null, stdout and stderr the tick's own.
func runChild(python, entry string, environ []string, handoff string, units []string, stdout, stderr io.Writer) childResult {
	cmd := exec.Command(python, entry, "tick", "--phases", strings.Join(units, ","))
	cmd.Env = append(withoutKeys(environ, "FACTORY_CLI_IMPL", "FACTORY_TICK_HANDOFF"),
		"FACTORY_CLI_IMPL=python", "FACTORY_TICK_HANDOFF="+handoff)
	cmd.Stdin = nil // os/exec connects /dev/null
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return childResult{err: err}
	}
	err := cmd.Wait()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return childResult{signal: ws.Signal().String()}
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return childResult{err: err} // an output copy failed
		}
	}
	return childResult{code: cmd.ProcessState.ExitCode()}
}

func withoutKeys(environ []string, keys ...string) []string {
	out := make([]string, 0, len(environ))
next:
	for _, kv := range environ {
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}
