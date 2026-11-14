package sandbox

import (
	"bufio"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestEgressPolicy: every host of the Python allow-lists (ALLOW and WORKER_ALLOW) is allowed; any other host, a raw
// IP, another port, a look-alike name and any other method or HTTP version is refused.
func TestEgressPolicy(t *testing.T) {
	type tc struct {
		name, line string
		allow      []string
		ok         bool
	}
	var cases []tc
	for _, h := range []string{"api.anthropic.com", "github.com", "api.github.com", "codeload.githubusercontent.com",
		"objects.githubusercontent.com", "pypi.org", "files.pythonhosted.org"} {
		cases = append(cases,
			tc{"allowed " + h, "CONNECT " + h + ":443 HTTP/1.1", WorkerAllow, true},
			tc{"allowed upper " + h, "CONNECT " + strings.ToUpper(h) + ":443 HTTP/1.1", WorkerAllow, true},
			tc{"refused wrong port " + h, "CONNECT " + h + ":80 HTTP/1.1", WorkerAllow, false},
			tc{"refused subdomain of " + h, "CONNECT evil." + h + ":443 HTTP/1.1", WorkerAllow, false},
			tc{"refused suffix of " + h, "CONNECT " + h + ".evil.com:443 HTTP/1.1", WorkerAllow, false},
			tc{"refused no port " + h, "CONNECT " + h + " HTTP/1.1", WorkerAllow, false})
	}
	cases = append(cases,
		tc{"default allow-list api", "CONNECT api.anthropic.com:443 HTTP/1.1", []string{AllowDefault}, true},
		tc{"default allow-list refuses github", "CONNECT github.com:443 HTTP/1.1", []string{AllowDefault}, false},
		tc{"eval trials refuse pypi", "CONNECT pypi.org:443 HTTP/1.1", []string{AllowDefault}, false},
		tc{"other host", "CONNECT example.com:443 HTTP/1.1", WorkerAllow, false},
		tc{"raw IPv6 documentation", "CONNECT [2001:db8::1]:443 HTTP/1.1", WorkerAllow, false},
		tc{"raw IPv6", "CONNECT [2606:4700::1111]:443 HTTP/1.1", WorkerAllow, false},
		tc{"localhost", "CONNECT localhost:443 HTTP/1.1", WorkerAllow, false},
		tc{"loopback IP", "CONNECT 127.0.0.1:443 HTTP/1.1", WorkerAllow, false},
		tc{"decimal IP", "CONNECT 2130706433:443 HTTP/1.1", WorkerAllow, false},
		tc{"userinfo trick", "CONNECT github.com\x40evil.com:443 HTTP/1.1", WorkerAllow, false},
		tc{"trailing dot", "CONNECT github.com.:443 HTTP/1.1", WorkerAllow, false},
		tc{"port with letters", "CONNECT github.com:44a HTTP/1.1", WorkerAllow, false},
		tc{"plain GET", "GET http://github.com/ HTTP/1.1", WorkerAllow, false},
		tc{"lowercase method", "connect github.com:443 HTTP/1.1", WorkerAllow, false},
		tc{"missing version", "CONNECT github.com:443", WorkerAllow, false},
		tc{"not HTTP version", "CONNECT github.com:443 FTP/1.1", WorkerAllow, false},
		tc{"extra token", "CONNECT github.com:443 HTTP/1.1 x", WorkerAllow, false},
		tc{"empty line", "", WorkerAllow, false},
		tc{"empty allow-list", "CONNECT github.com:443 HTTP/1.1", nil, false},
		tc{"tab separators", "CONNECT\tgithub.com:443\tHTTP/1.1", WorkerAllow, true},
		tc{"http 1.0", "CONNECT github.com:443 HTTP/1.0", WorkerAllow, true})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, port, ok := AllowedTarget(c.line, c.allow)
			if ok != c.ok {
				t.Fatalf("AllowedTarget(%q) ok=%v, want %v", c.line, ok, c.ok)
			}
			if ok && (port != 443 || !strings.EqualFold(host, strings.Split(strings.Fields(c.line)[1], ":")[0])) {
				t.Errorf("target = %s:%d", host, port)
			}
		})
	}
	t.Run("the worker list is exactly the seven Python hosts", func(t *testing.T) {
		want := "api.anthropic.com:443 github.com:443 api.github.com:443 codeload.githubusercontent.com:443 objects.githubusercontent.com:443 pypi.org:443 files.pythonhosted.org:443"
		if got := strings.Join(WorkerAllow, " "); got != want {
			t.Errorf("WorkerAllow = %q", got)
		}
		if AllowDefault != "api.anthropic.com:443" || MaxLife != 20*time.Minute {
			t.Errorf("default %q life %v", AllowDefault, MaxLife)
		}
	})
}

// proxy starts Serve on a socket in a temp dir; upstream maps host:port to a real address (the test seam).
func startProxy(t *testing.T, allow []string, upstream map[string]string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "p.sock")
	ready, stop := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ServeOpts{Sock: sock, Allow: allow, Resolver: UpstreamResolver(upstream), Ready: ready, Stop: stop, MaxLife: time.Minute})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Serve exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not start")
	}
	t.Cleanup(func() { close(stop); <-done })
	return sock
}

