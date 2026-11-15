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
	[]byte(`{"a":1}}`),
	[]byte(`{"a":1} x}`),
	[]byte(`{"a":1}  `),
	[]byte(`{"a":1} ]`),
	[]byte(`[1]`),
	[]byte(`{}`),
	[]byte("{\"a\":\"\x01\"}"),
	[]byte(`{"a":"\ud800"}`),
	[]byte(`{"a":01}`),
	[]byte(`{"a":-NaN}`),
	[]byte(`{"a":NaN1}`),
	[]byte(`{"a":1NaN}`),
	[]byte(`{"a":Infinity5}`),
	[]byte(`{"a":NaN.5}`),
	[]byte(`{"a":[NaN, -Infinity , Infinity]}`),
	[]byte(`{NaN:1}`),
	[]byte(`{"a":--Infinity}`),
	[]byte(`{"a":- Infinity}`),
	[]byte(`{"a":{"b":NaN}}`),
	[]byte(`{"a":1,"a":2}`),
	[]byte("\xc2\xa0{\"a\":1}"),
	[]byte("{\"a\":1}\xc2\xa0"),
	[]byte("\xe2\x80\x83{\"a\":1}"),
	[]byte("\xc2\x85{\"a\":1}\xc2\x85"),
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

// TestDecodeObjTrailingDataMatchesPython feeds decodeObj directly (no strip / brace prefilter).
func TestDecodeObjTrailingDataMatchesPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	cases := [][]byte{
		[]byte(`{"a":1}}`), []byte(`{"a":1} x`), []byte(`{"a":1}  `),
		[]byte("{\"a\":1} \n\t\r"), []byte(`{"a":1}{"b":2}`), []byte(`{"a":1} ]`),
	}
	var hexes []string
	for _, c := range cases {
		hexes = append(hexes, hexOf(c))
	}
	in, _ := json.Marshal(hexes)
	const py = `
import json, sys
out = []
for h in json.load(sys.stdin):
    try:
        out.append(isinstance(json.loads(bytes.fromhex(h)), dict))
    except ValueError:
        out.append(False)
print(json.dumps(out))
`
	cmd := exec.Command("python3", "-I", "-c", py)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []bool
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		_, got := decodeObj(c)
		if got != want[i] {
			t.Errorf("decodeObj(%q) = %v, python %v", c, got, want[i])
		}
	}
}
