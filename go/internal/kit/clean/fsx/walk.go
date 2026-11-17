package fsx

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"time"
)

// SkipMountPoint is the Skipped reason for a directory on another filesystem.
const SkipMountPoint = "mount point"

// Entry is one item reported by Walk.
type Entry struct {
	Rel       string      // root-relative, slash separated
	Info      fs.FileInfo // from lstat
	IsSymlink bool        // a symlink is a leaf; its target is never opened
	Skipped   string      // non-empty: not descended, with the reason
}

// Walk visits rel and everything below it using lstat only. A symlink is
// reported as a leaf and never opened. A directory whose st_dev differs from
// the root's is reported once with Skipped == "mount point" and not entered.
// rel must be relative and free of "..". fn may return fs.SkipDir to not
// descend into a directory; any other error stops the walk.
func Walk(root *Root, rel string, fn func(Entry) error) error {
	c, err := cleanRel(rel)
	if err != nil {
		return err
	}
	fi, err := root.r.Lstat(c)
	if err != nil {
		return err
	}
	err = walk(root, c, fi, fn)
	if errors.Is(err, fs.SkipDir) {
		return nil
	}
	return err
}

func walk(root *Root, rel string, fi fs.FileInfo, fn func(Entry) error) error {
	e := Entry{Rel: rel, Info: fi, IsSymlink: fi.Mode()&fs.ModeSymlink != 0}
	if fi.IsDir() && rel != "." {
		if dev, ok := devOf(fi); ok && dev != root.dev {
			e.Skipped = SkipMountPoint
			return fn(e)
		}
	}
	if err := fn(e); err != nil {
		return err
	}
	if !fi.IsDir() || e.IsSymlink {
		return nil
	}
	d, err := root.r.Open(rel)
	if err != nil {
		return err
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, de := range ents {
		child := path.Join(rel, de.Name())
		cfi, err := root.r.Lstat(child)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // vanished while walking
			}
			return err
		}
		if err := walk(root, child, cfi, fn); err != nil {
			if errors.Is(err, fs.SkipDir) {
				continue
			}
			return err
		}
	}
	return nil
}

// SizeInfo is the result of Size.
type SizeInfo struct {
	Bytes  int64
	Files  int64
	Newest time.Time
}

// Size sums lstat sizes under rel (symlinks count as their own lstat size,
// mount points are not entered) and reports the file count and newest mtime.
func Size(root *Root, rel string) (SizeInfo, error) {
	var s SizeInfo
	err := Walk(root, rel, func(e Entry) error {
		if e.Skipped != "" {
			return nil
		}
		if e.Info.Mode().IsDir() {
			return nil
		}
		s.Bytes += e.Info.Size()
		s.Files++
		if m := e.Info.ModTime(); m.After(s.Newest) {
			s.Newest = m
		}
		return nil
	})
	return s, err
}
