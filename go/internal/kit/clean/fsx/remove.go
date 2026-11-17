package fsx

import (
	"errors"
	"fmt"
)

// ErrChanged means the item no longer matches the planned identity.
var ErrChanged = errors.New("fsx: item changed since plan")

// ErrMount means the item is or contains a mount point (another filesystem).
var ErrMount = errors.New("fsx: item contains a mount point")

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
	var mount string
	if err := Walk(root, c, func(e Entry) error {
		if e.Skipped != "" && mount == "" {
			mount = e.Rel
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if mount != "" {
		return 0, fmt.Errorf("%w: %s", ErrMount, mount)
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
