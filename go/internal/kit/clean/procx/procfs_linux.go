//go:build linux

package procx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procfs reads a /proc tree through injectable file operations.
type procfs struct {
	root     string
	readlink func(string) (string, error)
	readDir  func(string) ([]os.DirEntry, error)
	readFile func(string) ([]byte, error)
	euid     int
	owner    func(string) (int, error) // uid owning a /proc/<pid> directory
}

func realProcfs() procfs {
	return procfs{root: "/proc", readlink: os.Readlink, readDir: os.ReadDir, readFile: os.ReadFile, euid: os.Geteuid(), owner: dirOwner}
}

// CwdInside is Fail if any visible process has its cwd at or under dir.
func CwdInside(dir string) Result { return realProcfs().scan(dir, false) }

// HeldOpen is Fail if any visible process has a file open at or under path.
// Only open file descriptors count (not mmaps or running executables).
func HeldOpen(path string) Result { return realProcfs().scan(path, true) }

// Processes lists every process from /proc.
func Processes() ([]Process, error) { return realProcfs().processes() }

func dirOwner(path string) (int, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("no stat_t for " + path)
	}
	return int(st.Uid), nil
}

func gone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func (p procfs) pids() ([]int, error) {
	ents, err := p.readDir(p.root)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, n)
		}
	}
	return pids, nil
}

// kernelThread reports whether pid is provably a kernel thread (pid 2 or
// child of 2); those have no user cwd or fds, so an unreadable link is ruled out.
func (p procfs) kernelThread(pid int) bool {
	if pid == 2 {
		return true
	}
	b, err := p.readFile(filepath.Join(p.root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	st, err := parseStat(b)
	return err == nil && st.ppid == 2
}

// foreignUID reports whether pid is provably not ours (another uid or non-dumpable) while we are
// not root. Such a process is outside what we can see (the kernel denies its
// /proc links), so a permission error on it is ruled out. As root nothing is
// foreign: every permission error stays Unknown.
func (p procfs) foreignUID(pid int) bool {
	if p.euid == 0 {
		return false
	}
	// The kernel makes root-owned cwd and fd links for a non-dumpable process (e.g.
	// a privilege-separated sshd child) even when its Uid is ours; it is just
	// as unreadable, so that counts as foreign too.
	for _, e := range []string{"cwd", "fd"} {
		if o, err := p.owner(filepath.Join(p.root, strconv.Itoa(pid), e)); err == nil && o != p.euid {
			return true
		}
	}
	b, err := p.readFile(filepath.Join(p.root, strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Uid:"); ok {
			f := strings.Fields(v)
			if len(f) == 0 {
				return false
			}
			n, err := strconv.Atoi(f[0])
			return err == nil && n != p.euid
		}
	}
	return false
}

// scan checks cwd links (fds=false) or fd links (fds=true) against target.
func (p procfs) scan(target string, fds bool) Result {
	roots := candidates(target)
	pids, err := p.pids()
	if err != nil {
		return unknown("read " + p.root + ": " + err.Error())
	}
	what := "cwd"
	if fds {
		what = "fd"
	}
	var unread []string
	note := func(pid int, err error) {
		if gone(err) {
			return
		}
		if errors.Is(err, fs.ErrPermission) && (p.kernelThread(pid) || p.foreignUID(pid)) {
			return
		}
		unread = append(unread, fmt.Sprintf("pid %d %s: %v", pid, what, err))
	}
	for _, pid := range pids {
		base := filepath.Join(p.root, strconv.Itoa(pid))
		var links []string
		if fds {
			ents, err := p.readDir(filepath.Join(base, "fd"))
			if err != nil {
				note(pid, err)
				continue
			}
			for _, e := range ents {
				links = append(links, filepath.Join(base, "fd", e.Name()))
			}
		} else {
			links = []string{filepath.Join(base, "cwd")}
		}
		for _, l := range links {
			dest, err := p.readlink(l)
			if err != nil {
				note(pid, err)
				continue
			}
			if under(dest, roots) {
				return fail(fmt.Sprintf("pid %d %s %s", pid, what, dest))
			}
		}
	}
	if len(unread) > 0 {
		return unknown(fmt.Sprintf("%d unreadable (first: %s)", len(unread), unread[0]))
	}
	return pass()
}

type statInfo struct {
	comm  string
	ppid  int
	ticks uint64
}

// parseStat parses /proc/<pid>/stat; comm may itself contain spaces and parens.
func parseStat(b []byte) (statInfo, error) {
	s := string(b)
	o, c := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if o < 0 || c < o {
		return statInfo{}, fmt.Errorf("malformed stat %q", s)
	}
	f := strings.Fields(s[c+1:])
	if len(f) < 20 {
		return statInfo{}, fmt.Errorf("short stat %q", s)
	}
	ppid, e1 := strconv.Atoi(f[1])
	ticks, e2 := strconv.ParseUint(f[19], 10, 64)
	if e1 != nil || e2 != nil {
		return statInfo{}, fmt.Errorf("bad numbers in stat %q", s)
	}
	return statInfo{comm: s[o+1 : c], ppid: ppid, ticks: ticks}, nil
}

func (p procfs) processes() ([]Process, error) {
	pids, err := p.pids()
	if err != nil {
		return nil, err
	}
	sb, err := p.readFile(filepath.Join(p.root, "stat"))
	if err != nil {
		return nil, err
	}
	var btime int64 = -1
	for _, l := range strings.Split(string(sb), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			if btime, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
				return nil, err
			}
		}
	}
	if btime < 0 {
		return nil, errors.New("btime missing from " + p.root + "/stat")
	}
	var out []Process
	for _, pid := range pids {
		base := filepath.Join(p.root, strconv.Itoa(pid))
		b, err := p.readFile(filepath.Join(base, "stat"))
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		st, err := parseStat(b)
		if err != nil {
			return nil, err
		}
		status, err := p.readFile(filepath.Join(base, "status"))
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		uid := -1
		for _, l := range strings.Split(string(status), "\n") {
			if v, ok := strings.CutPrefix(l, "Uid:"); ok {
				f := strings.Fields(v)
				if len(f) == 0 {
					return nil, fmt.Errorf("pid %d: empty Uid line", pid)
				}
				if uid, err = strconv.Atoi(f[0]); err != nil {
					return nil, fmt.Errorf("pid %d: bad Uid: %w", pid, err)
				}
			}
		}
		if uid < 0 {
			return nil, fmt.Errorf("pid %d: no Uid in status", pid)
		}
		cl, err := p.readFile(filepath.Join(base, "cmdline"))
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var argv []string
		if len(cl) > 0 {
			argv = strings.Split(strings.TrimSuffix(string(cl), "\x00"), "\x00")
		}
		cwd, _ := p.readlink(filepath.Join(base, "cwd"))       // best effort; empty when unreadable
		start := time.Unix(btime+int64(st.ticks/100), 0).UTC() // USER_HZ is 100 on Linux
		out = append(out, Process{PID: pid, PPID: st.ppid, UID: uid,
			Start: start.Format(time.RFC3339), Comm: st.comm, Argv: argv, Cwd: cwd})
	}
	return out, nil
}
