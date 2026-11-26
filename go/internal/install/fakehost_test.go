package install

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRepo = "https://github.com/volekerb/hivemill.git"

const testAgents = "NAME ORG ROLE STATUS\nboss claude boss live\ndirector claude director live\n"

const testTags = "aaaa\trefs/tags/v0.1.0\naaaa\trefs/tags/v0.10.0\naaaa\trefs/tags/v0.9.0\naaaa\trefs/tags/v1.0.0-rc1\n"

// fakeHost runs the installer over a temporary HOME. Reads of the file system are real; every command, download and
// background process is answered by the test, so nothing here touches the network, herdr, systemd or the real home.
type fakeHost struct {
	*OSHost
	mu      sync.Mutex
	euid    int
	machine string
	goos    string // "" means linux
	memsize string // darwin: the `sysctl -n hw.memsize` output; "fail" makes it exit 1
	mem     float64
	cpus    int
	tools   map[string]bool
	ghAuth  bool
	systemd bool
	tags    string
	origin  string
	rules   func(argv []string) (int, string, bool)
	assets  map[string][]byte
	httpRC  map[string]int

	tty     bool
	ttyRuns [][]string
	ttyRun  func(f *fakeHost, argv []string) (int, string) // the fake `gh auth login`; its output goes to ttyOut only
	ttyOut  strings.Builder                                // what the user would see on the terminal; never the installer log
	enters  int                                            // Enter presses the test will give
	waited  int
	onEnter func(f *fakeHost)

	calls   [][]string
	muts    [][]string
	popens  [][]string
	downs   []string
	removed []string
}

func newFake(t *testing.T) *fakeHost {
	t.Helper()
	home := t.TempDir()
	env := map[string]string{"PATH": filepath.Join(home, ".local", "bin") + ":/usr/bin"}
	f := &fakeHost{
		OSHost: &OSHost{HomeDir: home, Env: env}, euid: 1000, machine: "x86_64", mem: 16, cpus: 8,
		tools:  map[string]bool{"git": true, "herdr": true, "gh": true, "claude": true, "python3": true},
		ghAuth: true, systemd: true, tags: testTags, origin: testRepo, assets: map[string][]byte{}, httpRC: map[string]int{},
	}
	return f
}

func (f *fakeHost) login(t *testing.T) {
	t.Helper()
	p := filepath.Join(f.HomeDir, ".claude")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeHost) Euid() int       { return f.euid }
func (f *fakeHost) Machine() string { return f.machine }
func (f *fakeHost) GOOS() string {
	if f.goos == "" {
		return "linux"
	}
	return f.goos
}
func (f *fakeHost) MemGiB() (float64, error) {
	return f.mem, nil
}
func (f *fakeHost) CPUCount() int         { return f.cpus }
func (f *fakeHost) Sleep(d time.Duration) {}
func (f *fakeHost) HTTPStatus(url string) int {
	if rc, ok := f.httpRC[url]; ok {
		return rc
	}
	return 200
}
func (f *fakeHost) Download(url, dest string) error {
	f.mu.Lock()
	f.downs = append(f.downs, url)
	f.mu.Unlock()
	b, ok := f.assets[url]
	if !ok {
		return stepf("cannot fetch %s", url)
	}
	return os.WriteFile(dest, b, 0o644)
}
func (f *fakeHost) Popen(argv []string, logpath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.popens = append(f.popens, argv)
	return nil
}

func (f *fakeHost) Run(argv []string, cwd string, timeout time.Duration, mutate bool) (int, string) {
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	if mutate {
		f.muts = append(f.muts, argv)
	}
	f.mu.Unlock()
	if f.rules != nil {
		if rc, out, ok := f.rules(argv); ok {
			return rc, out
		}
	}
	joined := strings.Join(argv, " ")
	name := filepath.Base(argv[0])
	switch {
	case joined == "sysctl -n hw.memsize":
		if f.memsize == "fail" {
			return 1, "sysctl: unknown oid"
		}
		return 0, f.memsize + "\n"
	case len(argv) == 2 && argv[1] == "--version" && name == "python3":
		return 0, "Python 3.13.5\n"
	case len(argv) == 2 && argv[1] == "--version" && f.tools[name]:
		return 0, name + " version 1.2.3\n"
	case len(argv) == 2 && argv[1] == "version" && name == "go":
		return 1, "go: not found"
	case name == "gh" && joined == "gh auth status":
		if f.ghAuth {
			return 0, "Logged in"
		}
		return 1, "not logged in"
	case joined == "systemctl --user show-environment":
		if f.systemd {
			return 0, "HOME=x"
		}
		return 1, "Failed to connect to bus"
	case len(argv) >= 3 && argv[0] == "git" && f.tools["git"] && argv[1] == "ls-remote":
		return 0, f.tags
	case len(argv) >= 5 && argv[0] == "git" && argv[1] == "-C" && argv[3] == "remote":
		return 0, f.origin + "\n"
	case len(argv) >= 4 && argv[0] == "git" && argv[1] == "clone":
		if err := os.MkdirAll(argv[len(argv)-1], 0o755); err != nil {
			return 1, err.Error()
		}
		return 0, "Cloning"
	case argv[0] == "git" && f.tools["git"]:
		return 0, ""
	case name == "factory" && len(argv) == 2 && argv[1] == "--version":
		return 0, "hivemill 0.4.0\n"
	case name == "herdr" && len(argv) == 3 && argv[1] == "workspace":
		return 0, "ok"
	case name == "factory" || name == "bash" || name == "systemctl" || name == "loginctl" || name == "launchctl":
		return 0, ""
	}
	return 127, "not found: " + joined
}

func (f *fakeHost) mutated(sub string) bool {
	for _, m := range f.muts {
		if strings.Contains(strings.Join(m, " "), sub) {
			return true
		}
	}
	return false
}

func (f *fakeHost) IsTerminal() bool { return f.tty }

func (f *fakeHost) RunTTY(argv []string) int {
	f.mu.Lock()
	f.ttyRuns = append(f.ttyRuns, argv)
	f.mu.Unlock()
	if f.ttyRun == nil {
		return 1
	}
	rc, out := f.ttyRun(f, argv)
	f.ttyOut.WriteString(out)
	return rc
}

func (f *fakeHost) WaitEnter() error {
	f.waited++
	if f.waited > f.enters {
		return io.EOF
	}
	if f.onEnter != nil {
		f.onEnter(f)
	}
	return nil
}
