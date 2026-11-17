//go:build linux || darwin

package fsx

import "syscall"

// DiskFree returns the total and available bytes of the filesystem holding path.
func DiskFree(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)                                     //nolint:unconvert // width differs per OS
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs, nil //nolint:unconvert
}
