package sandbox

import (
	"fmt"
	"path/filepath"
	"sort"
)

// OpKind is one step of the allow-list view.
type OpKind string

const (
	OpTmpfs    OpKind = "tmpfs"     // empty tmpfs over a hide tree
	OpMask     OpKind = "mask"      // tmpfs over a masked dir
	OpBindRW   OpKind = "bind-rw"   // writable bind of a listed path
	OpBindRO   OpKind = "bind-ro"   // read-only bind (nosuid/nodev/noexec kept)
	OpHideFile OpKind = "hide-file" // /dev/null over a secret file
)

// Op is one mount of the allow-list view.
type Op struct {
	Kind OpKind
	Path string
	Size string // tmpfs size= for OpTmpfs and OpMask
}

func depth(p string) int { return len(splitPath(p)) }

// PlanAllow is the ordered mount plan of nsbox._allow_view, without touching any mount: a tmpfs over every hide
// tree, then only what the caller listed bound back, shorter paths first (an ro parent before its rw child, a mask
// after its parent and before its re-bound children), then /dev/null over the hide files. The executor walks it
// as-is; any failing step aborts before exec, never a partial view.
func PlanAllow(c *Cfg) ([]Op, error) {
	rw := make([]string, 0, len(c.Rw))
	for _, k := range c.Rw {
		rw = append(rw, Abs(k))
	}
	ro := make([]string, 0, len(c.Ro))
	for _, k := range c.Ro {
		ro = append(ro, Abs(k))
	}
	for _, p := range rw {
		if contains(ro, p) {
			return nil, fmt.Errorf("nsbox: path listed both rw and ro: %s", p)
		}
	}
	masks := make([]string, 0, len(c.Masks))
	for _, m := range c.Masks {
		masks = append(masks, Abs(m))
	}
	size := c.TmpfsSize
	if size == "" {
		size = TmpfsSize
	}
	sizes := map[string]string{}
	for k, v := range c.TmpfsSizes {
		sizes[Realpath(k)] = v
	}
	sizeOf := func(p string) string {
		if s, ok := sizes[p]; ok {
			return s
		}
		return size
	}
	var dirs []string
	for _, d := range c.HideDirs {
		d = Abs(d)
		if (isDir(d) || isLink(d)) && d != "/" {
			dirs = append(dirs, d)
		}
	}
	dirs = dedupe(dirs)
	var top []string
	for _, d := range dirs {
		nested := false
		for _, o := range dirs {
			if o != d && under(d, o) {
				nested = true
			}
		}
		if !nested {
			top = append(top, d)
		}
	}
	dirs = top
	for _, m := range masks {
		if !isDir(m) {
			return nil, fmt.Errorf("nsbox: mask dir missing or not a directory: %s", m)
		}
	}
	type entry struct {
		path string
		kind OpKind
	}
	var entries []entry
	for _, k := range rw {
		entries = append(entries, entry{k, OpBindRW})
	}
	for _, k := range ro {
		entries = append(entries, entry{k, OpBindRO})
	}
	for _, m := range masks {
		entries = append(entries, entry{m, OpMask})
	}
	sort.SliceStable(entries, func(i, j int) bool { return depth(entries[i].path) < depth(entries[j].path) })
	var ops []Op
	for _, d := range dirs {
		ops = append(ops, Op{OpTmpfs, d, sizeOf(d)})
	}
	for _, e := range entries {
		op := Op{Kind: e.kind, Path: e.path}
		if e.kind == OpMask {
			op.Size = sizeOf(e.path)
		}
		ops = append(ops, op)
	}
	for _, f := range c.HideFiles {
		f = Abs(f)
		if isFile(f) || isLink(f) {
			ops = append(ops, Op{Kind: OpHideFile, Path: f})
		}
	}
	return ops, nil
}

// Writable lists the paths the plan binds writable: the only host paths a box can write besides its own tmpfs.
func Writable(ops []Op) []string {
	var out []string
	for _, o := range ops {
		if o.Kind == OpBindRW {
			out = append(out, o.Path)
		}
	}
	return out
}

// RefuseWritable is the fail-closed check behind TestMountsReadOnly: an error when any writable bind lies at or above
// one of the protected trees (the live factory home, its installed lib, the real home), because that would make the
// whole tree writable. A writable path strictly inside one (the db dir, a worktree) is a listed, deliberate bind.
func RefuseWritable(ops []Op, protected []string) error {
	for _, w := range Writable(ops) {
		for _, p := range protected {
			p = filepath.Clean(p)
			if under(p, w) {
				return fmt.Errorf("sandbox: writable bind %s is at or above the protected tree %s", w, p)
			}
		}
	}
	return nil
}

// denyDirs is the deny-mode set of dirs to hide (real, outermost only) for the given cfg and sibling trial dirs.
func denyDirs(c *Cfg, siblings []string) []string {
	var dirs []string
	for _, d := range c.HideDirs {
		if isDir(d) {
			if r := Realpath(d); r != "/" {
				dirs = append(dirs, r)
			}
		}
	}
	dirs = dedupe(append(dirs, siblings...))
	var out []string
	for _, d := range dirs {
		nested := false
		for _, o := range dirs {
			if o != d && under(d, o) {
				nested = true
			}
		}
		if !nested {
			out = append(out, d)
		}
	}
	return out
}
