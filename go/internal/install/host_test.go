package install

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tlsHost(t *testing.T, srv *httptest.Server) *OSHost {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &OSHost{HomeDir: t.TempDir(), Env: map[string]string{}, DownloadIdle: 300 * time.Millisecond,
		StatusTimeout: 300 * time.Millisecond, tlsConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
}

func TestDownloadWritesTheBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "hivemill-install/1.0" {
			http.Error(w, "bad agent", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("payload")) // a test server: the client sees a short body if this fails
	}))
	defer srv.Close()
	h := tlsHost(t, srv)
	dest := filepath.Join(t.TempDir(), "out")
	if err := h.Download(srv.URL+"/a", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "payload" {
		t.Errorf("body = %q", b)
	}
}

func TestDownloadRefusesPlainHTTP(t *testing.T) {
	h := &OSHost{HomeDir: t.TempDir()}
	err := h.Download("http://example.test/x", filepath.Join(t.TempDir(), "o"))
	if err == nil || !strings.Contains(err.Error(), "refusing non-https download") {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadRefusesRedirectAwayFromHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
	}))
	defer srv.Close()
	err := tlsHost(t, srv).Download(srv.URL+"/a", filepath.Join(t.TempDir(), "o"))
	if err == nil || !strings.Contains(err.Error(), "refusing redirect away from https") {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadHTTPErrorNamesTheStatus(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	err := tlsHost(t, srv).Download(srv.URL+"/a", filepath.Join(t.TempDir(), "o"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 404 for "+srv.URL+"/a") {
		t.Errorf("err = %v", err)
	}
}

// A server that accepts the request and then sends nothing must end in a timeout error, not a hang.
func TestDownloadTimesOutOnAHangingServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	err := tlsHost(t, srv).Download(srv.URL+"/a", filepath.Join(t.TempDir(), "o"))
	// The idle timer and the transport's header timeout share one limit, so either may report first.
	if err == nil || !(strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "timeout awaiting response headers")) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

// A body that stalls halfway also times out; a slow but steady one does not.
func TestDownloadIdleTimeoutIsPerRead(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		for i := 0; i < 8; i++ {
			_, _ = w.Write([]byte("x"))
			fl.Flush()
			time.Sleep(100 * time.Millisecond) // total 800 ms, above the 600 ms limit, but never idle that long
		}
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "o")
	steady := tlsHost(t, srv)
	steady.DownloadIdle = 600 * time.Millisecond
	if err := steady.Download(srv.URL+"/a", dest); err != nil {
		t.Fatalf("a steady slow download failed: %v", err)
	}
	stall := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer stall.Close()
	if err := tlsHost(t, stall).Download(stall.URL+"/a", dest); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("a stalled body: %v", err)
	}
}

