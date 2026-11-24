package macbox

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// aclRunner answers `ls -lde <dir>` from a table and records every other command.
type aclRunner struct {
	ls    map[string]string // dir -> ls -lde output
	exits map[string]int    // command prefix -> exit
	calls []string
	files map[string]string
}

func (a *aclRunner) Run(name string, args ...string) (int, string, string) {
	line := name + " " + strings.Join(args, " ")
	a.calls = append(a.calls, line)
	for p, e := range a.exits {
		if strings.HasPrefix(line, p) {
			return e, "", "boom"
		}
	}
	if name == lsPath {
		out, ok := a.ls[args[len(args)-1]]
		if !ok {
			return 1, "", "no such dir"
		}
		return 0, out, ""
	}
	return 0, "", ""
}
func (a *aclRunner) Stat(string) (os.FileInfo, error)                  { return nil, errors.New("unused") }
func (a *aclRunner) ReadDir(string) ([]string, error)                  { return nil, errors.New("unused") }
func (a *aclRunner) CreateExclusive(string, []byte, os.FileMode) error { return errors.New("unused") }
func (a *aclRunner) Remove(string) error                               { return nil }
func (a *aclRunner) WriteFile(n string, d []byte, _ os.FileMode) error {
	if a.files == nil {
		a.files = map[string]string{}
	}
	a.files[n] = string(d)
	return nil
}

const lsHead = "drwxr-xr-x+ 5 owner staff 160 Nov 24 10:00 /w/c\n"

func roACL(rights string) string {
	return lsHead + " 0: user:_hivemill_ro allow " + rights + "\n"
}

func TestParseACL(t *testing.T) {
	es, err := ParseACL(lsHead + " 0: user:_hivemill_rw allow read,write\n 1: group:staff deny delete\n")
	if err != nil || len(es) != 2 {
		t.Fatalf("got %v %v", es, err)
	}
	if es[0].Account != AccountRW || !es[0].CanWrite() || es[1].CanWrite() {
		t.Fatalf("entries %+v", es)
	}
	if _, err := ParseACL(" 0: user:_hivemill_ro maybe read\n"); err == nil {
		t.Fatal("a malformed entry must be an error")
	}
	if es, err := ParseACL("drwxr-xr-x 5 owner staff 160 Nov 24 /w/c\n"); err != nil || len(es) != 0 {
		t.Fatalf("no ACL: %v %v", es, err)
	}
}

func TestCheckClones(t *testing.T) {
	good := CloneSpec{CoderClone: "/w/c", QAClone: "/w/c-qa"}
	cases := []struct {
		name string
		c    CloneSpec
		ls   map[string]string
		want string // "" = accepted
	}{
		{"ro read-only on coder", good, map[string]string{"/w/c": roACL("read,list"), "/w/c-qa": lsHead}, ""},
		{"ro write on coder", good, map[string]string{"/w/c": roACL("read,write"), "/w/c-qa": lsHead}, ReasonClone},
		{"ro add_file on coder", good, map[string]string{"/w/c": roACL("read,add_file"), "/w/c-qa": lsHead}, ReasonClone},
		{"rw entry on qa clone", good, map[string]string{"/w/c": lsHead, "/w/c-qa": " 0: user:_hivemill_rw allow read\n"}, ReasonClone},
		{"ro write on qa clone is fine", good, map[string]string{"/w/c": lsHead, "/w/c-qa": roACL("read,write")}, ""},
		{"qa is the coder clone", CloneSpec{"/w/c", "/w/c"}, map[string]string{"/w/c": lsHead}, ReasonClone},
		{"qa not a sibling", CloneSpec{"/w/c", "/x/c-qa"}, map[string]string{"/w/c": lsHead}, ReasonClone},
		{"unreadable coder acl", good, map[string]string{}, ReasonClone},
		{"no qa clone", CloneSpec{CoderClone: "/w/c"}, map[string]string{"/w/c": roACL("read")}, ""},
	}
	for _, tc := range cases {
		got := CheckClones(&aclRunner{ls: tc.ls}, tc.c)
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("%s: refused %v", tc.name, got)
		case tc.want != "" && (got == nil || got.Reason != tc.want):
			t.Errorf("%s: got %v want reason %s", tc.name, got, tc.want)
		}
	}
}

func TestApplyCloneACLs(t *testing.T) {
	r := &aclRunner{}
	if err := PrepareClones(r, CloneSpec{CoderClone: "/w/c", QAClone: "/w/c-qa"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.calls, "\n")
	for _, w := range []string{
		"/bin/chmod -R -N /w/c", "user:_hivemill_rw allow read,write", "user:_hivemill_ro allow read,execute",
		"/bin/chmod -R -N /w/c-qa", "user:_hivemill_ro allow read,write",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	if strings.Contains(strings.Split(got, "/bin/chmod -R -N /w/c-qa")[1], AccountRW) {
		t.Errorf("the qa clone must get no %s entry:\n%s", AccountRW, got)
	}
	if err := ApplyCloneACLs(&aclRunner{exits: map[string]int{"/bin/chmod": 1}}, "/w/c", AccountRW, ""); err == nil {
		t.Error("a failing chmod must be an error")
	}
	if err := ApplyCloneACLs(r, "rel/c", AccountRW, ""); err == nil {
		t.Error("a relative clone path must be refused")
	}
}

func TestPrepareAgentDirs(t *testing.T) {
	r := &aclRunner{}
	if err := PrepareAgentDirs(r, "/a/agent", AccountRO); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.calls, "\n")
	if !strings.Contains(got, "/bin/mkdir -p /a/agent/bin") || !strings.Contains(got, "user:_hivemill_ro allow") {
		t.Errorf("calls:\n%s", got)
	}
	if _, ok := r.files["/a/agent/gitconfig"]; !ok {
		t.Error("gitconfig not written")
	}
	if strings.Contains(got, "chmod -R +a user:_hivemill_ro allow "+aclRWRights+" /a/agent\n") {
		t.Error("the agent dir itself must not be worker-writable")
	}
}
