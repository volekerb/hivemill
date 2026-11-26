package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noASCEnv hides any real key of the machine the tests run on.
func noASCEnv(t *testing.T) {
	t.Helper()
	for _, name := range append(append([]string{}, ascValueEnvs...), ascPathEnvs...) {
		t.Setenv(name, "")
	}
}

func TestScrubSecretsPEMBlock(t *testing.T) {
	noASCEnv(t)
	// the header is split, as in the other fake keys of this repo: no source line may read as a real key
	block := "-----BEGIN " + "PRIVATE KEY-----\nEXAMPLEfakeKeyLineOne\nEXAMPLEfakeKeyLineTwo\n-----END " + "PRIVATE KEY-----"
	for name, key := range map[string]string{
		"pem":     block,
		"escaped": strings.ReplaceAll(block, "\n", `\n`),
		"ec":      strings.ReplaceAll(block, "PRIVATE KEY", "EC PRIVATE KEY"),
	} {
		if got := ScrubSecrets(key); got != "[asc-key redacted]" {
			t.Errorf("%s: %q", name, got)
		}
		in := "before\t é\r\n{\"key\": \"" + key + "\"}\nbetween " + key + " after\n"
		want := "before\t é\r\n{\"key\": \"[asc-key redacted]\"}\nbetween [asc-key redacted] after\n"
		if got := ScrubSecrets(in); got != want {
			t.Errorf("%s in text: %q", name, got)
		}
	}
}

func TestScrubSecretsEnvValues(t *testing.T) {
	noASCEnv(t)
	id, key := "EXAMPLEKEYID", "EXAMPLE-fake-key-content-EXAMPLEKEYID-tail"
	t.Setenv("ASC_KEY_ID", id)
	t.Setenv("APP_STORE_CONNECT_API_KEY_KEY", key)
	t.Setenv("ASC_ISSUER_ID", "short") // under 8 characters: not a secret
	got := ScrubSecrets("id=" + id + " key=" + key + " again " + id + " short")
	if want := "id=[asc-key redacted] key=[asc-key redacted] again [asc-key redacted] short"; got != want {
		t.Errorf("got %q", got)
	}
}

func TestScrubSecretsKeyFile(t *testing.T) {
	for _, env := range ascPathEnvs {
		noASCEnv(t)
		content := "EXAMPLEfileLineOne\r\nEXAMPLEfileLineTwo\r\nshort\r\n"
		p := filepath.Join(t.TempDir(), "AuthKey_EXAMPLE.p8")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, p)
		got := ScrubSecrets("all:" + content + "one EXAMPLEfileLineTwo, short")
		if want := "all:[asc-key redacted]one [asc-key redacted], short"; got != want {
			t.Errorf("%s: %q", env, got)
		}
	}
}

func TestScrubSecretsCleanTextUnchanged(t *testing.T) {
	noASCEnv(t)
	t.Setenv("ASC_KEY_PATH", filepath.Join(t.TempDir(), "missing.p8"))
	t.Setenv("ASC_KEY_ID", "EXAMPLEKEYID")
	in := "ok\r\n-----BEGIN CERTIFICATE-----\nEXAMPLE\n-----END CERTIFICATE-----\nPRIVATE KEY é\n"
	if got := ScrubSecrets(in); got != in {
		t.Errorf("got %q", got)
	}
}

func TestRecordPromptScrubsASCKey(t *testing.T) {
	noASCEnv(t)
	key := "EXAMPLE-fake-asc-key-content"
	t.Setenv("ASC_KEY_CONTENT", key)
	r, run, _ := promptRunner(t)
	r.recordPrompt(run, "task", "ci", "the log ends: "+key+"\n")
	b, err := os.ReadFile(filepath.Join(r.Env.StateDir(), "prompts", "T-0001", "001-ci.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "the log ends: [asc-key redacted]\n" {
		t.Errorf("recorded %q", b)
	}
}
