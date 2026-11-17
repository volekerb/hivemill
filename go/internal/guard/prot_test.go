package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		"GOCACHE=~/.local/lib/factory/x " + g + "go test", g + "../../go/bin/go test", g + "gofmt -w ../x.go", g + "gofmt -w",
		g + "go test -exec \"git push\" ./...", g + "go build -toolexec=/tmp/x ./...", g + "go vet -vettool=/tmp/x ./...",
		"GOFLAGS=-exec=/tmp/x " + g + "go test ./...",
		g + "go fmt ~/.local/go/src/cmd/gofmt", g + "go fmt ~/.local/lib/factory/x", "cd ~/.local/go/src/cmd/gofmt && " + g + "go fmt",
		g + "go fmt ../x", g + "go fmt /etc", g + "go build -pkgdir ~/.local/go/pkg ./...", g + "go test -pkgdir=~/.local/go/pkg ./...",
		g + "go test -test.coverprofile=~/.local/go/c.out ./...", g + "go test -test.cpuprofile ~/.local/go/c ./...",
		g + "go test -test.outputdir=~/.local/go ./...", g + "go test -test.trace=~/.local/lib/factory/t ./...",
		g + "go test ./... -args -test.coverprofile=~/.local/go/c.out", g + "go test -test.memprofile ~/.local/go/m ./...",
		"cd ~/.local/go/src/cmd/gofmt && " + g + "go build", "cd ~/.local/go/src/cmd/gofmt && " + g + "go test -c",
		"cd ~/.local/lib/factory && " + g + "go build ./...", g + "go -C ~/.local/go/src/cmd/gofmt build"}
	block = append(block, g+"gofmt -w=1 ../x.go", g+"gofmt -w=t ../x.go", g+"gofmt -w=TRUE ../x.go", g+"go -C ~/.local/go/src/cmd/gofmt fmt", g+"go -C /etc fmt",
		"cd ~/.local/go/src/cmd/gofmt && GOFLAGS=-c "+g+"go test", "cd ~/.local/go/src/cmd/gofmt && GOFLAGS=-c=true "+g+"go test",
		"GOMODCACHE=~/.local/lib/factory/go/mod "+g+"go test ./...")
	allow = append(allow, g+"gofmt -w=0 x.go", g+"gofmt -w=true x.go", g+"gofmt -w=1 x.go",
		"GOPROXY=off GOMODCACHE=~/.local/lib/factory/go/mod "+g+"go test ./...", "GOFLAGS=-c "+g+"go test ./...", g+"go -C . fmt")
	for _, c := range []string{"~/.local/go/bin/gofmt -cpuprofile ~/.local/go/x .", "~/.local/go/bin/go -C ~/.local/go/src/cmd/gofmt build -o out", "~/.local/go/bin/go -C ~/.local/go/src/cmd/gofmt test -c -o t.test", "~/.local/go/bin/go test -test.blockprofile ~/.local/go/b ./...", "~/.local/go/bin/go test -test.mutexprofile=~/.local/go/m ./...", "~/.local/go/bin/go test -test.gocoverdir=~/.local/go ./...", "~/.local/go/bin/go test -test.fuzzcachedir=~/.local/go ./...", "~/.local/go/bin/go test -test.testlogfile=~/.local/go/l ./...", "~/.local/go/bin/go build -modfile ~/.local/go/m.mod ./..."} {
		block = append(block, strings.ReplaceAll(c, "~/.local/go/bin/", g))
	}
	allow = append(allow, g+"go fmt ./...", g+"go fmt", g+"go build", g+"go test -c", g+"go build -o bin/x ./cmd/x",
		g+"go test -test.coverprofile=c.out ./...", g+"go test -c -o x.test", g+"go build -o /dev/null ./...",
		g+"go test -run TestX -v -count=1 -race -timeout 5m -short -cover ./...", g+"go test -mod=readonly ./...", g+"go test -mod readonly ./...",
		g+"go vet ./...", g+"gofmt -l -s -d .", "GOPROXY=off GOFLAGS='-mod mod' "+g+"go test ./...", "GOPROXY=off "+g+"go test -mod=mod ./...",
		g+"go test -run '-o' ./...")
	// T-1498 review round 3 (N5, N6): blocked by the per-subcommand flag allow-list, default deny
	pt := "cd ~/.local/go/src/cmd/gofmt && " + g
	block = append(block, pt+"go test -c -args -C /tmp", pt+"go test -c -run -o", pt+"go build -tags -o", pt+"go test -c -args -o x",
		g+"go test -mod mod ./...", g+"go test --mod=mod ./...", "GOFLAGS='-mod mod' "+g+"go test ./...", "GOFLAGS=--mod=mod "+g+"go test ./...",
		g+"go test ./... -args -test.v", g+"go build -pkgdir /tmp/pk ./...", g+"gofmt -cpuprofile /tmp/p .", g+"go test -c -o /tmp/outside.test",
		g+"go -x build ./...", g+"go test -overlay /tmp/o.json ./...", g+"go test -C /tmp -run X", g+"go test -run",
		g+"go build -ldflags=-extld=/tmp/evil ./...", g+"go build -gcflags=-json=0,/x ./...", g+"go test -ldflags=-extld=/tmp/evil ./...",
		g+"go fmt strings", g+"go fmt std", g+"go fmt cmd/...", g+"go test -fuzz=. strings", g+"go test -fuzz=. ~/.local/go/src/strings",
		g+"go list -m foo@latest", "cd ~/.local/go/src/strings && "+g+"go test -fuzz=.", "GOFLAGS=-fuzz=. "+g+"go test strings")
	// T-1498 review round 4 (N7, N8): `--` is not pre-stripped (a value flag consumes it); profile flags keep pkg.test in the cwd
	block = append(block, g+"go build -tags -- -pkgdir ~/.local/go/pkg ./x", g+"go build -tags -- -toolexec=/tmp/x ./x",
		g+"go test -run -- -exec /tmp/x ./x", g+"go test -c -run -- -o /etc/evilout ./x", g+"go test -- -test.cpuprofile=/tmp/x",
		"GOFLAGS=-- "+g+"go test ./...", pt+"go test -cpuprofile /dev/null", pt+"go test -memprofile=/dev/null .",
		pt+"go test -test.blockprofile /dev/null", pt+"go test -mutexprofile /dev/null", pt+"go test -trace /dev/null",
		pt+"go test -coverprofile /dev/null", g+"go build $'-o' ~/.local/lib/factory/factory-go ./x", g+"go build ${X}-o ~/.local/lib/factory/factory-go ./x",
		g+"go test $'-exec' /tmp/x ./x", g+"go build $'-toolexec=/tmp/x' ./x")
	// T-1498 review round 5 (N9, N10): any word holding an expansion or glob is refused (default deny, once in tcParse)
	block = append(block, g+"go test -c ./x$X",
		"X=' -o ~/.local/lib/factory/w'; "+g+"go test -c ./x$X",
		"bash -c 'X=\" -o ~/.local/lib/factory/x\"; "+g+"go test -c ./x$X'",
		"X=' -exec /tmp/x'; "+g+"go test ./x$X",
		g+"go test -c -run $X ./x",
		g+"go test -c -run a$X ./x",
		g+"go build -tags $X ./x",
		g+"go test -c ./x [-]o ~/.local/lib/factory/x",
		g+"go test -c ./x [-]exec /tmp/e",
		g+"go test -c ./x [-]pkgdir ~/.local/go/pkg",
		g+"go test ./x [-]toolexec=/tmp/e",
		g+"go test -c ./x *o ~/.local/lib/factory/x",
		g+"go test -c ./x ?o ~/.local/lib/factory/x",
		g+"go test ./x a~b",
		g+"go test ./x ~user/y")
	// T-1498 review round 6: the same rule in every position (prefix env value, flag, flag value, positional, after --)
	for _, p := range []string{"GOFLAGS=-run=%s {g}go test ./x", "GOCACHE=/tmp/%s {g}go test ./x", "GOFLAGS=%s {g}go test ./x", "{g}go test -run %s ./x",
		"{g}go test -tags=%s ./x", "{g}go test ./x%s", "{g}go test ./x %s", "{g}go vet -- ./x%s", "{g}go vet -- %s", "{g}gofmt -l %s", "{g}gofmt -r %s ."} {
		for _, m := range []string{"$x", "${x}", "$(id)", "`id`", "[-]o", "*", "?", "{a,b}"} {
			block = append(block, fmt.Sprintf(strings.ReplaceAll(p, "{g}", g), m))
		}
	}
	allow = append(allow, g+"go build ./x ./y/...", g+"go vet -- ./...", g+"go test -cpuprofile /dev/null ./...", g+"go build -tags x -- ./...")
	allow = append(allow, g+"go fmt ./pkg", g+"go test -fuzz=. -fuzztime=5s ./pkg", "GOPROXY=off "+g+"go list -m foo@latest", g+"go list -m all")
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

