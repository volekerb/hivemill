// Command block-eval parses and evaluates typed block conditions for the factory tick.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"factory/go/internal/blockcond"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "block-eval: internal error: %v\n", r)
			code = 2
		}
	}()
	if len(args) != 1 {
		fmt.Fprintf(stderr, "usage: block-eval parse|eval\n")
		return 2
	}
	switch args[0] {
	case "parse":
		return cmdParse(stdin, stdout)
	case "eval":
		return cmdEval(stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		return 2
	}
}

func cmdParse(stdin io.Reader, stdout io.Writer) int {
	sc := bufio.NewScanner(stdin)
	var out []blockcond.Cond
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		c, err := blockcond.Parse(line)
		if err != nil {
			return 2
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return 2
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return 2
	}
	return 0
}

func cmdEval(stdin io.Reader, stdout, stderr io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return 2
	}
	var facts blockcond.Facts
	if err := json.Unmarshal(raw, &facts); err != nil {
		return 2
	}
	result, err := blockcond.Eval(facts)
	if err != nil {
		if strings.Contains(err.Error(), "unknown probe") {
			return 2
		}
		fmt.Fprintf(stderr, "block-eval: %v\n", err)
		return 2
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(result); err != nil {
		return 2
	}
	b := buf.Bytes()
	if _, err := stdout.Write(b); err != nil {
		return 2
	}
	return 0
}
