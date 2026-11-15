package main

// The stateful herdr mode: a port of tests/stubs/herdr. State lives in the JSON file $FAKE_HERDR_STATE; a call reads
// it, handles the command, and writes it back (no lock, like the Python stub: tests run calls one at a time).
// It is on when $FAKE_HERDR_MODE is "stateful" (factorytest.Env.HerdrStateful); otherwise herdr is scripted.

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// stdout is where answers go; tests swap it.
var stdout io.Writer = os.Stdout

// crash stands for an uncaught Python exception in the original stub (IndexError, KeyError, ...): the Python stub
// exits 1 with a traceback, this one exits 1 with the reason.
type crash struct{ msg string }

func crashf(format string, a ...any) { panic(crash{fmt.Sprintf(format, a...)}) }

// herdrStateful answers one herdr call and returns its exit code. It runs the FAKE_HERDR_HOOK first.
func herdrStateful(argv []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			c, ok := r.(crash)
			if !ok {
				panic(r)
			}
			fmt.Fprintf(os.Stderr, "herdr stub: %s (the Python stub raises here); fix the test state or call\n", c.msg)
			code = 1
		}
	}()
	argv = wtf8Args(argv)
	statePath := os.Getenv("FAKE_HERDR_STATE")
	if statePath == "" {
		fmt.Fprintln(os.Stderr, "herdr stub: FAKE_HERDR_STATE is not set; run the tool through factorytest.Env")
		return 2
	}
	if err := runHook(statePath, argv); err != nil {
		fmt.Fprintf(os.Stderr, "herdr stub: %v\n", err)
		return 1
	}
	st, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "herdr stub: %v\n", err)
		return 1
	}
	h := &herdr{argv: argv, st: st}
	code, write := h.handle()
	if write {
		if err := os.WriteFile(statePath, []byte(dumps(st)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "herdr stub: cannot write %s: %v; fix the test home\n", statePath, err)
			return 1
		}
	}
	return code
}

// wtf8Args maps bytes that are not UTF-8 to U+DC80..U+DCFF in WTF-8, which is what Python's argv decoding
// (surrogateescape) gives them.
func wtf8Args(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if utf8.ValidString(a) {
			out[i] = a
			continue
		}
		var b strings.Builder
		for j := 0; j < len(a); {
			r, size := utf8.DecodeRuneInString(a[j:])
			if r == utf8.RuneError && size == 1 {
				writeWTF8(&b, 0xdc00+rune(a[j]))
			} else {
				b.WriteString(a[j : j+size])
			}
			j += size
		}
		out[i] = b.String()
	}
	return out
}

// pySplit is str.split() with no argument: runs of Python whitespace separate the words.
func pySplit(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
}

// runHook is the test hook of the Python stub: FAKE_HERDR_HOOK is a shell command run once, on the first call (or on
// the first call whose argv starts with the words of FAKE_HERDR_HOOK_AT), before the call is handled. The marker
// file STATE.hook-ran is written before the hook runs, so a herdr call the hook itself makes does not run it again.
// The hook has no timeout (as in Python); its output goes to this process's stdout and stderr.
func runHook(statePath string, argv []string) error {
	hook := os.Getenv("FAKE_HERDR_HOOK")
	if hook == "" {
		return nil
	}
	once := statePath + ".hook-ran"
	if _, err := os.Stat(once); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot stat %s: %w; fix the test home", once, err)
	}
	if at := os.Getenv("FAKE_HERDR_HOOK_AT"); at != "" {
		words := pySplit(at)
		n := len(words)
		if n > len(argv) {
			n = len(argv)
		}
		if strings.Join(argv[:n], " ") != at {
			return nil
		}
	}
	if skip := os.Getenv("FAKE_HERDR_HOOK_SKIP"); skip != "" { // Go-only extension: let the first N matching calls pass
		n, err := strconv.Atoi(skip)
		if err != nil || n < 0 {
			return fmt.Errorf("FAKE_HERDR_HOOK_SKIP=%q is not a count >= 0; use Env.HerdrHookAfter", skip)
		}
		seen := statePath + ".hook-seen"
		count := 0
		if b, err := os.ReadFile(seen); err == nil {
			if count, err = strconv.Atoi(strings.TrimSpace(string(b))); err != nil {
				return fmt.Errorf("%s does not hold a count: %w; delete it", seen, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot read %s: %w; fix the test home", seen, err)
		}
		if count < n {
			if err := os.WriteFile(seen, []byte(strconv.Itoa(count+1)), 0o644); err != nil {
				return fmt.Errorf("cannot write %s: %w; fix the test home", seen, err)
			}
			return nil
		}
	}
	f, err := os.Create(once)
	if err != nil {
		return fmt.Errorf("cannot create %s: %w; fix the test home", once, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close %s: %w", once, err)
	}
	cmd := exec.Command("/bin/sh", "-c", hook)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) { // check=False: a failing hook is ignored, a hook that cannot start is not
			return fmt.Errorf("cannot run FAKE_HERDR_HOOK: %w", err)
		}
	}
	return nil
}

