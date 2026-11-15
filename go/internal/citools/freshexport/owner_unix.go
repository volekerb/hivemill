package freshexport

import (
	"io/fs"
	"syscall"
)

// ownerOf is the uid owning the file, or -1 when the platform does not say.
func ownerOf(st fs.FileInfo) int {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid)
	}
	return -1
}
