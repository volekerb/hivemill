//go:build linux

package gowrap

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// landlockABI returns the kernel's Landlock ABI version, 0 when Landlock is not available.
func landlockABI() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	switch errno {
	case 0:
		return int(v), nil
	case unix.ENOSYS, unix.EOPNOTSUPP:
		return 0, nil
	}
	return 0, errno
}

// writeRights are the write-type FS rights the given ABI knows. Read and execute are not handled, so reads
// stay open everywhere.
func writeRights(abi int) uint64 {
	r := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		r |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		r |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		r |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return r
}

// fileRights are the handled rights that apply to a non-directory rule target.
func fileRights(handled uint64) uint64 {
	return handled & (unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV)
}

// landlockApply restricts the calling OS thread (and everything it execs): writes are allowed only beneath dirs
// (all handled rights) and on files (file-type rights). The caller must exec from this same locked thread.
func landlockApply(abi int, dirs, files []string) error {
	runtime.LockOSThread()
	handled := writeRights(abi)
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(NO_NEW_PRIVS): %w", err)
	}
	attr := struct{ handledFS uint64 }{handled} // the 8-byte ABI 1 prefix; the rest of the struct is zero
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	rfd := int(fd)
	defer unix.Close(rfd)
	add := func(path string, rights uint64) error {
		pfd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer unix.Close(pfd)
		pb := unix.LandlockPathBeneathAttr{Allowed_access: rights, Parent_fd: int32(pfd)}
		_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rfd), unix.LANDLOCK_RULE_PATH_BENEATH,
			uintptr(unsafe.Pointer(&pb)), 0, 0, 0)
		if errno != 0 {
			return fmt.Errorf("landlock_add_rule %s: %w", path, errno)
		}
		return nil
	}
	for _, d := range dirs {
		if err := add(d, handled); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := add(f, fileRights(handled)); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rfd), 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}
