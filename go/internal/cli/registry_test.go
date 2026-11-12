package cli

import (
	"os"

	"factory/go/internal/identity"
	"reflect"
	"strings"
	"testing"
)

// withHandler registers a test-only command for the duration of t.
func withHandler(t *testing.T, name, action string, h handler) {
	t.Helper()
	register(name, action, h)
	t.Cleanup(func() { delete(registry, key{name, action}) })
}

func okParse(rest []string) (any, bool) { return rest, true }

func TestRegistry(t *testing.T) {
	t.Run("duplicate key panics", func(t *testing.T) {
		withHandler(t, "zz-dup", "act", handler{parse: okParse})
		defer func() {
			r := recover()
			if r == nil || !strings.Contains(r.(string), "duplicate registration") {
				t.Fatalf("want a duplicate-registration panic, got %v", r)
			}
		}()
		register("zz-dup", "act", handler{parse: okParse})
	})
	t.Run("the shipped registrations are unique and present", func(t *testing.T) {
		for _, n := range []string{"whoami", "agents", "flags", "config"} {
			if _, ok := registry[key{n, ""}]; !ok {
				t.Errorf("%s is not registered", n)
			}
		}
	})
	t.Run("action key routes and passes the rest", func(t *testing.T) {
		withHandler(t, "zz-task", "show", handler{parse: okParse})
		h, p, ok := lookup([]string{"zz-task", "show", "T-1", "--x"})
		if !ok || h.name != "zz-task" || h.action != "show" || !reflect.DeepEqual(p, []string{"T-1", "--x"}) {
			t.Fatalf("lookup = %+v %v %v", h, p, ok)
		}
	})
	t.Run("single key routes", func(t *testing.T) {
		withHandler(t, "zz-one", "", handler{parse: okParse})
		_, p, ok := lookup([]string{"zz-one", "a", "b"})
		if !ok || !reflect.DeepEqual(p, []string{"a", "b"}) {
			t.Fatalf("lookup = %v %v", p, ok)
		}
	})
	t.Run("unknown action delegates", func(t *testing.T) {
		withHandler(t, "zz-task", "show", handler{parse: okParse, run: func(*ctx, any) (int, error) { return 0, nil }})
		for _, args := range [][]string{{"zz-task", "bogus"}, {"zz-task"}, {"nothing-here"}, nil} {
			r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
			r.run(args...)
			if len(r.execs) != 1 || r.out.Len() != 0 {
				t.Fatalf("%v: want one exec and no output, got %d execs, stdout %q", args, len(r.execs), r.out.String())
			}
		}
	})
	t.Run("empty action is an argument and delegates", func(t *testing.T) {
		for _, name := range []string{"whoami", "agents", "flags", "config"} {
			if _, _, ok := lookup([]string{name, ""}); ok {
				t.Fatalf("%s with an empty argument must delegate", name)
			}
		}
	})
	t.Run("parse false delegates", func(t *testing.T) {
		withHandler(t, "zz-no", "", handler{parse: func([]string) (any, bool) { return nil, false },
			run: func(*ctx, any) (int, error) { t.Fatal("run must not be called"); return 0, nil }})
		r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
		r.run("zz-no", "x")
		if len(r.execs) != 1 {
			t.Fatalf("want one exec, got %d", len(r.execs))
		}
	})
	t.Run("raw exit writes stderr verbatim", func(t *testing.T) {
		withHandler(t, "zz-raw", "", handler{parse: okParse,
			run: func(*ctx, any) (int, error) { return 0, &rawExit{Code: 3, Stderr: "x\n"} }})
		r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
		if code := r.run("zz-raw"); code != 3 || r.errb.String() != "x\n" || r.out.Len() != 0 || len(r.execs) != 0 {
			t.Fatalf("exit %d stderr %q stdout %q execs %d", code, r.errb.String(), r.out.String(), len(r.execs))
		}
	})
	t.Run("handler sees ctx and its output is flushed", func(t *testing.T) {
		withHandler(t, "zz-ctx", "", handler{parse: okParse, run: func(c *ctx, p any) (int, error) {
			if c.who != "owner" || c.db == nil || c.deps == nil || c.deps.pid() != 41 || c.deps.ppid() != 42 {
				t.Errorf("ctx = %+v", c)
			}
			c.out.WriteString("hi\n")
			return 5, nil
		}})
		r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
		if code := runWithPids(r, 41, 42, "zz-ctx"); code != 5 || r.out.String() != "hi\n" {
			t.Fatalf("exit %d stdout %q", code, r.out.String())
		}
	})
}

// runWithPids is rig.run with fixed Getpid/Getppid (cli_test.go's rig has no hook for them).
func runWithPids(r *rig, pid, ppid int, args ...string) int {
	r.out.Reset()
	r.errb.Reset()
	r.execs = nil
	var environ []string
	for k, v := range r.env {
		environ = append(environ, k+"="+v)
	}
	return Run(Deps{
		Args: args, Argv0: r.repo + "/bin/factory", Environ: environ,
		Env: identity.Env{
			Getenv:    func(k string) string { return r.env[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := r.env[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return r.pw, nil },
		},
		Stdout: &r.out, Stderr: &r.errb, Getpid: func() int { return pid }, Getppid: func() int { return ppid },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { return nil },
	})
}
