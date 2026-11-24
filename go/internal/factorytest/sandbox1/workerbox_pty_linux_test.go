//go:build linux

package sandbox1

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func wbGetsid(pid int) int {
	r, _, _ := syscall.Syscall(syscall.SYS_GETSID, uintptr(pid), 0, 0)
	return int(r)
}

func wbTcgetpgrp(fd int) int {
	var pg int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGPGRP, uintptr(unsafe.Pointer(&pg))); e != 0 {
		return -int(e)
	}
	return int(pg)
}

func wbIoctl(t testing.TB, f *os.File, req uintptr, arg unsafe.Pointer) {
	t.Helper()
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(arg)); e != 0 {
		t.Fatalf("ioctl %#x: %v", req, e)
	}
}

// wbPty runs argv with a pty as its controlling terminal (own session) and returns what it printed and its pid.
func wbPty(t testing.TB, argv []string, cwd string, env []string) (out string, pid int) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	defer master.Close()
	var unlock int32
	wbIoctl(t, master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock))
	var n uint32
	wbIoctl(t, master, syscall.TIOCGPTN, unsafe.Pointer(&n))
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open the pty slave: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0} // after setsid: the pty becomes the controlling tty
	wbMust(t, cmd.Start())
	slave.Close()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { // read until the child (the last holder of the slave) is gone: EIO
		defer close(done)
		b := make([]byte, 4096)
		for {
			k, err := master.Read(b)
			buf.Write(b[:k])
			if err != nil {
				return
			}
		}
	}()
	werr := cmd.Wait()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pty output did not end")
	}
	if werr != nil {
		t.Fatalf("pty run: %v\n%s", werr, buf.String())
	}
	return buf.String(), cmd.Process.Pid
}
