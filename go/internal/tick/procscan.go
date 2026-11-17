package tick

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/store"
)

// The /proc reads of the flagless and claude_audit units: proc_root, _scan_claude, _scan_host_agent_procs,
// claude_procs, cursor_procs and their argv helpers in bin/factory_cli.py. Read-only: nothing here signals a process.

// procEnt is one scan hit: the argv and the parent pid (hasPpid false when the stat file was unreadable).
type procEnt struct {
	Argv    []string
	Ppid    int
	HasPpid bool
}

// procDirIgnored records the once-per-process proc-dir-ignored event (_PROC_IGNORED).
var procDirIgnored bool

// procRoot is proc_root(): /proc, or $FACTORY_PROC_DIR off the live home only. On a live home a set variable is
// ignored and one proc-dir-ignored event per process records the attempt (a failed write is only reported).
func procRoot(r *reapDB) string {
	const v = "FACTORY_PROC_DIR"
	fake := os.Getenv(v)
	if fake == "" {
		return "/proc"
	}
	live, err := pruneIdentity().HomeIsLive()
	if err == nil && !live {
		return fake
	}
	// an undecidable liveness is treated as live: the safe side (a process could forge what the checks see)
	if !procDirIgnored {
		procDirIgnored = true
		who := os.Getenv("FACTORY_AGENT")
		if who == "" {
			who = "unknown"
		}
		if e := r.EventAs(who, "agent", who, "proc-dir-ignored", store.Ordered{{K: v, V: fake}}, ""); e != nil {
			fmt.Fprintf(os.Stderr, "factory: ignoring %s on the live home; event not recorded: %v\n", v, e)
		}
	}
	return "/proc"
}

// pyDecode is bytes.decode(errors="replace"): every maximal invalid subpart becomes one U+FFFD.
func pyDecode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || n > 1 {
			sb.WriteRune(r)
			i += n
			continue
		}
		sb.WriteRune(utf8.RuneError)
		i += invalidSubpart(b[i:])
	}
	return sb.String()
}

// invalidSubpart is the length of the maximal prefix of b (an invalid sequence) that could start a valid one.
func invalidSubpart(b []byte) int {
	lead := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		need = 1
	case lead >= 0xE0 && lead <= 0xEF:
		need = 2
		if lead == 0xE0 {
			lo = 0xA0
		}
		if lead == 0xED {
			hi = 0x9F
		}
	case lead >= 0xF0 && lead <= 0xF4:
		need = 3
		if lead == 0xF0 {
			lo = 0x90
		}
		if lead == 0xF4 {
			hi = 0x8F
		}
	default:
		return 1
	}
	n := 1
	for ; n <= need && n < len(b); n++ {
		l, h := byte(0x80), byte(0xBF)
		if n == 1 {
			l, h = lo, hi
		}
		if b[n] < l || b[n] > h {
			break
		}
	}
	return n
}

// splitArgv is `[x.decode(errors="replace") for x in cmdline.split(b"\0") if x]`.
func splitArgv(b []byte) []string {
	var out []string
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			out = append(out, pyDecode([]byte(p)))
		}
	}
	return out
}

// pyDigitValue is the decimal value of a Unicode Nd character (int() accepts them), -1 for any other.
func pyDigitValue(r rune) int {
	if r < 0x80 {
		if r >= '0' && r <= '9' {
			return int(r - '0')
		}
		return -1
	}
	for _, rg := range unicode.Nd.R16 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) && rg.Stride == 1 {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) && rg.Stride == 1 {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	return -1
}

// pyIntStr is int(str) for the values /proc holds: optional sign, Unicode decimal digits, single underscores between
// digits, Unicode whitespace around. ok is false where Python raises ValueError (or the value overflows int).
func pyIntStr(s string) (int, bool) {
	s = strings.TrimFunc(s, unicode.IsSpace)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	n, prevDigit := 0, false
	for _, r := range s {
		if r == '_' {
			if !prevDigit {
				return 0, false
			}
			prevDigit = false
			continue
		}
		d := pyDigitValue(r)
		if d < 0 {
			return 0, false
		}
		if n > (1<<62)/10 {
			return 0, false
		}
		n = n*10 + d
		prevDigit = true
	}
	if !prevDigit {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// pyIsDigits is str.isdigit() restricted to the decimal digits int() can read (a "²" dir name is Python's ValueError;
// here it is skipped).
func pyIsDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if pyDigitValue(r) < 0 {
			return false
		}
	}
	return true
}

// statAfterComm is `stat[stat.rindex(")") + 1:].split()`; ok is false when there is no ")".
func statAfterComm(stat string) ([]string, bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return nil, false
	}
	return strings.FieldsFunc(stat[i+1:], store.PyIsSpace), true
}

