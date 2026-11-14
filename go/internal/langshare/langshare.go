// Package langshare counts non-blank code lines of Go and Python in a git
// tree, so every report on the Go share of the repo uses the same method.
package langshare

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
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

func git(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Count measures the tree at ref (default HEAD) of the repo in dir.
// The ref is resolved to a commit id with --end-of-options and never reaches
// a shell.
func Count(repo, ref string) (Counts, error) {
	var c Counts
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
	batch := exec.Command("git", "-C", repo, "cat-file", "--batch")
	var stderr bytes.Buffer
	batch.Stderr = &stderr
	in, err := batch.StdinPipe()
	if err != nil {
		return c, fmt.Errorf("git cat-file: %v", err)
	}
	outPipe, err := batch.StdoutPipe()
	if err != nil {
		return c, fmt.Errorf("git cat-file: %v", err)
	}
	if err := batch.Start(); err != nil {
		return c, fmt.Errorf("git cat-file: %v", err)
	}
	defer func() { in.Close(); _ = batch.Wait() }()
	rd := bufio.NewReader(outPipe)

	for _, ent := range bytes.Split(tree, []byte{0}) {
		meta, p, ok := strings.Cut(string(ent), "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type sha
		if len(f) != 3 || f[1] != "blob" || f[0] == "120000" || IsExcluded(p) {
			continue
		}
		k := classify(p)
		if k == kNone {
			continue
		}
		data, err := readBlob(in, rd, f[2])
		if err != nil {
			return c, fmt.Errorf("git cat-file %s: %v: %s", p, err, strings.TrimSpace(stderr.String()))
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
	return c, nil
}

func readBlob(in io.Writer, rd *bufio.Reader, sha string) ([]byte, error) {
	if _, err := io.WriteString(in, sha+"\n"); err != nil {
		return nil, err
	}
	hdr, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	fs := strings.Fields(hdr) // sha type size
	if len(fs) != 3 {
		return nil, fmt.Errorf("unexpected header %q", strings.TrimSpace(hdr))
	}
	size, err := strconv.Atoi(fs[2])
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size+1) // trailing LF
	if _, err := io.ReadFull(rd, buf); err != nil {
		return nil, err
	}
	return buf[:size], nil
}