// loadState reads the state file: a missing, empty or blank file is an empty state. Missing top-level keys get
// their defaults (setdefault, appended in the Python order).
func loadState(path string) (*obj, error) {
	b, err := os.ReadFile(path)
	text := string(b)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot read %s: %w; fix the test home", path, err)
	}
	var st *obj
	if strings.Trim(text, " \t\n\r") == "" {
		st = newObj()
	} else {
		v, err := parseJSON(text)
		if err != nil {
			return nil, fmt.Errorf("%s is not JSON: %w; rewrite it with Env.SetHerdr", path, err)
		}
		o, ok := v.(*obj)
		if !ok {
			return nil, fmt.Errorf("%s holds %s, not a JSON object; rewrite it with Env.SetHerdr", path, dumps(v))
		}
		st = o
	}
	for _, d := range []struct {
		k string
		v any
	}{{"fail", false}, {"seq", intOf(0)}, {"agents", []any{}}, {"workspaces", []any{}}, {"tabs", []any{}}, {"panes", []any{}}} {
		if _, ok := st.get(d.k); !ok {
			st.set(d.k, d.v)
		}
	}
	return st, nil
}

type herdr struct {
	argv []string
	st   *obj
}

// truthy is Python's bool(v).
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case pyInt:
		return x.v.Sign() != 0
	case pyFloat:
		return float64(x) != 0
	case []any:
		return len(x) > 0
	case *obj:
		return len(x.keys) > 0
	}
	return true
}

func (h *herdr) list(key string) []any {
	v, _ := h.st.get(key)
	l, ok := v.([]any)
	if !ok {
		crashf("state %q is %s, not a list", key, dumps(v))
	}
	return l
}

func asObj(v any, what string) *obj {
	o, ok := v.(*obj)
	if !ok {
		crashf("%s is %s, not an object", what, dumps(v))
	}
	return o
}

// opt is the value after the first occurrence of name in argv, if any; a trailing name is Python's IndexError.
func (h *herdr) opt(name string) (string, bool) {
	for i, a := range h.argv {
		if a == name {
			if i+1 >= len(h.argv) {
				crashf("%s has no value", name)
			}
			return h.argv[i+1], true
		}
	}
	return "", false
}

// optv is opt as a JSON value (null when absent).
func (h *herdr) optv(name string) any {
	if v, ok := h.opt(name); ok {
		return v
	}
	return nil
}

func (h *herdr) arg(i int) string {
	if i >= len(h.argv) {
		crashf("argv has no element %d", i)
	}
	return h.argv[i]
}

func (h *herdr) newID(prefix string) string {
	v, _ := h.st.get("seq")
	n, ok := v.(pyInt)
	if !ok {
		crashf("state seq is %s, not an int", dumps(v))
	}
	n = pyInt{new(big.Int).Add(n.v, big.NewInt(1))}
	h.st.set("seq", n)
	return prefix + n.v.String()
}

func out(result any) {
	fmt.Fprintln(stdout, dumps(kv("id", "fake", "result", result)))
}

func (h *herdr) fail(code int, msg string) (int, bool) {
	fmt.Fprintln(os.Stderr, msg)
	return code, false
}

