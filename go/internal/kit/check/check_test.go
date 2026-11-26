package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testVersion = "1.2.3"

var hexa = strings.Repeat("ab", 32)

// Credential-looking fixtures are assembled at run time so this file is not itself a hit for secret scanners.
var (
	tokKey = "tok" + "en"
	pem    = "-----" + "BEGIN PRIV" + "ATE KEY-----"
	pwKey  = "Pass" + "word"
)

func put(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(p) // WriteFile keeps an existing file's mode
	mode := os.FileMode(0o644)
	if strings.Contains(rel, "/bin/") {
		mode = 0o755 // shims must be executable
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func shim(version string) string {
	s := "#!/bin/sh\nVERSION=\"" + version + "\"\n"
	for _, p := range wantPins {
		s += p + "=\"" + hexa + "\"\n"
	}
	return s + "base=https://github.com/example/example/releases\n"
}

const skillMD = "---\nname: demo\ndescription: Demo skill.\n---\n# demo\n"

// repoRoot builds a valid temp repo with one skill "demo" and a copy of the real kit/template.
func repoRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put(t, root, "VERSION", testVersion+"\n")
	put(t, root, "kit/skills.json", `{"schemaVersion":1,"skills":[{"name":"demo","level":"SOLO","plugin":"plugins/kit-demo","binary":"hm-demo","description":"A demo."}]}`)
	put(t, root, ".claude-plugin/marketplace.json", `{"plugins":[{"name":"hivemill-demo","source":"./plugins/kit-demo"}]}`)
	put(t, root, "go/cmd/hm-demo/main.go", "package main\n")
	p := "plugins/kit-demo/"
	put(t, root, p+".claude-plugin/plugin.json", `{"name":"hivemill-demo","version":"`+testVersion+`"}`)
	put(t, root, p+"skills/demo/SKILL.md", skillMD)
	put(t, root, p+"demo.example.json", `{"schemaVersion":1,"token":"<your-token>"}`)
	put(t, root, p+"bin/hm-demo", shim(testVersion))
	for _, n := range []string{"a", "b", "c"} {
		put(t, root, p+"eval/cases/"+n+".json", `{"argv":["--version"]}`)
	}
	copyTemplate(t, root)
	return root
}

func copyTemplate(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join("..", "..", "..", "..", "kit", "template")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		put(t, root, "kit/template/"+filepath.ToSlash(r), string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func lines(r Result) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.String())
	}
	return out
}

func TestValidRepoPasses(t *testing.T) {
	r := Run(repoRoot(t))
	if len(r.Findings) != 0 || r.Skills != 1 {
		t.Fatalf("want ok (1 skills), got %d skills, findings %v", r.Skills, lines(r))
	}
}

func TestMissingPieces(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
		want   string
	}{
		{"SKILL.md", func(t *testing.T, root string) {
			os.Remove(filepath.Join(root, "plugins/kit-demo/skills/demo/SKILL.md"))
		}, "plugins/kit-demo/skills/demo/SKILL.md: missing"},
		{"shim pins", func(t *testing.T, root string) {
			put(t, root, "plugins/kit-demo/bin/hm-demo", "VERSION=\""+testVersion+"\"\nhttps://x\n")
		}, "plugins/kit-demo/bin/hm-demo: pins missing or not 64 lowercase hex: SHA_LINUX_AMD64, SHA_LINUX_ARM64, SHA_DARWIN_AMD64, SHA_DARWIN_ARM64"},
		{"eval cases", func(t *testing.T, root string) {
			os.Remove(filepath.Join(root, "plugins/kit-demo/eval/cases/c.json"))
		}, "plugins/kit-demo/eval/cases: needs at least 3 .json cases, found 2"},
		{"marketplace entry", func(t *testing.T, root string) {
			put(t, root, ".claude-plugin/marketplace.json", `{"plugins":[]}`)
		}, ".claude-plugin/marketplace.json: no entry with name \"hivemill-demo\""},
		{"plugin version", func(t *testing.T, root string) {
			put(t, root, "plugins/kit-demo/.claude-plugin/plugin.json", `{"name":"hivemill-demo","version":"9.9.9"}`)
		}, "version \"9.9.9\" must equal the VERSION file"},
		{"shim version", func(t *testing.T, root string) {
			put(t, root, "plugins/kit-demo/bin/hm-demo", shim("9.9.9"))
		}, "VERSION \"9.9.9\" must equal the VERSION file"},
		{"http download base", func(t *testing.T, root string) {
			put(t, root, "plugins/kit-demo/bin/hm-demo", shim(testVersion)+"u=http://insecure.example\n")
		}, "download base must be https only"},
		{"binary main package", func(t *testing.T, root string) {
			os.RemoveAll(filepath.Join(root, "go/cmd/hm-demo"))
		}, "go/cmd/hm-demo: missing"},
		{"example config schemaVersion", func(t *testing.T, root string) {
			put(t, root, "plugins/kit-demo/demo.example.json", `{"a":1}`)
		}, "needs an integer schemaVersion"},
		{"skill front matter", func(t *testing.T, root string) {
			put(t, root, "plugins/kit-demo/skills/demo/SKILL.md", "# no front matter\n")
		}, "must start with a --- front matter block"},
		{"registry name", func(t *testing.T, root string) {
			put(t, root, "kit/skills.json", `{"schemaVersion":1,"skills":[{"name":"Bad_Name","level":"SOLO","plugin":"plugins/kit-Bad_Name","binary":"hm-Bad_Name","description":"x"}]}`)
		}, "name must be lowercase letters"},
		{"registry unknown field", func(t *testing.T, root string) {
			put(t, root, "kit/skills.json", `{"schemaVersion":1,"skills":[],"extra":1}`)
		}, "unknown field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := repoRoot(t)
			tc.mutate(t, root)
			got := lines(Run(root))
			if len(got) != 1 || !strings.Contains(got[0], tc.want) || !strings.HasPrefix(got[0], "kit-check: ") {
				t.Fatalf("want exactly one finding containing %q, got %v", tc.want, got)
			}
		})
	}
}

