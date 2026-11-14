// Command stub is the fake gh, herdr and hc of the Go test harness (package factorytest). It is one binary: the
// tool is the base name it is run as. Every call is appended to the JSONL file $FAKE_CALLS in exactly the bytes the
// Python stubs in tests/stubs write (Python json.dumps: ", " and ": " separators, ensure_ascii), so a test can compare
// call logs across implementations. Answers come from the per-test script $FAKE_STUB_SCRIPT (see answer).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func main() {
	tool := filepath.Base(os.Args[0])
	argv := os.Args[1:]
	calls := os.Getenv("FAKE_CALLS")
	if calls == "" {
		fmt.Fprintf(os.Stderr, "%s stub: FAKE_CALLS is not set; run the tool through factorytest.Env\n", tool)
		os.Exit(2)
	}
	var stdin *string
	if tool == "gh" && len(argv) >= 2 && argv[0] == "pr" && argv[1] == "create" {
		b, _ := io.ReadAll(os.Stdin)
		s := string(b)
		stdin = &s
	}
	if err := appendLine(calls, logLine(tool, argv, stdin)); err != nil {
		fmt.Fprintf(os.Stderr, "%s stub: cannot write %s: %v; fix the test home\n", tool, calls, err)
		os.Exit(2)
	}
	os.Exit(answer(tool, argv))
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// logLine is the Python json.dumps of the stub's log record, key order preserved.
func logLine(tool string, argv []string, stdin *string) string {
	var b strings.Builder
	b.WriteString(`{"tool": ` + pyStr(tool) + `, "argv": [`)
	for i, a := range argv {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pyStr(a))
	}
	b.WriteString("]")
	if tool == "gh" {
		b.WriteString(`, "env": {"GH_REPO": ` + pyOpt("GH_REPO") + `, "GH_HOST": ` + pyOpt("GH_HOST") + `}`)
		b.WriteString(`, "stdin": `)
		if stdin == nil {
			b.WriteString("null")
		} else {
			b.WriteString(pyStr(*stdin))
		}
	} else {
		b.WriteString(`, "sock": ` + pyOpt("HERDR_SOCKET_PATH"))
	}
	b.WriteString("}")
	return b.String()
}

func pyOpt(name string) string {
	v, ok := os.LookupEnv(name)
	if !ok {
		return "null"
	}
	return pyStr(v)
}

// pyStr is Python's json.dumps for a str (ensure_ascii=True).
func pyStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x7f || r == 0x7f:
			b.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		case r == utf8.RuneError:
			b.WriteString(`�`)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// rule is one scripted answer. The first rule of the tool whose Match substrings all occur in the space-joined argv
// wins (a substring ending in $ must end it). Exit 0 and an empty Out is a silent success.
type rule struct {
	Match []string `json:"match"`
	Out   string   `json:"out"`
	Err   string   `json:"err"`
	Exit  int      `json:"exit"`
}

func answer(tool string, argv []string) int {
	rules, err := loadRules(tool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s stub: %v\n", tool, err)
		return 2
	}
	joined := strings.Join(argv, " ")
	for _, r := range rules {
		if matches(joined, r.Match) {
			fmt.Print(r.Out)
			fmt.Fprint(os.Stderr, r.Err)
			return r.Exit
		}
	}
	return defaultAnswer(tool, argv)
}

func matches(joined string, subs []string) bool {
	for _, s := range subs {
		if strings.HasSuffix(s, "$") {
			if !strings.HasSuffix(joined, strings.TrimSuffix(s, "$")) {
				return false
			}
		} else if !strings.Contains(joined, s) {
			return false
		}
	}
	return true
}

func loadRules(tool string) ([]rule, error) {
	path := os.Getenv("FAKE_STUB_SCRIPT")
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v; fix the test home", path, err)
	}
	var all map[string][]rule
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("%s is not a {tool: [rule]} script: %v; rewrite it with Env.Script", path, err)
	}
	return all[tool], nil
}

// defaultAnswer mirrors the Python stubs where they are stateless: gh pr create prints the fixture URL, gh pr ready
// succeeds, herdr and hc answer {"id": "fake", "result": {}}. Anything else needs a script rule.
func defaultAnswer(tool string, argv []string) int {
	switch tool {
	case "gh":
		if len(argv) >= 2 && argv[0] == "pr" && argv[1] == "create" {
			fmt.Println("https://github.com/zz-test/repo/pull/7")
			return 0
		}
		if len(argv) >= 2 && argv[0] == "pr" && argv[1] == "ready" {
			return 0
		}
		fmt.Fprintln(os.Stderr, "gh stub: no script rule for this call; add one with Env.Script")
		return 1
	default:
		fmt.Println(`{"id": "fake", "result": {}}`)
		return 0
	}
}