// T-1498 N11: an empty go subcommand word (an empty quoted word after go) once looped forever in the Go port; the refusal must come back.
func TestGoEmptySubcommandReturns(t *testing.T) {
	_, wt, _ := protFixture(t)
	g := "~/.local/go/bin/"
	for _, c := range []string{g + "go ''", g + `go ""`, g + `go "" test ./...`, g + "go '' test", g + "go -C . ''"} {
		done := make(chan error, 1)
		go func() { done <- Check(c, wt, "coder", false) }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%q: expected a block", c)
			} else if !strings.Contains(err.Error(), "is not allowed") {
				t.Errorf("%q: expected the `go  is not allowed` refusal, got %v", c, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: Check did not return within 2s (empty subcommand loop)", c)
		}
	}
}

// T-1498 N11 follow-up: `go` on PATH with no subcommand word stays allowed for every role (Python parity);
// only an empty quoted word is refused.
func TestGoNoSubcommandUnchanged(t *testing.T) {
	_, wt, _ := protFixture(t)
	for _, role := range []string{"coder", "qa", "pm", "judge"} {
		for _, c := range []string{"go", "go -C .", "go -v"} {
			if err := Check(c, wt, role, false); err != nil {
				t.Errorf("%s: %q: expected allow, got %v", role, c, err)
			}
		}
		if err := Check("go ''", wt, role, false); err == nil {
			t.Errorf("%s: %q: expected a block", role, "go ''")
		}
	}
}