func (h *herdr) newTab(ws, label any) (*obj, *obj) {
	tab := kv("tab_id", h.newID("t"), "workspace_id", ws, "label", label, "pane_count", intOf(1))
	tid, _ := tab.get("tab_id")
	pane := kv("pane_id", h.newID("p"), "tab_id", tid, "workspace_id", ws)
	h.st.set("tabs", append(h.list("tabs"), tab))
	h.st.set("panes", append(h.list("panes"), pane))
	return tab, pane
}

// pyEq is Python == for the values the state can hold (an int equals a float of the same value, bool is an int).
func pyEq(a, b any) bool {
	toNum := func(v any) (*big.Float, bool) {
		switch x := v.(type) {
		case bool:
			if x {
				return big.NewFloat(1), true
			}
			return big.NewFloat(0), true
		case pyInt:
			return new(big.Float).SetInt(x.v), true
		case pyFloat:
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return nil, false
			}
			return big.NewFloat(float64(x)), true
		}
		return nil, false
	}
	if x, ok := toNum(a); ok {
		if y, ok := toNum(b); ok {
			return x.Cmp(y) == 0
		}
		return false
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case pyFloat:
		y, ok := b.(pyFloat)
		return ok && float64(x) == float64(y) // NaN and infinities
	}
	return false
}

// field is item["key"] of a list element; the Python stub raises KeyError/TypeError when it is not there.
func field(item any, key, where string) any {
	o := asObj(item, where)
	v, ok := o.get(key)
	if !ok {
		crashf("%s has no key %q", where, key)
	}
	return v
}

// getField is item.get(key) (None when absent).
func getField(item any, key, where string) any {
	v, _ := asObj(item, where).get(key)
	return v
}

