package cdp

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The pinned Chrome for Testing headless shell. These constants must equal the ones in ci/screenshots.py
// (TestBrowserPin reads that file and compares).
const (
	BrowserVersion = "131.0.6778.204"
	browserURL     = "https://storage.googleapis.com/chrome-for-testing-public/" + BrowserVersion + "/linux64/chrome-headless-shell-linux64.zip"
	browserSize    = 113084320
	browserSHA256  = "afaac86e302c4874245991a3d509d529c50ecdd447affff0cdc2b08520906c32"
	browserBinRel  = "chrome-headless-shell-linux64/chrome-headless-shell"
)

// verifyZip refuses unless the file has exactly the pinned size and sha256. Nothing is unzipped before this passes.
func verifyZip(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return failf("cannot stat %s: %v", path, err)
	}
	if st.Size() != browserSize {
		return refusef("browser zip %s has %d bytes, expected %d. Nothing was unzipped; delete it and rerun to download again.", path, st.Size(), browserSize)
	}
	f, err := os.Open(path)
	if err != nil {
		return failf("cannot open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return failf("cannot read %s: %v", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != browserSHA256 {
		return refusef("browser zip %s sha256 is %s, expected %s. Nothing was unzipped; delete it and rerun to download again.", path, got, browserSHA256)
	}
	return nil
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return failf("bad download URL: %v", err)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
	if err != nil {
		return failf("download of %s failed: %v. Check the network and retry, or pass --browser PATH.", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return failf("download of %s failed: HTTP %d.", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return failf("cannot create %s: %v", dest, err)
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, browserSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return failf("download of %s failed after %d bytes: %v", url, n, err)
	}
	return nil
}

// EnsureZip returns the verified cached zip, downloading it first when absent.
func EnsureZip(ctx context.Context, cacheDir string) (string, error) {
	zpath := filepath.Join(cacheDir, "chrome-headless-shell-"+BrowserVersion+".zip")
	if _, err := os.Stat(zpath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", failf("cannot create cache dir %s: %v", cacheDir, err)
		}
		part := zpath + ".part"
		if err := download(ctx, browserURL, part); err != nil {
			os.Remove(part)
			return "", err
		}
		if err := verifyZip(part); err != nil {
			os.Remove(part)
			return "", err
		}
		if err := os.Rename(part, zpath); err != nil {
			return "", failf("cannot move %s into place: %v", part, err)
		}
		return zpath, nil
	}
	if err := verifyZip(zpath); err != nil {
		return "", err
	}
	return zpath, nil
}

// Unzip extracts the verified zip into dest and returns the browser binary path.
func Unzip(zpath, dest string) (string, error) {
	zr, err := zip.OpenReader(zpath)
	if err != nil {
		return "", failf("cannot open %s: %v", zpath, err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		target := filepath.Join(dest, zf.Name)
		if !within(dest, target) {
			return "", failf("zip member %q escapes the target directory; aborting.", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", failf("mkdir %s: %v", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", failf("mkdir %s: %v", filepath.Dir(target), err)
		}
		if err := extractFile(zf, target); err != nil {
			return "", err
		}
	}
	bin := filepath.Join(dest, filepath.FromSlash(browserBinRel))
	if st, err := os.Stat(bin); err != nil || !st.Mode().IsRegular() {
		return "", failf("%s not found in the zip. The pinned archive layout changed; stop and report.", browserBinRel)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return "", failf("chmod %s: %v", bin, err)
	}
	return bin, nil
}

func extractFile(zf *zip.File, target string) error {
	rc, err := zf.Open()
	if err != nil {
		return failf("cannot read zip member %s: %v", zf.Name, err)
	}
	defer rc.Close()
	mode := zf.Mode().Perm() | 0o600
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return failf("cannot create %s: %v", target, err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return failf("cannot extract %s: %v", zf.Name, err)
	}
	return out.Close()
}

// Proc is a child started in its own process group.
type Proc struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// StartProc starts cmd in its own process group.
func StartProc(name string, cmd *exec.Cmd) (*Proc, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, failf("cannot start %s: %v", name, err)
	}
	p := &Proc{name: name, cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

// Stop terminates the whole process group (SIGTERM, 5 s grace, SIGKILL) and waits for the leader.
func (p *Proc) Stop() {
	pgid := p.cmd.Process.Pid
	select {
	case <-p.done:
	default:
		syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			syscall.Kill(-pgid, syscall.SIGKILL)
			<-p.done
		}
	}
	syscall.Kill(-pgid, syscall.SIGKILL) // stragglers left in the group; ESRCH when empty
}

// Launch starts the headless shell with the CDP pipe on fd 3 (to the browser) and fd 4 (from the browser).
func Launch(bin, profile string, w, h int) (*Proc, io.WriteCloser, io.ReadCloser, error) {
	toR, toW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, failf("pipe: %v", err)
	}
	fromR, fromW, err := os.Pipe()
	if err != nil {
		toR.Close()
		toW.Close()
		return nil, nil, nil, failf("pipe: %v", err)
	}
	cmd := exec.Command(bin, "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
		fmt.Sprintf("--window-size=%d,%d", w, h), "--user-data-dir="+profile, "--remote-debugging-pipe", "about:blank")
	cmd.ExtraFiles = []*os.File{toR, fromW}
	p, err := StartProc("browser", cmd)
	toR.Close()
	fromW.Close()
	if err != nil {
		toW.Close()
		fromR.Close()
		return nil, nil, nil, err
	}
	return p, toW, fromR, nil
}

// Wait blocks until the child exits and returns its exit error.
func (p *Proc) Wait() error {
	<-p.done
	return p.err
}

// DefaultCacheDir is where the pinned zip is cached: the user cache dir plus "hivemill-film".
func DefaultCacheDir() (string, error) {
	c, err := os.UserCacheDir()
	if err != nil {
		return "", failf("no user cache dir: %v", err)
	}
	return filepath.Join(c, "hivemill-film"), nil
}

// ExitError is an error that carries the process exit code the caller should use:
// 1 for a failure, 3 for a refusal (for example a zip that does not match the pin).
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

func refusef(f string, a ...any) error { return &ExitError{3, fmt.Sprintf(f, a...)} }
func failf(f string, a ...any) error   { return &ExitError{1, fmt.Sprintf(f, a...)} }

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
