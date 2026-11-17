//go:build linux

package sandbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Stage-1 setup (root inside the user namespace): bring lo up, start the loopback forwarder, hide the secret dirs
// under tmpfs, bind back only what the cfg lists, then exec stage 2. Every mount target is opened component by
// component with O_PATH|O_NOFOLLOW (through /proc/self/fd/<dirfd>/<name>, the stdlib has no openat), and mounts go
// onto /proc/self/fd/<fd>, so a symlink swapped in after the walk cannot redirect them. Any failure returns an error
// before exec: never a partial view.

const (
	msBind    = syscall.MS_BIND
	msRemount = syscall.MS_REMOUNT
	msRdonly  = syscall.MS_RDONLY
	lockedMnt = 0x3E // MS_NOSUID|MS_NODEV|MS_NOEXEC|MS_SYNCHRONOUS|MS_MANDLOCK: kept on a read-only remount
	oPath     = 0x200000
)

func fdp(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }

func mount(src, target, fstype string, flags uintptr, data string) error {
	if err := syscall.Mount(src, target, fstype, flags, data); err != nil {
		return fmt.Errorf("mount %s -> %s: %w", src, target, err)
	}
	return nil
}

// loUp sets IFF_UP on lo over rtnetlink (no ioctl: that needs unsafe).
func loUp() error {
	ifc, err := net.InterfaceByName("lo")
	if err != nil {
		return err
	}
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	msg := make([]byte, 32)
	le := binary.LittleEndian
	le.PutUint32(msg[0:], 32)
	le.PutUint16(msg[4:], syscall.RTM_NEWLINK)
	le.PutUint16(msg[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_ACK)
	le.PutUint32(msg[8:], 1)
	le.PutUint32(msg[20:], uint32(ifc.Index)) // ifinfomsg.ifi_index
	le.PutUint32(msg[24:], syscall.IFF_UP)    // ifi_flags
	le.PutUint32(msg[28:], syscall.IFF_UP)    // ifi_change
	if err := syscall.Sendto(fd, msg, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, _, err := syscall.Recvfrom(fd, buf, 0)
	if err != nil {
		return err
	}
	if n >= 20 && le.Uint16(buf[4:]) == syscall.NLMSG_ERROR {
		if code := int32(le.Uint32(buf[16:])); code != 0 {
			return fmt.Errorf("lo up: %w", syscall.Errno(-code))
		}
	}
	return nil
}

func startForwarder(c *Cfg) error {
	args := append(append([]string{}, DropCaps...), c.Self, "forward", *c.ForwardSock)
	cmd := exec.Command(c.Setpriv, args...)
	cmd.Dir = "/"
	cmd.Env = []string{}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nsbox: forwarder did not start: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	for i := 0; i < 100; i++ {
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:3128", 200*time.Millisecond); err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-exited:
			return errors.New("nsbox: forwarder did not start")
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("nsbox: forwarder did not start")
}

// openChain opens a bind source component by component. It returns (fd, "") or (-1, target) when the leaf is a
// symlink (the documented claude link). A symlink or non-directory parent component, a missing source: error.
func openChain(path string) (int, string, error) {
	names := splitPath(path)
	cur, err := syscall.Open("/", oPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	walked := ""
	for i, name := range names {
		walked += "/" + name
		nxt, err := syscall.Open(fdp(cur)+"/"+name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			syscall.Close(cur)
			if errors.Is(err, syscall.ENOENT) {
				return -1, "", fmt.Errorf("nsbox: bind source missing: %s", path)
			}
			return -1, "", fmt.Errorf("nsbox: cannot open bind source component %s: %w", walked, err)
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(nxt, &st); err != nil {
			syscall.Close(nxt)
			syscall.Close(cur)
			return -1, "", err
		}
		isLnk := st.Mode&syscall.S_IFMT == syscall.S_IFLNK
		if i == len(names)-1 {
			if isLnk {
				buf := make([]byte, 4096)
				n, err := syscall.Readlink(fdp(cur)+"/"+name, buf)
				syscall.Close(nxt)
				syscall.Close(cur)
				if err != nil {
					return -1, "", err
				}
				return -1, string(buf[:n]), nil
			}
			syscall.Close(cur)
			return nxt, "", nil
		}
		if isLnk || st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
			syscall.Close(nxt)
			syscall.Close(cur)
			return -1, "", fmt.Errorf("nsbox: bind source %s has a symlink or non-directory parent component: %s", path, walked)
		}
		syscall.Close(cur)
		cur = nxt
	}
	return cur, "", nil // path was "/"
}

// openTarget opens a mount target (or "link-parent": the parent of a symlink) with no-follow, creating missing
// directories on the chain fd. leaf is "dir" or "file" to create a missing leaf, "link-parent", or "" to require it.
func openTarget(path, leaf string) (int, error) {
	names := splitPath(path)
	cur, err := syscall.Open("/", oPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	walked := ""
	for i, name := range names {
		walked += "/" + name
		last := i == len(names)-1
		if last && leaf == "link-parent" {
			return cur, nil
		}
		p := fdp(cur) + "/" + name
		nxt, err := syscall.Open(p, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.ENOENT) {
			if last && leaf == "" {
				syscall.Close(cur)
				return -1, fmt.Errorf("nsbox: mount target missing: %s", path)
			}
			if !last || leaf == "dir" {
				err = syscall.Mkdir(p, 0o755)
			} else {
				var cfd int
				cfd, err = syscall.Open(p, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
				if err == nil {
					syscall.Close(cfd)
				}
			}
			if err == nil {
				nxt, err = syscall.Open(p, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			}
		}
		if err != nil {
			syscall.Close(cur)
			return -1, fmt.Errorf("nsbox: cannot open or create mount target component %s: %w", walked, err)
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(nxt, &st); err != nil {
			syscall.Close(nxt)
			syscall.Close(cur)
			return -1, err
		}
		if st.Mode&syscall.S_IFMT == syscall.S_IFLNK || (!last && st.Mode&syscall.S_IFMT != syscall.S_IFDIR) {
			syscall.Close(nxt)
			syscall.Close(cur)
			return -1, fmt.Errorf("nsbox: mount target %s has a symlink or non-directory component: %s", path, walked)
		}
		syscall.Close(cur)
		cur = nxt
	}
	return cur, nil
}

func fdIsDir(fd int) bool {
	var st syscall.Stat_t
	return syscall.Fstat(fd, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFDIR
}

func roRemount(target string) error {
	var sfs syscall.Statfs_t
	if err := syscall.Statfs(target, &sfs); err != nil {
		return err
	}
	return mount("", target, "", msBind|msRemount|msRdonly|(uintptr(sfs.Flags)&lockedMnt), "")
}

func bindOnto(srcFd int, path string, ro bool, leaf string) error {
	tfd, err := openTarget(path, leaf)
	if err != nil {
		return err
	}
	err = mount(fdp(srcFd), fdp(tfd), "", msBind, "")
	syscall.Close(tfd)
	if err != nil || !ro {
		return err
	}
	mfd, err := openTarget(path, "")
	if err != nil {
		return err
	}
	defer syscall.Close(mfd)
	return roRemount(fdp(mfd))
}

func tmpfsOnto(path, opts string) error {
	fd, err := openTarget(path, "")
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return mount("tmpfs", fdp(fd), "tmpfs", 0, opts)
}

// applyAllow executes PlanAllow. Sources are opened BEFORE any tmpfs goes on: a bind from /proc/self/fd survives it.
func applyAllow(c *Cfg) (dirs, keeps []string, err error) {
	ops, err := PlanAllow(c)
	if err != nil {
		return nil, nil, err
	}
	type src struct {
		fd   int
		link string
	}
	srcs := map[string]src{}
	var dirFds []int
	defer func() {
		for _, s := range srcs {
			if s.fd >= 0 {
				syscall.Close(s.fd)
			}
		}
		for _, fd := range dirFds {
			syscall.Close(fd)
		}
	}()
	for _, op := range ops {
		switch op.Kind {
		case OpTmpfs:
			fd, err := openTarget(op.Path, "")
			if err != nil {
				return nil, nil, err
			}
			dirFds = append(dirFds, fd)
			if !fdIsDir(fd) {
				return nil, nil, fmt.Errorf("nsbox: hide dir %s is not a directory any more", op.Path)
			}
			dirs = append(dirs, op.Path)
		case OpBindRW, OpBindRO:
			fd, link, err := openChain(op.Path)
			if err != nil {
				return nil, nil, err
			}
			srcs[op.Path] = src{fd, link}
			keeps = append(keeps, op.Path)
		case OpMask:
			fd, err := openTarget(op.Path, "")
			if err != nil {
				return nil, nil, err
			}
			syscall.Close(fd)
			dirs = append(dirs, op.Path)
		}
	}
	di := 0
	for _, op := range ops {
		opts := "mode=0755,size=" + op.Size
		switch op.Kind {
		case OpTmpfs:
			if err := mount("tmpfs", fdp(dirFds[di]), "tmpfs", 0, opts); err != nil {
				return nil, nil, fmt.Errorf("nsbox: allow view failed: %w", err)
			}
			di++
		case OpMask:
			if err := tmpfsOnto(op.Path, opts); err != nil {
				return nil, nil, fmt.Errorf("nsbox: allow view failed: %w", err)
			}
		case OpBindRW, OpBindRO:
			s := srcs[op.Path]
			if s.fd < 0 { // a symlink leaf stays a link; only recreate it where a tmpfs ate it
				pfd, err := openTarget(op.Path, "link-parent")
				if err != nil {
					return nil, nil, fmt.Errorf("nsbox: allow view failed: %w", err)
				}
				lp := fdp(pfd) + "/" + filepath.Base(op.Path)
				if _, serr := os.Lstat(lp); errors.Is(serr, fs.ErrNotExist) {
					err = syscall.Symlink(s.link, lp)
				}
				syscall.Close(pfd)
				if err != nil {
					return nil, nil, fmt.Errorf("nsbox: allow view failed: %w", err)
				}
				continue
			}
			leaf := "file"
			if fdIsDir(s.fd) {
				leaf = "dir"
			}
			if err := bindOnto(s.fd, op.Path, op.Kind == OpBindRO, leaf); err != nil {
				return nil, nil, fmt.Errorf("nsbox: allow view failed: %w", err)
			}
		case OpHideFile:
			ffd, err := openTarget(op.Path, "")
			if err != nil {
				return nil, nil, err
			}
			if fdIsDir(ffd) {
				syscall.Close(ffd)
				return nil, nil, fmt.Errorf("nsbox: hide file %s is a directory now", op.Path)
			}
			err = mount("/dev/null", fdp(ffd), "", msBind, "")
			syscall.Close(ffd)
			if err != nil {
				return nil, nil, fmt.Errorf("nsbox: allow view failed: %w", err)
			}
		}
	}
	return dirs, keeps, nil
}

// siblingTrials is the realpath of every other trial dir under trials_root (both sides resolved).
func siblingTrials(c *Cfg) []string {
	root, own := c.TrialsRoot, Realpath(c.Trial)
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Type()&fs.ModeSymlink == 0 && e.IsDir() {
			if p := Realpath(filepath.Join(root, e.Name())); p != own {
				out = append(out, p)
			}
		}
	}
	return out
}

// applyDeny is nsbox._hide_and_keep: tmpfs over the secret dirs, the kept paths bound back from fds opened first.
func applyDeny(c *Cfg) (dirs, keeps []string, err error) {
	dirs = denyDirs(c, siblingTrials(c))
	for _, k := range append(append([]string{}, c.Keep...), c.KeepRO...) {
		keeps = append(keeps, RealLeaf(k))
	}
	var ro []string
	for _, k := range c.KeepRO {
		ro = append(ro, RealLeaf(k))
	}
	type saved struct {
		path, link string
		fd         int
	}
	var sv []saved
	for _, k := range keeps {
		hidden := false
		for _, d := range dirs {
			hidden = hidden || under(k, d)
		}
		if _, serr := os.Lstat(k); !hidden || serr != nil {
			continue
		}
		if isLink(k) {
			l, _ := os.Readlink(k)
			sv = append(sv, saved{k, l, -1})
		} else {
			fd, err := syscall.Open(k, oPath|syscall.O_CLOEXEC, 0)
			if err != nil {
				return nil, nil, err
			}
			sv = append(sv, saved{k, "", fd})
		}
	}
	for _, d := range dirs {
		if err := mount("tmpfs", d, "tmpfs", 0, "mode=0755,size="+TmpfsSize); err != nil {
			return nil, nil, err
		}
	}
	for _, s := range sv {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			return nil, nil, err
		}
		if s.fd < 0 {
			if err := os.Symlink(s.link, s.path); err != nil {
				return nil, nil, err
			}
			continue
		}
		if fdIsDir(s.fd) {
			err = os.MkdirAll(s.path, 0o755)
		} else if f, ferr := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			f.Close()
		} else {
			err = ferr
		}
		if err == nil {
			err = mount(fdp(s.fd), s.path, "", msBind, "")
		}
		syscall.Close(s.fd)
		if err != nil {
			return nil, nil, err
		}
	}
	for _, k := range ro {
		if err := mount(k, k, "", msBind, ""); err != nil {
			return nil, nil, err
		}
		if err := roRemount(k); err != nil {
			return nil, nil, err
		}
	}
	for _, f := range c.HideFiles {
		if isFile(f) || isLink(f) {
			if err := mount("/dev/null", f, "", msBind, ""); err != nil {
				return nil, nil, err
			}
		}
	}
	return dirs, keeps, nil
}

// settleCwd re-resolves the inherited cwd through the new mounts when it is visible, else lands in the fallback:
// the inherited cwd pins the pre-mount dentry, so `..` and /proc/1/cwd would reach the real tree.
func settleCwd(c *Cfg, want string, dirs, keeps []string) {
	fallback := c.Trial
	if c.Mode == "allow" {
		fallback = "/"
		if len(c.Rw) > 0 {
			fallback = c.Rw[0]
		}
	}
	hidden := false
	for _, d := range dirs {
		hidden = hidden || under(want, d)
	}
	for _, k := range keeps {
		if under(want, k) {
			hidden = false
		}
	}
	if hidden || want == "" {
		_ = os.Chdir(fallback)
		return
	}
	if err := os.Chdir(want); err != nil {
		_ = os.Chdir(fallback)
	}
}

// TreeBytes is the bytes of the regular files under path (links not followed); it sizes the tmpfs copy.
func TreeBytes(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, err
}

// copyTree is subuid mode: a private tmpfs over the tree, filled from the read-only original (reached through the
// fd opened before the tmpfs went on), owned by the box uid. Failure returns an error before exec.
func copyTree(c *Cfg) error {
	tree := c.Copy.Tree
	srcFd, err := openTarget(tree, "")
	if err != nil {
		return fmt.Errorf("nsbox: copy of %s failed: %w", tree, err)
	}
	defer syscall.Close(srcFd)
	dstFd, err := openTarget(tree, "")
	if err != nil {
		return fmt.Errorf("nsbox: copy of %s failed: %w", tree, err)
	}
	defer syscall.Close(dstFd)
	fail := func(e error) error {
		return fmt.Errorf("nsbox: copy of %s into the private tmpfs failed: %v; the command was not run", tree, e)
	}
	if err := mount("tmpfs", fdp(dstFd), "tmpfs", 0, fmt.Sprintf("mode=0755,uid=%d,gid=%d,size=%s", SubUID, SubGID, c.Copy.Size)); err != nil {
		return fail(err)
	}
	srcRoot := fdp(srcFd)
	err = filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, _ := filepath.Rel(srcRoot, p)
		dst := filepath.Join(tree, rel)
		switch t := d.Type(); {
		case t.IsDir():
			if rel == "." {
				return nil
			}
			return os.MkdirAll(dst, 0o755)
		case t&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, dst)
		case t.IsRegular():
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			fi, _ := in.Stat()
			out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
		return nil // sockets, fifos and devices are not copied
	})
	if err != nil {
		return fail(err)
	}
	for _, d := range c.Copy.Mkdirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail(err)
		}
	}
	if err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		return os.Lchown(p, SubUID, SubGID)
	}); err != nil {
		return fail(err)
	}
	return nil
}

