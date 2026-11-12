package blockcond

import (
	"fmt"
	"os"
	"path/filepath"
)

func probeFilename(name string) (string, bool) {
	switch name {
	case "gh-worker-ro-token-mode-600":
		return ghWorkerTokenBase("ro"), true
	case "gh-worker-rw-token-mode-600":
		return ghWorkerTokenBase("rw"), true
	default:
		return "", false
	}
}

func ghWorkerTokenBase(kind string) string {
	return "gh-worker-" + kind + "-token"
}

// RunCheckProbe reports whether an allowlisted check probe is satisfied.
// It only Lstats the path; it never reads file bytes.
func RunCheckProbe(name string) (bool, error) {
	base, ok := probeFilename(name)
	if !ok {
		return false, fmt.Errorf("unknown probe: %s", name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("home dir: %w", err)
	}
	path := filepath.Join(home, ".config", "factory", base)
	st, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("lstat %s: %w", path, err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return false, nil
	}
	if st.Mode().Perm()&0o777 != 0o600 {
		return false, nil
	}
	return true, nil
}
