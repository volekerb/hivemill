//go:build darwin

package gate

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// statfsBytes is the total and available bytes of the filesystem holding dir.
func statfsBytes(dir string) (total, free int64, err error) {
	t, f, _, _, err := darwinStatfs(dir)
	return t, f, err
}

func cstr(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}

func darwinStatfs(dir string) (total, free int64, fstype, mount string, err error) {
	var v syscall.Statfs_t
	if err := syscall.Statfs(dir, &v); err != nil {
		return 0, 0, "", "", err
	}
	bs := int64(v.Bsize)
	return int64(v.Blocks) * bs, int64(v.Bavail) * bs, cstr(v.Fstypename[:]), cstr(v.Mntonname[:]), nil
}

func runCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

// NewHostFacts returns the readers of this OS. fakeStatvfs is ignored on macOS.
func NewHostFacts(_ string, _ int) HostFacts {
	return DarwinFacts{Run: runCmd, CPUs: runtime.NumCPU(), Statfs: darwinStatfs}
}
