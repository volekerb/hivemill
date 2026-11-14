// Package herdr is the Go client for the agent host: the herdr TUI binary and the herdr-core `hc`
// binary. It is a port of the herdr section of bin/factory_cli.py (herdr_socket, herdr_gate, herdr,
// herdr_start_error, herdr_agents, _agents_index, the _hc_* translators, close_named_pane,
// herdr_workspaces/tabs/panes). Same argv, same env, same timeout, same error strings.
//
// Every call is exec.Command with an argv slice; no shell is involved. The package has no caller on
// main yet; the tick and spawn ports wire it later.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout is Python's HERDR_TIMEOUT_S (per-call timeout of herdr()).
const DefaultTimeout = 90 * time.Second

// DefaultSocket is DEFAULT_CONFIG["herdr_socket"]; DeadSocket is what a worker in a non-live home gets.
const (
	DefaultSocket = "~/.config/herdr/herdr.sock"
	DeadSocket    = "/proc/nonexistent/worker-no-herdr.sock"
)

// socketEnv is the env var the TUI binary reads its socket from.
const socketEnv = "HERDR_SOCKET_PATH"

// Row is the part of an agents-table row the client reads (name, spawn_args JSON, pane_id).
type Row struct {
	Name      string
	SpawnArgs string
	PaneID    string
}

// Store is what the client needs from the factory DB. The caller supplies it (factorydb-backed in
// production, a map in tests), so this package has no DB dependency.
type Store interface {
	// Config returns the config value for key; ok=false when the key is unset (caller default applies).
	Config(key string) (val string, ok bool)
	// AgentRow returns the agents row for name, or nil when there is none.
	AgentRow(name string) *Row
	// Event records one events row (entity, id, action, detail).
	Event(entity, id, action, detail string)
}

// Agent is one host agent row, kept as the raw JSON object so no field is invented or dropped.
type Agent map[string]any

// Name returns the agent's name ("" when absent or not a string).
func (a Agent) Name() string { s, _ := a["name"].(string); return s }

// PaneID returns the agent's pane_id ("" when absent or not a string).
func (a Agent) PaneID() string { s, _ := a["pane_id"].(string); return s }

// Client talks to the host. Set Store before use.
type Client struct {
	Store   Store
	Timeout time.Duration // per call; 0 means DefaultTimeout
	// SpawnHostExe forces "hc" or "herdr" for every call (Python _SPAWN_HOST_EXE).
	SpawnHostExe string
	// Gate is herdr_gate(args): return a non-nil error to refuse the call. nil allows everything.
	Gate func(args []string) error
	// IsWorker and HomeLive feed the socket pin (herdr_socket): a worker in a non-live home gets
	// DeadSocket instead of the live socket. nil means "not a worker" / "home is live".
	IsWorker func() bool
	HomeLive func() bool
	// RunCwd/RunProgram/RunTail shape `hc run` for `agent start` (spawn ports set them; spawn scripts
	// are out of scope here). RunTail nil means [program, post...].
	RunCwd     string
	RunProgram string
	RunTail    func(program string, post []string) []string

	sock string
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) cfg(key, def string) string {
	if c.Store != nil {
		if v, ok := c.Store.Config(key); ok {
			return v
		}
	}
	return def
}

func (c *Client) event(entity, id, action, detail string) {
	if c.Store != nil {
		c.Store.Event(entity, id, action, detail)
	}
}

func (c *Client) row(name string) *Row {
	if c.Store == nil {
		return nil
	}
	return c.Store.AgentRow(name)
}

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return h + p[1:]
	}
	return p
}

func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	// Non-strict like Python Path.resolve(): resolve the longest existing prefix, keep the rest.
	rest := ""
	for cur := abs; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// isLiveSocket: val names the live default by any spelling Socket would hand over.
func isLiveSocket(val string) bool {
	if val == "" {
		return false
	}
	if val == DefaultSocket {
		return true
	}
	realHome := ""
	if u, err := user.Current(); err == nil {
		realHome = u.HomeDir
	}
	live := resolve(realHome + DefaultSocket[1:])
	cands := []string{resolve(expandUser(val))}
	if val == "~" || strings.HasPrefix(val, "~/") {
		cands = append(cands, resolve(realHome+val[1:]))
	}
	for _, x := range cands {
		if x == live {
			return true
		}
	}
	return false
}

