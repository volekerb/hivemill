//go:build linux

package tokens

import (
	"fmt"
	"os"
)

// fdPath is the real path of an open fd: the /proc/self/fd link.
func fdPath(fd int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
}
