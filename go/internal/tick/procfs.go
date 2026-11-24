package tick

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"factory/go/internal/identity"
	"factory/go/internal/store"
)

// The process-table half of the reap units: Python's _serve_proc_root, _serve_argv, _orphan_stat, _orphan_env,
// _orphan_ident, _serve_unit_pids, _serve_mainpid, _serve_pad and _orphan_task. Everything reads through one root, the
// real /proc or $FACTORY_SERVE_PROC_DIR off the live home, so tests run on a fake proc tree.

// reapSeams are the process-level calls the reap units make; tests replace them (never the real kill).
var (
	reapKill      = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	reapSleep     = time.Sleep
	reapNow       = time.Now
	reapGetpid    = os.Getpid
	reapGetppid   = os.Getppid
	reapGetpgrp   = syscall.Getpgrp
	reapGetuid    = os.Getuid
	reapTermWait  = 200 * time.Millisecond // the poll step of _orphan_term_kill (time.sleep(0.2))
	reapPreSignal = func() {}              // test hook: runs between the scan and the identity re-read of a signal
)

const (
	serveKillGraceS = 10.0 // SERVE_KILL_GRACE_S
	procChainMax    = 64   // the ancestor walks stop after 64 steps
	clkTck          = 100  // os.sysconf("SC_CLK_TCK") on Linux (USER_HZ is fixed at 100)
)

// pyDigitsRE is Python's str-pattern \d: every Unicode decimal digit, not only 0-9 (G8).
const pyDigitsRE = `\p{Nd}+`

var (
	pyPythonRE   = regexp.MustCompile(`^python3(\.` + pyDigitsRE + `)?$`)
	pyGoalTaskRE = regexp.MustCompile(`^(G-` + pyDigitsRE + `)-(T-` + pyDigitsRE + `)$`)
	pyGoalTailRE = regexp.MustCompile(`(G-` + pyDigitsRE + `)-(T-` + pyDigitsRE + `)$`)
)

// proc is one reap call's view of the process table.
type proc struct {
	db   *sql.DB
	root string
	env  identity.Env
}

var (
	procIgnoredMu sync.Mutex
	procIgnored   = map[string]bool{}
)

// newProc is _proc_dir_from_env("FACTORY_SERVE_PROC_DIR"): /proc, or the fake dir only when FACTORY_HOME is not a live
// home. On a live home a set variable is ignored (a process could forge what the checks see) and one proc-dir-ignored
// event per variable is recorded per process.
func newProc(db *sql.DB) *proc {
	env := pruneIdentity()
	p := &proc{db: db, root: "/proc", env: env}
	const v = "FACTORY_SERVE_PROC_DIR"
	fake := env.Getenv(v)
	if fake == "" {
		return p
	}
	live, err := env.HomeIsLive()
	if err == nil && !live {
		p.root = fake
		return p
	}
	// a home whose liveness cannot be decided is treated as live: the forged dir is ignored
	procIgnoredMu.Lock()
	first := !procIgnored[v]
	procIgnored[v] = true
	procIgnoredMu.Unlock()
	if first {
		who := env.Getenv("FACTORY_AGENT")
		if who == "" {
			who = "unknown"
		}
		if db != nil {
			now, cerr := pruneEventClock(env)
			if cerr == nil {
				s := &store.Store{DB: db, Clock: now}
				cerr = s.EventAs(who, "agent", who, "proc-dir-ignored", store.Ordered{{K: v, V: fake}}, "")
			}
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "factory: ignoring %s on the live home; event not recorded: %v\n", v, cerr)
			}
		}
	}
	return p
}

// home is HOME of this run (Path(os.environ.get("FACTORY_HOME", ~/factory))).
func (p *proc) home() string { return filepath.Clean(p.env.Home()) }

// worktrees is WORKTREES = HOME / "worktrees".
func (p *proc) worktrees() string { return filepath.Join(p.home(), "worktrees") }

// tmpDir is tmp_dir(): $FACTORY_TMP_DIR or /tmp.
func (p *proc) tmpDir() string {
	if v := p.env.Getenv("FACTORY_TMP_DIR"); v != "" {
		return filepath.Clean(v)
	}
	return "/tmp"
}

