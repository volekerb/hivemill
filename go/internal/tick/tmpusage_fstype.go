package tick

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	"factory/go/internal/store"
)

const darwinStatTimeout = 10 * time.Second

// darwinStat is DARWIN_STAT; tmpGOOS is sys.platform. Variables so tests can drive the darwin branch on Linux.
var (
	darwinStat = "/usr/bin/stat"
	tmpGOOS    = runtime.GOOS
)

// darwinFsType is the macOS branch of tmp_usage: statvfs does not name the fs type there, so `stat -f %T <tmp>`
// (apfs, hfs, tmpfs...) with the fixed tool env and a 10 s timeout. "" is Python None (failure or empty output).
func darwinFsType(tmp string) string {
	target := tmp
	if tmp == "/tmp" {
		target = "/private/tmp"
	}
	ctx, cancel := context.WithTimeout(context.Background(), darwinStatTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, darwinStat, "-f", "%T", target)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return store.PyStrip(string(out))
}
