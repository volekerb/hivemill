package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const skillSHA = "0123456789012345678901234567890123456789"

// skillHost records the git calls; a checkout creates the skill's SKILL.md in the fetched directory.
type skillHost struct {
	*OSHost
	calls []string
	fail  bool
}

func (h *skillHost) Run(argv []string, _ string, _ time.Duration, _ bool) (int, string) {
	call := strings.Join(argv, " ")
	h.calls = append(h.calls, call)
	switch {
	case argv[1] == "init":
		_ = os.MkdirAll(argv[len(argv)-1], 0o755)
	case h.fail && strings.Contains(call, " fetch "):
		return 128, "fatal: unreachable"
	case strings.Contains(call, " checkout "):
		dir := filepath.Join(argv[2], "skills", "demo")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("demo"), 0o600)
	}
	return 0, ""
}

func skillCtx(t *testing.T, registry string) (*Ctx, *skillHost, *bytes.Buffer) {
	t.Helper()
	h := &skillHost{OSHost: &OSHost{HomeDir: t.TempDir(), Env: map[string]string{}}}
	out := &bytes.Buffer{}
	c := NewCtx(h, &Args{}, out, false)
	if err := os.MkdirAll(filepath.Join(c.fh, "kit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.fh, "kit", "skills.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	return c, h, out
}

const demoRegistry = `{"schemaVersion":1,"skills":[{"name":"demo","repo":"https://github.com/o/r","commit":"` +
	skillSHA + `","path":"skills/demo"},{"name":"plug","plugin":"plugins/kit-plug"}]}`

func TestInstallSkillsFetchesThePinAndLinks(t *testing.T) {
	c, h, out := skillCtx(t, demoRegistry)
	installSkills(c)
	link := filepath.Join(c.home, ".claude", "skills", "demo")
	if b, err := os.ReadFile(filepath.Join(link, "SKILL.md")); err != nil || string(b) != "demo" {
		t.Fatalf("skill not linked: %v\n%s", err, out)
	}
	if len(h.calls) != 3 || !strings.HasSuffix(h.calls[1], "fetch --depth 1 https://github.com/o/r "+skillSHA) {
		t.Fatalf("calls: %q", h.calls)
	}
	installSkills(c)
	if len(h.calls) != 3 || !strings.Contains(out.String(), "kept the existing") {
		t.Fatalf("a second run must keep the skill: %q\n%s", h.calls, out)
	}
}

func TestInstallSkillsKeepsAnExistingDirectory(t *testing.T) {
	c, h, _ := skillCtx(t, demoRegistry)
	if err := os.MkdirAll(filepath.Join(c.home, ".claude", "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	installSkills(c)
	if len(h.calls) != 0 {
		t.Fatalf("calls: %q", h.calls)
	}
}

func TestInstallSkillsFailedFetchLeavesNothing(t *testing.T) {
	c, h, out := skillCtx(t, demoRegistry)
	h.fail = true
	installSkills(c)
	for _, p := range []string{".claude/skills/demo", ".local/share/hivemill/skills/demo@" + skillSHA[:12],
		".local/share/hivemill/skills/demo@" + skillSHA[:12] + ".installing"} {
		if h.Exists(filepath.Join(c.home, filepath.FromSlash(p))) {
			t.Fatalf("%s left behind", p)
		}
	}
	if !strings.Contains(out.String(), "demo not installed") {
		t.Fatalf("no warning:\n%s", out)
	}
}

func TestInstallSkillsRefusesAnInvalidEntry(t *testing.T) {
	c, h, out := skillCtx(t, strings.Replace(demoRegistry, "skills/demo", "../../elsewhere", 1))
	installSkills(c)
	if len(h.calls) != 0 || !strings.Contains(out.String(), "invalid entry") {
		t.Fatalf("calls: %q\n%s", h.calls, out)
	}
}

func TestInstallSkillsFollowsAMovedPinAndRepairsABrokenFetch(t *testing.T) {
	c, h, out := skillCtx(t, demoRegistry)
	installSkills(c)
	next := strings.Repeat("a", 40)
	if err := os.WriteFile(filepath.Join(c.fh, "kit", "skills.json"),
		[]byte(strings.Replace(demoRegistry, skillSHA, next, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	// an interrupted fetch left an empty directory of the new pin
	src := filepath.Join(c.home, ".local", "share", "hivemill", "skills", "demo@"+next[:12])
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	installSkills(c)
	cur, err := os.Readlink(filepath.Join(c.home, ".claude", "skills", "demo"))
	if err != nil || cur != filepath.Join(src, "skills", "demo") || len(h.calls) != 6 {
		t.Fatalf("link %q, %v, calls %q\n%s", cur, err, h.calls, out)
	}
}