// Setup is stage 1: it returns only on error, otherwise it execs stage 2.
func Setup(c *Cfg, args []string) error {
	if !c.Net {
		if err := loUp(); err != nil {
			return fmt.Errorf("nsbox: lo up: %w", err)
		}
	}
	if c.ForwardSock != nil {
		if err := startForwarder(c); err != nil {
			return err
		}
	}
	want := ""
	if c.Cwd != nil {
		want = Realpath(*c.Cwd)
	} else if wd, err := os.Getwd(); err == nil {
		want = Realpath(wd)
	}
	var dirs, keeps []string
	var err error
	if c.Mode == "allow" {
		dirs, keeps, err = applyAllow(c)
	} else {
		dirs, keeps, err = applyDeny(c)
	}
	if err != nil {
		return err
	}
	if c.Copy != nil {
		if err := copyTree(c); err != nil {
			return err
		}
	}
	settleCwd(c, want, dirs, keeps)
	env := os.Environ()
	drop := map[string]bool{}
	for _, k := range c.Scrub {
		drop[k] = true
	}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, set := c.EnvSet[k]; !drop[k] && !set {
			out = append(out, kv)
		}
	}
	keysSet := make([]string, 0, len(c.EnvSet))
	for k := range c.EnvSet {
		keysSet = append(keysSet, k)
	}
	sort.Strings(keysSet)
	for _, k := range keysSet {
		out = append(out, k+"="+c.EnvSet[k])
	}
	argv := ComposeStage2(c, args)
	return syscall.Exec(argv[0], argv, out)
}
