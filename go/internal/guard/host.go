package guard

import (
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// The module-level state of worker-guard.py that depends on the passwd entry, the live-home file and the path of
// the hook itself (LIVE_HOME, LIVE_HOMES, PW_DIR, CFG_DIR, PERSIST_*, RUN_SETTINGS, HOOK_REAL).

// PyHook is the absolute path of roles/hooks/worker-guard.py, passed by the shim as `--py-hook` (T-0353). The Go
// binary lives outside the repo, so it cannot find the hook from its own location: HOOK_REAL and the delegation
// to the Python hook (owngoal.go) both start from this path. "" = not given.
var PyHook string

// hostInfo is the set of module-level constants of worker-guard.py that come from the passwd entry.
type hostInfo struct {
	pwDir        string
	cfgDir       string // CFG_DIR: holds live_home
	liveHome     string // LIVE_HOME
	liveHomes    []string
	runSettings  []string // RUN_SETTINGS
	hookReal     string   // HOOK_REAL ("" when --py-hook was not given)
	persistFiles map[string]struct{}
	persistTrees []string
	persistAnc   map[string]struct{}
}

var (
	hostMu    sync.Mutex
	hostCache *hostInfo
	hostErr   error
)

// SetPyHook sets PyHook and drops the cached host state that depends on it.
func SetPyHook(path string) {
	PyHook = path
	resetHost()
}

// getHost computes the host state once. An unreadable passwd entry would make Python die at import (exit 2):
// the error is a block.
func getHost() (*hostInfo, error) {
	hostMu.Lock()
	defer hostMu.Unlock()
	if hostCache != nil || hostErr != nil {
		return hostCache, hostErr
	}
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		hostErr = blockf("cannot read the passwd entry: " + err.Error())
		return nil, hostErr
	}
	hostCache = buildHost(u.HomeDir, os.Getuid(), PyHook)
	return hostCache, nil
}

// resetHost drops the cache (tests change PyHook or the passwd directory).
func resetHost() {
	hostMu.Lock()
	hostCache, hostErr = nil, nil
	hostMu.Unlock()
}

// mustHost is getHost for the call sites that cannot return an error; main recovers the panic and exits 2.
func mustHost() *hostInfo {
	h, err := getHost()
	if err != nil {
		panic(err.Error())
	}
	return h
}

func buildHost(pwDir string, uid int, pyHook string) *hostInfo {
	primary, homes := deriveLiveHomes(pwDir, uid)
	h := &hostInfo{
		pwDir:        pwDir,
		cfgDir:       pyRealpath(pyJoin(pyJoin(pwDir, ".config"), "factory")),
		liveHome:     primary,
		liveHomes:    homes,
		persistFiles: map[string]struct{}{"/etc/gitconfig": {}},
		persistAnc:   map[string]struct{}{},
	}
	if pyHook != "" {
		h.hookReal = pyRealpath(pyHook)
	}
	for _, h2 := range homes {
		h.runSettings = append(h.runSettings, pyRealpath(pyJoin(pyJoin(h2, "run"), "settings")))
	}
	for _, n := range PersistNames {
		p := pyJoin(pwDir, n)
		h.persistFiles[p] = struct{}{}
		h.persistFiles[pyRealpath(p)] = struct{}{}
	}
	git := pyJoin(pyJoin(pwDir, ".config"), "git")
	h.persistTrees = uniq([]string{git, pyRealpath(git)})
	cfg := pyJoin(pwDir, ".config")
	h.persistAnc[cfg] = struct{}{}
	h.persistAnc[pyRealpath(cfg)] = struct{}{}
	return h
}

func uniq(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// deriveLiveHomes is a port of worker-guard.py:derive_live_homes(uid) for the passwd directory base:
// <base>/factory is always live; <base>/.config/factory/live_home can only add one (an invalid file is ignored).
// It never reads $HOME or any env var: a worker sets those.
func deriveLiveHomes(base string, uid int) (primary string, homes []string) {
	def := pyRealpath(pyJoin(base, "factory"))
	primary, homes = def, []string{def}
	cfgdir := pyJoin(pyJoin(base, ".config"), "factory")
	path := pyJoin(cfgdir, "live_home")
	st, err := os.Lstat(path)
	if err != nil {
		return
	}
	pst, err := os.Lstat(cfgdir)
	if err != nil {
		return
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || !pst.IsDir() || int(sys.Uid) != uid || st.Mode().Perm()&0o022 != 0 {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(b) {
		return
	}
	lines := pyLines(string(b))
	if len(lines) == 1 && strings.HasPrefix(lines[0], "/") && !strings.Contains(lines[0], "\x00") {
		primary = pyRealpath(lines[0])
		homes = uniq(append(homes, primary))
	}
	return
}

// liveHomes is LIVE_HOMES of worker-guard.py.
func liveHomes() ([]string, error) {
	h, err := getHost()
	if err != nil {
		return nil, err
	}
	return h.liveHomes, nil
}

// LiveHomesOf is derive_live_homes for the passwd directory base and the current uid, for the tests that
// compare it with the Python function: the primary home and the sorted set.
func LiveHomesOf(base string) (string, []string) {
	p, homes := deriveLiveHomes(base, os.Getuid())
	return p, sortedCopy(homes)
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// analyzerLiveHomes is Analyzer.live_homes: LIVE_HOMES plus the repo root of the real hook (dirname^3 of HOOK_REAL),
// which is the CI checkout when the hook runs from a worktree.
func analyzerLiveHomes() []string {
	h := mustHost()
	homes := append([]string(nil), h.liveHomes...)
	if h.hookReal != "" {
		homes = uniq(append(homes, pyDirname(pyDirname(pyDirname(h.hookReal)))))
	}
	return homes
}