// Socket is herdr_socket(): config herdr_socket (default ~/.config/herdr/herdr.sock), pinned per
// client; a worker in a non-live home never falls back to the live socket.
func (c *Client) Socket() string {
	if c.sock != "" {
		return c.sock
	}
	worker := c.IsWorker != nil && c.IsWorker()
	live := c.HomeLive == nil || c.HomeLive()
	val := c.cfg("herdr_socket", DefaultSocket)
	if isLiveSocket(val) && !live && worker {
		val = DeadSocket
	}
	if val == "" {
		val = DefaultSocket
	}
	c.sock = expandUser(val)
	return c.sock
}

// TerminalBackendConfig is terminal_backend_config: auto | herdr | hc, invalid falls back to auto.
func (c *Client) TerminalBackendConfig() string {
	v := strings.ToLower(strings.TrimSpace(c.cfg("terminal_backend", "auto")))
	if v == "herdr" || v == "hc" {
		return v
	}
	return "auto"
}

// TerminalBackendEffective is terminal_backend_effective: (label, host binary).
func (c *Client) TerminalBackendEffective() (label, exe string) {
	switch c.TerminalBackendConfig() {
	case "herdr":
		return "herdr", "herdr"
	case "hc":
		return "hc", "hc"
	}
	_, hErr := exec.LookPath("herdr")
	_, hcErr := exec.LookPath("hc")
	if hErr == nil {
		return "auto→herdr", "herdr"
	}
	if hcErr == nil {
		return "auto→hc", "hc"
	}
	return "auto→herdr", "herdr"
}

func spawnArgs(row *Row) map[string]any {
	m := map[string]any{}
	if row == nil || row.SpawnArgs == "" {
		return m
	}
	if err := json.Unmarshal([]byte(row.SpawnArgs), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func rowHostedOnHC(row *Row) bool {
	if hb, _ := spawnArgs(row)["host_backend"].(string); hb == "hc" {
		return true
	}
	return row.PaneID == row.Name
}

// agentHostExe is _agent_host_exe: per-spawn host_backend beats config terminal_backend.
func (c *Client) agentHostExe(row *Row) string {
	if row == nil {
		_, e := c.TerminalBackendEffective()
		return e
	}
	if hb, _ := spawnArgs(row)["host_backend"].(string); hb == "hc" || hb == "herdr" {
		return hb
	}
	if rowHostedOnHC(row) {
		return "hc"
	}
	_, e := c.TerminalBackendEffective()
	return e
}

func (c *Client) rowNeedsTUIIPC(row *Row) bool {
	if row == nil {
		return false
	}
	_, eff := c.TerminalBackendEffective()
	return c.agentHostExe(row) == "herdr" && eff == "hc"
}

// hostAgentIPCExe is _host_agent_ipc_exe: agent-scoped verbs follow the row's host_backend.
func (c *Client) hostAgentIPCExe(args []string) string {
	if c.SpawnHostExe == "hc" || c.SpawnHostExe == "herdr" {
		return c.SpawnHostExe
	}
	_, eff := c.TerminalBackendEffective()
	if len(args) >= 3 && args[0] == "agent" {
		switch args[1] {
		case "prompt", "read", "send-keys", "wait", "start":
			if r := c.row(args[2]); r != nil {
				return c.agentHostExe(r)
			}
		}
	}
	return eff
}

func (c *Client) hostIPCEnv(exe string) []string {
	env := os.Environ()
	if exe == "hc" {
		return env // hc uses its own HC_SOCKET; never alias the two
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, socketEnv+"=") {
			out = append(out, kv)
		}
	}
	return append(out, socketEnv+"="+c.Socket())
}

// --- hc translation -------------------------------------------------------------------------

func hcStatusWord(t any) string {
	m, ok := t.(map[string]any)
	if !ok {
		return "unknown"
	}
	switch st := m["status"].(type) {
	case nil:
		return "unknown"
	case map[string]any:
		state, _ := st["state"].(string)
		switch state {
		case "idle", "working", "done", "blocked", "error":
			return state
		}
		return "unknown"
	case string:
		switch st {
		case "idle", "working", "done", "blocked", "error", "unknown":
			return st
		}
		return "unknown"
	}
	return "unknown"
}

func hcTerminalToAgentRow(t any) any {
	m, ok := t.(map[string]any)
	if !ok {
		return t
	}
	row := map[string]any{}
	if v := m["name"]; v != nil {
		row["name"] = v
	}
	if v := m["agent"]; v != nil {
		row["agent"] = v
	}
	word := hcStatusWord(m)
	if _, has := m["status"]; has || word != "unknown" {
		row["agent_status"] = word
	}
	return row
}

// hcUnwrapResult returns nil (Python None) when the payload has no known shape.
func hcUnwrapResult(ipcArgv []string, payload any) map[string]any {
	p, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	cmd := ""
	if len(ipcArgv) > 0 {
		cmd = ipcArgv[0]
	}
	switch cmd {
	case "ls":
		terms, ok := p["terminals"].([]any)
		if !ok {
			return nil
		}
		rows := make([]any, 0, len(terms))
		for _, t := range terms {
			rows = append(rows, hcTerminalToAgentRow(t))
		}
		return map[string]any{"agents": rows}
	case "read":
		if v, has := p["text"]; has {
			return map[string]any{"text": v}
		}
		return nil
	case "run":
		if _, has := p["state"]; p["name"] == nil && !has {
			return nil
		}
		return map[string]any{"agent": hcTerminalToAgentRow(p)}
	case "send", "keys":
		return map[string]any{}
	}
	return nil
}

func lastLine(out string) string {
	if out == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	return lines[len(lines)-1]
}

func hcParseHostStdout(ipcArgv []string, out string) map[string]any {
	if len(ipcArgv) > 0 && ipcArgv[0] == "read" {
		return map[string]any{"text": out}
	}
	line := lastLine(out)
	if line == "" {
		return map[string]any{}
	}
	var payload any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return map[string]any{"raw": out}
	}
	if parsed := hcUnwrapResult(ipcArgv, payload); parsed != nil {
		return parsed
	}
	return map[string]any{"raw": out}
}

