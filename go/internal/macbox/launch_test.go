package macbox

import (
	"strings"
	"testing"
)

func TestRefuseUid(t *testing.T) {
	if err := RefuseLauncherUID(0, 501); err == nil {
		t.Fatal("uid 0 must be refused")
	}
	if err := RefuseLauncherUID(501, 501); err == nil {
		t.Fatal("owner uid must be refused")
	}
	if err := RefuseLauncherUID(502, 501); err != nil {
		t.Fatalf("worker uid should pass: %v", err)
	}
}

func TestDropsEnv(t *testing.T) {
	in := []string{
		"PATH=/bin", "HOME=/hidden", "LANG=C", "SSH_AUTH_SOCK=/tmp/s",
		"FACTORY_AGENT=a", "LC_CTYPE=UTF-8", "GH_TOKEN=tok",
	}
	out := FilterEnviron(in)
	for _, kv := range out {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || k == "SSH_AUTH_SOCK" {
			t.Fatalf("dropped key leaked: %s", k)
		}
	}
	got := EnvironMap(out)
	if got["PATH"] != "/bin" || got["GH_TOKEN"] != "tok" || got["LC_CTYPE"] != "UTF-8" {
		t.Fatalf("allow-listed keys missing: %v", got)
	}
	metaIn := []string{
		"PATH=/bin", "MACBOX_AGENT_HOME=/a", "MACBOX_CLAUDE_CONFIG_DIR=/c",
		"MACBOX_GIT_CONFIG_GLOBAL=/g", "MACBOX_TMPDIR=/t", "MACBOX_OWNER_UID=9", "DROP=1",
	}
	filtered := EnvironMap(FilterEnviron(metaIn))
	ApplyLauncherDirs(filtered)
	if filtered["HOME"] != "/a" || filtered["TMPDIR"] != "/t" {
		t.Fatalf("meta env through filter: %v", filtered)
	}
	m := map[string]string{"MACBOX_AGENT_HOME": "/a", "PATH": "/bin", "MACBOX_OWNER_UID": "9"}
	ApplyLauncherDirs(m)
	if m["HOME"] != "/a" || m["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Fatalf("launcher dirs: %v", m)
	}
	if _, ok := m["MACBOX_OWNER_UID"]; ok {
		t.Fatal("meta env must be stripped")
	}
}

func TestSplitLauncherArgv(t *testing.T) {
	rest, err := SplitLauncherArgv([]string{"macbox-launch", "--", "/usr/bin/sandbox-exec", "-f", "p"})
	if err != nil || rest[0] != "/usr/bin/sandbox-exec" {
		t.Fatalf("split: %v err %v", rest, err)
	}
	if _, err := SplitLauncherArgv([]string{"no-separator"}); err == nil {
		t.Fatal("missing -- must fail")
	}
}