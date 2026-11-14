// Package langshare counts non-blank code lines of Go and Python in a git
// tree, so every report on the Go share of the repo uses the same method.
package langshare

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// excluded lists path fragments of data, not code (fixtures, eval tasks,
// vendored code). A file is skipped when "/"+path contains any of them.
var excluded = []string{"/testdata/", "/skilleval/tasks/", "/go/vendor/"}

// Counts holds non-blank line totals. Test lines are included in Go/Python.
type Counts struct {
	Go          int
	GoTests     int
	Python      int
	PythonTests int
}

// GoShare returns go / (go + python) * 100, or 0 when both are zero.
func (c Counts) GoShare() float64 {
	if c.Go+c.Python == 0 {
		return 0
	}
	return float64(c.Go) / float64(c.Go+c.Python) * 100
}

// Text renders the three-line text report.
func (c Counts) Text() string {
	return fmt.Sprintf("go %d (%d tests)\npython %d (%d tests)\ngo share %.1f%%\n",
		c.Go, c.GoTests, c.Python, c.PythonTests, c.GoShare())
}

// JSON renders the one-object JSON report.
func (c Counts) JSON() string {
	return fmt.Sprintf("{\"go\":%d,\"go_tests\":%d,\"python\":%d,\"python_tests\":%d,\"go_share\":%.1f}\n",
		c.Go, c.GoTests, c.Python, c.PythonTests, c.GoShare())
}

// IsExcluded reports whether the path is data rather than code.
func IsExcluded(p string) bool {
	q := "/" + p
	for _, e := range excluded {
		if strings.Contains(q, e) {
			return true
		}
	}
	return false
}

type kind int

const (
	kNone kind = iota
	kGo
	kPython
	kMaybePython // extension-less: needs a shebang look
)

func classify(p string) kind {
	switch strings.ToLower(path.Ext(path.Base(p))) {
	case ".go":
		return kGo
	case ".py", ".pyi", ".pyw":
		return kPython
	case "":
		return kMaybePython
	}
	return kNone
}

func isPythonShebang(data []byte) bool {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	return bytes.HasPrefix(line, []byte("#!")) && bytes.Contains(line, []byte("python"))
}

func isGoTest(p string) bool { return strings.HasSuffix(p, "_test.go") }

func isPyTest(p string) bool {
	dir, base := path.Split(p)
	if strings.HasPrefix(base, "test_") {
		return true
	}
	for _, seg := range strings.Split(strings.TrimSuffix(dir, "/"), "/") {
		if seg == "tests" {
			return true
		}
	}
	return false
}

func nonBlank(data []byte) int {
	n := 0
	for _, l := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			n++
		}
	}
	return n
}

// gitTimeout bounds every git subprocess so a hung repo cannot hang the tool.
var gitTimeout = 2 * time.Minute

// maxBlob caps a single blob read into memory; a larger blob is an error.
var maxBlob = 64 << 20

// errTooLarge marks a blob over maxBlob. Its body is drained and, for
// extension-less files (only a shebang look), the file is skipped.
var errTooLarge = errors.New("blob too large")

