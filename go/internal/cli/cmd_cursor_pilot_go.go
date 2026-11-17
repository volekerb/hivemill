package cli

import (
	"fmt"
	"strings"

	"factory/go/internal/agentruntime"
	"factory/go/internal/cursorharness"
	"factory/go/internal/skilleval"
)

// cursor-pilot status (Python cmd_cursor_pilot). prepare, probe and run materialise policy from the rendered Claude
// settings and run the Python guard adapter, which stay with the Python spawn path until P4-L: their argv shapes are
// not parsed here, so Python answers them. Registered for P4-L: unreachable until "cursor-pilot" is added to
// cli_go_cmds.

func init() {
	register("cursor-pilot", "status", handler{parse: parseCursorPilotStatus, run: runCursorPilotStatus})
}

func parseCursorPilotStatus(rest []string) (any, bool) { return nil, len(rest) == 0 }

func runCursorPilotStatus(c *ctx, _ any) (int, error) {
	if w, del := callerIsWorker(c); del || w != "" {
		return 0, errDelegate // refuse_worker: Python's text, with its stub exemption
	}
	if _, err := requireRole(c, "director", "boss"); err != nil {
		return 0, err
	}
	rt, err := cfgText(c, "agent_runtimes")
	if err != nil {
		return 0, err
	}
	pilot, err := cfgText(c, "cursor_pilot_roles")
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(c.out, "agent_runtimes = %s\n", skilleval.PyRepr(rt))
	fmt.Fprintf(c.out, "cursor_pilot_roles = %s (legacy alias)\n", skilleval.PyRepr(pilot))
	pairs, perr := agentruntime.ParseRuntimesOrdered(rt, pilot)
	if perr != nil {
		fmt.Fprintf(c.out, "resolved = {'<error>': %s}\n", skilleval.PyRepr(perr.Error()))
	} else {
		items := make([]string, len(pairs))
		for i, p := range pairs {
			items[i] = skilleval.PyRepr(p.Role) + ": " + skilleval.PyRepr(p.Runtime)
		}
		fmt.Fprintf(c.out, "resolved = {%s}\n", strings.Join(items, ", "))
	}
	bin := cursorharness.FindAgentBinary()
	if bin == "" {
		bin = "(not on PATH)"
	}
	fmt.Fprintf(c.out, "agent_binary = %s\n", bin)
	for _, row := range agentruntime.WorksMatrix() {
		fmt.Fprintf(c.out, "%-10s  %s: %s\n", row.Status, row.Item, row.Detail)
	}
	return 0, nil
}
