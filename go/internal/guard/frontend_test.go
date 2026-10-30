package guard

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

type ppResult struct {
	Text   string      `json:"text"`
	Extras [][2]string `json:"extras"`
	Bodies [][2]string `json:"bodies"`
	Error  string      `json:"error"`
}

type subsResult struct {
	Subs  []string `json:"subs"`
	Error string   `json:"error"`
}

type goldenCase struct {
	Src         string               `json:"src"`
	Input       string               `json:"input"`
	Preprocess  ppResult             `json:"preprocess"`
	PreFold     ppResult             `json:"preprocess_fold"`
	SubsRaw     *subsResult          `json:"extract_subs_raw"`
	SubsPre     *subsResult          `json:"extract_subs_pre"`
	MaskRaw     *string              `json:"mask_quotes_raw"`
	MaskPre     *string              `json:"mask_quotes_pre"`
	SegmentsRaw [][3]json.RawMessage `json:"segments_raw"`
	SegmentsPre [][3]json.RawMessage `json:"segments_pre"`
}

type goldenFile struct {
	Python         string                     `json:"python"`
	CorpusCommands int                        `json:"corpus_commands"`
	EdgeInputs     int                        `json:"edge_inputs"`
	Constants      map[string]json.RawMessage `json:"constants"`
	Cases          []goldenCase               `json:"cases"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/frontend_golden.json")
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
	return g
}

func pairs(in []Extra) [][2]string {
	out := [][2]string{}
	for _, e := range in {
		out = append(out, [2]string{e.Kind, e.Body})
	}
	return out
}

func norm(p [][2]string) [][2]string {
	if p == nil {
		return [][2]string{}
	}
	return p
}

func checkPreprocess(t *testing.T, src, cmd string, fold bool, want ppResult) {
	t.Helper()
	var bodies []Heredoc
	text, extras, err := Preprocess(cmd, fold, &bodies)
	if want.Error != "" {
		if err == nil || err.Error() != want.Error {
			t.Errorf("%s fold=%v %q: error %v, want %q", src, fold, cmd, err, want.Error)
		}
		return
	}
	if err != nil {
		t.Errorf("%s fold=%v %q: unexpected error %v", src, fold, cmd, err)
		return
	}
	gotBodies := [][2]string{}
	for _, b := range bodies {
		gotBodies = append(gotBodies, [2]string{b.Head, b.Body})
	}
	if text != want.Text || !reflect.DeepEqual(norm(pairs(extras)), norm(want.Extras)) || !reflect.DeepEqual(gotBodies, norm(want.Bodies)) {
		t.Errorf("%s fold=%v %q:\n got text=%q extras=%q bodies=%q\nwant text=%q extras=%q bodies=%q",
			src, fold, cmd, text, pairs(extras), gotBodies, want.Text, want.Extras, want.Bodies)
	}
}

func checkSubs(t *testing.T, src, text string, want *subsResult) {
	t.Helper()
	if want == nil {
		return
	}
	subs, err := ExtractSubs(text)
	if want.Error != "" {
		if err == nil || err.Error() != want.Error {
			t.Errorf("%s extract_subs %q: error %v, want %q", src, text, err, want.Error)
		}
		return
	}
	if err != nil || !reflect.DeepEqual(append([]string{}, subs...), append([]string{}, want.Subs...)) {
		t.Errorf("%s extract_subs %q: got %q err %v, want %q", src, text, subs, err, want.Subs)
	}
}

func checkSegments(t *testing.T, src, text string, want [][3]json.RawMessage) {
	t.Helper()
	got := Segments(text)
	if len(got) != len(want) {
		t.Errorf("%s segments %q: %d segments, want %d", src, text, len(got), len(want))
		return
	}
	for i, w := range want {
		var s, e int
		var words []string
		if json.Unmarshal(w[0], &s) != nil || json.Unmarshal(w[1], &e) != nil || json.Unmarshal(w[2], &words) != nil {
			t.Fatalf("bad segment %s", w)
		}
		if got[i].Start != s || got[i].End != e || !reflect.DeepEqual(append([]string{}, got[i].Words...), append([]string{}, words...)) {
			t.Errorf("%s segments %q [%d]: got (%d,%d,%q), want (%d,%d,%q)", src, text, i, got[i].Start, got[i].End, got[i].Words, s, e, words)
		}
	}
}

// TestGolden compares preprocess, extract_subs, mask_quotes and segments with what the Python hook
// produced (ci/guard_golden.py) for every corpus command and every edge input.
func TestGolden(t *testing.T) {
	g := loadGolden(t)
	var corpus, edge int
	for _, c := range g.Cases {
		switch c.Src {
		case "corpus":
			corpus++
		case "edge":
			edge++
		}
		checkPreprocess(t, c.Src, c.Input, false, c.Preprocess)
		checkPreprocess(t, c.Src, c.Input, true, c.PreFold)
		checkSubs(t, c.Src, c.Input, c.SubsRaw)
		if c.MaskRaw != nil && MaskQuotes(c.Input) != *c.MaskRaw {
			t.Errorf("%s mask_quotes %q: got %q want %q", c.Src, c.Input, MaskQuotes(c.Input), *c.MaskRaw)
		}
		checkSegments(t, c.Src, c.Input, c.SegmentsRaw)
		if c.Preprocess.Error == "" {
			checkSubs(t, c.Src, c.Preprocess.Text, c.SubsPre)
			if c.MaskPre != nil && MaskQuotes(c.Preprocess.Text) != *c.MaskPre {
				t.Errorf("%s mask_quotes(pre) %q: got %q want %q", c.Src, c.Preprocess.Text, MaskQuotes(c.Preprocess.Text), *c.MaskPre)
			}
			checkSegments(t, c.Src, c.Preprocess.Text, c.SegmentsPre)
		}
	}
	if corpus != g.CorpusCommands || edge != g.EdgeInputs {
		t.Fatalf("golden holds %d corpus + %d edge cases, header says %d + %d", corpus, edge, g.CorpusCommands, g.EdgeInputs)
	}
	var corp struct {
		MustBlock []json.RawMessage `json:"must_block"`
		MustAllow []json.RawMessage `json:"must_allow"`
	}
	raw, err := os.ReadFile("../../../ci/guard_corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &corp); err != nil {
		t.Fatal(err)
	}
	cmds := map[string]bool{}
	for _, r := range append(corp.MustBlock, corp.MustAllow...) {
		var s string
		var o struct {
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal(r, &s) == nil {
			cmds[s] = true
		} else if err := json.Unmarshal(r, &o); err == nil {
			cmds[o.Cmd] = true
		} else {
			t.Fatalf("corpus entry %s is neither a string nor an object", r)
		}
	}
	if len(cmds) != corpus {
		t.Fatalf("ci/guard_corpus.json has %d distinct commands, golden has %d: run python3 ci/guard_golden.py", len(cmds), corpus)
	}
	t.Logf("compared %d corpus commands + %d edge inputs", corpus, edge)
}

// Expected values of the helper tests below were produced by the Python functions of
// roles/hooks/worker-guard.py (importlib by path; see ci/guard_golden.py for the loader).

func TestIsPunct(t *testing.T) {
	for in, want := range map[string]bool{"": false, "(": true, ">>": true, ";;": true, "&": true, "a": false, "(a": false, "$(": false} {
		if got := IsPunct(in); got != want {
			t.Errorf("IsPunct(%q) = %v", in, got)
		}
	}
}

func TestBraceExpand(t *testing.T) {
	cases := map[string][]string{
		"abc":          {"abc"},
		"a{b,c}d":      {"abd", "acd"},
		"{a,b}{c,d}":   {"ac", "ad", "bc", "bd"},
		"{a}":          {"{a}"},
		"{a,b":         {"{a,b"},
		"x{,y}":        {"x", "xy"},
		"{a,{b,c}}":    {"a", "b", "a", "c"},
		"a{b,c}{d,e}f": {"abdf", "abef", "acdf", "acef"},
		"{,}":          {"", ""},
		"é{ü,ö}":       {"éü", "éö"},
	}
	for in, want := range cases {
		if got := BraceExpand(in); !reflect.DeepEqual(got, want) {
			t.Errorf("BraceExpand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsStubHerdrClaudeUnder(t *testing.T) {
	stub := map[string]bool{"tests/stubs/herdr": true, "/x/tests/stubs/herdr": true, "/x/tests/./stubs/herdr": true,
		"/x/tests/y/../stubs/herdr": true, "/x/herdr": false, "tests/stub/herdr": false, "": false}
	for p, want := range stub {
		if got := IsStub(p); got != want {
			t.Errorf("IsStub(%q) = %v, want %v", p, got, want)
		}
	}
	herdr := map[string]bool{"herdr": true, "/usr/bin/herdr": true, "/usr/bin/herdr/": true, "/x/tests/stubs/herdr": false,
		"her*": true, "h?rdr": true, "[h]erdr": true, "[a-g]erdr": false, "[!a]erdr": true, "[": false, "*": true,
		"x*": false, "claude": false, "he[": false, "[a-z]*": true, "[z-a]erdr": false, "h[a-]rdr": false, "[!]erdr": false,
		"[]]erdr": false, "h\\erdr": false, "?????": true, "????": false, "[^h]erdr": true, "[a-cd-h]erdr": true,
		"[!a-z]erdr": false, "he[r-z]dr": true}
	for w, want := range herdr {
		if got := IsHerdr(w); got != want {
			t.Errorf("IsHerdr(%q) = %v, want %v", w, got, want)
		}
	}
	if !IsClaude("/a/claude") || !IsClaude("claude/") || IsClaude("claude2") || IsClaude("") {
		t.Error("IsClaude")
	}
	if !Under("/a/b", "/a/b") || !Under("/a/b/c", "/a/b/") || Under("/a/bc", "/a/b") || Under("/a", "/a/b") {
		t.Error("Under")
	}
}

func TestFoldMktemp(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"D=$", "(", "mktemp", "-d", ")"}, []string{"D=$(mktemp -d)"}},
		{[]string{"D=$", "(", "mktemp", "-p", "x", ")"}, []string{"D=$(mktemp -p x)"}},
		{[]string{"D=$", "(", "mktemp", "-u", ")"}, []string{"D=$", "(", "mktemp", "-u", ")"}},
		{[]string{"D=$", "(", "date", ")"}, []string{"D=$", "(", "date", ")"}},
		{[]string{"D=$", "(", "mktemp"}, []string{"D=$", "(", "mktemp"}},
		{[]string{"echo", "$", "("}, []string{"echo", "$", "("}},
	}
	for _, c := range cases {
		if got := FoldMktemp(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("FoldMktemp(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsDynamicStripPrefix(t *testing.T) {
	if !IsDynamic("`x`", false) || !IsDynamic("$X", false) || IsDynamic("a$X", false) || !IsDynamic("a$X", true) ||
		!IsDynamic("a{b", true) || IsDynamic("abc", true) {
		t.Error("IsDynamic")
	}
	cases := []struct{ in, want []string }{
		{[]string{"A=1", "B+=2", "ls"}, []string{"ls"}},
		{[]string{"time", "-p", "ls"}, []string{"ls"}},
		{[]string{"function", "f", "{", "ls"}, []string{"ls"}},
		{[]string{"if", "!", "then", "x", "A=1"}, []string{"x", "A=1"}},
		{[]string{}, []string{}},
	}
	for _, c := range cases {
		if got := StripPrefix(c.in); !reflect.DeepEqual(append([]string{}, got...), c.want) {
			t.Errorf("StripPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInterpArgs(t *testing.T) {
	cases := []struct {
		name         string
		rest         []string
		script, code int
		unknown      bool
	}{
		{"python3.12", []string{"-c", "x"}, -1, 1, false},
		{"python", []string{"-W", "ignore", "-c", "CODE"}, -1, 3, false},
		{"python", []string{"-X", "dev", "S.py"}, 2, -1, false},
		{"python", []string{"-cX"}, -1, 0, false},
		{"python", []string{"-m", "m"}, -1, -1, false},
		{"python", []string{"--foo"}, -1, -1, true},
		{"python", []string{"-Z"}, -1, -1, true},
		{"python", []string{"-"}, 0, -1, false},
		{"python", []string{"--"}, -1, -1, false},
		{"python", []string{"--", "s"}, 1, -1, false},
		{"python", []string{"-c"}, -1, -1, false},
		{"python", []string{"--version", "s.py"}, 1, -1, false},
		{"python", []string{"--check-hash-based-pycs", "always", "s"}, 2, -1, false},
		{"python", []string{"-Wx"}, -1, -1, false},
		{"python", []string{"-W"}, -1, -1, false},
		{"perl", []string{"-e", "x"}, -1, 1, false},
		{"perl", []string{"-I", "d", "s.pl"}, 2, -1, false},
		{"perl", []string{"-le", "x"}, -1, 1, false},
		{"perl", []string{"-lé", "x"}, 1, -1, false},
		{"node", []string{"--eval", "x"}, -1, 1, false},
		{"node", []string{"-r", "m", "s.js"}, 2, -1, false},
		{"node", []string{"-pe", "1"}, -1, 1, false},
		{"ruby", []string{"-e"}, -1, -1, false},
		{"php", []string{"-r", "x"}, -1, 1, false},
		{"deno", []string{"run", "x"}, -1, -1, true},
		{"bun", []string{"--", "x"}, 1, -1, false},
		{"lua", []string{"-l", "m", "-e", "x"}, -1, 3, false},
		{"sh", []string{"s"}, -1, -1, true},
		{"python3\n", []string{"-c", "x"}, -1, -1, true},
		{"node", []string{"-é"}, -1, -1, false},
		{"perl", []string{"--eval", "x"}, 1, -1, false},
		{"perl", []string{"-x"}, -1, -1, false},
	}
	for _, c := range cases {
		s, co, u := InterpArgs(c.name, c.rest)
		if s != c.script || co != c.code || u != c.unknown {
			t.Errorf("InterpArgs(%q, %q) = (%d, %d, %v), want (%d, %d, %v)", c.name, c.rest, s, co, u, c.script, c.code, c.unknown)
		}
	}
}

func TestScriptTokens(t *testing.T) {
	toks, err := ScriptTokens("D=$(mktemp -d) 2>&1 ls")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"D=$(mktemp -d)", ">&", "1", "ls"}
	if !reflect.DeepEqual(toks, want) {
		t.Errorf("tokens %q, want %q", toks, want)
	}
	if _, err := ScriptTokens("a 'b"); err == nil || err.Error() != "cannot parse command (No closing quotation)" {
		t.Errorf("unterminated quote: %v", err)
	}
}

func TestLauncherInCommandPosition(t *testing.T) {
	cases := map[string]bool{
		"env herdr server stop":      true,
		"herdr server stop":          false,
		"A=1 nohup x":                true,
		"bash -lc 'x'":               true,
		"bash -l x":                  false,
		"find . -exec herdr x ;":     true,
		"find . -name x":             false,
		"echo hi && sudo x":          true,
		"if x; then timeout 1 y; fi": true,
	}
	for in, want := range cases {
		if got := LauncherInCommandPosition(in); got != want {
			t.Errorf("LauncherInCommandPosition(%q) = %v, want %v", in, got, want)
		}
	}
}

func keys(m map[string]struct{}) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestGoldenConstants compares the module constants the port repeats with the hook's own.
func TestGoldenConstants(t *testing.T) {
	g := loadGolden(t)
	want := func(name string, into any) {
		t.Helper()
		raw, ok := g.Constants[name]
		if !ok {
			t.Fatalf("golden has no constant %s", name)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	sets := map[string]map[string]struct{}{
		"FACTORY_UNIT_CMDS": FactoryUnitCmds, "PRIV_WORDS": PrivWords, "WRAP_DATA": WrapData, "PERSIST_EDIT_TOOLS": PersistEditTools,
		"TERMS_ROLES": TermsRoles, "SHELLS": Shells, "WRAPPERS": Wrappers, "RESERVED": Reserved, "SYSTEMCTL_VERBS": SystemctlVerbs,
		"UNIT_MUTATING": UnitMutating, "SYSTEMCTL_NOUNIT": SystemctlNoUnit, "COPIERS": Copiers, "DATA_CMDS": DataCmds,
		"LAUNCHERS": Launchers, "FIND_EXEC": FindExec, "AWKS": Awks, "EDITORS": Editors, "DECLARERS": Declarers,
		"SCRUB_VARS": ScrubVars, "CFG_WRITERS": CfgWriters, "CFG_REMOVERS": CfgRemovers, "REMOTE_WRITE": RemoteWrite,
		"CONFIG_VALUE_OPTS": ConfigValueOpts, "GIT_VALUE_OPTS": GitValueOpts, "GIT_READ_ONLY": GitReadOnly,
		"BRANCH_LIST_LONG": BranchListLong, "BRANCH_PLAIN_LONG": BranchPlainLong, "BRANCH_VALUE_LONG": BranchValueLong,
		"CONFIG_READ": ConfigRead, "CONFIG_WRITE": ConfigWrite, "GH_VALUE_OPTS": GhValueOpts, "GH_MUTATING": GhMutating,
		"GH_REPO_VERBS": GhRepoVerbs, "GIT_CMD_KEYS": GitCmdKeys, "PY_FLAGS": PyFlags,
	}
	for name, got := range sets {
		var w []string
		want(name, &w)
		if !reflect.DeepEqual(keys(got), w) {
			t.Errorf("%s: got %q, want %q", name, keys(got), w)
		}
	}
	tuples := map[string][]string{"IDENT_VARS": IdentVars, "FACTORY_NAMES": FactoryNames, "PERSIST_NAMES": PersistNames}
	for name, got := range tuples {
		var w []string
		want(name, &w)
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s: got %q, want %q", name, got, w)
		}
	}
	for name, got := range map[string]map[string]map[string]struct{}{"BROAD_READS": BroadReads, "WRAP_VALUE_OPTS": WrapValueOpts} {
		var w map[string]*[]string
		want(name, &w)
		if len(w) != len(got) {
			t.Errorf("%s: %d entries, want %d", name, len(got), len(w))
		}
		for k, v := range w {
			gv, ok := got[k]
			switch {
			case !ok:
				t.Errorf("%s: missing %q", name, k)
			case v == nil && gv != nil:
				t.Errorf("%s[%q]: got %q, want None", name, k, keys(gv))
			case v != nil && !reflect.DeepEqual(keys(gv), append([]string{}, *v...)):
				t.Errorf("%s[%q]: got %q, want %q", name, k, keys(gv), *v)
			}
		}
	}
	var interp map[string]map[string][]string
	want("INTERP_OPTS", &interp)
	if len(interp) != len(interpOpts) {
		t.Errorf("INTERP_OPTS: %d entries, want %d", len(interpOpts), len(interp))
	}
	for name, spec := range interp {
		got := interpOpts[name]
		for field, set := range map[string]map[string]struct{}{"value": got.value, "code": got.code, "long_code": got.longCode} {
			if !reflect.DeepEqual(keys(set), append([]string{}, spec[field]...)) {
				t.Errorf("INTERP_OPTS[%s].%s: got %q, want %q", name, field, keys(set), spec[field])
			}
		}
	}
	var depth int
	var pkg, wordEnd string
	want("MAX_DEPTH", &depth)
	want("CLAUDE_PKG", &pkg)
	want("HEREDOC_WORD_END", &wordEnd)
	if depth != MaxDepth || pkg != ClaudePkg || wordEnd != HeredocWordEnd {
		t.Errorf("scalar constants differ: %d %q %q", depth, pkg, wordEnd)
	}
}
