package procx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var agentEnvVars = []string{"CLAUDECODE", "FACTORY_AGENT", "FACTORY_ROLE", "CURSOR_AGENT"}
var agentNames = map[string]bool{"claude": true, "cursor-agent": true, "herdr": true}

// AgentContext reports whether the caller runs under an AI agent: an agent
// environment variable is set (env is KEY=VALUE pairs, as os.Environ), or an
// ancestor of this process in procs is claude, cursor-agent or herdr, or has
// @anthropic-ai/claude-code in its argv. If the ancestry cannot be read it
// returns isAgent=true with the error (fail closed).
func AgentContext(env []string, procs []Process) (bool, string, error) {
	return agentContextFrom(env, procs, os.Getpid())
}

// AgentContextNow reads the process table itself and runs AgentContext.
func AgentContextNow(env []string) (bool, string, error) {
	procs, err := Processes()
	if err != nil {
		return true, "process table unreadable", err
	}
	return AgentContext(env, procs)
}

func agentContextFrom(env []string, procs []Process, start int) (bool, string, error) {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		for _, name := range agentEnvVars {
			if k == name && v != "" {
				return true, "env " + name + " is set", nil
			}
		}
	}
	byPID := make(map[int]Process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	cur, ok := byPID[start]
	if !ok {
		return true, "own process not in table", fmt.Errorf("pid %d missing from process table", start)
	}
	seen := map[int]bool{start: true}
	for cur.PPID > 1 {
		pp, ok := byPID[cur.PPID]
		if !ok {
			return true, "ancestry broken", fmt.Errorf("ancestor pid %d of %d missing from process table", cur.PPID, cur.PID)
		}
		if seen[pp.PID] {
			return true, "ancestry loop", fmt.Errorf("ppid loop at pid %d", pp.PID)
		}
		seen[pp.PID] = true
		if why := agentProc(pp); why != "" {
			return true, fmt.Sprintf("ancestor pid %d %s", pp.PID, why), nil
		}
		cur = pp
	}
	return false, "", nil
}

func agentProc(p Process) string {
	if agentNames[filepath.Base(p.Comm)] {
		return "comm " + filepath.Base(p.Comm)
	}
	if len(p.Argv) > 0 && agentNames[filepath.Base(p.Argv[0])] {
		return "argv0 " + filepath.Base(p.Argv[0])
	}
	for _, a := range p.Argv {
		if strings.Contains(a, "@anthropic-ai/claude-code") {
			return "runs @anthropic-ai/claude-code"
		}
	}
	return ""
}
