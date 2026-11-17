package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"factory/go/internal/shlex"
)

func (r *runState) cliSteps() {
	var factory string
	var env []string
	if !r.step("seed", func() (string, error) { return r.seed() }, false) {
		return
	}
	if !r.step("impl", func() (string, error) {
		var d string
		var err error
		factory, env, d, err = r.impl()
		return d, err
	}, false) {
		return
	}
	for i, s := range r.cfg.Setup {
		argv, err := shlex.Split(s)
		name := "setup-" + strconv.Itoa(i+1)
		if !r.step(name, func() (string, error) {
			if err != nil {
				return "", fmt.Errorf("bad --setup %q: %v", s, err)
			}
			code, tail, rerr := r.execLog(name, factory, argv, r.tree, env)
			if rerr != nil {
				return "", rerr
			}
			if code != 0 {
				return "", fmt.Errorf("setup %q exit %d: %s", s, code, tail)
			}
			return fmt.Sprintf("exit=0 args=%q", s), nil
		}, false) {
			return
		}
	}
	code := -1
	if !r.step("run", func() (string, error) {
		so, err := os.Create(filepath.Join(r.out, "stdout.txt"))
		if err != nil {
			return "", couldNotRun("create stdout.txt: %v", err)
		}
		defer so.Close()
		se, err := os.Create(filepath.Join(r.out, "stderr.txt"))
		if err != nil {
			return "", couldNotRun("create stderr.txt: %v", err)
		}
		defer se.Close()
		code, err = r.exec(factory, r.cfg.Args, r.tree, env, so, se)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("exit=%d", code), nil
	}, false) {
		return
	}
	if !r.step("check-exit", func() (string, error) {
		if code != r.cfg.ExpectExit {
			return "", fmt.Errorf("exit code %d, expected %d", code, r.cfg.ExpectExit)
		}
		return fmt.Sprintf("exit=%d", code), nil
	}, false) {
		return
	}
	for _, c := range []struct {
		kind string
		file string
		res  []*regexp.Regexp
	}{{"stdout", "stdout.txt", r.cfg.ExpectStdout}, {"stderr", "stderr.txt", r.cfg.ExpectStderr}} {
		for i, re := range c.res {
			if !r.step(fmt.Sprintf("check-%s-%d", c.kind, i+1), func() (string, error) {
				b, err := os.ReadFile(filepath.Join(r.out, c.file))
				if err != nil {
					return "", couldNotRun("read %s: %v", c.file, err)
				}
				if !re.Match(b) {
					return "", fmt.Errorf("%s does not match /%s/", c.kind, re)
				}
				return "match=/" + re.String() + "/", nil
			}, false) {
				return
			}
		}
	}
}

func (r *runState) seed() (string, error) {
	if r.cfg.Seed == "empty" {
		// An empty home still needs the DB schema, or every command fails with "no database": run the tree's own init.
		bin := filepath.Join(r.tree, "bin", "factory")
		code, tail, err := r.execLog("seed", bin, []string{"init"}, r.tree, r.childEnv("FACTORY_CLI_IMPL=python"))
		if err != nil {
			return "", err
		}
		if code != 0 {
			return "", couldNotRun("factory init exit %d: %s", code, tail)
		}
		return "seed=empty (factory init)", nil
	}
	py, err := r.lookPath("python3")
	if err != nil {
		return "", err
	}
	script := filepath.Join(r.tree, "ci", "demo_seed.py")
	if _, err := os.Stat(script); err != nil {
		return "", couldNotRun("seed script: %v", err)
	}
	// demo_seed.py refuses a home that equals FACTORY_HOME, so the seed runs without FACTORY_HOME and gets --home instead.
	var seedEnv []string
	for _, e := range r.childEnv() {
		if !strings.HasPrefix(e, "FACTORY_HOME=") {
			seedEnv = append(seedEnv, e)
		}
	}
	// demo_seed.py is committed without the executable bit, so it runs through python3.
	code, tail, err := r.execLog("seed", py, []string{script, "--home", r.tmp, "--force"}, r.tree, seedEnv)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", couldNotRun("seed exit %d: %s", code, tail)
	}
	return "seed=demo", nil
}

func (r *runState) impl() (string, []string, string, error) {
	if r.cfg.Impl == "python" {
		bin := filepath.Join(r.tree, "bin", "factory")
		if st, err := os.Stat(bin); err != nil || st.IsDir() {
			return "", nil, "", couldNotRun("%s not found", bin)
		}
		return bin, r.childEnv("FACTORY_CLI_IMPL=python"), "impl=python", nil
	}
	goBin, err := r.lookPath("go")
	if err != nil {
		if h, ok := envValue(r.environ, "HOME"); ok {
			p := filepath.Join(h, ".local", "go", "bin", "go")
			if _, serr := os.Stat(p); serr == nil {
				goBin, err = p, nil
			}
		}
	}
	if err != nil {
		return "", nil, "", err
	}
	out := filepath.Join(r.tmp, "factory-go")
	// The build keeps the inherited cache environment (a cold cache under the temp HOME would be slow); no FACTORY_* leaks in.
	var benv []string
	for _, e := range r.environ {
		if k, _, _ := strings.Cut(e, "="); !strings.HasPrefix(k, "FACTORY_") {
			benv = append(benv, e)
		}
	}
	benv = append(benv, "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off")
	code, tail, err := r.execLog("build", goBin, []string{"build", "-o", out, "./cmd/factory"}, filepath.Join(r.tree, "go"), benv)
	if err != nil {
		return "", nil, "", err
	}
	if code != 0 {
		return "", nil, "", fmt.Errorf("go build exit %d: %s", code, tail)
	}
	return out, r.childEnv(), "impl=go", nil
}
