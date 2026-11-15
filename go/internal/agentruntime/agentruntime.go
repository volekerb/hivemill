// Package agentruntime is the Go port of scout/agent_runtime.py: the multi-runtime agent harness. Claude Code is the
// default runtime; Cursor is the first extension (config agent_runtimes, e.g. "pm=cursor" or "*=cursor").
package agentruntime

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"factory/go/internal/cursorharness"
)

// Known is KNOWN: the runtime names.
var Known = []string{"claude", "cursor"}

func known(rt string) bool { return rt == "claude" || rt == "cursor" }

// allowed is ALLOWED[runtime]: claude takes every role (nil), cursor takes the harness allow-list.
func allowed(rt string) map[string]bool {
	if rt == "cursor" {
		return cursorharness.CursorRoles
	}
	return nil
}

func sortedKnown() string {
	s := append([]string(nil), Known...)
	sort.Strings(s)
	return strings.Join(s, ", ")
}

// pyList renders a sorted []str like Python's repr: ['a', 'b'].
func pyList(s []string) string {
	q := make([]string, len(s))
	for i, x := range s {
		q[i] = "'" + strings.ReplaceAll(x, "'", "\\'") + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func pyRepr(s string) string { return "'" + strings.ReplaceAll(s, "'", "\\'") + "'" }

// SpawnPlan is what the spawner needs to start one agent after place()/settings render.
type SpawnPlan struct {
	Runtime   string
	Argv      []string
	HerdrKind string // "" = None
	Launch    string // "herdr-kind" | "pane-run" | "subprocess"
	Binary    string
	Notes     []string
	Prepared  *cursorharness.Prepared
	SessionID string
	Workspace string
}

// SpawnContext is the input of PlanSpawn.
type SpawnContext struct {
	Role, Name, Cwd, Model, PromptText, Kickoff string
	SettingsPath                                string
	Allow, Deny                                 []string // the Claude settings permissions
	Home, HooksDir                              string
	PluginDirs, AddDirs                         []string
	Resume                                      string
	Fresh                                       bool
	Effort                                      []string
	AppendPromptFile                            string
	SessionID                                   string
	Evaluator                                   bool
	Sandbox                                     bool // Cursor --sandbox enabled (not Claude nsbox)
	// StateRoot is XDG_STATE_HOME or ~/.local/state; NewUUID makes session ids. Both are injected.
	StateRoot string
	NewUUID   func() string
}

// ParseRuntimes returns {role: runtime}. Bad tokens are an error. "*=cursor" and "all=cursor" expand to every
// Cursor-capable role.
func ParseRuntimes(agentRuntimes, cursorPilotRoles string) (map[string]string, error) {
	out := map[string]string{}
	if raw := cursorharness.PyStrip(agentRuntimes); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = cursorharness.PyStrip(part)
			if part == "" {
				continue
			}
			role, runtime, found := strings.Cut(part, "=")
			if !found {
				return nil, fmt.Errorf("agent_runtimes entry %s must be role=runtime (known runtimes: %s; "+
					"use *=cursor or all=cursor for every Cursor-capable role)", pyRepr(part), sortedKnown())
			}
			role, runtime = cursorharness.PyStrip(role), cursorharness.PyStrip(runtime)
			if !known(runtime) {
				return nil, fmt.Errorf("unknown runtime %s; known: %s", pyRepr(runtime), sortedKnown())
			}
			if role == "*" || role == "all" {
				if runtime == "claude" {
					continue // explicit all-claude: the default already is Claude
				}
				for _, r := range cursorharness.SortedRoles() {
					out[r] = runtime
				}
				continue
			}
			if runtime != "claude" && !allowed(runtime)[role] {
				return nil, fmt.Errorf("runtime %s allow-list is %s; not %s", pyRepr(runtime),
					pyList(cursorharness.SortedRoles()), pyRepr(role))
			}
			out[role] = runtime
		}
	}
	for _, role := range strings.Split(cursorPilotRoles, ",") {
		if role = cursorharness.PyStrip(role); role == "" {
			continue
		}
		if !cursorharness.CursorRoles[role] {
			return nil, fmt.Errorf("cursor_pilot_roles: %s not in cursor allow-list (%s)", pyRepr(role),
				strings.Join(cursorharness.SortedRoles(), ", "))
		}
		if _, ok := out[role]; !ok {
			out[role] = "cursor"
		}
	}
	return out, nil
}

// ResolveRuntime is the runtime of one role ("claude" unless configured).
func ResolveRuntime(role, agentRuntimes, cursorPilotRoles string) (string, error) {
	m, err := ParseRuntimes(agentRuntimes, cursorPilotRoles)
	if err != nil {
		return "", err
	}
	if rt, ok := m[role]; ok {
		return rt, nil
	}
	return "claude", nil
}

// UUID4 is a random RFC 4122 version-4 id, the default NewUUID.
func UUID4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (c SpawnContext) uuid() (string, error) {
	if c.NewUUID != nil {
		return c.NewUUID(), nil
	}
	return UUID4()
}

// PlanSpawn builds the spawn plan of one runtime.
func PlanSpawn(runtime string, ctx SpawnContext) (SpawnPlan, error) {
	switch runtime {
	case "claude":
		return planClaude(ctx)
	case "cursor":
		return planCursor(ctx)
	}
	return SpawnPlan{}, fmt.Errorf("no planner for runtime %s", pyRepr(runtime))
}

