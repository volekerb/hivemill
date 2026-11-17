package views

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Constants of web/views/agents.py's /proc snapshot.
const (
	procTTL    = 30 * time.Second      // PROC_TTL_S
	procBudget = 80 * time.Millisecond // PROC_BUDGET_S: a scan gives up after this long; the partial result is cached
)

// ProcInfo is what the Resources view reads of a claude process: its parent pid (nil when stat could not be read).
type ProcInfo struct{ PPID *int64 }

// ProcSnapshot is agents.proc_snapshot: pid -> process of the claude processes, nil when /proc is unreadable. It is a
// variable so a test (and the server wiring of P3e) can replace it; the default is the cached scan of the host.
var ProcSnapshot = cachedProcScan

var (
	procMu    sync.Mutex
	procAt    time.Time
	procValue map[int64]ProcInfo
	procHave  bool
	procClock = time.Now
)

func cachedProcScan() map[int64]ProcInfo {
	procMu.Lock()
	defer procMu.Unlock()
	now := procClock()
	if !procHave || now.Sub(procAt) >= procTTL {
		procValue = scanProcs(procDir())
		procAt, procHave = now, true
	}
	return procValue
}

// liveHomes is agents._live_homes: the passwd home's factory dir (never $HOME).
func liveHomes() map[string]bool {
	home := ""
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
	}
	return map[string]bool{pyRealpath(filepath.Join(home, "factory")): true}
}

// procDir is agents._proc_dir: /proc, or $FACTORY_PROC_DIR (a fake /proc for tests) only when FACTORY_HOME is not the
// live home.
func procDir() string {
	fake := os.Getenv("FACTORY_PROC_DIR")
	if fake == "" {
		return "/proc"
	}
	live := liveHomes()
	home := os.Getenv("FACTORY_HOME")
	if home == "" {
		for h := range live {
			home = h
		}
	}
	if live[pyRealpath(home)] {
		return "/proc"
	}
	return fake
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// cmdlineIsClaude is the filter of agents._scan: "claude" is the basename of one of the first three argv entries.
func cmdlineIsClaude(raw []byte) bool {
	n := 0
	for _, part := range bytes.Split(raw, []byte{0}) {
		if len(part) == 0 {
			continue
		}
		if n++; n > 3 {
			return false
		}
		s := string(part)
		if s[strings.LastIndexByte(s, '/')+1:] == "claude" {
			return true
		}
	}
	return false
}

// parseStatPPid is int(stat[stat.rindex(")") + 1:].split()[1]): nil where Python's except clause swallows the error.
func parseStatPPid(stat string) *int64 {
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return nil
	}
	f := pyFields(stat[i+1:])
	if len(f) < 2 {
		return nil
	}
	n, ok := pyIntText(f[1])
	if !ok {
		return nil
	}
	return &n
}

// scanProcs is agents._scan over the directory root; nil when it cannot be listed.
func scanProcs(root string) map[int64]ProcInfo {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	deadline := procClock().Add(procBudget)
	found := map[int64]ProcInfo{}
	for _, e := range ents {
		if !allDigits(e.Name()) {
			continue
		}
		if procClock().After(deadline) {
			break
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), "cmdline"))
		if err != nil || !cmdlineIsClaude(raw) {
			continue
		}
		pid, ok := pyIntText(e.Name())
		if !ok {
			continue
		}
		info := ProcInfo{}
		if st, err := os.ReadFile(filepath.Join(root, e.Name(), "stat")); err == nil {
			info.PPID = parseStatPPid(strings.ToValidUTF8(string(st), "�"))
		}
		found[pid] = info
	}
	return found
}

// ClaudeTotal is agents.claude_total: the top-level claude processes (one whose parent is itself a claude is a
// subagent or MCP launcher). ok is false when the snapshot is unknown.
func ClaudeTotal(snap map[int64]ProcInfo) (int, bool) {
	if snap == nil {
		return 0, false
	}
	n := 0
	pids := make([]int64, 0, len(snap))
	for p := range snap {
		pids = append(pids, p)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	for _, p := range pids {
		pp := snap[p].PPID
		if pp == nil {
			n++
			continue
		}
		if _, ok := snap[*pp]; !ok {
			n++
		}
	}
	return n, true
}

func pyJoin(a, b string) string {
	switch {
	case strings.HasPrefix(b, "/"):
		return b
	case a == "" || strings.HasSuffix(a, "/"):
		return a + b
	}
	return a + "/" + b
}

// pySplit is posixpath.split: (head, tail) with the trailing slashes of head removed unless it is all slashes.
func pySplit(p string) (string, string) {
	i := strings.LastIndexByte(p, '/') + 1
	head, tail := p[:i], p[i:]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head, tail
}

// joinRealpath is posixpath._joinrealpath (non-strict): a symlink that cannot be resolved (dangling, through a missing
// component, or a loop) leaves the rest of the path as it is instead of failing.
func joinRealpath(path, rest string, seen map[string]*string) (string, bool) {
	if strings.HasPrefix(rest, "/") {
		rest, path = rest[1:], "/"
	}
	for rest != "" {
		name, tail, _ := strings.Cut(rest, "/")
		rest = tail
		switch name {
		case "", ".":
			continue
		case "..":
			if path != "" {
				head, last := pySplit(path)
				path = head
				if last == ".." {
					path = pyJoin(pyJoin(head, ".."), "..")
				}
			} else {
				path = ".."
			}
			continue
		}
		newpath := pyJoin(path, name)
		st, err := os.Lstat(newpath)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			path = newpath
			continue
		}
		if prev, ok := seen[newpath]; ok {
			if prev != nil { // already resolved: use the cached value
				path = *prev
				continue
			}
			return pyJoin(newpath, rest), false // a symlink loop: the resolved part plus the rest unchanged
		}
		seen[newpath] = nil // not resolved yet
		target, err := os.Readlink(newpath)
		if err != nil {
			return pyJoin(newpath, rest), false
		}
		var ok bool
		if path, ok = joinRealpath(path, target, seen); !ok {
			return pyJoin(path, rest), false
		}
		resolved := path
		seen[newpath] = &resolved
	}
	return path, true
}

// pyRealpath is os.path.realpath (non-strict) of a path: symlinks are resolved as far as they can be; a dangling link,
// a link through a missing component and a loop keep the unresolved tail, as in Python, never an error.
func pyRealpath(p string) string {
	if !filepath.IsAbs(p) {
		if cwd, err := os.Getwd(); err == nil {
			p = pyJoin(cwd, p)
		}
	}
	out, _ := joinRealpath("", p, map[string]*string{})
	return filepath.Clean(out)
}