func git(repo string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s: %w", gitTimeout, err)
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// parseTreeEntry splits one `ls-tree -z` record "<mode> <type> <sha>\t<path>".
func parseTreeEntry(ent []byte) (mode, typ, sha, p string, ok bool) {
	meta, p, found := strings.Cut(string(ent), "\t")
	if !found {
		return "", "", "", "", false
	}
	f := strings.Fields(meta)
	if len(f) != 3 {
		return "", "", "", "", false
	}
	return f[0], f[1], f[2], p, true
}

// parseBatchHeader reads the size from a `cat-file --batch` header
// "<sha> <type> <size>". A malformed, negative or over-cap size is an error.
func parseBatchHeader(hdr string) (int, error) {
	fs := strings.Fields(hdr)
	if len(fs) != 3 {
		return 0, fmt.Errorf("unexpected header %q", strings.TrimSpace(hdr))
	}
	size, err := strconv.Atoi(fs[2])
	if err != nil {
		return 0, fmt.Errorf("bad blob size in header %q: %w", strings.TrimSpace(hdr), err)
	}
	if size < 0 {
		return 0, fmt.Errorf("negative blob size %d", size)
	}
	if size > maxBlob {
		return size, fmt.Errorf("blob size %d over cap %d: %w", size, maxBlob, errTooLarge)
	}
	return size, nil
}

// Count measures the tree at ref (default HEAD) of the repo in dir.
// The ref is resolved to a commit id with --end-of-options and never reaches
// a shell.
func Count(repo, ref string) (c Counts, err error) {
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := git(repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return c, err
	}
	tree, err := git(repo, "ls-tree", "-r", "-z", strings.TrimSpace(string(sha)))
	if err != nil {
		return c, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	batch := exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "--batch")
	var stderr bytes.Buffer
	batch.Stderr = &stderr
	in, err := batch.StdinPipe()
	if err != nil {
		return c, fmt.Errorf("git cat-file: %w", err)
	}
	outPipe, err := batch.StdoutPipe()
	if err != nil {
		return c, fmt.Errorf("git cat-file: %w", err)
	}
	if err := batch.Start(); err != nil {
		return c, fmt.Errorf("git cat-file: %w", err)
	}
	done := false
	defer func() {
		if done {
			return
		}
		// Error path: kill first so Wait cannot block on a full pipe.
		_ = in.Close()
		_ = batch.Process.Kill()
		_ = batch.Wait() // exit status is the kill we caused; err already set
	}()
	rd := bufio.NewReader(outPipe)

	for _, ent := range bytes.Split(tree, []byte{0}) {
		mode, typ, blob, p, ok := parseTreeEntry(ent)
		if !ok || typ != "blob" || mode == "120000" || IsExcluded(p) {
			continue
		}
		k := classify(p)
		if k == kNone {
			continue
		}
		data, err := readBlob(in, rd, blob)
		if errors.Is(err, errTooLarge) && k == kMaybePython {
			continue // a big extension-less blob is a binary, not a script
		}
		if err != nil {
			// Stop cat-file first so its stderr copier is finished before we read it.
			_ = in.Close()
			_ = batch.Process.Kill()
			_ = batch.Wait()
			done = true
			msg := strings.TrimSpace(stderr.String())
			if ctx.Err() != nil {
				msg = "timed out after " + gitTimeout.String() + ": " + msg
			}
			return c, fmt.Errorf("git cat-file %s: %w: %s", p, err, msg)
		}
		if k == kMaybePython {
			if !isPythonShebang(data) {
				continue
			}
			k = kPython
		}
		n := nonBlank(data)
		if k == kGo {
			c.Go += n
			if isGoTest(p) {
				c.GoTests += n
			}
		} else {
			c.Python += n
			if isPyTest(p) {
				c.PythonTests += n
			}
		}
	}
	// Success path: EOF on stdin ends cat-file; its exit status must be clean.
	done = true
	if err := in.Close(); err != nil {
		return Counts{}, fmt.Errorf("git cat-file: close stdin: %w", err)
	}
	if err := batch.Wait(); err != nil {
		return Counts{}, fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return c, nil
}

func readBlob(in io.Writer, rd *bufio.Reader, sha string) ([]byte, error) {
	if _, err := io.WriteString(in, sha+"\n"); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	hdr, err := rd.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	size, err := parseBatchHeader(hdr)
	if errors.Is(err, errTooLarge) {
		if _, derr := io.CopyN(io.Discard, rd, int64(size)+1); derr != nil {
			return nil, fmt.Errorf("drain blob: %w", derr)
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size+1) // trailing LF
	if _, err := io.ReadFull(rd, buf); err != nil {
		return nil, fmt.Errorf("read blob: %w", err)
	}
	return buf[:size], nil
}
