package guard

import (
	"strings"
	"testing"
)

// T-0771 / T-0352 B3: table tests for the Go port of Analyzer.builtin_assign and Analyzer.call_assign. Each case was
// checked against the Python hook (no-push role: exit 2 for a block, 0 for an allow).

func TestBuiltinAssign(t *testing.T) {
	cases := []struct {
		name   string
		assign []string
		words  []string
		block  string // substring of the refusal; "" = allowed
	}{
		{"read HOME", nil, []string{"read", "HOME"}, "read"},
		{"read plain", nil, []string{"read", "line"}, ""},
		{"read -r GIT_DIR", nil, []string{"read", "-r", "GIT_DIR"}, "read"},
		{"read -p prompt consumes its argument", nil, []string{"read", "-p", "HOME", "line"}, ""},
		{"read -pHOME cluster tail is the prompt", nil, []string{"read", "-pHOME", "line"}, ""},
		{"read -a HOME names the array", nil, []string{"read", "-a", "HOME"}, "read"},
		{"read -aPATH", nil, []string{"read", "-aPATH"}, "read"},
		{"read subscripted target", nil, []string{"read", "PATH[0]"}, "read"},
		{"read dynamic target", nil, []string{"read", "$v"}, "dynamic target name"},
		{"printf -v HOME", nil, []string{"printf", "-v", "HOME", "%s", "x"}, "printf"},
		{"printf -vPAGER", nil, []string{"printf", "-vPAGER", "%s", "x"}, "printf"},
		{"printf no -v", nil, []string{"printf", "%s", "HOME"}, ""},
		{"printf -v plain", nil, []string{"printf", "-v", "out", "%s", "x"}, ""},
		{"mapfile PATH", nil, []string{"mapfile", "PATH"}, "mapfile"},
		{"mapfile -t XDG_CONFIG_HOME", nil, []string{"mapfile", "-t", "XDG_CONFIG_HOME"}, "mapfile"},
		{"readarray -d consumes delimiter", nil, []string{"readarray", "-d", "HOME", "arr"}, ""},
		{"readarray GIT_X", nil, []string{"readarray", "GIT_X"}, "readarray"},
		{"for HOME", nil, []string{"for", "HOME", "in", "a"}, "for"},
		{"for plain", nil, []string{"for", "i", "in", "a"}, ""},
		{"select PATH", nil, []string{"select", "PATH", "in", "a"}, "select"},
		{"getopts optstring too", nil, []string{"getopts", "ab", "HOME"}, "getopts"},
		{"getopts GIT_ name", nil, []string{"getopts", "a:", "GIT_OPT"}, "getopts"},
		{"getopts plain", nil, []string{"getopts", "ab", "opt"}, ""},
		{"let HOME=1", nil, []string{"let", "HOME=1"}, "let"},
		{"let expression names", nil, []string{"let", "x=PATH+1"}, ""},
		{"let bare PATH", nil, []string{"let", "PATH"}, "let"},
		{"declare HOME", nil, []string{"declare", "HOME=x"}, "declare"},
		{"typeset PAGER", nil, []string{"typeset", "-x", "PAGER=less"}, "typeset"},
		{"local GIT_DIR", nil, []string{"local", "GIT_DIR=x"}, "local"},
		{"declare plain", nil, []string{"declare", "-x", "v=1"}, ""},
		{"declare -n nameref to HOME", nil, []string{"declare", "-n", "r=HOME"}, "declare -n"},
		{"declare -n nameref to plain", nil, []string{"declare", "-n", "r=other"}, ""},
		{"declare -n dynamic target", nil, []string{"declare", "-n", "r=$x"}, "dynamic target name"},
		{"wait -p HOME", nil, []string{"wait", "-p", "HOME"}, "wait"},
		{"wait -pPATH", nil, []string{"wait", "-pPATH"}, "wait"},
		{"wait plain", nil, []string{"wait", "-n"}, ""},
		{"HOME colon-equals default", nil, []string{"echo", "$" + "{HOME:=x}"}, "default"},
		{"HOME equals default", nil, []string{"echo", "$" + "{HOME=x}"}, "default"},
		{"plain colon-equals default", nil, []string{"echo", "$" + "{plain:=x}"}, ""},
		{"indirect default", nil, []string{"echo", "$" + "{!v:=x}"}, "indirect"},
		{"default in an assignment word", []string{"v=$" + "{PATH:=/x}"}, []string{"true"}, "default"},
		{"no protected default anywhere", nil, []string{"true"}, ""},
		{"absolute path basename", nil, []string{"/usr/bin/read", "HOME"}, "read"},
		{"no words", nil, nil, ""},
		// non-ASCII: a letter is a word rune, so éHOME is another name and GIT_é still has the GIT_ prefix
		{"non-ASCII prefix is another name", nil, []string{"read", "éHOME"}, ""},
		{"GIT_ with non-ASCII tail", nil, []string{"read", "GIT_é"}, "read"},
		{"non-ASCII plain target", nil, []string{"read", "été"}, ""},
		{"non-ASCII flag char before the target", nil, []string{"read", "-é", "HOME"}, "read"},
		{"non-ASCII digit subscript is not a subscript", nil, []string{"read", "HOME[٣]"}, "read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Analyzer{Role: "qa"}
			err := a.builtinAssign(c.assign, c.words, nil)
			if c.block == "" {
				if err != nil {
					t.Fatalf("want allowed, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.block) {
				t.Fatalf("want a block mentioning %q, got %v", c.block, err)
			}
		})
	}
	a := &Analyzer{Role: "qa"}
	if err := a.builtinAssign(nil, []string{"true"}, []string{"$" + "{HOME:=x}"}); err == nil {
		t.Fatal("a HOME default in extra text must block")
	}
}

func TestCallAssign(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		bodies []string
		block  string
	}{
		{"named fd HOME", "exec {HOME}>f", nil, "named-fd"},
		{"named fd GIT_X read side", "exec {GIT_X}<f", nil, "named-fd"},
		{"named fd plain", "exec {fd}>f", nil, ""},
		{"named fd dynamic", "exec {a$b}>f", nil, "named-fd"},
		{"named fd after $ is a parameter expansion", "echo $" + "{HOME}>f", nil, ""},
		{"named fd after backslash is escaped", `echo \{HOME}>f`, nil, ""},
		{"named fd after word char", "echo x{HOME}>f", nil, ""},
		{"coproc HOME cmd", "coproc HOME { cat; }", nil, "coproc"},
		{"coproc quoted name", "coproc 'PATH' cat", nil, "coproc"},
		{"coproc dynamic name", "coproc $n cat", nil, "coproc"},
		{"coproc plain name", "coproc worker cat", nil, ""},
		{"coproc cmd without name", "coproc cat;", nil, ""},
		{"-coproc is not the keyword", "ls -coproc HOME x", nil, ""},
		{"declare -n naming HOME", "declare -n r; HOME=x", nil, "nameref"},
		{"local -n naming GIT_DIR", "local -n r=a; echo GIT_DIR", nil, "nameref"},
		{"typeset -rn naming PATH", "typeset -rn r PATH", nil, "nameref"},
		{"declare -n without protected name", "declare -n r=a", nil, ""},
		{"protected name without nameref", "declare -x v; echo HOME", nil, ""},
		{"nameref check spans the whole call, not one statement", "declare -n r; echo ok; HOME=x", nil, "nameref"},
		{"-declare is not the keyword", "ls -declare -n HOME", nil, ""},
		{"declarex word", "declarex -n HOME", nil, ""},
		{"PATHX is not PATH", "declare -n r=PATHX", nil, ""},
		{"a body with a protected default blocks", "true", []string{"$" + "{HOME:=x}"}, "default"},
		{"a body without a default is allowed", "true", []string{"read HOME"}, ""},
		{"non-ASCII before named fd", "echo é{HOME}>f", nil, ""},
		{"non-ASCII before coproc", "echo écoproc HOME cat", nil, ""},
		{"non-ASCII inside named fd name", "exec {é}>f", nil, ""},
		{"non-ASCII name with nameref", "declare -n r=été", nil, ""},
		{"non-ASCII whitespace before coproc name", "coproc HOME cat", nil, "coproc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Analyzer{Role: "qa"}
			err := a.callAssign(c.text, c.bodies)
			if c.block == "" {
				if err != nil {
					t.Fatalf("want allowed, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.block) {
				t.Fatalf("want a block mentioning %q, got %v", c.block, err)
			}
		})
	}
}

func TestPrecededBy(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		i     int
		extra string
		want  bool
	}{
		{"start of string", "abc", 0, "", false},
		{"negative offset", "abc", -1, "", false},
		{"ASCII letter before", "ab", 1, "", true},
		{"underscore before", "_b", 1, "", true},
		{"digit before", "1b", 1, "", true},
		{"space before", " b", 1, "", false},
		{"punctuation not in extra", "$b", 1, "", false},
		{"extra rune before", "$b", 1, "$\\", true},
		{"dash only with extra", "-b", 1, "-", true},
		{"dash without extra", "-b", 1, "", false},
		{"offset at end of string", "ab", 2, "", true},
		{"multibyte letter before", "éb", len("é"), "", true},
		{"multibyte letter after ASCII", "xéb", len("xé"), "", true},
		{"multibyte non-letter before (em dash)", "—b", len("—"), "", false},
		{"multibyte extra rune before", "€b", len("€"), "€", true},
		{"non-ASCII digit before", "٣b", len("٣"), "", true},
		{"CJK letter before", "漢b", len("漢"), "", true},
		{"emoji before", "😀b", len("😀"), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := precededBy(c.s, c.i, c.extra); got != c.want {
				t.Fatalf("precededBy(%q, %d, %q) = %v, want %v", c.s, c.i, c.extra, got, c.want)
			}
		})
	}
}

func TestNamesProtectedAndDeclaresNameref(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"echo HOME", true}, {"echo $HOME/x", true}, {"echo PATHX", false}, {"echo XPATH", false}, {"GIT_DIR=x", true},
		{"echo GIT_", true}, {"echo éHOME", false}, {"echo HOMEé", false}, {"echo GIT_é", true}, {"echo été", false},
	} {
		if got := namesProtected(c.text); got != c.want {
			t.Errorf("namesProtected(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	for _, c := range []struct {
		text string
		want bool
	}{
		{"declare -n r", true}, {"typeset -rn r", true}, {"local -n r", true}, {"declare -x r", false}, {"declare r -n", true},
		{"x; declare -n r", true}, {"declare; echo -n", false}, {"-declare -n r", false}, {"ldeclare -n r", false},
		{"édeclare -n r", false}, {"declaré -n r", false},
	} {
		if got := declaresNameref(c.text); got != c.want {
			t.Errorf("declaresNameref(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
