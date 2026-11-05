package guard

import (
	"strings"
	"testing"
)

func TestScriptTokensEchoPipe(t *testing.T) {
	cmd := "echo 'import subprocess; subprocess.run([\"git\",\"push\"])' | python3"
	text, _, err := Preprocess(cmd, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	toks, err := ScriptTokens(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"echo", `import subprocess; subprocess.run(["git","push"])`, "|", "python3"}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens %#v", len(toks), toks)
	}
	for i := range want {
		if toks[i] != want[i] {
			t.Fatalf("[%d] got %q want %q (all %#v)", i, toks[i], want[i], toks)
		}
	}
}

func TestStdinScanEchoPipe(t *testing.T) {
	a, err := NewAnalyzer(t.TempDir(), "researcher", false)
	if err != nil {
		t.Fatal(err)
	}
	cmd := "echo 'import subprocess; subprocess.run([\"git\",\"push\"])' | python3"
	// mirror script token split
	text, _, err := Preprocess(cmd, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	toks, err := ScriptTokens(text)
	if err != nil {
		t.Fatal(err)
	}
	var cmds []pcmd
	var cur []string
	piped := false
	for i := 0; i < len(toks); {
		tok := toks[i]
		if !IsPunct(tok) {
			cur = append(cur, tok)
			i++
			continue
		}
		if (strings.Contains(tok, "<") || strings.Contains(tok, ">")) && !strings.Contains(tok, "(") {
			i++
			continue
		}
		for _, piece := range splitParens(tok) {
			cmds = append(cmds, pcmd{cur, piped, nil})
			cur, piped = nil, strings.Contains(piece, "|")
		}
		i++
	}
	cmds = append(cmds, pcmd{cur, piped, nil})
	if len(cmds) != 2 || !cmds[1].piped {
		t.Fatalf("cmds=%#v", cmds)
	}
	if err := a.Script(cmd); err == nil {
		t.Fatalf("expected block, pipeSrc=%q", a.pipeSrc)
	}
}

func TestStdinScanPythonExtractUnicode(t *testing.T) {
	const nbsp = "\u00a0"
	for _, cmd := range []string{
		"python3 - <<'EOF'\nimport zipfile; zipfile.ZipFile('a').extract" + nbsp + "(\"x\")\nEOF",
		"python\u0663 - <<'EOF'\nimport zipfile; zipfile.ZipFile('a').extractall('x')\nEOF",
	} {
		if err := Check(cmd, "", "qa", false); err == nil {
			t.Fatalf("qa %q: allowed, want archive stdin block", cmd)
		}
	}
}
