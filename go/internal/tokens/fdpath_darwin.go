//go:build darwin

package tokens

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	fGetPath = 50   // F_GETPATH, <sys/fcntl.h>
	maxPath  = 1024 // MAXPATHLEN
)

// fdPath is the real path of an open fd: fcntl(fd, F_GETPATH) into a MAXPATHLEN buffer. x/sys is not a direct
// dependency of this module, so the call goes through syscall. An error or an empty path fails the caller closed.
func fdPath(fd int) (string, error) {
	var buf [maxPath]byte
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fGetPath, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return "", e
	}
	for i, c := range buf {
		if c == 0 {
			if i == 0 {
				return "", fmt.Errorf("F_GETPATH: empty path")
			}
			return string(buf[:i]), nil
		}
	}
	return "", fmt.Errorf("F_GETPATH: path not terminated")
}