func TestHTTPStatus(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	h := tlsHost(t, srv)
	// the status probe uses the default transport, which does not trust the test CA: it reports 0 like Python's OSError path
	if got := h.HTTPStatus(srv.URL + "/x"); got != 0 {
		t.Errorf("untrusted TLS: %d", got)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer plain.Close()
	if got := h.HTTPStatus(plain.URL + "/x"); got != http.StatusTeapot {
		t.Errorf("status = %d", got)
	}
	if got := h.HTTPStatus(plain.URL + "/slow"); got != 0 {
		t.Errorf("a hanging server must time out to 0, got %d", got)
	}
	if got := h.HTTPStatus("http://127.0.0.1:1/"); got != 0 {
		t.Errorf("refused: %d", got)
	}
}

func TestRunMergesOutputAndReportsExit(t *testing.T) {
	h := &OSHost{HomeDir: t.TempDir(), Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	rc, out := h.Run([]string{"sh", "-c", "echo out; echo err >&2; exit 3"}, "", 0, false)
	if rc != 3 || !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Errorf("rc=%d out=%q", rc, out)
	}
	if rc, out := h.Run([]string{"sh", "-c", "printf 'a\\r\\nb\\rc'"}, "", 0, false); rc != 0 || out != "a\nb\nc" {
		t.Errorf("universal newlines: rc=%d out=%q", rc, out)
	}
	if rc, out := h.Run([]string{"sh", "-c", "kill -9 $$"}, "", 0, false); rc != -9 {
		t.Errorf("a signal is -9 like Python: rc=%d out=%q", rc, out)
	}
	if rc, _ := h.Run([]string{"pwd"}, "/", 0, false); rc != 0 {
		t.Error("cwd")
	}
}

func TestRunMissingCommandIs127(t *testing.T) {
	h := &OSHost{HomeDir: t.TempDir(), Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	if rc, out := h.Run([]string{"definitely-not-a-tool-xyz"}, "", 0, false); rc != 127 || out == "" {
		t.Errorf("rc=%d out=%q", rc, out)
	}
}

// The child's PATH starts with ~/.local/bin, so a tool the installer just put there is found.
func TestRunUsesTheChildPath(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	mustMkdir(t, bin)
	mustWrite(t, filepath.Join(bin, "mytool"), "#!/bin/sh\necho hello-from-local-bin\n")
	if err := os.Chmod(filepath.Join(bin, "mytool"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &OSHost{HomeDir: home, Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	if rc, out := h.Run([]string{"mytool"}, "", 0, false); rc != 0 || !strings.Contains(out, "hello-from-local-bin") {
		t.Errorf("rc=%d out=%q", rc, out)
	}
}

func TestRunTimeoutReturns124(t *testing.T) {
	h := &OSHost{HomeDir: t.TempDir(), Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	start := time.Now()
	rc, out := h.Run([]string{"sleep", "30"}, "", 300*time.Millisecond, false)
	if rc != 124 || !strings.HasPrefix(out, "timed out after") {
		t.Errorf("rc=%d out=%q", rc, out)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("the timeout did not stop the child")
	}
}

func TestInstallFileIsAtomicAndModed(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustWrite(t, src, "bin")
	dst := filepath.Join(dir, "dst")
	h := &OSHost{HomeDir: dir}
	if err := h.InstallFile(src, dst, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, %v", st, err)
	}
	if _, err := os.Stat(dst + ".part"); err == nil {
		t.Error(".part left behind")
	}
	if err := h.InstallFile(filepath.Join(dir, "nope"), dst, 0o755); err == nil {
		t.Error("a missing source must fail")
	}
}

func writeTarGz(t testing.TB, path string, files map[string]byte, body string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, typ := range files {
		hdr := &tar.Header{Name: name, Typeflag: typ, Mode: 0o755, Size: int64(len(body))}
		if typ == tar.TypeSymlink {
			hdr.Size, hdr.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtractMember(t *testing.T) {
	dir := t.TempDir()
	tb := filepath.Join(dir, "a.tar.gz")
	writeTarGz(t, tb, map[string]byte{"gh/bin/gh": tar.TypeReg, "gh/link": tar.TypeSymlink}, "ELF")
	h := &OSHost{HomeDir: dir}
	dest := filepath.Join(dir, "gh")
	if err := h.ExtractMember(tb, "gh/bin/gh", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "ELF" {
		t.Errorf("content %q", b)
	}
	if err := h.ExtractMember(tb, "gh/link", dest); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("symlink member: %v", err)
	}
	if err := h.ExtractMember(tb, "gh/missing", dest); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing member: %v", err)
	}
	bad := filepath.Join(dir, "bad.tar.gz")
	mustWrite(t, bad, "not gzip")
	if err := h.ExtractMember(bad, "x", dest); err == nil {
		t.Error("a corrupt archive must fail")
	}
}

func TestSymlink(t *testing.T) {
	dir := t.TempDir()
	h := &OSHost{HomeDir: dir}
	link := filepath.Join(dir, "l")
	if err := h.Symlink("/a", link); err != nil {
		t.Fatal(err)
	}
	if err := h.Symlink("/a", link); err != nil {
		t.Errorf("same target again: %v", err)
	}
	if err := h.Symlink("/b", link); err == nil || !strings.Contains(err.Error(), "already points elsewhere") {
		t.Errorf("other target: %v", err)
	}
	file := filepath.Join(dir, "f")
	mustWrite(t, file, "x")
	if err := h.Symlink("/a", file); err == nil || !strings.Contains(err.Error(), "is not a symlink") {
		t.Errorf("a file: %v", err)
	}
}

func TestPopenDetachesAndLogs(t *testing.T) {
	dir := t.TempDir()
	h := &OSHost{HomeDir: dir, Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	log := filepath.Join(dir, "log")
	if err := h.Popen([]string{"sh", "-c", "echo started"}, log); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(log); strings.Contains(string(b), "started") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the background process never wrote its log")
}

func TestMemGiBReadsProc(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip("no /proc/meminfo on this system")
	}
	if g, err := (&OSHost{}).MemGiB(); err != nil || g <= 0 {
		t.Errorf("mem = %v, %v", g, err)
	}
}

// A read that ends cleanly while the idle timer has cancelled the request is a timeout, never a complete body.
func TestIdleReaderCancelledReadIsTimeout(t *testing.T) {
	for name, src := range map[string]io.Reader{"eof": strings.NewReader(""), "data": strings.NewReader("x")} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &idleReader{r: src, t: time.NewTimer(time.Hour), d: time.Hour, ctx: ctx}
		if _, err := r.Read(make([]byte, 4)); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err = %v, want context.Canceled", name, err)
		}
	}
}

// withStdin swaps os.Stdin for a pipe holding data and returns the read end, so a test can see what was left unread.
func withStdin(t *testing.T, data string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(data); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close(); w.Close() })
	return r
}

func TestWaitEnterRefusesNonTTYStdin(t *testing.T) {
	r := withStdin(t, "x\n")
	if err := (&OSHost{}).WaitEnter(); err == nil {
		t.Fatal("WaitEnter read a pipe")
	}
	buf := make([]byte, 8)
	n, _ := r.Read(buf)
	if n != 2 {
		t.Errorf("the pipe bytes were consumed: %d left", n)
	}
}

func TestRunTTYRefusesNonTTYStdin(t *testing.T) {
	r := withStdin(t, "x\n")
	marker := filepath.Join(t.TempDir(), "started")
	if rc := (&OSHost{}).RunTTY([]string{"sh", "-c", "touch " + marker}); rc == 0 {
		t.Fatal("RunTTY ran with a pipe on stdin")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the child was started")
	}
	buf := make([]byte, 8)
	if n, _ := r.Read(buf); n != 2 {
		t.Errorf("the pipe bytes were consumed: %d left", n)
	}
}
