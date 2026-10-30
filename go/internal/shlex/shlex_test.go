package shlex

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

type goldenCase struct {
	Src    string   `json:"src"`
	Mode   string   `json:"mode"`
	Input  string   `json:"input"`
	Tokens []string `json:"tokens"`
	Error  string   `json:"error"`
	Quoted *string  `json:"quoted"`
}

type goldenFile struct {
	Python         string       `json:"python"`
	CorpusCommands int          `json:"corpus_commands"`
	EdgeInputs     int          `json:"edge_inputs"`
	Cases          []goldenCase `json:"cases"`
}

func run(mode, in string) ([]string, error) {
	switch mode {
	case "script":
		return Script(in)
	case "default":
		return Lex(in, DefaultOptions())
	default:
		return Split(in)
	}
}

// TestGolden compares every case CPython 3.13 produced (ci/shlex_golden.py).
func TestGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if g.Python != "3.13" {
		t.Fatalf("golden made by CPython %q, want 3.13", g.Python)
	}
	// Every corpus command and edge input is lexed in three modes and quoted once.
	if want := (g.CorpusCommands + g.EdgeInputs) * 4; len(g.Cases) < want {
		t.Fatalf("golden has %d cases, want at least %d", len(g.Cases), want)
	}
	var corpus struct {
		MustBlock []json.RawMessage `json:"must_block"`
		MustAllow []json.RawMessage `json:"must_allow"`
	}
	craw, err := os.ReadFile("../../../ci/guard_corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(craw, &corpus); err != nil {
		t.Fatal(err)
	}
	cmds := map[string]bool{}
	for _, raw := range append(corpus.MustBlock, corpus.MustAllow...) {
		var obj struct {
			Cmd string `json:"cmd"`
		}
		var str string
		if json.Unmarshal(raw, &str) == nil {
			cmds[str] = true
		} else if err := json.Unmarshal(raw, &obj); err == nil {
			cmds[obj.Cmd] = true
		} else {
			t.Fatalf("corpus entry %s is neither a string nor an object", raw)
		}
	}
	if g.CorpusCommands != len(cmds) {
		t.Fatalf("golden covers %d corpus commands, corpus has %d: regenerate with python3 ci/shlex_golden.py", g.CorpusCommands, len(cmds))
	}
	compared := 0
	for _, c := range g.Cases {
		if c.Mode == "quote" {
			if c.Quoted == nil {
				t.Fatalf("quote case %q has no expected output", c.Input)
			}
			if got := Quote(c.Input); got != *c.Quoted {
				t.Errorf("Quote(%q) = %q, CPython %q", c.Input, got, *c.Quoted)
			}
			compared++
			continue
		}
		got, err := run(c.Mode, c.Input)
		switch {
		case c.Error != "":
			if err == nil || err.Error() != c.Error {
				t.Errorf("%s(%q): err %v, CPython ValueError %q", c.Mode, c.Input, err, c.Error)
			}
		case err != nil:
			t.Errorf("%s(%q): unexpected error %v, CPython tokens %q", c.Mode, c.Input, err, c.Tokens)
		default:
			want := c.Tokens
			if want == nil {
				want = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s(%q) = %q, CPython %q", c.Mode, c.Input, got, want)
			}
		}
		compared++
	}
	t.Logf("compared %d golden cases (%d corpus commands, %d edge inputs)", compared, g.CorpusCommands, g.EdgeInputs)
}

func TestKnownTokens(t *testing.T) {
	tests := []struct {
		name string
		mode string
		in   string
		want []string
	}{
		{"redirect run", "script", "x>&2", []string{"x", ">&", "2"}},
		{"pipe amp run", "script", "a|&b", []string{"a", "|&", "b"}},
		{"double semicolon", "script", "a;;b", []string{"a", ";;", "b"}},
		{"latin1 word default", "default", "café>x", []string{"café", ">", "x"}},
		{"cjk word script", "script", "中文>x", []string{"中文", ">", "x"}},
		{"cjk default splits per char", "default", "中文", []string{"中", "文"}},
		{"comment default", "default", "a #b\nc", []string{"a", "c"}},
		{"comment kept in script", "script", "a #b", []string{"a", "#b"}},
		{"empty quoted token", "split", "'' x", []string{"", "x"}},
		{"escaped quote in dquote", "split", `"a\"b"`, []string{`a"b`}},
		{"backslash kept in dquote", "split", `"a\nb"`, []string{`a\nb`}},
		{"empty input", "split", "", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(tc.mode, tc.in)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s(%q) = %q, %v; want %q", tc.mode, tc.in, got, err, tc.want)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		mode string
		in   string
		want error
	}{
		{"open single quote", "script", "a 'b", ErrNoClosingQuotation},
		{"open double quote", "split", `a "b`, ErrNoClosingQuotation},
		{"trailing backslash", "script", `a \`, ErrNoEscapedCharacter},
		{"backslash at end in dquote", "split", `"a\`, ErrNoEscapedCharacter},
		{"lone backslash", "split", `\`, ErrNoEscapedCharacter},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(tc.mode, tc.in)
			if !errors.Is(err, tc.want) || got != nil {
				t.Fatalf("%s(%q) = %q, %v; want error %v", tc.mode, tc.in, got, err, tc.want)
			}
		})
	}
}

func TestUnsupportedOptions(t *testing.T) {
	if _, err := Lex("a b", Options{}); !errors.Is(err, ErrUnsupportedOptions) {
		t.Fatalf("got %v, want ErrUnsupportedOptions", err)
	}
}

func TestQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "''"},
		{"abc", "abc"},
		{"a b", "'a b'"},
		{"it's", `'it'"'"'s'`},
		{"a/b-c.d_e:f,g@h%i+j=k", "a/b-c.d_e:f,g@h%i+j=k"},
		{"café", "'café'"},
		{"$x", "'$x'"},
	}
	for _, tc := range tests {
		if got := Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
