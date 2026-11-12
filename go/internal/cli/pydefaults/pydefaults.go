// Package pydefaults reads the DEFAULT_CONFIG literal out of bin/factory_cli.py. It is used by the go generate
// program that writes defaults_gen.go and by the drift test; the CLI itself never parses Python.
package pydefaults

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// Entry is one DEFAULT_CONFIG item in source order.
type Entry struct{ Key, Value string }

var line = regexp.MustCompile(`^\s*"([A-Za-z0-9_]+)":\s*"((?:[^"\\]|\\.)*)",\s*(?:#.*)?$`)

// Parse returns the entries of the `DEFAULT_CONFIG = {` literal. Every line inside must be a comment, blank or one
// `"key": "value",` item; anything else is an error (the generator must be taught the new shape, never guess).
func Parse(src []byte) ([]Entry, error) {
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	in := false
	var out []Entry
	seen := map[string]bool{}
	n := 0
	for sc.Scan() {
		n++
		t := sc.Text()
		if !in {
			if strings.HasPrefix(t, "DEFAULT_CONFIG = {") {
				if strings.TrimSpace(strings.TrimPrefix(t, "DEFAULT_CONFIG = {")) != "" {
					return nil, fmt.Errorf("line %d: DEFAULT_CONFIG must open on its own line", n)
				}
				in = true
			}
			continue
		}
		if t == "}" {
			return out, nil
		}
		if s := strings.TrimSpace(t); s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		m := line.FindStringSubmatch(t)
		if m == nil {
			return nil, fmt.Errorf("line %d: not a simple `\"key\": \"value\",` item: %s", n, t)
		}
		v, err := unescape(m[2])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if seen[m[1]] {
			return nil, fmt.Errorf("line %d: duplicate key %s", n, m[1])
		}
		seen[m[1]] = true
		out = append(out, Entry{m[1], v})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("DEFAULT_CONFIG literal not found or not closed by a line holding only }")
}

func unescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case '\\', '"', '\'':
			b.WriteByte(s[i])
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			return "", fmt.Errorf("unsupported escape \\%c", s[i])
		}
	}
	return b.String(), nil
}
