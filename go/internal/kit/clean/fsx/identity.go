package fsx

import (
	"io/fs"
	"syscall"
)

// Identity pins an item so apply can tell it is still the one that was planned.
type Identity struct {
	Dev     uint64
	Ino     uint64
	MtimeNs int64
	Size    int64
}

// devOf extracts (dev, ino) from a FileInfo. It is a variable so tests can
// simulate a different filesystem.
var devOf = func(fi fs.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // int32 on darwin
}

func inoOf(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino) //nolint:unconvert
	}
	return 0
}

func identityOf(fi fs.FileInfo) Identity {
	dev, _ := devOf(fi)
	return Identity{Dev: dev, Ino: inoOf(fi), MtimeNs: fi.ModTime().UnixNano(), Size: fi.Size()}
}

// Identity lstat-s rel inside the root and returns its identity.
func (r *Root) Identity(rel string) (Identity, error) {
	c, err := cleanRel(rel)
	if err != nil {
		return Identity{}, err
	}
	fi, err := r.r.Lstat(c)
	if err != nil {
		return Identity{}, err
	}
	return identityOf(fi), nil
}

// Same reports whether two identities are equal.
func Same(a, b Identity) bool { return a == b }
