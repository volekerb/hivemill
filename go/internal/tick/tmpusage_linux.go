//go:build linux

package tick

import "syscall"

// statvfs is os.statvfs: total and free-for-unprivileged bytes of the filesystem holding path.
func statvfs(path string) (total, free int64, err error) {
	var v syscall.Statfs_t
	if err = syscall.Statfs(path, &v); err != nil {
		return 0, 0, err
	}
	return int64(v.Blocks) * int64(v.Frsize), int64(v.Bavail) * int64(v.Frsize), nil
}
