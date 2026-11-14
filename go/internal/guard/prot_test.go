package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-0925: the protected trees for workers (tests/test_worker_guard_prot_trees.py is the behaviour contract; this runs
// the Go port on the same shapes). The live checkout is a temp dir named by WORKER_GUARD_LIVE_HOME.

func protFixture(t *testing.T) (live, wt, other string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	live, other = filepath.Join(root, "live"), filepath.Join(root, "other")
	wt = filepath.Join(live, "worktrees", "G-000-T-0000")
	for _, d := range []string{"bin", "go", "roles/hooks", "roles/settings"} {
		for _, base := range []string{live, wt} {
			if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_GUARD_LIVE_HOME", live)
	return live, wt, other
}

func TestProtTreesBlocked(t *testing.T) {
	live, wt, other := protFixture(t)
	cmds := []string{
		"echo x > ~/.local/lib/fact*/worker-guard.stamp", "echo x > ~/.local/g?/go.env", "echo x > ~/.config/go/env",
		"echo x > @L/run/settin?s/coder.json", "echo x > @L/rol*/hooks/worker-guard.py",
		"curl -sSLo ~/.local/go/go.env URL", "wget -O ~/.local/lib/factory/worker-guard URL", "wget -P ~/.local/go URL",
		"go build -o @L/bin/x ./cmd/x", "gcc -o ~/.local/go/x a.c", "tar -C ~/.local/go -xf a.tar",
		"tar xf a.tar --directory=@L/go", "tar -cf ~/.local/go/x.tar foo", "unzip -d ~/.config/factory a.zip",
		"7z x a.7z -o@L/bin", "go env -w GOFLAGS=-mod=mod", "go env -u GOFLAGS", "go -C ~/.local/go build ./...",
		"cd ~/.local/lib/factory && tar -xf a.tar", "cd @L/bin && echo x > f", "cd @L/go && unzip a.zip",
		"cd $X && tar -xf a.tar", "tar --one-top-level=$HOME/.local/lib/factory -xf a.tar",
		"git clone URL @L/bin/x", "git -C ~/.local/go clone URL", "git clone --separate-git-dir ~/.local/go/g URL /tmp/z",
		"rm -r @L/go", "sed -i s/a/b/ @L/bin/factory_cli.py", "mv @L/bin /tmp/y",
	}
	for _, c := range cmds {
		c = strings.ReplaceAll(c, "@L", live)
		for _, cwd := range []string{other, live, wt} {
			if err := Check(c, cwd, "coder", false); err == nil {
				t.Errorf("%q from %s: expected a block", c, cwd)
			}
		}
	}
}

func TestProtTreesAllowed(t *testing.T) {
	live, wt, other := protFixture(t)
	cmds := []string{
		"curl -o /tmp/x URL", "wget -qO- URL", "go build -o /tmp/x ./cmd/x", "tar -C /tmp/x -xf a.tar",
		"tar --one-top-level=/tmp/x -xf a.tar", "cd /tmp/x && tar -xf a.tar", "cd $X && echo x > f", "ls ~/.config/fact*",
		"go env", "go env GOFLAGS", "go version", "tar -tf a.tar", "unzip -l a.zip", "tar -czf /tmp/x.tgz -C ~/.local/go .",
		"cat ~/.local/go/go.env", "cp ~/.local/go/go.env /tmp/x", "echo x > ~/.local/gox/y", "echo x > @L/binx/y",
		"echo x > @L/gox/y", "echo x > @L/README.md", "cat @L/bin/factory_cli.py", "@L/bin/factory whoami",
		"echo x > @L/worktrees/G-000-T-0000/bin/x", "git clone URL /tmp/x",
	}
	for _, c := range cmds {
		c = strings.ReplaceAll(c, "@L", live)
		if err := Check(c, other, "coder", false); err != nil {
			t.Errorf("%q from other: %v", c, err)
		}
	}
	// the same relative command: refused in the live checkout, allowed in a worktree or elsewhere
	for _, c := range []string{"echo x > bin/factory_cli.py", "cp /tmp/x go/x.go", "go build -o ./bin/x ./cmd/x"} {
		if err := Check(c, live, "coder", false); err == nil {
			t.Errorf("%q from the live checkout: expected a block", c)
		}
		for _, cwd := range []string{wt, other} {
			if err := Check(c, cwd, "coder", false); err != nil {
				t.Errorf("%q from %s: %v", c, cwd, err)
			}
		}
	}
}

func TestProtEditTools(t *testing.T) {
	live, wt, _ := protFixture(t)
	home := mustHost().pwDir
	for _, p := range []string{home + "/.local/lib/factory/worker-guard", home + "/.config/factory/live_home",
		home + "/.local/go/go.env", home + "/.config/go/env", live + "/bin/factory_cli.py", live + "/go/cmd/x.go",
		live + "/roles/hooks/worker-guard.py", live + "/roles/settings/coder.json.in", live + "/run/settings/coder.json"} {
		data := map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": p}, "cwd": wt}
		if rc, _ := PersistEdit(data, "coder"); rc != 2 {
			t.Errorf("Write %s: exit %d, want 2", p, rc)
		}
	}
	for _, p := range []string{wt + "/bin/x", wt + "/go/x.go", live + "/binx/y", live + "/README.md", home + "/.local/gox/y", "/tmp/y"} {
		data := map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": p}, "cwd": wt}
		if rc, msg := PersistEdit(data, "coder"); rc != 0 {
			t.Errorf("Write %s: exit %d (%s), want 0", p, rc, msg)
		}
	}
}

func TestOptValues(t *testing.T) {
	cases := []struct {
		args    []string
		letters string
		longs   []string
		want    string
	}{
		{[]string{"-o", "x"}, "o", nil, "x"},
		{[]string{"-ox"}, "o", nil, "x"},
		{[]string{"-sSLo", "x", "URL"}, "o", nil, "x"},
		{[]string{"--output=x"}, "", []string{"--output"}, "x"},
		{[]string{"--output", "x"}, "", []string{"--output"}, "x"},
		{[]string{"-o", "x", "-o", "y"}, "o", nil, "x y"},
		{[]string{"-fPIC"}, "o", nil, ""},
	}
	for _, c := range cases {
		if got := strings.Join(optValues(c.args, c.letters, c.longs...), " "); got != c.want {
			t.Errorf("optValues(%q, %q, %q) = %q, want %q", c.args, c.letters, c.longs, got, c.want)
		}
	}
}

// T-1498: the Go toolchain for workers (tests/test_worker_guard_prot_trees.py GoToolchainForWorkers is the contract).
func TestGoToolchainForWorkers(t *testing.T) {
	_, wt, _ := protFixture(t)
	g := "~/.local/go/bin/"
	allow := []string{g + "go test ./...", g + "go build ./cmd/x", g + "go version", g + "go mod verify",
		"GOTOOLCHAIN=local GOFLAGS=-mod=mod GOPROXY=off GOMODCACHE=~/.local/lib/factory/go/mod " + g + "go vet ./...",
		"CGO_ENABLED=0 GOCACHE=/tmp/x " + g + "go test ./...", g + "gofmt -l .", g + "gofmt -d -s .",
		"$HOME/.local/go/bin/go test ./...", "PATH=~/.local/go/bin:$PATH " + g + "go test ./...", g + "gofmt -w x.go"}
	block := []string{g + "go mod download", g + "go get x@v1", g + "go install ./...", g + "go mod tidy", g + "go clean -modcache",
		g + "go work sync", g + "go", "GOFLAGS=-mod=mod " + g + "go test ./...", "GOPROXY=on " + g + "go test", "GOROOT=/tmp/x " + g + "go test",
		"echo x > ~/.local/go/bin/go", "cp /tmp/x ~/.local/go/bin/go", g + "go build -o ~/.local/lib/factory/factory-go ./cmd/factory",
		"GOBIN=~/.local/go/bin " + g + "go install ./...", g + "go env -w GOFLAGS=x", g + "go test -coverprofile=~/.local/go/c.out ./...",
		"GOCACHE=~/.local/lib/factory/x " + g + "go test", g + "../../go/bin/go test", g + "gofmt -w ../x.go", g + "gofmt -w"}
	for _, role := range []string{"coder", "qa", "reviewer"} {
		for _, c := range allow {
			if err := Check(c, wt, role, false); err != nil {
				t.Errorf("%s: %q: expected allow, got %v", role, c, err)
			}
		}
		for _, c := range block {
			if err := Check(c, wt, role, false); err == nil {
				t.Errorf("%s: %q: expected a block", role, c)
			}
		}
	}
	for _, role := range []string{"pm", "judge"} { // other roles: as on origin/main
		for _, c := range []string{g + "go test ./...", g + "go mod download"} {
			if err := Check(c, wt, role, false); err != nil {
				t.Errorf("%s: %q: unchanged roles must still be allowed, got %v", role, c, err)
			}
		}
	}
}
