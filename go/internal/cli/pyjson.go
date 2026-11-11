package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// jnode is a JSON value that keeps object key order, so json.dumps(..., indent=1) can be reproduced exactly.
type jnode struct {
	kind byte // 'n' null, 'b' bool, 's' string, 'i' integer, 'a' array, 'o' object
	s    string
	b    bool
	arr  []*jnode
	keys []string
	vals []*jnode
}

var errUnsupportedJSON = errors.New("json value this binary cannot reproduce byte for byte")

var intLit = regexp.MustCompile(`^-?[0-9]+$`)

var loneSurrogate = regexp.MustCompile(`(?i)\\u[dD][89a-fA-F][0-9a-fA-F]{2}`)

func parseJSON(data []byte) (*jnode, error) {
	if !utf8.Valid(data) {
		return nil, errUnsupportedJSON // Python's read_text raises; let it word the error
	}
	if loneSurrogate.Match(data) {
		return nil, errUnsupportedJSON // surrogate escapes: Python keeps them, Go would not
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	return n, nil
}

func parseValue(dec *json.Decoder) (*jnode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case nil:
		return &jnode{kind: 'n'}, nil
	case bool:
		return &jnode{kind: 'b', b: t}, nil
	case string:
		return &jnode{kind: 's', s: t}, nil
	case json.Number:
		if !intLit.MatchString(t.String()) {
			return nil, errUnsupportedJSON // Python prints floats with repr(); not reproduced
		}
		if t.String() == "-0" {
			return &jnode{kind: 'i', s: "0"}, nil // Python parses -0 as the int 0
		}
		return &jnode{kind: 'i', s: t.String()}, nil
	case json.Delim:
		if t == '[' {
			n := &jnode{kind: 'a'}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				n.arr = append(n.arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		}
		n := &jnode{kind: 'o'}
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, ok := kt.(string)
			if !ok {
				return nil, fmt.Errorf("object key %v is not a string", kt)
			}
			if seen[k] {
				return nil, errUnsupportedJSON // duplicate key: Python keeps the first position, last value
			}
			seen[k] = true
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, k)
			n.vals = append(n.vals, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return n, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", tok)
}

func (n *jnode) get(k string) (*jnode, bool) {
	for i, kk := range n.keys {
		if kk == k {
			return n.vals[i], true
		}
	}
	return nil, false
}

// set replaces k in place or appends it, like a dict assignment.
func (n *jnode) set(k string, v *jnode) {
	for i, kk := range n.keys {
		if kk == k {
			n.vals[i] = v
			return
		}
	}
	n.keys = append(n.keys, k)
	n.vals = append(n.vals, v)
}

func (n *jnode) clone() *jnode {
	c := *n
	c.keys = append([]string(nil), n.keys...)
	c.vals = append([]*jnode(nil), n.vals...)
	return &c
}

func strNode(s string) *jnode { return &jnode{kind: 's', s: s} }
func nullNode() *jnode        { return &jnode{kind: 'n'} }

// pyStr is str(value) for the cell types a table can hold; anything else cannot be reproduced here.
func (n *jnode) pyStr() (string, error) {
	switch n.kind {
	case 'n':
		return "", nil // table() prints None as ""
	case 'b':
		if n.b {
			return "True", nil
		}
		return "False", nil
	case 's', 'i':
		return n.s, nil
	}
	return "", errUnsupportedJSON
}

// dumpIndent1 is json.dumps(value, indent=1) (ensure_ascii, separators ',' and ': ').
func dumpIndent1(n *jnode) string {
	var b strings.Builder
	dump(&b, n, 0)
	return b.String()
}

func dump(b *strings.Builder, n *jnode, depth int) {
	switch n.kind {
	case 'n':
		b.WriteString("null")
	case 'b':
		if n.b {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case 'i':
		b.WriteString(n.s)
	case 's':
		dumpString(b, n.s)
	case 'a':
		if len(n.arr) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, v := range n.arr {
			b.WriteString(strings.Repeat(" ", depth+1))
			dump(b, v, depth+1)
			if i < len(n.arr)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat(" ", depth) + "]")
	case 'o':
		if len(n.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range n.keys {
			b.WriteString(strings.Repeat(" ", depth+1))
			dumpString(b, k)
			b.WriteString(": ")
			dump(b, n.vals[i], depth+1)
			if i < len(n.keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat(" ", depth) + "}")
	}
}

func dumpString(b *strings.Builder, s string) {
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
			fmt.Fprintf(b, `\u%04x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case r >= 0x10000:
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
		default:
			fmt.Fprintf(b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
}
