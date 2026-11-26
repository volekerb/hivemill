package hm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ciWaitRe matches an instruction to wait for the checks, across line breaks.
var ciWaitRe = regexp.MustCompile(`(?i)wait(s|ing)?\s+(for\s+(the\s+)?)?CI\b|\bCI\s+wait\b|checks\s+--watch`)

// No task session waits for CI: it pushes and stops, and the runner watches the checks. Every role prompt, its embedded
// copy and every string of the package (the kickoffs, nudges and resume texts are built from them) is scanned.
func TestPromptHasNoCIWait(t *testing.T) {
	check := func(where, text string) {
		// The dispatcher's pause reason describes waiting runs; no session receives it.
		text = strings.ReplaceAll(text, "runs wait for CI, a merge or the owner", "")
		if m := ciWaitRe.FindString(text); m != "" {
			t.Errorf("%s tells a session to wait for CI: %q", where, m)
		}
	}
	for _, glob := range []string{"../../../roles/hivemill/*", "assets/*.md", "assets/*.json.in"} {
		files, err := filepath.Glob(glob)
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no files (%v)", glob, err)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			check(f, string(b))
		}
	}
	sources, err := fs.Glob(os.DirFS("."), "*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range sources {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			// A concatenation is read as one text, so a phrase split over two literals is still found.
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			text, whole := literalText(e)
			if text != "" {
				check(fset.Position(e.Pos()).String(), text)
			}
			return !whole
		})
	}
	// The built texts, as a session receives them.
	for _, none := range []bool{false, true} {
		run := &Run{Agent: "a", Branch: "b", Base: "main", CINone: none}
		task := &Task{ID: "T-1", Title: "t", Spec: "s"}
		for name, text := range map[string]string{
			"kickoff": Kickoff(run, task), "eval kickoff": EvalKickoff(run, task), "review kickoff": EvalReviewKickoff(run, task),
		} {
			check(name, text)
		}
		if k := Kickoff(run, task); !strings.HasSuffix(k, pushAndStop+".") {
			t.Errorf("kickoff does not end with %q: %s", pushAndStop, strconv.Quote(k))
		}
	}
}

// literalText joins the string literals of a literal or a "+" chain; other operands become a space. whole reports that
// the expression was read, so its parts need no second look; a chain with a call in it is still walked for the call's
// own arguments.
func literalText(e ast.Expr) (text string, whole bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", true
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lw := literalText(x.X)
		r, rw := literalText(x.Y)
		if l == "" && r == "" {
			return "", false
		}
		if !lw {
			l += " "
		}
		return l + r, lw && rw
	}
	return "", false
}