// HcTranslate is _hc_translate_args: map a TUI herdr verb to the hc argv; ok=false means "no hc
// equivalent" (Python None).
func (c *Client) HcTranslate(args []string) (out []string, ok bool) {
	a := args
	if len(a) == 0 {
		return nil, false
	}
	if a[0] == "agent" && len(a) >= 2 {
		switch verb := a[1]; {
		case verb == "list":
			return []string{"ls"}, true
		case verb == "start" && len(a) >= 3:
			name, kind := a[2], "claude"
			var post []string
			for i := 3; i < len(a); {
				switch {
				case a[i] == "--kind" && i+1 < len(a):
					kind = a[i+1]
					i += 2
				case (a[i] == "--pane" || a[i] == "--timeout") && i+1 < len(a):
					i += 2
				case a[i] == "--":
					post = append([]string{}, a[i+1:]...)
					i = len(a)
				default:
					i++
				}
			}
			out = []string{"run", "--bg", "--name", name}
			cwd := c.RunCwd
			if cwd == "" {
				for j, tok := range post {
					if tok == "--workspace" && j+1 < len(post) {
						cwd = post[j+1]
						break
					}
				}
			}
			if cwd != "" {
				out = append(out, "--cwd", cwd)
			}
			program := c.RunProgram
			if program == "" {
				program = kind
			}
			if c.RunTail != nil {
				out = append(out, c.RunTail(program, post)...)
			} else {
				out = append(out, program)
				out = append(out, post...)
			}
			return out, true
		case verb == "prompt" && len(a) >= 4:
			return []string{"send", a[2], a[3]}, true
		case verb == "read" && len(a) >= 3:
			out = []string{"read", a[2]}
			for i := 3; i < len(a); {
				if a[i] == "--lines" && i+1 < len(a) {
					out = append(out, "--lines", a[i+1])
					i += 2
				} else {
					i++
				}
			}
			return out, true
		case verb == "send-keys" && len(a) >= 3:
			return append([]string{"keys", a[2]}, a[3:]...), true
		}
	}
	return nil, false
}

func (c *Client) hostIPCArgv(exe string, args []string) ([]string, bool) {
	if exe != "hc" {
		return append([]string{}, args...), true
	}
	return c.HcTranslate(args)
}

// --- exec -----------------------------------------------------------------------------------

func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func pySeconds(d time.Duration) string {
	s := d.Seconds()
	if s == float64(int64(s)) {
		return strconv.FormatInt(int64(s), 10)
	}
	return strconv.FormatFloat(s, 'g', -1, 64)
}