func connectVia(t *testing.T, sock, target string) (net.Conn, string) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	must(t, err)
	t.Cleanup(func() { c.Close() })
	_, err = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	must(t, err)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	status, _ := br.ReadString('\n')
	return &bufConn{c, br}, strings.TrimSpace(status)
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func TestEgressProxy(t *testing.T) {
	var dialed atomic.Int32
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			dialed.Add(1)
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	sock := startProxy(t, WorkerAllow, map[string]string{"github.com:443": echo.Addr().String()})
	t.Run("an allowed host is connected and bytes flow both ways", func(t *testing.T) {
		c, status := connectVia(t, sock, "github.com:443")
		if !strings.HasPrefix(status, "HTTP/1.1 200") {
			t.Fatalf("status = %q", status)
		}
		for {
			l, _ := c.(*bufConn).r.ReadString('\n')
			if l == "\r\n" || l == "" {
				break
			}
		}
		_, _ = io.WriteString(c, "ping")
		buf := make([]byte, 4)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Errorf("echo = %q, %v", buf, err)
		}
	})
	before := dialed.Load()
	for _, target := range []string{"example.com:443", "127.0.0.1:443", "github.com:80", "evil.github.com:443"} {
		t.Run("refused with 403 and no upstream dial: "+target, func(t *testing.T) {
			_, status := connectVia(t, sock, target)
			if !strings.HasPrefix(status, "HTTP/1.1 403") {
				t.Errorf("status = %q", status)
			}
		})
	}
	t.Run("a non-CONNECT request is refused", func(t *testing.T) {
		c, err := net.Dial("unix", sock)
		must(t, err)
		defer c.Close()
		_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: github.com\r\n\r\n")
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		status, _ := bufio.NewReader(c).ReadString('\n')
		if !strings.HasPrefix(status, "HTTP/1.1 403") {
			t.Errorf("status = %q", status)
		}
	})
	t.Run("denied requests never reached the upstream", func(t *testing.T) {
		if got := dialed.Load(); got != before {
			t.Errorf("upstream dialed %d times for denied requests", got-before)
		}
	})
	t.Run("an allowed host whose upstream is down is a 502", func(t *testing.T) {
		dead, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := dead.Addr().String()
		dead.Close()
		s2 := startProxy(t, []string{"pypi.org:443"}, map[string]string{"pypi.org:443": addr})
		_, status := connectVia(t, s2, "pypi.org:443")
		if !strings.HasPrefix(status, "HTTP/1.1 502") {
			t.Errorf("status = %q", status)
		}
	})
	t.Run("the socket is 0600", func(t *testing.T) {
		fi, err := os.Stat(sock)
		must(t, err)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	})
	t.Run("bytes sent with the CONNECT head reach the upstream", func(t *testing.T) {
		c, err := net.Dial("unix", sock)
		must(t, err)
		defer c.Close()
		_, _ = io.WriteString(c, "CONNECT github.com:443 HTTP/1.1\r\n\r\nEARLY")
		br := bufio.NewReader(c)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = br.ReadString('\n')
		_, _ = br.ReadString('\n')
		buf := make([]byte, 5)
		if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "EARLY" {
			t.Errorf("echo = %q, %v", buf, err)
		}
	})
}

func TestServeLifecycle(t *testing.T) {
	t.Run("Serve ends when the watched pid is gone", func(t *testing.T) {
		cmd := exec.Command("true")
		must(t, cmd.Run())
		sock := filepath.Join(t.TempDir(), "p.sock")
		done := make(chan error, 1)
		go func() { done <- Serve(ServeOpts{Sock: sock, WatchPid: cmd.Process.Pid, MaxLife: time.Minute}) }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not stop for a dead pid")
		}
		if _, err := os.Stat(sock); err == nil {
			t.Error("the socket was left behind")
		}
	})
	t.Run("Serve ends at max_life and removes its socket", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "p.sock")
		start := time.Now()
		must(t, Serve(ServeOpts{Sock: sock, MaxLife: 400 * time.Millisecond}))
		if time.Since(start) > 3*time.Second {
			t.Error("Serve overran its life")
		}
		if _, err := os.Stat(sock); err == nil {
			t.Error("the socket was left behind")
		}
	})
	t.Run("a stale socket file is replaced", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "p.sock")
		must(t, os.WriteFile(sock, []byte("stale"), 0o600))
		must(t, Serve(ServeOpts{Sock: sock, MaxLife: 100 * time.Millisecond}))
	})
	t.Run("PidAlive", func(t *testing.T) {
		if !PidAlive(os.Getpid()) {
			t.Error("own pid is not alive")
		}
		if PidAlive(0) || PidAlive(1<<30) {
			t.Error("a bogus pid is alive")
		}
		z := exec.Command("true")
		must(t, z.Start())
		time.Sleep(200 * time.Millisecond) // exited but not yet waited: a zombie
		if PidAlive(z.Process.Pid) {
			t.Error("a zombie counts as alive")
		}
		_ = z.Wait()
	})
}

func TestForwarder(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "up.sock")
	up, err := net.Listen("unix", sock)
	must(t, err)
	t.Cleanup(func() { up.Close() })
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() { _ = ForwardOn(ln, sock) }()
	t.Run("a TCP connection reaches the proxy socket", func(t *testing.T) {
		c, err := net.Dial("tcp", ln.Addr().String())
		must(t, err)
		defer c.Close()
		_, _ = io.WriteString(c, "hello")
		buf := make([]byte, 5)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
			t.Errorf("got %q, %v", buf, err)
		}
	})
	t.Run("a dead proxy socket closes the client", func(t *testing.T) {
		ln2, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln2.Close()
		go func() { _ = ForwardOn(ln2, filepath.Join(t.TempDir(), "gone.sock")) }()
		c, err := net.Dial("tcp", ln2.Addr().String())
		must(t, err)
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Error("read from a forwarder with a dead upstream succeeded")
		}
	})
}
