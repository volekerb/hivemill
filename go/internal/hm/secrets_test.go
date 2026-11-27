package hm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestScrubSecretsLocalCI(t *testing.T) {
	noASCEnv(t)
	key := "EXAMPLE-fake-asc-key-content"
	t.Setenv("ASC_KEY_CONTENT", key)
	block := "-----BEGIN " + "PRIVATE KEY-----\nEXAMPLEfakeKeyLineOne\n-----END " + "PRIVATE KEY-----"
	f := newLocalCIFake(t, "none", "make test")
	terms := filepath.Join(t.TempDir(), "terms.txt")
	if err := os.WriteFile(terms, []byte("zz-example-term\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.factoryCfg(t, "scrub_terms_file", terms)
	f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) {
		return 1, "building\n" + block + "\nkey " + key + "\n"
	}
	logdir := t.TempDir()
	_, note, err := f.r.waitLocalCI(f.run, headA, "make test", time.Minute, logdir)
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(logdir, "gate-cmd.log")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "building\n[asc-key redacted]\nkey [asc-key redacted]\n"; string(b) != want {
		t.Errorf("gate-cmd.log: %q", b)
	}
	if fi, err := os.Stat(log); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("gate-cmd.log mode: %v %v", fi, err)
	}
	// the job's own copy is scrubbed too
	if own, err := os.ReadFile(filepath.Join(logdir, "localci-"+headA[:12], "gate-cmd.log")); err != nil || string(own) != string(b) {
		t.Errorf("job log: %q %v", own, err)
	}
	for name, text := range map[string]string{"tail": f.run.LocalCI.Tail, "note": note} {
		if strings.Contains(text, key) || strings.Contains(text, "EXAMPLEfakeKeyLineOne") || !strings.Contains(text, "[asc-key redacted]") {
			t.Errorf("%s: %q", name, text)
		}
	}
	// the public-text guard reads the log before the scrub: it is still withheld from the PR
	if !f.run.LocalCI.Private {
		t.Error("a log with a key block must stay private")
	}
}

// The round's log cannot be rewritten (it is read-only): it does not stay, and the note says so instead of naming it.
func TestGateLogRemovedWhenScrubFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	noASCEnv(t)
	key := "EXAMPLE-fake-asc-key-content"
	t.Setenv("ASC_KEY_CONTENT", key)
	for _, rc := range []int{0, 1, 124} {
		f := newLocalCIFake(t, "none", "make test")
		logdir := t.TempDir()
		log := filepath.Join(logdir, "gate-cmd.log")
		f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) {
			if err := os.WriteFile(log, []byte("an earlier round: "+key+"\n"), 0o400); err != nil {
				t.Fatal(err)
			}
			return rc, "key " + key + "\n"
		}
		_, note, err := f.r.waitLocalCI(f.run, headA, "make test", time.Minute, logdir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(log); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("exit %d: the log is still there: %v", rc, err)
		}
		if !strings.Contains(note, "gate log removed: scrub failed") || strings.Contains(note, "gate-cmd.log") ||
			strings.Contains(note, logdir) || strings.Contains(note, key) {
			t.Errorf("exit %d: note %q", rc, note)
		}
	}
}

// A job that is forgotten without a result (a failed start) leaves no key in its log.
func TestScrubSecretsForgottenJobLog(t *testing.T) {
	noASCEnv(t)
	key := "EXAMPLE-fake-asc-key-content"
	t.Setenv("ASC_KEY_CONTENT", key)
	r, _ := testRunner(t, "")
	j := &ciJob{Task: "T-0001", Head: headA, Dir: t.TempDir()}
	own := filepath.Join(j.Dir, "gate-cmd.log")
	if err := os.WriteFile(own, []byte("key "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.forgetCIJob(j)
	if b, err := os.ReadFile(own); err != nil || string(b) != "key [asc-key redacted]\n" {
		t.Errorf("job log: %q %v", b, err)
	}
}

// A public repo's pr create still refuses a key block: the scrub does not turn the refusal into a redacted PR.
func TestScrubSecretsPRBody(t *testing.T) {
	noASCEnv(t)
	key := "EXAMPLE-fake-asc-key-content"
	t.Setenv("ASC_KEY_CONTENT", key)
	r, s := testRunner(t, "claude-coder-t-0001-a7")
	var ghArgs []string
	var ghIn string
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		ghArgs, ghIn = args, in
		return 0, "", ""
	}
	r.Tools.Git = func(string, ...string) (int, string, string) { return 0, "factory/t-0001\n", "" }
	if _, err := s.db.Exec(`INSERT INTO agents(name,role,goal_id,task_id,status) VALUES('claude-coder-t-0001-a7','task','G-001','T-0001','live')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PR("create", "T-0001: add "+key, "the key is "+key, "/wt"); err != nil {
		t.Fatal(err)
	}
	if ghIn != "the key is [asc-key redacted]" || !strings.Contains(strings.Join(ghArgs, " "), "--title T-0001: add [asc-key redacted] --body-file") {
		t.Errorf("gh %q stdin %q", ghArgs, ghIn)
	}
	terms := filepath.Join(t.TempDir(), "terms.txt")
	if err := os.WriteFile(terms, []byte("zz-example-term\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"public_text_repos": "zz-test/www", "scrub_terms_file": terms} {
		if err := s.SetFactoryCfg(k, v); err != nil {
			t.Fatal(err)
		}
	}
	ghArgs = nil
	block := "-----BEGIN " + "PRIVATE KEY-----\nEXAMPLEfakeKeyLineOne\n-----END " + "PRIVATE KEY-----"
	if _, err := r.PR("create", "T-0001: x", block, "/wt"); err == nil || !strings.Contains(err.Error(), "public text") || ghArgs != nil {
		t.Errorf("key block: %v (gh %q)", err, ghArgs)
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