// timeoutText is str(subprocess.TimeoutExpired): "Command '['herdr', 'agent', 'list']' timed out after 90 seconds".
func timeoutText(argv []string, d time.Duration) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = pyRepr(a)
	}
	return fmt.Sprintf("Command '[%s]' timed out after %s seconds", strings.Join(parts, ", "), pySeconds(d))
}

// notFoundText is str(FileNotFoundError) for a missing executable.
func notFoundText(exe string) string {
	return fmt.Sprintf("[Errno 2] No such file or directory: %s", pyRepr(exe))
}

type runOut struct {
	stdout, stderr string
	code           int
	timedOut       bool
	notFound       bool
}

func (c *Client) run(exe string, argv []string, d time.Duration) runOut {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.Env = c.hostIPCEnv(exe)
	cmd.WaitDelay = time.Second // a grandchild holding the pipes must not hang the call
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	r := runOut{stdout: so.String(), stderr: se.String()}
	var ee *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		r.timedOut = true
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	case err != nil:
		r.notFound = true // not on PATH / cannot start: the OSError path in Python
	}
	return r
}

// parseResult turns stdout into the result factory expects (TUI: the last line's "result" field).
func parseResult(exe string, ipcArgv []string, stdout string) any {
	out := strings.TrimSpace(stdout)
	if exe == "hc" {
		return hcParseHostStdout(ipcArgv, out)
	}
	if out == "" {
		return map[string]any{}
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(lastLine(out)), &env); err != nil {
		return map[string]any{"raw": out}
	}
	res, ok := env["result"]
	if !ok {
		return map[string]any{"raw": out}
	}
	return res
}

func joinN(args []string, n int) string {
	if len(args) > n {
		args = args[:n]
	}
	return strings.Join(args, " ")
}

func (c *Client) gate(args []string) error {
	if c.Gate != nil {
		return c.Gate(args)
	}
	return nil
}

// call is the shared body of herdr()/_host_ipc_result(). A nil result with nil error is Python None.
func (c *Client) call(exe string, args []string, check bool) (any, error) {
	ipcArgv, ok := c.hostIPCArgv(exe, args)
	if !ok {
		if check {
			return nil, fmt.Errorf("hc has no equivalent for herdr %s", joinN(args, 3))
		}
		return nil, nil
	}
	r := c.run(exe, ipcArgv, c.timeout())
	if r.timedOut || r.notFound {
		if check {
			why := timeoutText(append([]string{exe}, ipcArgv...), c.timeout())
			if r.notFound {
				why = notFoundText(exe)
			}
			return nil, fmt.Errorf("%s %s failed: %s", exe, joinN(args, 2), why)
		}
		return nil, nil
	}
	if r.code != 0 {
		if check {
			msg := strings.TrimSpace(r.stderr)
			if msg == "" {
				msg = strings.TrimSpace(r.stdout)
			}
			return nil, fmt.Errorf("%s %s failed: %s", exe, joinN(args, 3), msg)
		}
		return nil, nil
	}
	return parseResult(exe, ipcArgv, r.stdout), nil
}

// Herdr is herdr(*args, check=...): run one call on the binary chosen for args. With check=false a
// failure yields (nil, nil) (Python None); with check=true it yields an error.
func (c *Client) Herdr(check bool, args ...string) (any, error) {
	if err := c.gate(args); err != nil {
		return nil, err
	}
	return c.call(c.hostAgentIPCExe(args), args, check)
}

// HostIPCResult is _host_ipc_result: one call on a named binary, ignoring config terminal_backend.
func (c *Client) HostIPCResult(exe string, check bool, args ...string) (any, error) {
	if err := c.gate(args); err != nil {
		return nil, err
	}
	return c.call(exe, args, check)
}

