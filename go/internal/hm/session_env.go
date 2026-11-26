package hm

import (
	"os"
	"path/filepath"
	"strings"
)

// sessionTmpRoot holds one temp dir per session. It is on disk: /tmp may be a small RAM tmpfs that parallel go builds
// fill up.
func sessionTmpRoot(userHome string) string {
	return filepath.Join(userHome, ".cache", "hivemill", "tmp")
}

// sessionEnv is the base env of a session. TMPDIR and CLAUDE_CODE_TMPDIR are the session's own dir; the go temp dir and
// build cache are shared by all sessions.
func sessionEnv(userHome, name string) map[string]string {
	tmp := filepath.Join(sessionTmpRoot(userHome), name)
	godir := filepath.Join(userHome, ".cache", "hivemill", "go")
	return map[string]string{"PATH": sessionPath(userHome), "LANG": "C.UTF-8", "TERM": "xterm-256color", "HOME": userHome,
		"FACTORY_AGENT": name, "TMPDIR": tmp, "CLAUDE_CODE_TMPDIR": tmp,
		"GOTMPDIR": filepath.Join(godir, "tmp"), "GOCACHE": filepath.Join(godir, "build")}
}

// prepareSessionDirs creates the dirs sessionEnv names: go and Claude Code expect them to exist. For a session placed
// on another machine (machine != "") it drops those variables instead: dirs made here do not exist there, and its home
// may differ.
func prepareSessionDirs(env map[string]string, machine string) error {
	if machine != "" {
		for _, k := range []string{"TMPDIR", "CLAUDE_CODE_TMPDIR", "GOTMPDIR", "GOCACHE"} {
			delete(env, k)
		}
		return nil
	}
	for _, k := range []string{"TMPDIR", "GOTMPDIR", "GOCACHE"} {
		if err := os.MkdirAll(env[k], 0o700); err != nil {
			return err
		}
	}
	return nil
}

// goodSessionName reports whether name is a single path element, so that its temp dir is a child of sessionTmpRoot.
func goodSessionName(name string) bool {
	return name != "" && name != "." && !strings.Contains(name, "/") && !strings.Contains(name, "..")
}
