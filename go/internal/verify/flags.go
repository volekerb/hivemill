package verify

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config is the parsed command line.
type Config struct {
	Mode         string
	Tree, Out    string
	Timeout      time.Duration
	Impl, Seed   string
	Setup        []string
	ExpectExit   int
	ExpectStdout []*regexp.Regexp
	ExpectStderr []*regexp.Regexp
	Args         []string
}

type listFlag struct {
	vals *[]string
	chk  func(string) error
}

func (l listFlag) String() string { return "" }
func (l listFlag) Set(v string) error {
	if l.chk != nil {
		if err := l.chk(v); err != nil {
			return err
		}
	}
	*l.vals = append(*l.vals, v)
	return nil
}

func compileAll(src []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, s := range src {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("bad regex %q: %v", s, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// ParseArgs parses MODE and its flags. The mode defaults to cli in the result line when MODE itself is bad.
func ParseArgs(args []string) (Config, error) {
	c := Config{Mode: "cli", ExpectExit: 0}
	if len(args) == 0 {
		return c, errors.New("usage: verify cli|mod|web [flags] [-- FACTORY ARGS...]")
	}
	switch args[0] {
	case "cli", "mod", "web":
		c.Mode = args[0]
	default:
		return c, fmt.Errorf("unknown mode %q (want cli, mod or web)", args[0])
	}
	var usage bytes.Buffer
	fs := flag.NewFlagSet("verify "+c.Mode, flag.ContinueOnError)
	fs.SetOutput(&usage)
	fs.StringVar(&c.Tree, "tree", "", "code under test (default: git toplevel of cwd)")
	fs.StringVar(&c.Out, "out", "", "artefact directory (default: new temp dir)")
	c.Timeout = 120 * time.Second
	if c.Mode != "cli" {
		c.Timeout = 60 * time.Second
	}
	fs.DurationVar(&c.Timeout, "timeout", c.Timeout, "whole-run timeout")
	var setup, so, se []string
	if c.Mode == "cli" {
		c.Impl, c.Seed = "python", "empty"
		fs.StringVar(&c.Impl, "impl", c.Impl, "python or go")
		fs.StringVar(&c.Seed, "seed", c.Seed, "empty or demo")
		fs.Var(listFlag{&setup, nil}, "setup", "factory args to run before the main command (repeatable)")
		fs.Var(listFlag{&so, nil}, "expect-stdout", "regex stdout must match (repeatable)")
		fs.Var(listFlag{&se, nil}, "expect-stderr", "regex stderr must match (repeatable)")
		fs.Func("expect-exit", "expected exit code (default 0)", func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			c.ExpectExit = n
			return nil
		})
	}
	if err := fs.Parse(args[1:]); err != nil {
		return c, fmt.Errorf("bad flags: %v", err)
	}
	c.Args = fs.Args()
	if c.Timeout <= 0 {
		return c, errors.New("bad flags: --timeout must be positive")
	}
	if c.Mode == "web" {
		return c, errors.New("web mode is not implemented yet")
	}
	if c.Mode == "mod" && len(c.Args) > 0 {
		return c, errors.New("bad flags: mod mode takes no arguments")
	}
	if c.Mode == "cli" {
		if c.Impl != "python" && c.Impl != "go" {
			return c, fmt.Errorf("bad flags: --impl %q (want python or go)", c.Impl)
		}
		if c.Seed != "empty" && c.Seed != "demo" {
			return c, fmt.Errorf("bad flags: --seed %q (want empty or demo)", c.Seed)
		}
		if len(c.Args) == 0 {
			return c, errors.New("bad flags: cli mode needs FACTORY ARGS after --")
		}
		c.Setup = setup
		var err error
		if c.ExpectStdout, err = compileAll(so); err != nil {
			return c, fmt.Errorf("bad flags: %v", err)
		}
		if c.ExpectStderr, err = compileAll(se); err != nil {
			return c, fmt.Errorf("bad flags: %v", err)
		}
	}
	return c, nil
}

func trimTail(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
