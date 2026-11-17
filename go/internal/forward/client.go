package forward

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// SSHArgs is the ssh argv (after "ssh") for one call. The CLI's argv travels in the header, never on this line.
func SSHArgs(c Config) []string {
	return []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", c.HQHost, c.RemoteReceiver, "--serve"}
}

// ForwardStdin says whether the client's stdin carries data to send: a pipe, a socket or a regular file. A
// terminal, /dev/null or a closed stdin sends nothing, so the remote CLI sees EOF at once.
func ForwardStdin(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil { // closed or invalid fd: nothing to read
		return false
	}
	m := st.Mode()
	return m&os.ModeNamedPipe != 0 || m&os.ModeSocket != 0 || m.IsRegular()
}

// maxSSHErr bounds the ssh stderr kept before the server's Ack arrives.
const maxSSHErr = 64 << 10

// gate holds stderr back until the server's Ack shows the ssh connection is up, so an ssh failure can be
// reported as one "forward: ssh to ... failed" line; after Ack it passes stderr through unchanged.
type gate struct {
	mu   sync.Mutex
	dst  io.Writer
	open bool
	buf  bytes.Buffer
	err  error
}

func (g *gate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open {
		if g.err == nil {
			_, g.err = g.dst.Write(p)
		}
		return len(p), nil // keep draining ssh even if our stderr is gone
	}
	if room := maxSSHErr - g.buf.Len(); room > 0 {
		if len(p) > room {
			g.buf.Write(p[:room])
		} else {
			g.buf.Write(p)
		}
	}
	return len(p), nil
}

// flush opens the gate and writes what it held.
func (g *gate) flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.open {
		g.open = true
		if g.buf.Len() > 0 {
			_, g.err = g.dst.Write(g.buf.Bytes())
		}
		g.buf.Reset()
	}
}

func (g *gate) held() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := strings.Join(strings.Fields(g.buf.String()), " ")
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}

// RunClient forwards one call: args are the factory arguments, environ the caller's env (only allow-listed keys
// are sent), cwd the caller's working directory, stdin nil for "no stdin". It returns the exit code to exit with.
func RunClient(c Config, args, environ []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := c.ValidateClient(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	hdr, err := EncodeHeader(Header{Argv: args, Env: ClientEnv(environ), Cwd: cwd})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	sshFailed := func(detail string) int {
		fmt.Fprintf(stderr, "forward: ssh to %s failed: %s\n", c.HQHost, detail)
		return 255
	}
	cmd := exec.Command("ssh", SSHArgs(c)...)
	cmd.Env = environ // ssh itself needs HOME, SSH_AUTH_SOCK; it sends no env unless ssh_config says SendEnv
	errGate := &gate{dst: stderr}
	cmd.Stderr = errGate
	in, err := cmd.StdinPipe()
	if err != nil {
		return sshFailed(err.Error())
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return sshFailed(err.Error())
	}
	if err := cmd.Start(); err != nil {
		return sshFailed(err.Error())
	}

	var readErr error
	var readMu sync.Mutex
	go func() {
		// Not waited for: when the remote exits first, a stdin that never reaches EOF must not hold us.
		defer in.Close()
		if _, err := in.Write(hdr); err != nil {
			return // ssh is gone; its exit status tells why
		}
		if stdin == nil {
			return
		}
		if err := copyStdin(in, stdin); err != nil {
			readMu.Lock()
			readErr = err
			readMu.Unlock()
			// Reading our stdin failed (not EOF): kill ssh so the remote sees a dropped connection, not a clean
			// EOF that would pass for the whole input. Bytes sent before the failure may already have been
			// read (see docs/multi-machine.md).
			if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
				fmt.Fprintf(stderr, "forward: kill ssh: %v\n", kerr)
			}
		}
	}()

	acked := false
	ack := make([]byte, len(Ack))
	n, rerr := io.ReadFull(out, ack)
	if rerr == nil && string(ack) == Ack {
		acked = true
		errGate.flush()
	} else if n > 0 {
		errGate.flush() // not our server talking: pass everything through as it came
		if _, err := stdout.Write(ack[:n]); err != nil {
			stdout = io.Discard // our stdout is gone; still drain ssh below
		}
	}
	if _, err := io.Copy(stdout, out); err != nil {
		// Our stdout is gone (e.g. a closed pipe): keep draining so ssh can finish and report its status.
		if _, derr := io.Copy(io.Discard, out); derr != nil {
			fmt.Fprintf(stderr, "forward: reading ssh output: %v\n", derr)
		}
	}
	werr := cmd.Wait()
	code := exitCode(werr)

	readMu.Lock()
	rErr := readErr
	readMu.Unlock()
	if rErr != nil {
		errGate.flush()
		fmt.Fprintf(stderr, "forward: reading stdin failed, call aborted: %v\n", rErr)
		return 2
	}
	if !acked && code == 255 {
		detail := errGate.held()
		if detail == "" {
			detail = "exit 255"
		}
		return sshFailed(detail)
	}
	errGate.flush()
	if code < 0 {
		return sshFailed(werr.Error())
	}
	return code
}

// copyStdin copies src to dst. A write error (the remote closed its stdin or exited) ends the copy quietly; a
// read error is returned.
func copyStdin(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 64<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return nil
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// exitCode maps a Wait error to a process exit code: the code itself, 128+signal for a signal, -1 when no
// process status exists.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return -1
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}
