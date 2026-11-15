package spawn

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzSlug(f *testing.F) {
	for _, s := range []string{"", "Coder T-1", "İstanbul", "Kx", "a\xffb", strings.Repeat("x", 40)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Slug(s)
		if got == "" || len(got) > 32 || got[0] < 'a' || got[0] > 'z' || strings.HasSuffix(got, "-") {
			t.Fatalf("Slug(%q) = %q breaks the slug shape", s, got)
		}
		if Slug(got) != got {
			t.Fatalf("Slug is not idempotent on %q -> %q -> %q", s, got, Slug(got))
		}
	})
}

func FuzzDecodeText(f *testing.F) {
	seed, _ := os.ReadFile("testdata/roles/_common.md")
	f.Add(seed)
	f.Add([]byte("a\r\nb\rc\xff"))
	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := DecodeText(b)
		if err == nil && (strings.ContainsRune(s, '\r') || !utf8.ValidString(s)) {
			t.Fatalf("DecodeText(%q) = %q keeps a CR or invalid UTF-8", b, s)
		}
	})
}

func FuzzParseRuntimes(f *testing.F) {
	f.Add("coder=cursor", "pm")
	f.Add("*=cursor, all=claude", "")
	f.Add("x'y=cursor", "researcher")
	f.Fuzz(func(t *testing.T, a, b string) {
		if m, err := ParseRuntimes(a, b); err == nil {
			for _, v := range m {
				if v != "claude" && v != "cursor" {
					t.Fatalf("runtime %q", v)
				}
			}
		}
	})
}

func FuzzSettingsText(f *testing.F) {
	seed, _ := os.ReadFile("testdata/roles/settings/coder.json.in")
	f.Add(string(seed), "/w/u")
	f.Add(`{"a": "@FACTORY_LIVE_HOME@"}`, "a\"b\\c\x00é𝔘\xff")
	f.Add(`{"a": "@FACTORY_X@"}`, "")
	f.Fuzz(func(t *testing.T, tpl, home string) {
		text, r := SettingsText(tpl, "t.json.in", SettingsTokens(home, home, home))
		if r == nil && (!json.Valid([]byte(text)) || strings.Contains(text, "@FACTORY_")) {
			t.Fatalf("accepted a settings text that is invalid or holds a token: %q", text)
		}
		_, _ = settingsEnv(text)
	})
}

func FuzzJSONEscape(f *testing.F) {
	f.Add("plain \"q\" \\ \t é 𝔘 \x7f")
	f.Add("a\xffb")
	f.Fuzz(func(t *testing.T, s string) {
		var back string
		if err := json.Unmarshal([]byte(`"`+jsonEscape(s)+`"`), &back); err != nil {
			t.Fatalf("jsonEscape(%q) is not a JSON string body: %v", s, err)
		}
		if utf8.ValidString(s) && back != s {
			t.Fatalf("round trip %q -> %q", s, back)
		}
	})
}

func FuzzParseArgsObject(f *testing.F) {
	f.Add([]byte(`{"host_backend": "hc", "n": 1.5}`))
	f.Add([]byte(`[1]`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, b []byte) {
		row := &AgentRow{SpawnArgs: b}
		_ = priorSpawnArg(row, "org")
		_ = parseArgsObject(b)
	})
}