func TestSecretFindings(t *testing.T) {
	root := repoRoot(t)
	put(t, root, "plugins/kit-demo/demo.example.json", `{"schemaVersion":1,"`+tokKey+`":"abcd1234realvalue"}`)
	put(t, root, "plugins/kit-demo/eval/cases/a.json", `{"env":{"X":"ghp_0123456789abcdefghij"}}`)
	put(t, root, "plugins/kit-demo/eval/cases/b.json", `{"args":["github_pat_11AAAA"]}`)
	put(t, root, "plugins/kit-demo/eval/fixtures/k.json", `{"note":"`+pem+`"}`)
	got := lines(Run(root))
	if len(got) != 4 {
		t.Fatalf("want 4 findings, got %d: %v", len(got), got)
	}
	for _, g := range got {
		if !strings.Contains(g, "credential") && !strings.Contains(g, "non-placeholder") {
			t.Fatalf("unexpected finding %q", g)
		}
	}
}

func TestSecretKeysAndShapes(t *testing.T) {
	for _, body := range []string{
		`{"schemaVersion":1,"API_KEY":"real"}`,
		`{"schemaVersion":1,"nested":{"db` + pwKey + `":"hunter2"}}`,
		`{"schemaVersion":1,"list":[{"client_secret":"zzz"}]}`,
		`{"schemaVersion":1,"x":"sk-abc123"}`,
		`{"schemaVersion":1,"` + tokKey + `":{"value":"abc123"}}`,
		`{"schemaVersion":1,"` + tokKey + `s":[{"v":"abc123"}]}`,
		`{"schemaVersion":1,"h":"Bearer ghp_` + `0123456789abcd"}`,
		`{"schemaVersion":1,"cmd":"curl -H x-api-key:sk-` + `ant12345"}`,
		`{"schemaVersion":1,"privateKey":"MIIE"}`,
	} {
		root := repoRoot(t)
		put(t, root, "plugins/kit-demo/demo.example.json", body)
		if got := lines(Run(root)); len(got) != 1 {
			t.Fatalf("%s: want 1 finding, got %v", body, got)
		}
	}
	root := repoRoot(t)
	put(t, root, "plugins/kit-demo/eval/fixtures/e.env", "API_"+tokKey+"=abc123\n")
	if got := lines(Run(root)); len(got) != 1 {
		t.Fatalf("env fixture: want 1 finding, got %v", got)
	}
	root = repoRoot(t)
	put(t, root, "plugins/kit-demo/eval/fixtures/x.txt", "key = ghp_"+"abcdefghijklmnop\n")
	if got := lines(Run(root)); len(got) != 1 {
		t.Fatalf("text fixture: want 1 finding, got %v", got)
	}
}

