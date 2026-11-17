package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fuzzExempt is a ratchet: the parse* funcs that had no Fuzz test on main when
// TestEveryParserHasFuzz was added. It may only shrink: when a parser gains a
// Fuzz func, the test demands its entry be removed. A new parser may not be
// added here; it must ship with a Fuzz func.
var fuzzExempt = map[string]string{
	"parseAgents":            "uncovered on main at a4352fb55",
	"parseConfig":            "uncovered on main at a4352fb55",
	"parseCursorPilotStatus": "uncovered on main at a4352fb55",
	"parseDecimal":           "uncovered on main at a4352fb55",
	"parseFlags":             "uncovered on main at a4352fb55",
	"parseGoalList":          "uncovered on main at a4352fb55",
	"parseInbox":             "uncovered on main at a4352fb55",
	"parseJSON":              "uncovered on main at a4352fb55",
	"parseLessonList":        "uncovered on main at a4352fb55",
	"parseLessonShow":        "uncovered on main at a4352fb55",
	"parseMsg":               "uncovered on main at a4352fb55",
	"parseReport":            "uncovered on main at a4352fb55",
	"parseRetroFile":         "uncovered on main at a4352fb55",
	"parseRetroPacket":       "uncovered on main at a4352fb55",
	"parseRetroShow":         "uncovered on main at a4352fb55",
	"parseScheduleList":      "uncovered on main at a4352fb55",
	"parseScheduleRemove":    "uncovered on main at a4352fb55",
	"parseScorecard":         "uncovered on main at a4352fb55",
	"parseScoutList":         "uncovered on main at a4352fb55",
	"parseScoutRuns":         "uncovered on main at a4352fb55",
	"parseScoutShow":         "uncovered on main at a4352fb55",
	"parseSkilleval":         "uncovered on main at a4352fb55",
	"parseStmt":              "uncovered on main at a4352fb55",
	"parseTaskList":          "uncovered on main at a4352fb55",
	"parseTaskReady":         "uncovered on main at a4352fb55",
	"parseTick":              "uncovered on main at a4352fb55",
	"parseValue":             "uncovered on main at a4352fb55",
	"parseVerdict":           "uncovered on main at a4352fb55",
	"parseWhoami":            "uncovered on main at a4352fb55",
}

var parserName = regexp.MustCompile(`^parse[A-Z]`)

// TestEveryParserHasFuzz fails when a top-level parse* func in a non-test file
// of this package is not called from any Fuzz* func in a _test.go file here.
func TestEveryParserHasFuzz(t *testing.T) {
	fset := token.NewFileSet()
	parsers := map[string]string{} // parser -> file base name
	called := map[string]bool{}    // names called inside Fuzz* funcs

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		isTest := strings.HasSuffix(path, "_test.go")
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			name := fn.Name.Name
			if !isTest && parserName.MatchString(name) {
				parsers[name] = path
			}
			if isTest && strings.HasPrefix(name, "Fuzz") && fn.Body != nil {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok {
							called[id.Name] = true
						}
					}
					return true
				})
			}
		}
	}

	var problems []string
	for p, file := range parsers {
		_, exempt := fuzzExempt[p]
		switch {
		case !called[p] && !exempt:
			problems = append(problems, "argv parser "+p+" ("+file+") has no Fuzz test: add a Fuzz func that calls "+p)
		case called[p] && exempt:
			problems = append(problems, "fuzzExempt lists "+p+" but it now has a Fuzz test: remove it from fuzzExempt")
		}
	}
	for p := range fuzzExempt {
		if _, ok := parsers[p]; !ok {
			problems = append(problems, "fuzzExempt lists "+p+" but no such parser exists: remove it")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Errorf("%s", p)
	}
}