func (h *herdr) handle() (int, bool) {
	st, argv := h.st, h.argv
	if f, _ := st.get("fail"); truthy(f) {
		return h.fail(1, "herdr: server not running")
	}
	cmd := strings.Join(argv[:min(2, len(argv))], " ")
	if fc, ok := st.get("fail_cmds"); ok {
		hit := false
		switch x := fc.(type) {
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok && s == cmd {
					hit = true
				}
			}
		case string:
			hit = strings.Contains(x, cmd)
		case *obj:
			_, hit = x.get(cmd)
		default:
			crashf("state fail_cmds is %s, not a list", dumps(fc))
		}
		if hit {
			return h.fail(1, "herdr: "+cmd+" failed")
		}
	}
	two := len(argv) >= 2
	is := func(a, b string) bool { return two && argv[0] == a && argv[1] == b }
	switch {
	case is("agent", "list"):
		if v, ok := st.get("agent_list_ok"); ok {
			var left any
			var neg bool
			switch x := v.(type) {
			case pyInt:
				n := new(big.Int).Sub(x.v, big.NewInt(1))
				left, neg = pyInt{n}, n.Sign() < 0
			case pyFloat:
				left, neg = pyFloat(float64(x)-1), float64(x)-1 < 0
			case bool:
				n := big.NewInt(-1)
				if x {
					n = big.NewInt(0)
				}
				left, neg = pyInt{n}, n.Sign() < 0
			default:
				crashf("state agent_list_ok is %s, not a number", dumps(v))
			}
			st.set("agent_list_ok", left)
			if neg {
				fmt.Fprintln(os.Stderr, "herdr: agent list failed")
				return 1, true // the decremented counter is saved first, as in Python
			}
		}
		a, _ := st.get("agents")
		out(kv("agents", a))
	case is("agent", "start") && h.optv("--timeout") != nil && !timeoutOK(h.optv("--timeout").(string)):
		return h.fail(1, "herdr: invalid_agent_timeout: timeout_ms must be greater than 3000 and at most 300000")
	case is("agent", "start") && truthyKey(st, "start_fail"):
		sf, _ := st.get("start_fail")
		msg := "agent_not_ready"
		if s, ok := sf.(string); ok {
			msg = s
		}
		return h.fail(1, "herdr: "+msg)
	case is("agent", "start"):
		if ss, _ := st.get("start_sleep"); truthy(ss) && h.optv("--timeout") == "90000" {
			sleepPy(ss)
		}
		name := h.arg(2)
		sess, _ := h.opt("--session-id")
		if sess == "" {
			sess = "sess-" + name
		}
		agent := kv("name", name, "agent", "claude", "pane_id", h.optv("--pane"), "agent_status", "idle",
			"agent_session", kv("value", sess))
		if !truthyKey(st, "slow_list") {
			kept := []any{}
			for _, a := range h.list("agents") {
				if !pyEq(getField(a, "name", "agents item"), name) {
					kept = append(kept, a)
				}
			}
			st.set("agents", append(kept, agent))
		}
		out(kv("agent", agent))
	case is("workspace", "list"):
		w, _ := st.get("workspaces")
		out(kv("workspaces", w))
	case is("workspace", "create"):
		ws := kv("workspace_id", h.newID("w"), "label", h.optv("--label"))
		st.set("workspaces", append(h.list("workspaces"), ws))
		wid, _ := ws.get("workspace_id")
		tab, pane := h.newTab(wid, "1")
		out(kv("workspace", ws, "tab", tab, "root_pane", pane))
	case is("tab", "list"):
		want := h.optv("--workspace")
		tabs := []any{}
		for _, t := range h.list("tabs") {
			if pyEq(field(t, "workspace_id", "tabs item"), want) {
				tabs = append(tabs, t)
			}
		}
		out(kv("tabs", tabs))
	case is("tab", "create"):
		tab, pane := h.newTab(h.optv("--workspace"), h.optv("--label"))
		out(kv("tab", tab, "root_pane", pane))
	case is("tab", "rename"):
		id, label := h.arg(2), h.arg(3)
		for _, t := range h.list("tabs") {
			if pyEq(field(t, "tab_id", "tabs item"), id) {
				asObj(t, "tabs item").set("label", label)
			}
		}
		out(newObj())
	case is("pane", "list"):
		ws, have := h.opt("--workspace")
		panes := []any{}
		for _, p := range h.list("panes") {
			if !have || pyEq(field(p, "workspace_id", "panes item"), ws) {
				panes = append(panes, p)
			}
		}
		out(kv("panes", panes))
	case is("pane", "split"):
		anchorID := h.arg(2)
		var anchor any
		for _, p := range h.list("panes") {
			if pyEq(field(p, "pane_id", "panes item"), anchorID) {
				anchor = p
				break
			}
		}
		if anchor == nil {
			crashf("pane split: no pane %q (StopIteration)", anchorID)
		}
		tid := field(anchor, "tab_id", "pane")
		pane := kv("pane_id", h.newID("p"), "tab_id", tid, "workspace_id", field(anchor, "workspace_id", "pane"))
		st.set("panes", append(h.list("panes"), pane))
		for _, t := range h.list("tabs") {
			if pyEq(field(t, "tab_id", "tabs item"), tid) {
				o := asObj(t, "tabs item")
				pc, _ := o.get("pane_count")
				n, ok := pc.(pyInt)
				if !ok {
					crashf("tab pane_count is %s, not an int", dumps(pc))
				}
				o.set("pane_count", pyInt{new(big.Int).Add(n.v, big.NewInt(1))})
			}
		}
		out(kv("pane", pane))
	case is("pane", "process-info"):
		return h.processInfo()
	case is("pane", "close") && truthyKey(st, "pane_close_fail"):
		return h.fail(1, "herdr: pane close failed")
	case is("pane", "close") && truthyKey(st, "pane_close_noop"):
		out(newObj())
	case is("agent", "wait"):
		w, ok := st.get("wait_sleep")
		if !ok {
			w = intOf(0)
		}
		sleepPy(w)
		out(newObj())
	case is("agent", "send-keys"):
		out(newObj())
	case is("pane", "close"):
		id := h.arg(2)
		panes, agents := []any{}, []any{}
		for _, p := range h.list("panes") {
			if !pyEq(field(p, "pane_id", "panes item"), id) {
				panes = append(panes, p)
			}
		}
		for _, a := range h.list("agents") {
			if !pyEq(getField(a, "pane_id", "agents item"), id) {
				agents = append(agents, a)
			}
		}
		st.set("panes", panes)
		st.set("agents", agents)
		out(newObj())
	case is("agent", "read"):
		name := h.arg(2)
		var text any = ""
		if rt, ok := st.get("read_text"); ok {
			if v, ok := asObj(rt, "state read_text").get(name); ok {
				text = v
			}
		}
		out(kv("text", text))
	case is("pane", "read"):
		var text any = ""
		if v, ok := st.get("pane_read_text"); ok {
			text = v
		}
		out(kv("text", text))
	default: // agent prompt, pane run/report-metadata, workspace report-metadata, ...
		out(kv("text", ""))
	}
	return 0, true
}

