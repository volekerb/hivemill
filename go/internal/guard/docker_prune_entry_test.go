package guard

// G-060 SC7 / T-1724: agents still cannot run docker prune. The scheduled hm-clean job may prune as the owner's
// user, but no factory agent may gain Docker (PRIV) rights. These tests drive the REAL hook entry point,
// `sh roles/hooks/worker-guard --role <role>` (the shim), with a PreToolUse payload on stdin, under both guard
// implementations the shim offers (python, and go via the run/guard_impl flag plus a stamped binary). The guard
// itself is unchanged by this task. That `hm-clean apply` run by an agent cannot prune is covered by T-1716.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const dockerPrivMsg = "privilege escalation (`docker`) is not allowed for factory agents"

var dockerRepoRoot = func() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Dir(dir) // go.mod lives in <repo>/go
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("no go.mod above the test directory")
		}
		dir = parent
	}
}()

var (
	dockerBinOnce sync.Once
	dockerBinPath string
	dockerBinErr  error
)

// dockerGuardBin builds the Go hook binary once.
func dockerGuardBin(t *testing.T) string {
	t.Helper()
	dockerBinOnce.Do(func() {
		dockerBinPath = filepath.Join(testBinDir, "worker-guard")
		cmd := exec.Command("go", "build", "-o", dockerBinPath, "./cmd/worker-guard")
		cmd.Dir = filepath.Join(dockerRepoRoot, "go")
		if out, err := cmd.CombinedOutput(); err != nil {
			dockerBinErr = fmt.Errorf("go build ./cmd/worker-guard: %v: %s", err, out)
		}
	})
	if dockerBinErr != nil {
		t.Fatalf("%v; fix the Go build first (a Go guard run never skips)", dockerBinErr)
	}
	return dockerBinPath
}

// dockerHook is one runnable hook: the real shim and Python hook copied into a temp live home, so that the
// run/guard_impl flag and the stamped binary can select the implementation without touching the repository.
type dockerHook struct {
	shim string
	env  []string
}

func newDockerHook(t *testing.T, impl string) dockerHook {
	t.Helper()
	live, home := t.TempDir(), t.TempDir()
	hooks := filepath.Join(live, "roles", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"worker-guard", "worker-guard.py"} {
		b, err := os.ReadFile(filepath.Join(dockerRepoRoot, "roles/hooks", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hooks, f), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if impl == "go" {
		bin := dockerGuardBin(t)
		lib := filepath.Join(home, ".local", "lib", "factory")
		if err := os.MkdirAll(lib, 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lib, "worker-guard"), b, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lib, "worker-guard.stamp"), []byte("testhash"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(live, "run"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(live, "run", "guard_impl"), []byte("go testhash\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dockerHook{
		shim: filepath.Join(hooks, "worker-guard"),
		env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "FACTORY_HOME=" + t.TempDir(),
			"WORKER_GUARD_SHIM_TRACE=1"},
	}
}

// run feeds one Bash command to the hook; it returns the exit code, stderr and the implementation the shim chose.
func (h dockerHook) run(t *testing.T, args []string, command string) (int, string, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": command}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{h.shim}, args...)...)
	cmd.Dir = dockerRepoRoot
	cmd.Env = h.env
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running sh roles/hooks/worker-guard: %v", err)
		}
		code = ee.ExitCode()
	}
	out := stderr.String()
	chosen := ""
	if m := regexp.MustCompile(`(?m)^impl=(\w+)$`).FindStringSubmatch(out); m != nil {
		chosen = m[1]
	}
	return code, out, chosen
}

// dockerGuardArgs reads roles/settings/*.json.in and returns, per role, the hook arguments its settings pass to
// the worker guard (`--role <role>`, or `--hq` for the HQ roles that call worker-guard.py directly).
func dockerGuardArgs(t *testing.T) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dockerRepoRoot, "roles/settings/*.json.in"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no roles/settings/*.json.in found under %s: %v", dockerRepoRoot, err)
	}
	re := regexp.MustCompile(`worker-guard(?:\.py)? (--role [a-z-]+|--hq)"`)
	out := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		role := strings.TrimSuffix(filepath.Base(f), ".json.in")
		if m := re.FindSubmatch(b); m != nil {
			out[role] = strings.Fields(string(m[1]))
		}
	}
	return out
}

func TestDockerPruneEntryBlocked(t *testing.T) {
	roles := dockerGuardArgs(t)
	for _, r := range []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "lead"} {
		if roles[r] == nil {
			t.Fatalf("roles/settings/%s.json.in has no worker-guard hook; this test must cover it", r)
		}
	}
	cmds := []string{
		"docker system prune -af",
		"docker image prune -af --filter until=168h",
		"docker builder prune -af",
		"/usr/bin/docker container prune -f",
		"sh -c 'docker image prune -af'",
		"env docker image prune -af",
	}
	for _, impl := range []string{"python", "go"} {
		hook := newDockerHook(t, impl)
		for role, args := range roles {
			for _, c := range cmds {
				t.Run(fmt.Sprintf("DockerPruneEntry/%s/%s/%s", role, impl, c), func(t *testing.T) {
					code, stderr, chosen := hook.run(t, args, c)
					if chosen != impl {
						t.Fatalf("shim chose impl %q, want %q (stderr %q)", chosen, impl, stderr)
					}
					if code == 0 {
						t.Fatalf("%v %q: exit 0, want blocked", args, c)
					}
					if !strings.Contains(stderr, dockerPrivMsg) {
						t.Fatalf("%v %q: exit %d, stderr lacks the PRIV message %q: %q", args, c, code, dockerPrivMsg, stderr)
					}
				})
			}
		}
	}
}

// docker as data only (P2): `ls docs` is the allowed form; the shape `grep -c docker docs/README.md` is tried first.
func TestDockerPruneEntryAllowed(t *testing.T) {
	roles := dockerGuardArgs(t)
	for _, impl := range []string{"python", "go"} {
		hook := newDockerHook(t, impl)
		for role, args := range roles {
			t.Run(fmt.Sprintf("DockerPruneEntry/allowed/%s/%s", role, impl), func(t *testing.T) {
				code, stderr, chosen := hook.run(t, args, "grep -c docker docs/README.md")
				if chosen != impl {
					t.Fatalf("shim chose impl %q, want %q", chosen, impl)
				}
				if code != 0 {
					t.Fatalf("%v: docker as data was blocked (exit %d): %q", args, code, stderr)
				}
			})
		}
	}
}

// every role's settings deny Bash(docker:*), so a role missing from the blocked list above is still covered.
func TestDockerPruneEntrySettingsDeny(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(dockerRepoRoot, "roles/settings/*.json.in"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no roles/settings/*.json.in found: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"Bash(docker:*)"`) {
			t.Errorf("%s has no Bash(docker:*) deny entry; add it or cover the role in the blocked test", f)
		}
	}
}
