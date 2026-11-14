package factorytest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Binaries are built once per test binary (sync.Once) and cached across test binaries by a hash of the Go sources,
// go.mod and go.sum, under <os temp>/factorytest-cache-<uid>/<hash>/. A cold cache costs one `go build` of
// ./cmd/factory and ./internal/factorytest/stub; a warm one costs hashing the sources.
var (
	buildOnce  sync.Once
	buildOut   builtTools
	buildErr   error
	buildCount int // builds this process ran (0 when the cache was warm)
)

type builtTools struct {
	Dir     string // cache dir for this source hash
	Factory string // factory-go
	Stub    string // gh/herdr/hc stub
}

// ModuleRoot is the directory holding go.mod (the go/ directory), found from the test's working directory.
func ModuleRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", fmt.Errorf("no go.mod above the working directory; run the tests from inside the go/ module")
		}
		d = p
	}
}

// RepoRoot is the checkout root (the parent of go/).
func RepoRoot() (string, error) {
	m, err := ModuleRoot()
	if err != nil {
		return "", err
	}
	return filepath.Dir(m), nil
}

func goTool() (string, error) {
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("go not found on PATH or in GOROOT; install Go (a Go test never skips)")
}

// srcHash fingerprints everything that goes into the two binaries: every non-test .go file under cmd/ and
// internal/, go.mod and go.sum, by path and content.
func srcHash(mod string) (string, error) {
	var files []string
	for _, f := range []string{"go.mod", "go.sum"} {
		files = append(files, filepath.Join(mod, f))
	}
	for _, sub := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(mod, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		rel, _ := filepath.Rel(mod, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func buildTools() (builtTools, error) {
	buildOnce.Do(func() { buildOut, buildErr = doBuild() })
	return buildOut, buildErr
}

func doBuild() (builtTools, error) {
	mod, err := ModuleRoot()
	if err != nil {
		return builtTools{}, err
	}
	hash, err := srcHash(mod)
	if err != nil {
		return builtTools{}, fmt.Errorf("hash the Go sources: %v", err)
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("factorytest-cache-%d", os.Getuid()), hash)
	t := builtTools{Dir: dir, Factory: filepath.Join(dir, "factory-go"), Stub: filepath.Join(dir, "stub")}
	if exists(t.Factory) && exists(t.Stub) {
		return t, nil
	}
	goBin, err := goTool()
	if err != nil {
		return builtTools{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return builtTools{}, err
	}
	for _, b := range []struct{ out, pkg string }{{t.Factory, "./cmd/factory"}, {t.Stub, "./internal/factorytest/stub"}} {
		tmp := b.out + fmt.Sprintf(".tmp%d", os.Getpid())
		cmd := exec.Command(goBin, "build", "-o", tmp, b.pkg)
		cmd.Dir = mod
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.Remove(tmp)
			return builtTools{}, fmt.Errorf("go build %s failed: %v\n%s\nfix the build first", b.pkg, err, out)
		}
		if err := os.Rename(tmp, b.out); err != nil { // atomic: a racing test binary builds the same bytes
			return builtTools{}, err
		}
		buildCount++
		logBuild(dir, b.pkg)
	}
	return t, nil
}

// logBuild appends one line per build to <cache dir>/build.log so a test can prove the binary is built once.
func logBuild(dir, pkg string) {
	f, err := os.OpenFile(filepath.Join(dir, "build.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "built %s by pid %d\n", pkg, os.Getpid())
}

// BuildLog returns the lines of the build log of the current source hash.
func BuildLog() ([]string, error) {
	t, err := buildTools()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(t.Dir, "build.log"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n"), nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// linkOrCopy puts src at dst: a hard link when the file system allows it (no 20 MB copy per test), else a copy.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
