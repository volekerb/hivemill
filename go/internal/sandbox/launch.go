package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// Subuid stage 0: a new user namespace whose ids newuidmap/newgidmap map as 0 -> owner, 1.. -> the subuid range,
// then the stage-1 unshare runs inside it. Any failure returns non-zero and never runs the command.

// launchChildArg is the hidden stage-0 child: it waits for the parent's go byte on fd 3 and execs stage 1.
const launchChildArg = "--launch-child"

// SubuidAvailable reports whether this box can map a subordinate uid range. It fails closed: any doubt is
// (false, why); the caller must not fall back to the owner uid.
func SubuidAvailable(unshare, self string) (bool, string) {
	for _, tool := range []string{"newuidmap", "newgidmap"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false, tool + " not found (install the uidmap package)"
		}
	}
	name := ""
	if u, err := user.LookupId(strconv.Itoa(os.Getuid())); err == nil {
		name = u.Username
	}
	for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
		if _, n := SubidRange(f, name, os.Getuid()); n < 2 {
			return false, fmt.Sprintf("no subordinate id range for %s in %s (usermod --add-subuids/--add-subgids)", name, f)
		}
	}
	if ok, why := UsernsAvailable(unshare); !ok {
		return false, why
	}
	probe := &Cfg{Unshare: which(unshare), Setpriv: which("setpriv"), Self: self, UID: os.Getuid(), GID: os.Getgid(), Subuid: true, Mode: "allow", Net: true}
	argv, err := ComposeStage1(probe, []string{"true"})
	if err != nil {
		return false, err.Error()
	}
	var stderr bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, "subuid mapping probe failed: " + strings.TrimSpace(stderr.String())
	}
	return true, "ok"
}

// Launch is subuid stage 0. It returns the exit code of the child (128+signal when killed).
func Launch(c *Cfg, args []string, stderr io.Writer) int {
	uid, gid := os.Getuid(), os.Getgid()
	name := ""
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		name = u.Username
	}
	us, uc := SubidRange("/etc/subuid", name, uid)
	gs, gc := SubidRange("/etc/subgid", name, uid)
	if uc < 2 || gc < 2 {
		fmt.Fprintln(stderr, "nsbox: no subordinate id range in /etc/subuid or /etc/subgid; the command was not run")
		return 1
	}
	js, err := json.Marshal(c)
	if err != nil {
		fmt.Fprintln(stderr, "nsbox:", err)
		return 1
	}
	goR, goW, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(stderr, "nsbox:", err)
		return 1
	}
	defer goW.Close()
	child := exec.Command(c.Self, append([]string{launchChildArg, string(js), "--"}, args...)...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.ExtraFiles = []*os.File{goR}
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	if err := child.Start(); err != nil {
		goR.Close()
		fmt.Fprintf(stderr, "nsbox: subuid mapping failed: child could not create a user namespace: %v; the command was not run\n", err)
		return 1
	}
	goR.Close()
	pid := strconv.Itoa(child.Process.Pid)
	var failed string
	for _, m := range []struct {
		tool       string
		own        int
		start, cnt int
	}{{"newuidmap", uid, us, uc}, {"newgidmap", gid, gs, gc}} {
		var se bytes.Buffer
		cmd := exec.Command(m.tool, pid, "0", strconv.Itoa(m.own), "1", "1", strconv.Itoa(m.start), strconv.Itoa(m.cnt))
		cmd.Stderr = &se
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(se.String())
			if msg == "" {
				msg = err.Error()
			}
			failed = m.tool + " failed: " + msg
			break
		}
	}
	if failed != "" {
		goW.Close() // the child reads EOF and exits 125
		_ = child.Wait()
		fmt.Fprintf(stderr, "nsbox: subuid mapping failed: %s; the command was not run\n", failed)
		return 1
	}
	if _, err := goW.Write([]byte("1")); err != nil {
		_ = child.Wait()
		return 1
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			_ = child.Process.Signal(s)
		}
	}()
	_ = child.Wait()
	signal.Stop(sigs)
	if ws, ok := child.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return child.ProcessState.ExitCode()
}

// LaunchChild is what the stage-0 child does inside the new user namespace once its ids are mapped.
func LaunchChild(c *Cfg, args []string, stderr io.Writer) int {
	f := os.NewFile(3, "go")
	buf := make([]byte, 1)
	if n, _ := f.Read(buf); n != 1 || buf[0] != '1' {
		return 125
	}
	js, err := json.Marshal(c)
	if err != nil {
		return 126
	}
	argv := []string{c.Unshare}
	if !c.Net {
		argv = append(argv, "-n")
	}
	argv = append(argv, "-m")
	if c.PidNS() {
		argv = append(argv, "--pid", "--fork", "--kill-child", "--mount-proc")
	}
	argv = append(argv, "--propagation", "private", c.Self, "--setup", string(js), "--")
	argv = append(argv, args...)
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "nsbox: subuid child failed: %v\n", err)
	}
	return 126
}