// StartError is herdr_start_error: (result, errText). errText is "" on success.
func (c *Client) StartError(args ...string) (any, string) {
	if err := c.gate(args); err != nil {
		return nil, err.Error()
	}
	exe := c.hostAgentIPCExe(args)
	ipcArgv, ok := c.hostIPCArgv(exe, args)
	if !ok {
		return nil, "hc has no equivalent for herdr " + joinN(args, 3)
	}
	r := c.run(exe, ipcArgv, c.timeout())
	switch {
	case r.timedOut:
		return nil, "timed out: " + timeoutText(append([]string{exe}, ipcArgv...), c.timeout())
	case r.notFound:
		return nil, exe + " not found: " + notFoundText(exe)
	case r.code != 0:
		msg := strings.TrimSpace(r.stderr)
		if msg == "" {
			msg = strings.TrimSpace(r.stdout)
		}
		if msg == "" {
			msg = fmt.Sprintf("exit %d", r.code)
		}
		return nil, msg
	}
	return parseResult(exe, ipcArgv, r.stdout), ""
}

// --- agents ---------------------------------------------------------------------------------

func normalizeAgentRow(a any) Agent {
	m, ok := a.(map[string]any)
	if !ok {
		return nil
	}
	// Python replaces only None and "" (any other value is kept as is).
	if st, isStr := m["agent_status"].(string); m["agent_status"] == nil || (isStr && st == "") {
		cp := make(Agent, len(m)+1)
		for k, v := range m {
			cp[k] = v
		}
		cp["agent_status"] = "unknown"
		return cp
	}
	return Agent(m)
}

// agentsFromResult: nil means "unknown shape" (Python None); a non-nil empty slice means the host
// answered with zero agents.
func agentsFromResult(r any) []Agent {
	m, ok := r.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := m["agents"].([]any)
	if !ok {
		return nil
	}
	out := make([]Agent, 0, len(list))
	for _, a := range list {
		if n := normalizeAgentRow(a); n != nil {
			out = append(out, n)
		}
	}
	return out
}

// ErrUnreachable is returned by Agents when the host cannot be asked or answers in an unknown shape.
var ErrUnreachable = errors.New("herdr unreachable: agent list failed or had an unknown shape")

// Agents is herdr_agents(). On failure it returns (nil, ErrUnreachable): a nil list is Python's None
// and must never be treated as "no agents". An empty non-nil list with a nil error means zero agents.
func (c *Client) Agents() ([]Agent, error) {
	r, err := c.Herdr(false, "agent", "list")
	if err != nil {
		return nil, err
	}
	if l := agentsFromResult(r); l != nil {
		return l, nil
	}
	return nil, ErrUnreachable
}

// HostAgents is _host_agents_list(exe): list one named host binary ("herdr" or "hc"), fail-soft.
func (c *Client) HostAgents(exe string) ([]Agent, error) {
	args := []string{"agent", "list"}
	if err := c.gate(args); err != nil {
		return nil, err
	}
	ipcArgv, ok := c.hostIPCArgv(exe, args)
	if !ok {
		return nil, ErrUnreachable
	}
	r := c.run(exe, ipcArgv, c.timeout())
	if r.timedOut || r.notFound || r.code != 0 {
		return nil, ErrUnreachable
	}
	if l := agentsFromResult(parseResult(exe, ipcArgv, r.stdout)); l != nil {
		return l, nil
	}
	return nil, ErrUnreachable
}

// Index is _agents_index: name map and pane map; rows without a name / pane_id are skipped.
func Index(agents []Agent) (byName, byPane map[string]Agent) {
	byName, byPane = map[string]Agent{}, map[string]Agent{}
	for _, a := range agents {
		if n := a.Name(); n != "" {
			byName[n] = a
		}
		if p := a.PaneID(); p != "" {
			byPane[p] = a
		}
	}
	return byName, byPane
}

func (c *Client) listedForExe(rowExe string, row *Row) ([]Agent, error) {
	_, eff := c.TerminalBackendEffective()
	switch rowExe {
	case "hc":
		return c.HostAgents("hc")
	case "herdr":
		if c.rowNeedsTUIIPC(row) || eff == "hc" {
			return c.HostAgents("herdr")
		}
	}
	return c.Agents()
}

// --- close / kill ---------------------------------------------------------------------------

// ClosePaneByID is _herdr_pane_close with a fail-soft close (check=false).
func (c *Client) ClosePaneByID(row *Row, paneID string) error {
	if c.rowNeedsTUIIPC(row) {
		_, err := c.HostIPCResult("herdr", false, "pane", "close", paneID)
		return err
	}
	_, err := c.Herdr(false, "pane", "close", paneID)
	return err
}