func truthyKey(st *obj, k string) bool { v, _ := st.get(k); return truthy(v) }

// timeoutOK is 3000 < int(s) <= 300000; a string int() rejects is Python's ValueError.
func timeoutOK(s string) bool {
	n, ok := pyParseInt(s)
	if !ok {
		crashf("invalid literal for int(): %q", s)
	}
	return n.Cmp(big.NewInt(3000)) > 0 && n.Cmp(big.NewInt(300000)) <= 0
}

// pyParseInt is Python's int(str): surrounding whitespace, an optional sign, decimal digits of any script (Unicode
// Nd) with single underscores between digits.
func pyParseInt(s string) (*big.Int, bool) {
	s = strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return nil, false
	}
	var digits []byte
	prevUnderscore := true // an underscore may not lead
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return nil, false
			}
			prevUnderscore = true
			continue
		}
		d, ok := decimalValue(r)
		if !ok {
			return nil, false
		}
		digits = append(digits, byte('0'+d))
		prevUnderscore = false
	}
	if prevUnderscore {
		return nil, false
	}
	n, ok := new(big.Int).SetString(string(digits), 10)
	if !ok {
		return nil, false
	}
	if neg {
		n.Neg(n)
	}
	return n, true
}

// decimalValue is the digit value of a Unicode Nd rune: Nd runes come in runs of ten starting at a zero.
func decimalValue(r rune) (int, bool) {
	if !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	first := r
	for unicode.Is(unicode.Nd, first-1) {
		first--
	}
	return int(r-first) % 10, true
}

// sleepPy is time.sleep(float(v)) for a number or a numeric string.
func sleepPy(v any) {
	var f float64
	switch x := v.(type) {
	case bool:
		if x {
			f = 1
		}
	case pyInt:
		f, _ = new(big.Float).SetInt(x.v).Float64()
	case pyFloat:
		f = float64(x)
	case string:
		var err error
		t := strings.TrimFunc(x, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
		f, err = strconv.ParseFloat(t, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			crashf("could not convert string to float: %q", x)
		}
	default:
		crashf("float() argument must be a string or a number, not %s", dumps(v))
	}
	if math.IsNaN(f) || f < 0 {
		crashf("sleep length must be a non-negative number, got %v", f)
	}
	time.Sleep(time.Duration(math.Min(f, 1e6) * float64(time.Second)))
}

func (h *herdr) processInfo() (int, bool) {
	argv := h.argv
	paneID, have := h.opt("--pane")
	if !have {
		if len(argv) > 2 && !strings.HasPrefix(argv[2], "-") {
			return h.fail(2, "herdr: unknown option: "+argv[2])
		}
		return h.fail(2, "herdr: pane process-info requires --pane")
	}
	var pane any = newObj()
	for _, p := range h.list("panes") {
		if pyEq(field(p, "pane_id", "panes item"), paneID) {
			pane = p
			break
		}
	}
	po := asObj(pane, "pane")
	if fx, _ := po.get("fixture"); truthy(fx) {
		path, ok := fx.(string)
		if !ok {
			crashf("pane fixture is %s, not a path", dumps(fx))
		}
		b, err := os.ReadFile(path)
		if err != nil {
			crashf("cannot read fixture %s: %v", path, err)
		}
		fmt.Fprintln(stdout, strings.TrimFunc(string(b), func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }))
	} else if raw, ok := po.get("raw_result"); ok && raw != nil {
		out(raw)
	} else {
		var fg any = "zsh"
		if v, ok := po.get("fg"); ok {
			fg = v
		}
		pid, _ := po.get("pane_id")
		out(kv("process_info", kv("foreground_processes", []any{kv("argv", []any{fg}, "cmdline", fg, "name", fg, "pid", intOf(1))},
			"pane_id", pid, "shell_pid", intOf(1)), "type", "pane_process_info"))
	}
	return 0, true
}