// parsePpid is int(fields[1]) of a stat file's text.
func parsePpid(stat string) (int, bool) {
	f, ok := statAfterComm(stat)
	if !ok || len(f) < 2 {
		return 0, false
	}
	return pyIntStr(f[1])
}

func readPpid(root string, pid string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(root, pid, "stat"))
	if err != nil {
		return 0, false
	}
	return parsePpid(pyDecode(b))
}

// procStart is _proc_start: field 22 of the stat file, "" with ok=false when unreadable.
func procStart(root, pid string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, pid, "stat"))
	if err != nil {
		return "", false
	}
	f, ok := statAfterComm(pyDecode(b))
	if !ok || len(f) < 20 {
		return "", false
	}
	return f[19], true
}

func hasBase(argv []string, base string) bool {
	for i, x := range argv {
		if i >= 3 {
			break
		}
		if pyBase(x) == base {
			return true
		}
	}
	return false
}

// pyBase is os.path.basename.
func pyBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// flagValues is the values of `--flag X` pairs first, then of `--flag=X` forms, in argv order within each.
func flagValues(argv []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			out = append(out, argv[i+1])
		}
	}
	for _, x := range argv {
		if strings.HasPrefix(x, flag+"=") {
			out = append(out, x[len(flag)+1:])
		}
	}
	return out
}

func argvWorkspace(argv []string) string {
	if v := flagValues(argv, "--workspace"); len(v) > 0 {
		return v[0]
	}
	return ""
}

func isCursorAgentArgv(argv []string) bool {
	for i, x := range argv {
		if i >= 3 {
			break
		}
		switch pyBase(x) {
		case "cursor-agent":
			return true
		case "agent":
			if argvWorkspace(argv) != "" {
				return true
			}
		}
	}
	return false
}

// isClaudeCmd is _is_claude_cmd: argv[0] is claude, or node/nodejs with a claude cli as argv[1].
func isClaudeCmd(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	first := pyBase(argv[0])
	return first == "claude" || ((first == "node" || first == "nodejs") && len(argv) > 1 && pyBase(argv[1]) == "claude")
}

// scanProcs walks root: every numeric dir whose cmdline passes match. A root that cannot be listed is an error.
func scanProcs(root string, match func([]string) bool) (map[int]procEnt, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read proc dir %s: %w", root, err)
	}
	found := map[int]procEnt{}
	for _, e := range entries {
		name := e.Name()
		if !pyIsDigits(name) {
			continue
		}
		pid, ok := pyIntStr(name)
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, name, "cmdline"))
		if err != nil {
			continue // a process that exited, or one we may not read: not a hit
		}
		argv := splitArgv(b)
		if !match(argv) {
			continue
		}
		ent := procEnt{Argv: argv}
		ent.Ppid, ent.HasPpid = readPpid(root, name)
		found[pid] = ent
	}
	return found, nil
}

// scanClaude is _scan_claude; nil when the proc dir is unreadable (callers treat that as no process).
func scanClaude(root string) map[int]procEnt {
	m, err := scanProcs(root, func(a []string) bool { return hasBase(a, "claude") })
	if err != nil {
		return nil
	}
	return m
}

// scanHostAgentProcs is _scan_host_agent_procs.
func scanHostAgentProcs(root string) map[int]procEnt {
	m, err := scanProcs(root, func(a []string) bool { return hasBase(a, "claude") || isCursorAgentArgv(a) })
	if err != nil {
		return nil
	}
	return m
}

type pidArgv struct {
	Pid  int
	Argv []string
}

func sortedPids[V any](m map[int]V) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}

// claudeProcs is claude_procs: the claude processes running --resume/--session-id <session>. argvs nil scans.
func claudeProcs(root, session string, argvs map[int][]string) []pidArgv {
	if argvs == nil {
		argvs = map[int][]string{}
		for p, v := range scanClaude(root) {
			argvs[p] = v.Argv
		}
	}
	var out []pidArgv
	for _, p := range sortedPids(argvs) {
		for _, flag := range []string{"--resume", "--session-id"} {
			if containsStr(flagValues(argvs[p], flag), session) {
				out = append(out, pidArgv{p, argvs[p]})
				break
			}
		}
	}
	return out
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// cursorProcs is cursor_procs: cursor-agent processes whose --workspace resolves to workspace.
func cursorProcs(root, workspace string, realpath func(string) string) []pidArgv {
	want := realpath(workspace)
	var out []pidArgv
	all := scanHostAgentProcs(root)
	for _, p := range sortedPids(all) {
		argv := all[p].Argv
		if !isCursorAgentArgv(argv) {
			continue
		}
		if w := argvWorkspace(argv); w != "" && realpath(w) == want {
			out = append(out, pidArgv{p, argv})
		}
	}
	return out
}
