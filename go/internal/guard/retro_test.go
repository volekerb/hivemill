package guard

import (
	"strings"
	"testing"
)

func TestRetroFactoryAllowList(t *testing.T) {
	a, err := NewAnalyzer(t.TempDir(), "retro", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"whoami", "inbox", "done", "retro"} {
		if err := a.retroFactory(sub, true); err != nil {
			t.Errorf("retroFactory(%q) = %v, want nil", sub, err)
		}
	}
	if err := a.retroFactory("judge", true); err == nil || !strings.Contains(err.Error(), "allowed: done, inbox, retro, whoami") {
		t.Errorf("retroFactory(judge) = %v, want block with allow-list", err)
	}
	if err := a.retroFactory("", false); err == nil || !strings.Contains(err.Error(), "factory ") {
		t.Errorf("bare factory = %v, want block", err)
	}
}

func TestRetroRolesCodeFactoryPath(t *testing.T) {
	if !retroRolesCode("/tmp/x/factory/roles/coder.md") {
		t.Error("factory/roles path in text should hit retro_roles_code")
	}
	if retroRolesCode("/tmp/zz-only.txt") {
		t.Error("unrelated path should not hit")
	}
}

func TestRetroResolvePrintf(t *testing.T) {
	got, ok := retroResolvePathExpr("$(printf %s /tmp/; printf %s roles/coder.md)")
	if !ok || got != "/tmp/roles/coder.md" {
		t.Errorf("resolve = %q ok=%v, want /tmp/roles/coder.md", got, ok)
	}
}

func TestRetroRolesCodePrintfFrag(t *testing.T) {
	frag := "$(printf %s /tmp/; printf %s roles/coder.md)"
	if retroRolesCode(frag) {
		t.Fatalf("retroRolesCode(%q) = true, want false (Python)", frag)
	}
	ccwd := t.TempDir()
	cmd := `echo x > "$(printf %s /tmp/; printf %s roles/coder.md)"`
	for _, m := range retroSubstRedirectFrags(cmd) {
		if retroRolesSubst(m, ccwd, true) {
			t.Fatalf("retroRolesSubst(%q) = true, want false", m)
		}
	}
	if err := Check(cmd, ccwd, retroRole, false); err != nil {
		t.Fatalf("Check allowed cmd: %v", err)
	}
}
