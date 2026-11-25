package hm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"factory/go/internal/shlex"
)

// The worker guard of a standalone home. The role settings run `sh <home>/roles/hooks/worker-guard --role R` on every
// tool call; a repo checkout has that shim (it picks the Python hook or the Go binary), a standalone home does not, and
// without it every call would be blocked. For such a home RenderSettings points the hook at `hivemill guard`, which
// runs the same Go guard in-process (guard.HookMain) with the home added to the live homes. A home that has the shim
// renders exactly as before.

// SelfBinary is the absolute path of the running hivemill binary (symlinks resolved), which the guard hook and the
// pre-push hook of a standalone home call. Tests replace it.
var SelfBinary = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// GuardShim is the repo's guard shim under a home.
func GuardShim(home string) string { return filepath.Join(home, "roles", "hooks", "worker-guard") }

// guardWrap runs `<bin> guard --home <home> <hook args>` and maps every exit status but 0 and 2 to 2: Claude Code
// treats only exit 2 as "blocked", so a missing binary (127), a signal or a crash must never let the call through.
const guardWrap = `b=$1; h=$2; shift 2; "$b" guard --home "$h" "$@"; rc=$?; ` +
	`if [ "$rc" != 0 ] && [ "$rc" != 2 ]; then echo "hivemill guard failed ($rc); blocking" >&2; rc=2; fi; exit "$rc"`

// StandaloneGuardCommand is the hook command prefix that replaces `sh <home>/roles/hooks/worker-guard`; the role
// arguments of the template follow it.
func StandaloneGuardCommand(bin, home string) string {
	return "sh -c " + shlex.Quote(guardWrap) + " hivemill-guard " + shlex.Quote(bin) + " " + shlex.Quote(home)
}

// standaloneGuard rewrites the guard hook of rendered settings text (JSON) when the home has no shim. It returns the
// text unchanged when the shim exists, and an error when the shim cannot be checked or the binary is unknown.
func standaloneGuard(text, home string) (string, error) {
	_, err := os.Stat(GuardShim(home))
	if err == nil {
		return text, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("cannot check the guard shim %s: %w", GuardShim(home), err)
	}
	bin, err := SelfBinary()
	if err != nil || !filepath.IsAbs(bin) {
		return "", fmt.Errorf("the guard hook needs the absolute path of the hivemill binary: %v", err)
	}
	o, n := jsonInner("sh "+GuardShim(home)+" "), jsonInner(StandaloneGuardCommand(bin, home)+" ")
	if !strings.Contains(text, o) {
		return text, nil // a template without the guard hook
	}
	return strings.ReplaceAll(text, o, n), nil
}

// jsonInner is s as a JSON string without the quotes (no HTML escaping).
func jsonInner(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSuffix(b.String(), "\n")
	return out[1 : len(out)-1]
}

// PrePushHook is the git pre-push hook of a task clone: the public-text check of what the push publishes
// (`hivemill check public-text`, the Go port of ci/pre_push_public_text.py, same arguments and exit codes). git treats
// any non-zero exit, a missing binary included, as a refused push.
func PrePushHook(home string) (string, error) {
	bin, err := SelfBinary()
	if err != nil || !filepath.IsAbs(bin) {
		return "", fmt.Errorf("the pre-push hook needs the absolute path of the hivemill binary: %v", err)
	}
	return "#!/bin/sh\n# factory pre-push public-text guard (G-021 T-0696): hivemill check public-text\nexec " + shlex.Quote(bin) +
		" check public-text --home " + shlex.Quote(home) + " \"$@\"\n", nil
}
