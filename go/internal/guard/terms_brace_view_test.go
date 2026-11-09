package guard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// termsGhFpEnv is the T-1121 fixture: a terms file under a directory named factory, sibling work cwd, guard export.
func termsGhFpEnv(t *testing.T) (home, work string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "db", "factory.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(root, "factory")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	terms := filepath.Join(secrets, "scrub-terms.txt")
	if err := os.WriteFile(terms, []byte("zebracorp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work = filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExport(t, home, exportDoc{Config: map[string]string{"scrub_terms_file": terms}})
	t.Setenv("FACTORY_HOME", home)
	t.Setenv(termsVar, terms)
	unsetEnv(t, "WORD")
	return home, work
}

func termsGhCheck(t *testing.T, cmd, work, role string) int {
	t.Helper()
	err := Check(cmd, work, role, false)
	if err == nil {
		return 0
	}
	var pb *PublicBlock
	if errors.As(err, &pb) && strings.Contains(pb.Msg, "owner-terms") {
		return 2
	}
	if err != nil {
		t.Fatalf("unexpected block for %q: %v", cmd, err)
	}
	return 0
}

func TestTermsBraceViewLoosenedForms(t *testing.T) {
	_, work := termsGhFpEnv(t)
	loosened := []string{
		"grep -nE 'by [0-9]{1,3}' roles/factory.md",
		"grep -rnE 'factory/t-[0-9]+ [0-9a-f]{7,40}' docs tests",
		"gh pr list --head factory/t-1 --json number,title --jq '.[]|{n:.number,t:.title}'",
		"gh pr view 1 --json a,b -q '{a:.a,b:.b}' factory/x",
		"gh pr view 1 --json a --template '{{.a}},' factory/x",
		"egrep 'a{1,2}' roles/factory.md",
		"rg -n 'a{1,2}' roles/factory.md",
		"jq -r '{a:.a,b:.b}' factory.json",
		"gh pr list --head factory/t-1 --json number --jq '.[]|{n:.number,m:1}' | grep 'a{1,2}'",
		"grep -rn 'a{1,2}' docs $WORD",
	}
	for _, cmd := range loosened {
		for _, role := range []string{"coder", "qa", "reviewer", "judge", "pm"} {
			if got := termsGhCheck(t, cmd, work, role); got != 0 {
				t.Errorf("LOOSENED: role %s cmd %q exit %d, want 0", role, cmd, got)
			}
		}
	}
}

func TestTermsBraceViewDashQuotedRefused(t *testing.T) {
	_, work := termsGhFpEnv(t)
	dash := []string{
		"rg '--glob={s,x}*' a . factory",
		"rg '-g{s,x}*' a . factory",
		"rg -n '--glob={s,x}*' a . factory",
		"rg -e '--glob={s,x}*' a . factory",
		"grep '--include={s,x}' a . factory",
		"egrep '--include={s,x}' a . factory",
		"jq '--arg={a,b}' . factory.json",
		"jq -r '--arg={a,b}' . factory.json",
	}
	for _, cmd := range dash {
		for _, role := range []string{"coder", "qa", "reviewer", "judge", "pm"} {
			if got := termsGhCheck(t, cmd, work, role); got != 2 {
				t.Errorf("DASH: role %s cmd %q exit %d, want 2", role, cmd, got)
			}
		}
	}
}

func TestTermsBraceViewGiveUpRefused(t *testing.T) {
	_, work := termsGhFpEnv(t)
	for _, cmd := range []string{
		"gh pr view 1 --jq '{a,b}' # factory",
		"grep 'a{1,2}' factory/x > out",
		"grep 'a{1,2}' < factory/x",
	} {
		if got := termsGhCheck(t, cmd, work, "coder"); got != 2 {
			t.Errorf("give-up %q exit %d, want 2", cmd, got)
		}
	}
}

func TestTermsBraceViewDepthStaysOriginal(t *testing.T) {
	_, work := termsGhFpEnv(t)
	root := filepath.Dir(filepath.Dir(work))
	secrets := filepath.Join(root, "factory")
	cmd := "eval \"grep -E 'a{1,2}' " + secrets + "/x\""
	if got := termsGhCheck(t, cmd, work, "coder"); got != 2 {
		t.Errorf("depth>0 %q exit %d, want 2", cmd, got)
	}
}

// TestTermsBraceViewHelper pins blanking on a few strings without the full Check path.
func TestTermsBraceViewHelper(t *testing.T) {
	cases := map[string]string{
		"grep -nE 'by [0-9]{1,3}' roles/factory.md": "grep -nE '' roles/factory.md",
		"rg -n 'a{1,2}' roles/factory.md":           "rg -n '' roles/factory.md",
		"rg '--glob={s,x}*' a . factory":            "rg '--glob={s,x}*' a . factory",
	}
	for in, want := range cases {
		if got := termsBraceView(in); got != want {
			t.Errorf("termsBraceView(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzTermsBraceView(f *testing.F) {
	f.Add("grep -n 'a{1,2}' roles/factory.md")
	f.Add("rg '--glob={s,x}*' a . factory")
	f.Fuzz(func(t *testing.T, cmd string) {
		got := termsBraceView(cmd)
		if len(got) > len(cmd)+2 {
			t.Fatalf("output longer than input+2")
		}
	})
}