// planClaude mirrors today's _spawn_start claude_args (Claude Code via herdr --kind claude).
func planClaude(ctx SpawnContext) (SpawnPlan, error) {
	argv := []string{"--model", ctx.Model, "--permission-mode", "auto", "--settings", ctx.SettingsPath}
	if ctx.AppendPromptFile != "" {
		argv = append(argv, "--append-system-prompt-file", ctx.AppendPromptFile)
	}
	argv = append(argv, ctx.Effort...)
	if !ctx.Evaluator {
		for _, d := range ctx.AddDirs {
			argv = append(argv, "--add-dir", d)
		}
	}
	argv = append(argv, "-n", ctx.Name)
	if ctx.Role != "boss" {
		argv = append(argv, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	}
	for _, p := range ctx.PluginDirs {
		argv = append(argv, "--plugin-dir", p)
	}
	sid := ctx.SessionID
	if ctx.Resume != "" {
		argv = append(argv, "--resume", ctx.Resume, "--system-prompt-snapshot", "off")
		sid = ctx.Resume
	} else {
		if sid == "" {
			u, err := ctx.uuid()
			if err != nil {
				return SpawnPlan{}, err
			}
			sid = u
		}
		argv = append(argv, "--session-id", sid)
		if ctx.Fresh {
			argv = append(argv, "--system-prompt-snapshot", "off")
		}
	}
	return SpawnPlan{Runtime: "claude", Argv: argv, HerdrKind: "claude", Launch: "herdr-kind", SessionID: sid,
		Workspace: ctx.Cwd}, nil
}

// planCursor is Cursor via herdr --kind cursor (same pane lifecycle as Claude).
func planCursor(ctx SpawnContext) (SpawnPlan, error) {
	if ctx.Evaluator || ctx.Role == "evaluator" {
		return SpawnPlan{}, errors.New("runtime cursor: evaluator/skilleval stays on Claude")
	}
	hq := cursorharness.HQRoles[ctx.Role]
	ws, err := cursorharness.PolicyWorkspace(ctx.Home, ctx.Name, ctx.Cwd, ctx.StateRoot)
	if err != nil {
		return SpawnPlan{}, err
	}
	prepared, err := cursorharness.MaterializeCursorPolicy(ws, ctx.Role, cursorharness.MaterializeOpts{
		HooksDir: ctx.HooksDir, Allow: ctx.Allow, Deny: ctx.Deny, SystemPrompt: ctx.PromptText, HQ: hq,
		FactoryHome: os.Getenv("FACTORY_HOME"),
	})
	if err != nil {
		return SpawnPlan{}, err
	}
	binary := cursorharness.FindAgentBinary()
	if binary == "" {
		return SpawnPlan{}, errors.New("cursor runtime: agent/cursor-agent not on PATH; " +
			"install from https://cursor.com/docs/cli/installation")
	}
	addDirs := append([]string(nil), ctx.AddDirs...)
	cwdR, wsR, homeR := absResolve(ctx.Cwd), absResolve(ws), absResolve(ctx.Home)
	if cwdR != wsR && !contains(addDirs, cwdR) {
		addDirs = append([]string{cwdR}, addDirs...)
	}
	if !contains(addDirs, homeR) && homeR != wsR {
		addDirs = append(addDirs, homeR)
	}
	argv := []string{"--workspace", wsR, "--model", cursorharness.MapModel(ctx.Model), "--trust", "--force"}
	if ctx.Sandbox {
		argv = append(argv, "--sandbox", "enabled")
	}
	if ctx.Resume != "" {
		argv = append(argv, "--resume", ctx.Resume)
	}
	for _, d := range ctx.PluginDirs {
		argv = append(argv, "--plugin-dir", d)
	}
	for _, d := range addDirs {
		argv = append(argv, "--add-dir", d)
	}
	sid := ctx.Resume
	if sid == "" {
		sid = ctx.SessionID
	}
	if sid == "" {
		if sid, err = ctx.uuid(); err != nil {
			return SpawnPlan{}, err
		}
	}
	notes := []string{"cursor uses herdr --kind cursor (same place/prompt/tick path as Claude)",
		"policy materialised into " + wsR}
	if ctx.Sandbox {
		notes = append(notes, "Cursor --sandbox enabled (Claude nsbox not used)")
	}
	if hq {
		notes = append(notes, "HQ role: adapter --hq")
	}
	return SpawnPlan{Runtime: "cursor", Argv: argv, HerdrKind: "cursor", Launch: "herdr-kind", Binary: binary,
		Notes: notes, Prepared: &prepared, SessionID: sid, Workspace: wsR}, nil
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func absResolve(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// LaunchSubprocess starts a non-herdr runtime; stdout and stderr go to logPath (append). The caller records the pid
// and must Wait on the returned command.
func LaunchSubprocess(plan SpawnPlan, cwd string, env []string, logPath string) (*exec.Cmd, error) {
	if len(plan.Argv) == 0 {
		return nil, errors.New("launch_subprocess: empty argv")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o777); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(logPath), err)
	}
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}
	cmd := exec.Command(plan.Argv[0], plan.Argv[1:]...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	// The child holds its own descriptor now; ours is closed whether or not Start succeeded.
	if cerr := log.Close(); cerr != nil && startErr == nil {
		return nil, fmt.Errorf("close %s: %w", logPath, cerr)
	}
	if startErr != nil {
		return nil, fmt.Errorf("start %s: %w", plan.Argv[0], startErr)
	}
	return cmd, nil
}

// WorksMatrix is the `factory cursor-pilot status` matrix with the registry row first.
func WorksMatrix() []cursorharness.Row {
	rows := cursorharness.WorksNeedsWork()
	return append([]cursorharness.Row{{Item: "Multi-runtime registry (claude default, cursor extension)", Status: "WORKS",
		Detail: "scout.agent_runtime.resolve_runtime / plan_spawn; *=cursor for whole factory"}}, rows...)
}