func TestShimNotExecutable(t *testing.T) {
	root := repoRoot(t)
	if err := os.Chmod(filepath.Join(root, "plugins/kit-demo/bin/hm-demo"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := lines(Run(root))
	if len(got) != 1 || !strings.Contains(got[0], "plugins/kit-demo/bin/hm-demo: not executable") {
		t.Fatalf("got %v", got)
	}
}

func TestSecretNumberUnderKey(t *testing.T) {
	root := repoRoot(t)
	put(t, root, "plugins/kit-demo/demo.example.json", `{"schemaVersion":1,"password":123456}`)
	if got := lines(Run(root)); len(got) != 1 {
		t.Fatalf("want 1 finding, got %v", got)
	}
}

func TestSecretPlaceholdersAllowed(t *testing.T) {
	root := repoRoot(t)
	put(t, root, "plugins/kit-demo/demo.example.json", `{"schemaVersion":1,"token":"<your-token>","password":"","apiKey":"<key>","tokenCount":5,"tokenizer":true,"note":"a sk plan","u":"ask-me"}`)
	put(t, root, "plugins/kit-demo/eval/fixtures/x.txt", "no credentials here, ghp_ alone\n")
	if got := lines(Run(root)); len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

func TestTemplateValid(t *testing.T) {
	r := Run(repoRoot(t))
	for _, l := range lines(r) {
		if strings.Contains(l, "kit/template") {
			t.Fatalf("template must validate: %s", l)
		}
	}
}

func TestTemplateBroken(t *testing.T) {
	root := repoRoot(t)
	os.Remove(filepath.Join(root, "kit/template/skills/NAME/SKILL.md"))
	put(t, root, "kit/template/NAME.example.json", `{"schemaVersion":1,"`+tokKey+`":"real-value-here"}`)
	got := strings.Join(lines(Run(root)), "\n")
	for _, w := range []string{"kit/template/skills/NAME/SKILL.md: missing", "kit/template/NAME.example.json: key \"token\""} {
		if !strings.Contains(got, w) {
			t.Fatalf("want %q in:\n%s", w, got)
		}
	}
	os.RemoveAll(filepath.Join(root, "kit/template"))
	if got := lines(Run(root)); len(got) != 1 || !strings.Contains(got[0], "kit/template: missing") {
		t.Fatalf("got %v", got)
	}
}

func TestTemplateShimRefusesUnpinned(t *testing.T) {
	cmd := exec.Command("sh", filepath.Join("..", "..", "..", "..", "kit", "template", "bin", "hm-NAME"), "--version")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unpinned shim must exit non-zero, output %q", out)
	}
	if !strings.Contains(string(out), "not pinned yet") {
		t.Fatalf("message must say it is not pinned: %q", out)
	}
}

func TestMissingRegistryAndVersion(t *testing.T) {
	got := lines(Run(t.TempDir()))
	joined := strings.Join(got, "\n")
	for _, w := range []string{"VERSION:", "kit/skills.json:", "kit/template: missing"} {
		if !strings.Contains(joined, w) {
			t.Fatalf("want %q in:\n%s", w, joined)
		}
	}
}

func TestExternalSkill(t *testing.T) {
	const sha = "0123456789012345678901234567890123456789"
	good := `{"schemaVersion":1,"skills":[{"name":"demo","description":"d","use_when":"w",` +
		`"repo":"https://github.com/o/r","commit":"` + sha + `","path":"skills/demo","license":"MIT",` +
		`"install":"git fetch ` + sha + ` # demo@` + sha[:12] + `"}]}`
	about := func(registry, note string) string {
		root := t.TempDir()
		put(t, root, "kit/skills.json", registry)
		if note != "" {
			put(t, root, "kit/skills/THIRD_PARTY.md", note)
		}
		var b []string
		for _, f := range Run(root).Findings {
			if f.Path == "kit/skills.json" || f.Path == "kit/skills/THIRD_PARTY.md" || strings.HasPrefix(f.Path, "go/cmd/") {
				b = append(b, f.String())
			}
		}
		return strings.Join(b, "\n")
	}
	note := "demo " + sha
	if got := about(good, note); got != "" {
		t.Fatalf("a good external skill has findings:\n%s", got)
	}
	swap := func(old, repl string) string { return about(strings.Replace(good, old, repl, 1), note) }
	for want, got := range map[string]string{
		"full 40-character":       swap(`"commit":"`+sha, `"commit":"main`),
		"no level, plugin":        swap(`"license"`, `"binary":"hm-demo","license"`),
		"cannot read":             about(good, ""),
		"and its pinned commit":   about(good, "demo at another pin"),
		"use_when is required":    swap(`"use_when":"w",`, ""),
		"path must be a relative": swap("skills/demo", "../x"),
		"names the pinned commit": swap("git fetch "+sha, "git pull"),
		"install must be":         swap("demo@"+sha[:12], "demo@old"),
		"repo must be https":      swap("https://github.com/o/r", "git@x:o/r"),
		"license must name":       swap(`"license":"MIT",`, ""),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}
