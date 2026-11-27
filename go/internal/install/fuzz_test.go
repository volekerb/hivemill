package install

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzResolveLatest(f *testing.F) {
	f.Add(testTags)
	f.Add("aaaa\trefs/tags/v1.٣.0\nbbbb\trefs/tags/v1.3.0^{}\n")
	f.Add("aaaa refs/tags/v2.0.0\naaaa\vrefs/tags/v3.0.0\n")
	f.Fuzz(func(t *testing.T, s string) {
		tag := resolveLatest(s)
		if tag != "" && !tagRe.MatchString(tag) {
			t.Fatalf("resolveLatest returned %q, which is not a release tag", tag)
		}
	})
}

func FuzzAgentStatus(f *testing.F) {
	f.Add(testAgents)
	f.Add("NAME STATUS\nboss live\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) { _ = agentStatus(s) })
}

func FuzzParseArgs(f *testing.F) {
	f.Add("--dry-run\x00--port\x009000")
	f.Add("--yes\x00--ref=v1\x00--project-id\x00p\x00--project-repo\x00/x")
	f.Add("--dry-run\x00--herdr-app\x00--factory\x00Workshop=http://x\x00--factory\x00http://y")
	f.Add("--check\x00--port\x00٣")
	f.Fuzz(func(t *testing.T, s string) {
		a, err := ParseArgs(strings.Split(s, "\x00"))
		if err == nil && a == nil {
			t.Fatal("nil args without an error")
		}
		if err == nil && (a.Port < 0 || a.Port > 65535) {
			t.Fatalf("port out of range: %d", a.Port)
		}
	})
}

func FuzzDecodeOrdered(f *testing.F) {
	f.Add(mainLayout)
	f.Add(`{"a":[1,2.5e3,-0,{"b":null}],"a":"x"}`)
	f.Add(`[`)
	f.Fuzz(func(t *testing.T, s string) {
		v, err := decodeOrdered([]byte(s))
		if err != nil {
			return
		}
		if _, err := encodeOrdered(v); err != nil {
			t.Fatalf("a decoded document cannot be encoded: %v", err)
		}
		_, _, _ = barLayout(v, true)
		_, _, _ = dashboardConfig(v, &Args{})
		_, _, _ = dashboardConfig(v, &Args{Factories: []string{"A=http://x"}})
	})
}

func FuzzRedact(f *testing.F) {
	f.Add(leakText)
	f.Add("https://user:pw@example.com/x")
	f.Add("Authorization: Bearer x")
	f.Fuzz(func(t *testing.T, s string) {
		out := redact(s)
		if strings.Contains(s, leakText) && strings.Contains(out, leakText) {
			t.Fatal("a token survived redaction")
		}
	})
}

func FuzzExtractMember(f *testing.F) {
	dir := f.TempDir()
	seed := filepath.Join(dir, "seed.tar.gz")
	writeTarGz(f, seed, map[string]byte{"gh/bin/gh": tar.TypeReg}, "ELF")
	if b, err := os.ReadFile(seed); err == nil {
		f.Add(b)
	}
	f.Add([]byte("not gzip"))
	h := &OSHost{HomeDir: dir}
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "a.tar.gz")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = h.ExtractMember(p, "gh/bin/gh", filepath.Join(t.TempDir(), "out"))
	})
}
