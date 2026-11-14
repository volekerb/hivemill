package data

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// parityLines are lines on which Go's trimming/decoding once disagreed with Python's bytes.strip() + json.loads.
var parityLines = [][]byte{
	[]byte(`{"a":1}`),
	[]byte("  {\"a\":1}\t\r\x0b\x0c"),
	[]byte(" {\"a\":1}"),            // NBSP prefix: Python keeps it, so the line is malformed
	[]byte("{\"a\":1} "),            // NBSP suffix
	[]byte("\u0085{\"a\":1}\u0085"), // NEL
	[]byte(" {\"a\":1}"),            // EM SPACE
	[]byte("{\"a\":\"\xff\"}"),      // invalid UTF-8
	[]byte(`{"a":NaN}`),             // Python accepts
	[]byte(`{"a":-Infinity,"b":Infinity}`),
	[]byte(`{"a":"NaN"}`),
	[]byte(`{"a":1}{"b":2}`),
	[]byte(`{"a":1,}`),
	[]byte(`[1]`),
	[]byte(`{}`),
	[]byte("{\"a\":\"\x01\"}"),
	[]byte(`{"a":"\ud800"}`),
	[]byte(`{"a":01}`),
}

const pyClassify = `
import json, sys
out = []
for raw in json.load(sys.stdin):
    raw = bytes.fromhex(raw).strip()
    if not raw:
        out.append("empty"); continue
    if not (raw.startswith(b"{") and raw.endswith(b"}")):
        out.append("bad"); continue
    try:
        d = json.loads(raw)
    except ValueError:
        out.append("bad"); continue
    out.append("ok" if isinstance(d, dict) else "bad")
print(json.dumps(out))
`

func goClassify(line []byte) string {
	raw := pyStrip(line)
	if len(raw) == 0 {
		return "empty"
	}
	if !(raw[0] == '{' && raw[len(raw)-1] == '}') {
		return "bad"
	}
	if _, ok := decodeObj(raw); !ok {
		return "bad"
	}
	return "ok"
}

func TestLineParsingMatchesPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	var hexes []string
	for _, l := range parityLines {
		hexes = append(hexes, hexOf(l))
	}
	in, _ := json.Marshal(hexes)
	cmd := exec.Command("python3", "-I", "-c", pyClassify)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	for i, l := range parityLines {
		if got := goClassify(l); got != want[i] {
			t.Errorf("line %q: go %s, python %s", l, got, want[i])
		}
	}
}

func hexOf(b []byte) string {
	const h = "0123456789abcdef"
	s := make([]byte, 0, 2*len(b))
	for _, c := range b {
		s = append(s, h[c>>4], h[c&15])
	}
	return string(s)
}

func TestScanCountsMalformedLikePython(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	for _, l := range parityLines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_, c := scan(dir, map[string]Attr{}, "", "")
	n := 0
	for _, l := range parityLines {
		if goClassify(l) == "bad" {
			n++
		}
	}
	if c.MalformedLines != int64(n) {
		t.Errorf("scan malformed = %d, want %d", c.MalformedLines, n)
	}
}

func TestCIFetchArgv(t *testing.T) {
	c := NewCICache()
	c.Gh = "gh"
	var got []string
	c.Run = func(ctx context.Context, gh string, args ...string) ([]byte, error) {
		got = append([]string{gh}, args...)
		return []byte(`{"state":"OPEN","statusCheckRollup":[]}`), nil
	}
	c.Fetch("https://github.com/o/r/pull/1")
	want := []string{"gh", "pr", "view", "https://github.com/o/r/pull/1", "--json", "state,statusCheckRollup"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}
