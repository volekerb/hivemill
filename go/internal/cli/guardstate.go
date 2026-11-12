package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// guardBinaryState is guard_binary_state(): ok | missing | stale, the Go guard binary against its stamp and
// `git rev-parse HEAD:go` of the checkout that holds go/.
func (d *Deps) guardBinaryState() (string, error) {
	lib := filepath.Join(d.Env.UserHome(), ".local", "lib", "factory")
	stamp := readLine(filepath.Join(lib, "worker-guard.stamp"))
	if !executable(filepath.Join(lib, "worker-guard")) || stamp == "" {
		return "missing", nil
	}
	live, err := d.Env.HomeIsLive()
	if err != nil {
		return "", errDelegate
	}
	src := d.Env.Home()
	if live {
		primary, _, err := d.Env.DeriveLiveHomes()
		if err != nil {
			return "", errDelegate
		}
		src = primary
	}
	if stamp == d.srcHash(src, live) {
		return "ok", nil
	}
	return "stale", nil
}

// srcHash is guard_srchash: git rev-parse HEAD:go, "" when git fails or there is no go/ tree.
func (d *Deps) srcHash(src string, live bool) string {
	fileProto := "always"
	if live {
		fileProto = "never"
	}
	args := []string{"-C", src,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always", "-c", "protocol.file.allow=" + fileProto,
		"-c", "commit.gpgSign=false", "-c", "tag.gpgSign=false", "rev-parse", "HEAD:go"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(append([]string(nil), d.Environ...), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readLine(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func executable(p string) bool {
	return syscall.Access(p, 1) == nil // os.access(p, X_OK)
}