func (p *proc) path(pid int, leaf string) string {
	return filepath.Join(p.root, strconv.Itoa(pid), leaf)
}

// pids is `[int(d.name) for d in root.iterdir() if d.name.isdigit()]`, sorted by name as Python sorts the paths.
// Names with a non-ASCII digit never occur in /proc and are skipped.
func (p *proc) pids() ([]int, error) {
	ents, err := os.ReadDir(p.root)
	if err != nil {
		return nil, fmt.Errorf("read proc root %s: %w", p.root, err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []int
	for _, n := range names {
		if isASCIIDigits(n) {
			v, err := strconv.Atoi(n)
			if err != nil {
				continue // longer than an int: not a pid
			}
			out = append(out, v)
		}
	}
	return out, nil
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// decodeReplace is bytes.decode(errors="replace"): one U+FFFD per invalid byte.
func decodeReplace(b []byte) string { return string([]rune(string(b))) }

// argv is _serve_argv: the cmdline of a pid, nil when it is gone, unreadable or a zombie (empty cmdline).
func (p *proc) argv(pid int) []string {
	b, err := os.ReadFile(p.path(pid, "cmdline"))
	if err != nil {
		return nil
	}
	var out []string
	for _, x := range bytes.Split(b, []byte{0}) {
		if len(x) > 0 {
			out = append(out, decodeReplace(x))
		}
	}
	return out
}

// stat is _orphan_stat: (ppid, pgid, starttime) from /proc/<pid>/stat; ok=false when it is gone or unreadable.
func (p *proc) stat(pid int) (ppid, pgid, start int, ok bool) {
	b, err := os.ReadFile(p.path(pid, "stat"))
	if err != nil {
		return 0, 0, 0, false
	}
	s := decodeReplace(b)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 > len(s) {
		return 0, 0, 0, false
	}
	f := strings.FieldsFunc(s[i+2:], store.PyIsSpace)
	if len(f) < 20 {
		return 0, 0, 0, false
	}
	var v [3]int
	for k, idx := range []int{1, 2, 19} {
		n, perr := pyIntStr(f[idx])
		if perr != nil {
			return 0, 0, 0, false
		}
		v[k] = n
	}
	return v[0], v[1], v[2], true
}

// parent is `(_orphan_stat(pid) or (None,))[0]`.
func (p *proc) parent(pid int) (int, bool) {
	pp, _, _, ok := p.stat(pid)
	return pp, ok
}

// ident is _orphan_ident: (argv, starttime) right now; ok=false when either is unreadable.
type ident struct {
	argv  []string
	start int
}

func (a ident) equal(b ident) bool {
	if a.start != b.start || len(a.argv) != len(b.argv) {
		return false
	}
	for i := range a.argv {
		if a.argv[i] != b.argv[i] {
			return false
		}
	}
	return true
}

func (p *proc) ident(pid int) (ident, bool) {
	_, _, st, ok := p.stat(pid)
	av := p.argv(pid)
	if !ok || av == nil {
		return ident{}, false
	}
	return ident{av, st}, true
}

// environ is _orphan_env: the process environment, ok=false when unreadable.
func (p *proc) environ(pid int) (map[string]string, bool) {
	b, err := os.ReadFile(p.path(pid, "environ"))
	if err != nil {
		return nil, false
	}
	m := map[string]string{}
	for _, x := range bytes.Split(b, []byte{0}) {
		if k, v, found := strings.Cut(decodeReplace(x), "="); found {
			m[k] = v
		}
	}
	return m, true
}

// unitPids is _serve_unit_pids: pids whose cgroup is factory-serve.service.
func (p *proc) unitPids() (map[int]bool, error) {
	pids, err := p.pids()
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, pid := range pids {
		b, rerr := os.ReadFile(p.path(pid, "cgroup"))
		if rerr != nil {
			continue
		}
		if serveCgroupRE.MatchString(decodeReplace(b)) {
			out[pid] = true
		}
	}
	return out, nil
}

var serveCgroupRE = regexp.MustCompile(`(?m)/factory-serve\.service(/|$)`)

// mainPID is _serve_mainpid: the MainPID of factory-serve.service, 0 when systemd cannot say. $FACTORY_SERVE_MAINPID
// replaces the call (tests). The systemctl call has Python's 10 s timeout.
func mainPID(env identity.Env) int {
	if fake, ok := lookupEnv(env, "FACTORY_SERVE_MAINPID"); ok {
		if isASCIIDigits(fake) {
			if n, err := strconv.Atoi(fake); err == nil {
				return n
			}
		}
		return 0
	}
	out, ok := runCapture(10*time.Second, "systemctl", "--user", "show", "factory-serve.service", "-p", "MainPID", "--value")
	if !ok {
		return 0
	}
	out = store.PyStrip(out)
	if isASCIIDigits(out) {
		if n, err := strconv.Atoi(out); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// skipSet is _orphan_skip: the tick, its ancestors and the factory-serve unit's processes.
func (p *proc) skipSet(main int) (map[int]bool, error) {
	skip := map[int]bool{reapGetpid(): true}
	if main > 0 {
		skip[main] = true
	}
	unit, err := p.unitPids()
	if err != nil {
		return nil, err
	}
	for k := range unit {
		skip[k] = true
	}
	cur, ok := reapGetppid(), true
	for i := 0; i < procChainMax; i++ {
		if !ok || cur <= 1 {
			break
		}
		skip[cur] = true
		cur, ok = p.parent(cur)
	}
	return skip, nil
}

// pyBasename is os.path.basename.
func pyBasename(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// pyNormpath is posixpath.normpath (a leading "//" stays, three or more collapse).
func pyNormpath(path string) string {
	if path == "" {
		return "."
	}
	initial := 0
	if strings.HasPrefix(path, "/") {
		initial = 1
		if strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "///") {
			initial = 2
		}
	}
	var out []string
	for _, c := range strings.Split(path, "/") {
		if c == "" || c == "." {
			continue
		}
		if c != ".." || (initial == 0 && len(out) == 0) || (len(out) > 0 && out[len(out)-1] == "..") {
			out = append(out, c)
		} else if len(out) > 0 {
			out = out[:len(out)-1]
		}
	}
	res := strings.Repeat("/", initial) + strings.Join(out, "/")
	if res == "" {
		return "."
	}
	return res
}

// pyInt is int(str): optional sign, ASCII or Unicode decimal digits, single underscores between digits, surrounding
// whitespace ignored. Anything else is an error, as ValueError.
func pyIntStr(s string) (int, error) {
	t := pyASCIIDigits(store.PyStrip(s))
	neg := false
	if t != "" && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	if t == "" || t[0] == '_' || t[len(t)-1] == '_' || strings.Contains(t, "__") || strings.Trim(t, "0123456789_") != "" {
		return 0, fmt.Errorf("invalid literal for int() with base 10: %s", pyReprStr(s))
	}
	n, err := strconv.Atoi(strings.ReplaceAll(t, "_", ""))
	if err != nil {
		return 0, fmt.Errorf("invalid literal for int() with base 10: %s: %w", pyReprStr(s), err)
	}
	if neg {
		n = -n
	}
	return n, nil
}

// pyReprStr is repr(str) for the common case (single quotes, backslash and quote escaped).
func pyReprStr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	s = r.Replace(s)
	if q == "'" {
		s = strings.ReplaceAll(s, "'", `\'`)
	}
	return q + s + q
}

// realpath is os.path.realpath (non-strict).
func realpath(p string) string { return identity.Realpath(p) }

// relParts is Path(path).relative_to(base).parts: ok=false when path is not base or below it (lexical, as pathlib).
func relParts(base, path string) ([]string, bool) {
	b, q := splitParts(base), splitParts(path)
	if len(q) < len(b) {
		return nil, false
	}
	for i := range b {
		if b[i] != q[i] {
			return nil, false
		}
	}
	return q[len(b):], true
}

func splitParts(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

// dollar is the text a Python `$` sees: it also matches before one trailing newline.
func dollar(s string) string { return strings.TrimSuffix(s, "\n") }

// servePad is _serve_pad: (pad dir, task id) when path lies inside an agent's temp dir, else ok=false.
func (p *proc) servePad(path string) (pad, task string, ok bool) {
	tmp := realpath(p.tmpDir())
	parts, in := relParts(tmp, realpath(path))
	if !in || len(parts) == 0 {
		return "", "", false
	}
	if parts[0] == fmt.Sprintf("claude-%d", reapGetuid()) {
		if len(parts) < 2 {
			return "", "", false
		}
		slug := regexp.MustCompile("^" + regexp.QuoteMeta(nonAlnumDash(p.worktrees())+"-") + `(G-` + pyDigitsRE + `)-(T-` + pyDigitsRE + `)(?:-qa)?$`)
		m := slug.FindStringSubmatch(dollar(parts[1]))
		if m == nil {
			return "", "", false
		}
		return filepath.Join(tmp, parts[0], parts[1]), m[2], true
	}
	if id, found := searchTaskID(parts[0]); found {
		return filepath.Join(tmp, parts[0]), id, true
	}
	return "", "", false
}

// nonAlnumDash is re.sub(r"[^A-Za-z0-9]", "-", s): every non-ASCII-alphanumeric character becomes one dash.
func nonAlnumDash(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r < 128 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

// searchTaskID is re.search(r"(?<![A-Za-z0-9])[Tt]-(\d+)(?![0-9])", s) giving "T-" + the digits.
func searchTaskID(s string) (string, bool) {
	rs := []rune(s)
	isAlnum := func(r rune) bool {
		return r < 128 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}
	for i := 0; i < len(rs); i++ {
		if (rs[i] != 'T' && rs[i] != 't') || i+1 >= len(rs) || rs[i+1] != '-' || (i > 0 && isAlnum(rs[i-1])) {
			continue
		}
		j := i + 2
		for j < len(rs) && unicode.Is(unicode.Nd, rs[j]) {
			j++
		}
		if j == i+2 {
			continue
		}
		return "T-" + string(rs[i+2:j]), true
	}
	return "", false
}

// orphanTask is _orphan_task: ((goal, task), attributed_by) when cwd, scratchpad and argv all point at one task's
// worktree, else ok=false (nothing found, or two tasks).
func (p *proc) orphanTask(cwd string, argv []string) (goal, task, by string, ok bool) {
	wt := realpath(p.worktrees())
	inWorktree := func(path string) (string, string, bool) {
		rel, in := relParts(wt, path)
		if !in || len(rel) == 0 {
			return "", "", false
		}
		m := pyGoalTaskRE.FindStringSubmatch(rel[0])
		if m == nil {
			return "", "", false
		}
		return m[1], m[2], true
	}
	type hit struct{ by, goal, task string }
	var found []hit
	if g, t, in := inWorktree(cwd); in {
		found = append(found, hit{"cwd", g, t})
	}
	if pad, _, isPad := p.servePad(cwd); isPad && filepath.Base(filepath.Dir(pad)) == fmt.Sprintf("claude-%d", reapGetuid()) {
		if m := pyGoalTailRE.FindStringSubmatch(dollar(filepath.Base(pad))); m != nil {
			found = append(found, hit{"scratchpad", m[1], m[2]})
		}
	}
	if len(argv) > 1 {
		for _, el := range argv[1:] {
			if el == "" {
				continue
			}
			joined := el
			if !strings.HasPrefix(el, "/") {
				joined = cwd + "/" + el
			}
			if g, t, in := inWorktree(realpath(joined)); in {
				found = append(found, hit{"argv", g, t})
			}
		}
	}
	if len(found) == 0 {
		return "", "", "", false
	}
	for _, h := range found[1:] {
		if h.goal != found[0].goal || h.task != found[0].task {
			return "", "", "", false
		}
	}
	return found[0].goal, found[0].task, found[0].by, true
}

// lookupEnv is os.environ.get(k) is not None: set-but-empty counts as set.
func lookupEnv(env identity.Env, k string) (string, bool) {
	if env.LookupEnv != nil {
		return env.LookupEnv(k)
	}
	v := env.Getenv(k)
	return v, v != ""
}

// processZone is the zone a naive stored timestamp is read in: datetime.fromisoformat(...).astimezone() uses the
// process's local zone.
func processZone() *time.Location { return time.Now().Location() }