// KillTerminal is _hc_kill_terminal: "killed", "not-found" or "unknown" (caller retries). listed may
// be nil to list hc here.
func (c *Client) KillTerminal(name string, listed []Agent) string {
	if row := c.row(name); row != nil && c.agentHostExe(row) != "hc" {
		return "unknown"
	}
	if listed == nil {
		l, err := c.HostAgents("hc")
		if err != nil {
			return "unknown"
		}
		listed = l
	}
	found := false
	for _, h := range listed {
		if h.Name() == name {
			found = true
			break
		}
	}
	if !found {
		return "not-found"
	}
	r := c.run("hc", []string{"kill", name}, c.timeout())
	if r.timedOut || r.notFound || r.code != 0 {
		return "unknown"
	}
	c.event("agent", name, "hc-kill", name)
	return "killed"
}

func (c *Client) skipped(name, pane string, listed []Agent) {
	other := ""
	if pane != "" {
		for _, x := range listed {
			if x.PaneID() == pane {
				other = x.Name()
				break
			}
		}
	}
	reason := "no host pane under this name"
	if other != "" {
		reason = "pane held by " + other
	}
	d, _ := json.Marshal(map[string]string{"agent": name, "pane": pane, "reason": reason})
	c.event("agent", name, "pane-close-skipped", string(d))
}

// ClosePane is close_named_pane: close the pane the host lists under this exact agent name.
// state is "closed", "not-found" or "unknown"; hostPane is the id (or hc name) that was closed.
func (c *Client) ClosePane(name, pane string) (state, hostPane string) {
	row := c.row(name)
	var rowExe string
	if row != nil {
		rowExe = c.agentHostExe(row)
	} else {
		_, rowExe = c.TerminalBackendEffective()
	}
	listed, err := c.listedForExe(rowExe, row)
	if err != nil {
		return "unknown", ""
	}
	if rowExe == "hc" {
		found := false
		for _, x := range listed {
			if x.Name() == name {
				found = true
				break
			}
		}
		if !found {
			c.skipped(name, pane, listed)
			return "not-found", ""
		}
		switch c.KillTerminal(name, nil) {
		case "killed":
			return "closed", name
		case "unknown":
			return "unknown", ""
		}
		return "not-found", ""
	}
	for _, x := range listed {
		if x.Name() == name && x.PaneID() != "" {
			_ = c.ClosePaneByID(row, x.PaneID())
			return "closed", x.PaneID()
		}
	}
	c.skipped(name, pane, listed)
	return "not-found", ""
}

// --- workspaces / tabs / panes --------------------------------------------------------------

func (c *Client) listKey(key string, args ...string) []any {
	r, _ := c.Herdr(false, args...)
	m, _ := r.(map[string]any)
	l, _ := m[key].([]any)
	return l
}

func toMaps(l []any) []map[string]any {
	out := make([]map[string]any, 0, len(l))
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Workspaces is herdr_workspaces(): workspace_id -> workspace.
func (c *Client) Workspaces() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, w := range toMaps(c.listKey("workspaces", "workspace", "list")) {
		if id, ok := w["workspace_id"].(string); ok {
			out[id] = w
		}
	}
	return out
}

// Tabs is herdr_tabs(ws).
func (c *Client) Tabs(ws string) []map[string]any {
	return toMaps(c.listKey("tabs", "tab", "list", "--workspace", ws))
}

// Panes is herdr_panes(ws).
func (c *Client) Panes(ws string) []map[string]any {
	return toMaps(c.listKey("panes", "pane", "list", "--workspace", ws))
}

// --- test-stub detection ---------------------------------------------------------------------

func underStubs(p string) bool { return strings.Contains("/"+p, "/tests/stubs/") }

// IsTestStub is herdr_is_test_stub: `herdr` on PATH is a tests/stubs file that is not the real herdr.
func IsTestStub() bool {
	found, err := exec.LookPath("herdr")
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(found)
	if err != nil {
		real = found
	}
	if !underStubs(real) {
		return false
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == "" {
			d = "."
		}
		cand := filepath.Join(d, "herdr")
		fi, err := os.Stat(cand)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		cr, err := filepath.EvalSymlinks(cand)
		if err != nil {
			cr = cand
		}
		if !underStubs(cr) {
			if rfi, err := os.Stat(real); err == nil && os.SameFile(fi, rfi) {
				return false
			}
			break // the first real herdr on PATH is a different file
		}
	}
	return true
}
