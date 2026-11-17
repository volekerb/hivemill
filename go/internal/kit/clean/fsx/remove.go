package fsx

import (
	"errors"
	"fmt"
)

// ErrChanged means the item no longer matches the planned identity.
var ErrChanged = errors.New("fsx: item changed since plan")

// Remove deletes rel inside the root if its identity still equals want.
// A symlink is removed as a link only. It returns the bytes freed, measured
// before the delete. The root itself cannot be removed.
func Remove(root *Root, rel string, want Identity) (int64, error) {
	c, err := cleanRel(rel)
	if err != nil {
		return 0, err
	}
	if c == "." {
		return 0, fmt.Errorf("%w: cannot remove the root itself", ErrEscape)
	}
	fi, err := root.r.Lstat(c)
	if err != nil {
		return 0, err
	}
	if !Same(identityOf(fi), want) {
		return 0, fmt.Errorf("%w: %s", ErrChanged, c)
	}
	sz, err := Size(root, c)
	if err != nil {
		return 0, err
	}
	if err := root.r.RemoveAll(c); err != nil {
		return 0, err
	}
	return sz.Bytes, nil
}
