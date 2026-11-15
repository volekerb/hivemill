package main

import (
	"io"
	"math/big"
	"strings"
	"testing"
)

// Fixtures: state files the Python stub and the tests write, plus the odd values Python's json module accepts.
var stateSeeds = []string{
	`{}`,
	`{"fail": false, "seq": 3, "agents": [{"name": "zz-a", "agent": "claude", "pane_id": "p2", "agent_status": "idle", "agent_session": {"value": "s"}}], "workspaces": [{"workspace_id": "w1", "label": "ws"}], "tabs": [{"tab_id": "t2", "workspace_id": "w1", "label": "1", "pane_count": 1}], "panes": [{"pane_id": "p2", "tab_id": "t2", "workspace_id": "w1"}]}`,
	`{"start_sleep": 0.5, "wait_sleep": 1e-05, "big": 123456789012345678901234567890, "f": 1e22, "n": NaN, "i": -Infinity, "z": -0.0}`,
	`{"read_text": {"zz-a": "line\né😀"}, "fail_cmds": ["tab create"], "start_fail": "boom", "lone": "\ud800"}`,
	` [1, 2.5, "x", null, true, {"a": {"a": 1}}] `,
	`{"k": 1, "k": 2}`,
	"", `{`, `{"a" 1}`, `[1,]`, `"\u12"`, `01`, `-`, `1e`, "\"\x01\"", `{"a": "\ud83dA"}`,
}

// FuzzParseState: a state file never panics the decoder, and what parses dumps to a fixed point (dump, parse, dump
// again gives the same bytes) so the stub's rewrite of the file is stable.
func FuzzParseState(f *testing.F) {
	for _, s := range stateSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := parseJSON(s)
		if err != nil {
			return
		}
		d1 := dumps(v)
		v2, err := parseJSON(d1)
		if err != nil {
			t.Fatalf("dumps output %q does not parse: %v", d1, err)
		}
		if d2 := dumps(v2); d2 != d1 {
			t.Fatalf("dumps is not a fixed point: %q then %q", d1, d2)
		}
	})
}

const fuzzState = `{"seq": 4, "agents": [{"name": "zz-a", "pane_id": "p2"}], "workspaces": [{"workspace_id": "w1", "label": "ws"}], ` +
	`"tabs": [{"tab_id": "t2", "workspace_id": "w1", "label": "1", "pane_count": 1}], "panes": [{"pane_id": "p2", "tab_id": "t2", "workspace_id": "w1"}]}`

// FuzzHerdrArgv: any argv (words split by NUL) handled against a fixed state ends in an exit code or a crash (the
// Python stub's uncaught exception), never a Go panic, hang or write outside the state value.
func FuzzHerdrArgv(f *testing.F) {
	for _, a := range []string{
		"tab\x00create\x00--workspace\x00w1\x00--label\x00x", "agent\x00start\x00zz-b\x00--pane\x00p2\x00--timeout\x005000",
		"agent\x00start\x00zz-b\x00--timeout\x00٣٣٣٣", "agent\x00start\x00zz-b\x00--timeout\x00x", "agent\x00list",
		"pane\x00close\x00p2", "pane\x00split\x00p2", "pane\x00split\x00nope", "pane\x00process-info\x00--pane\x00p2",
		"pane\x00process-info\x00x", "tab\x00rename\x00t2\x00new", "workspace\x00create\x00--label", "agent\x00read\x00zz-a", "", "agent", "\xff\xfe",
	} {
		f.Add(a)
	}
	stdout = io.Discard
	f.Fuzz(func(t *testing.T, a string) {
		st, err := parseJSON(fuzzState)
		if err != nil {
			t.Fatal(err)
		}
		h := &herdr{argv: wtf8Args(strings.Split(a, "\x00")), st: st.(*obj)}
		func() {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(crash); !ok {
						t.Fatalf("panic %v on argv %q", r, a)
					}
				}
			}()
			h.handle()
		}()
		if got := dumps(h.st); got == "" {
			t.Fatal("state dumps to nothing")
		}
	})
}

func TestPyParseInt(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string // "" = rejected
	}{
		{"5000", "5000"}, {" 5000 ", "5000"}, {"+7", "7"}, {"-7", "-7"}, {"1_000", "1000"},
		{"٣٣٣٤", "3334"}, // Arabic-Indic digits, as int() reads them
		{"5 ", "5"}, {"\v5\v", "5"}, {"\x1c5\x1f", "5"},
		{"", ""}, {"_1", ""}, {"1_", ""}, {"1__0", ""}, {"1.5", ""}, {"x", ""}, {"٣x", ""},
		{"\U0001d7d9\U0001d7da", "12"}, // mathematical bold digits one, two
	} {
		n, ok := pyParseInt(c.in)
		if c.want == "" {
			if ok {
				t.Errorf("pyParseInt(%q) = %v, want a refusal", c.in, n)
			}
			continue
		}
		if !ok || n.Cmp(mustBig(c.want)) != 0 {
			t.Errorf("pyParseInt(%q) = %v, %v, want %s", c.in, n, ok, c.want)
		}
	}
}

func mustBig(s string) *big.Int { n, _ := new(big.Int).SetString(s, 10); return n }

func TestPySplit(t *testing.T) {
	// Python's "agent start\vlist\x1cx".split() separates at NBSP, \v and \x1c
	got := pySplit("agent start\vlist\x1cx  y")
	if strings.Join(got, "|") != "agent|start|list|x|y" {
		t.Fatalf("pySplit = %q", got)
	}
}

func TestFloatRepr(t *testing.T) {
	for in, want := range map[string]string{
		"0.5": "0.5", "1e-05": "1e-05", "0.0001": "0.0001", "1e22": "1e+22", "1e16": "1e+16", "123456789012345.0": "123456789012345.0",
		"1.5e300": "1.5e+300", "-0.0": "-0.0", "100.0": "100.0", "2.5e-07": "2.5e-07", "12345.678": "12345.678", "NaN": "NaN", "-Infinity": "-Infinity",
	} {
		v, err := parseJSON(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := dumps(v); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
