package sandbox

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Egress pieces of the sandbox: the allow-listing CONNECT proxy (runs OUTSIDE the namespace, listens on a unix
// socket) and the loopback forwarder (runs inside, 127.0.0.1:3128 -> that socket).

// AllowDefault is the single-host allow-list of eval trials.
const AllowDefault = "api.anthropic.com:443"

// MaxLife is how long a proxy lives at most.
const MaxLife = 20 * time.Minute

// WorkerAllow is the worker-pane allow-list (ADR M11673): inference, git over https, package installs.
var WorkerAllow = []string{
	"api.anthropic.com:443", "github.com:443", "api.github.com:443", "codeload.githubusercontent.com:443",
	"objects.githubusercontent.com:443", "pypi.org:443", "files.pythonhosted.org:443",
}

var (
	denied      = []byte("HTTP/1.1 403 Forbidden\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
	established = []byte("HTTP/1.1 200 Connection Established\r\n\r\n")
	badGateway  = []byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
)

// Resolver maps an allow-listed host:port to something net.Dial can connect to. Only called for allowed targets.
type Resolver func(host string, port int) (network, addr string, err error)

// DefaultResolver is the real DNS answer (the first address).
func DefaultResolver(host string, port int) (string, string, error) {
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return "", "", fmt.Errorf("no address for %s: %v", host, err)
	}
	return "tcp", net.JoinHostPort(addrs[0], strconv.Itoa(port)), nil
}

// UpstreamResolver is the test seam: host:port -> address from a table (a TCP "host:port" or a unix path), else DNS.
func UpstreamResolver(table map[string]string) Resolver {
	return func(host string, port int) (string, string, error) {
		if hit, ok := table[strings.ToLower(host)+":"+strconv.Itoa(port)]; ok {
			if strings.HasPrefix(hit, "/") {
				return "unix", hit, nil
			}
			return "tcp", hit, nil
		}
		return DefaultResolver(host, port)
	}
}

// PidAlive is false for a missing pid and for a zombie (kill(pid, 0) alone says yes to a zombie).
func PidAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	s := b[i+2]
	return s != 'Z' && s != 'X'
}

// AllowedTarget is the host and port of an allowed CONNECT request line. Exact match, host case-insensitive; any
// other host, a raw IP, another port or any other method or version is refused.
func AllowedTarget(requestLine string, allow []string) (string, int, bool) {
	parts := strings.Fields(requestLine)
	if len(parts) != 3 || parts[0] != "CONNECT" || !strings.HasPrefix(parts[2], "HTTP/") {
		return "", 0, false
	}
	i := strings.LastIndex(parts[1], ":")
	if i < 0 {
		return "", 0, false
	}
	host, port := parts[1][:i], parts[1][i+1:]
	for _, entry := range allow {
		j := strings.LastIndex(entry, ":")
		if j < 0 {
			continue
		}
		if strings.EqualFold(host, entry[:j]) && port == entry[j+1:] {
			n, err := strconv.Atoi(port)
			if err != nil {
				return "", 0, false
			}
			return host, n, true
		}
	}
	return "", 0, false
}

// relay copies bytes both ways until either side closes.
func relay(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { a.Close(); b.Close() }
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		once.Do(closeBoth)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

func readHead(c net.Conn) (head string, rest []byte, err error) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	br := bufio.NewReaderSize(c, 8192)
	var buf []byte
	for !bytes.Contains(buf, []byte("\r\n\r\n")) && len(buf) < 8192 {
		chunk := make([]byte, 4096)
		n, rerr := br.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if rerr != nil {
			break
		}
	}
	h, r, _ := bytes.Cut(buf, []byte("\r\n\r\n"))
	line, _, _ := bytes.Cut(h, []byte("\r\n"))
	return string(line), r, nil
}

func handle(conn net.Conn, allow []string, resolve Resolver) {
	line, rest, _ := readHead(conn)
	host, port, ok := AllowedTarget(line, allow)
	if !ok { // denied: no DNS, no upstream connection
		_, _ = conn.Write(denied)
		conn.Close()
		return
	}
	network, addr, err := resolve(host, port)
	var up net.Conn
	if err == nil {
		up, err = net.DialTimeout(network, addr, 10*time.Second)
	}
	if err != nil {
		_, _ = conn.Write(badGateway)
		conn.Close()
		return
	}
	_, _ = conn.Write(established)
	if len(rest) > 0 {
		_, _ = up.Write(rest)
	}
	relay(conn, up)
}

// ServeOpts are the proxy settings. WatchPid 0 means no watch.
type ServeOpts struct {
	Sock     string
	Allow    []string
	WatchPid int
	MaxLife  time.Duration
	Resolver Resolver
	Ready    chan<- struct{}
	Stop     <-chan struct{} // optional: closing it ends Serve (tests)
}

// Serve is the accept loop. It returns when the watched pid is gone, MaxLife elapsed or Stop closed.
func Serve(o ServeOpts) error {
	if o.MaxLife == 0 {
		o.MaxLife = MaxLife
	}
	if o.Resolver == nil {
		o.Resolver = DefaultResolver
	}
	if len(o.Allow) == 0 {
		o.Allow = []string{AllowDefault}
	}
	if err := os.Remove(o.Sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", o.Sock)
	if err != nil {
		return err
	}
	defer os.Remove(o.Sock)
	defer ln.Close()
	if err := os.Chmod(o.Sock, 0o600); err != nil {
		return err
	}
	ul := ln.(*net.UnixListener)
	if o.Ready != nil {
		close(o.Ready)
	}
	deadline := time.Now().Add(o.MaxLife)
	for time.Now().Before(deadline) && (o.WatchPid == 0 || PidAlive(o.WatchPid)) {
		select {
		case <-o.Stop:
			return nil
		default:
		}
		_ = ul.SetDeadline(time.Now().Add(300 * time.Millisecond))
		conn, err := ul.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go handle(conn, o.Allow, o.Resolver)
	}
	return nil
}

// Forward runs inside the namespace: every TCP connection on host:port goes to the proxy's unix socket. It only
// returns on a listen or accept error.
func Forward(sock, host string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return ForwardOn(ln, sock)
}

// ForwardOn is Forward on an existing listener.
func ForwardOn(ln net.Listener, sock string) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			up, err := net.DialTimeout("unix", sock, 10*time.Second)
			if err != nil {
				c.Close()
				return
			}
			relay(c, up)
		}()
	}
}
