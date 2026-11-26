package guard

// The guard2 cases of go/internal/factorytest (persist, push_pin, repocfg, repoprot; T-1920, guard port 2a): every
// TestG2<name> is the old Test<name> with the same inputs and the same expected answers, but it calls HookMain
// in-process (g2_helpers_test.go) instead of the built binary. The map is testdata/portmap/guard2a.txt.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---- from go/internal/factorytest/guard2/persist_test.go ----
// Port of tests/test_worker_guard_persist.py (T-0334, T-0957): workers cannot persist config into the live box
// (global/system git config, ~/.config/git, shell init files, ~/.local/bin). Every call runs the real Go guard binary.

var (
	g2PRoles   = []string{"coder", "qa", "reviewer"}
	g2PNoPush  = map[string]bool{"qa": true, "reviewer": true} // T-0755: read-only git allow-list roles
	g2PCfgRead = regexp.MustCompile(`--get|--list| -l(?: |$)`)
	g2PWorkers = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout", "evaluator"}

	g2PProtected = []string{".gitconfig", ".config/git/config", ".config/git/attributes", ".config/git/ignore", ".bashrc", ".zshrc",
		".zshenv", ".profile", ".bash_profile", ".zprofile"}
	g2PTools = []string{"Edit", "MultiEdit", "NotebookEdit", "Write"} // sorted
)

// pEnv is one test's view: an empty temp FACTORY_HOME (T-0636: the result must not depend on the caller's live home).
type g2PEnv struct {
	t    *testing.T
	home string
	pw   string
}

func g2NewP(t *testing.T) *g2PEnv {
	t.Helper()
	return &g2PEnv{t: t, home: g2RealTemp(t), pw: g2Pw(t)}
}

func (p *g2PEnv) hook(payload map[string]any, role string, hq bool) g2Res {
	p.t.Helper()
	args := g2RoleArgs(role)
	if hq {
		args = []string{"--hq"}
	}
	return g2HookJSON(p.t, payload, g2Opts{args: args, cwd: g2RepoRoot(p.t), set: map[string]string{"FACTORY_HOME": p.home}})
}

func (p *g2PEnv) bashIn(cmd, role string, hq bool, cwd string) g2Res {
	p.t.Helper()
	pl := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
	if cwd != "" {
		pl["cwd"] = cwd
	}
	return p.hook(pl, role, hq)
}

func (p *g2PEnv) blocked(cmd, role, cwd string) {
	p.t.Helper()
	r := p.bashIn(cmd, role, false, cwd)
	if r.code != 2 {
		p.t.Errorf("should block (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
}

func (p *g2PEnv) allowed(cmd, role, cwd string) {
	p.t.Helper()
	r := p.bashIn(cmd, role, false, cwd)
	if r.code != 0 {
		p.t.Errorf("should allow (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
}

func TestG2PersistGitConfigGlobalAndSystemWritesBlockedForEveryWorkerRole(t *testing.T) {
	p := g2NewP(t)
	for _, role := range g2PRoles {
		for _, cmd := range []string{"git config --global user.name x", "git config --global --add core.fsmonitor /tmp/x",
			"git config --global --unset user.name", "git config --global --unset-all a.b",
			"git config --global --replace-all a.b c", "git config --global --edit",
			"git config --global -e", "git config --global --rename-section a b",
			"git config --global --remove-section a", "git config --system core.fsmonitor x",
			"git config --global set a.b c", "git config --global unset a.b",
			"git -c core.pager=cat config --global a.b c", "git -C /tmp config --global a.b c",
			"git config --glo a.b c",
			"env git config --global a.b c", "sh -c 'git config --global a.b c'"} {
			p.blocked(cmd, role, "")
		}
	}
}

func TestG2PersistGitConfigFileToProtectedPathBlocked(t *testing.T) {
	p := g2NewP(t)
	for _, role := range g2PRoles {
		for _, rel := range g2PProtected {
			p.blocked(fmt.Sprintf("git config -f %s/%s core.fsmonitor x", p.pw, rel), role, "")
		}
	}
	for _, cmd := range []string{"git config --file ~/.gitconfig a.b c", "git config --file=$HOME/.config/git/config a.b c",
		"git config -f${HOME}/.gitconfig a.b c", "git config -f /etc/gitconfig a.b c",
		"git config -f ~/x/../.gitconfig a.b c", "git config --file ~/.gitconfig --unset a.b"} {
		p.blocked(cmd, "coder", "")
	}
}

func TestG2PersistAliasRoutesToGlobalConfigBlocked(t *testing.T) {
	p := g2NewP(t)
	p.blocked("git -c alias.c=config c --global a.b c", "coder", "")
	p.blocked("git -c 'alias.c=!git config --global a.b c' c", "coder", "")
	p.blocked("git -c 'alias.c=!echo x >> ~/.bashrc' c", "coder", "")
}

func TestG2PersistBashWritesToEveryProtectedPathBlocked(t *testing.T) {
	p := g2NewP(t)
	tmpls := []string{"echo x > {p}", "echo x >> {p}", "echo x | tee {p}", "cp /tmp/a {p}", "mv /tmp/a {p}",
		"ln -sf /tmp/a {p}", "install -m644 /tmp/a {p}", "sed -i s/a/b/ {p}", "dd if=/tmp/a of={p}", "bash -c 'echo x > {p}'"}
	for _, role := range g2PRoles { // every spelling and tool for the coder; qa and reviewer share the same code path
		rels := g2PProtected[:1]
		if role == "coder" {
			rels = g2PProtected
		}
		for _, rel := range rels {
			for _, spell := range []string{"~/" + rel, "$HOME/" + rel, p.pw + "/" + rel, p.pw + "/x/../" + rel} {
				for _, tm := range tmpls {
					p.blocked(strings.ReplaceAll(tm, "{p}", spell), role, "")
				}
			}
		}
	}
	for _, cmd := range []string{"cp /tmp/a /etc/gitconfig", "echo x > /etc/gitconfig", "install /tmp/a /etc/gitconfig",
		"cp /tmp/a ~/.config/git/", "cp -t ~/.config/git /tmp/a", "mv ~/.bashrc /tmp/gone",
		"ln /tmp/a ~/.bashrc", "ln ~/.bashrc /tmp/hard", "echo x > ~/.config/git/sub/new"} {
		p.blocked(cmd, "coder", "")
	}
}

func TestG2PersistRelativeAndCdSpellingsBlocked(t *testing.T) {
	p := g2NewP(t)
	p.blocked("cd ~ && echo x > .bashrc", "coder", "")
	p.blocked("cd && echo x >> .zshrc", "coder", "")
	p.blocked("cd ~/.config/git && echo x > config", "coder", "")
	p.blocked("echo x > .bashrc", "coder", p.pw)
	p.blocked("echo x > ../.zshrc", "coder", filepath.Join(p.pw, "factory"))
	p.blocked("cd $SOMEWHERE && echo x > .bashrc", "coder", "") // unknown cwd: the name alone decides
	p.blocked("echo x > ~/.bash*", "coder", "")
	p.blocked("echo x > ~/.{bashrc,motd}", "coder", "")
	p.blocked("echo x > $X/.bashrc", "coder", "")
}

func TestG2PersistSelfReviewBypassesBlocked(t *testing.T) {
	p := g2NewP(t)
	for _, cmd := range []string{"cp /tmp/.bashrc ~/", "cp /tmp/.bashrc ~", "cp -t ~ /tmp/.gitconfig", "mv /tmp/.bashrc ~",
		"cp /tmp/config ~/.config/git/", "f=~/.bashrc; echo x > $f", "f=.bashrc; echo x > ~/$f",
		"export F=.bashrc; echo x > ~/$F", "echo x > ~/.$(echo bashrc)", "echo x > $(echo ~)/.bashrc",
		"echo x > ~/.bash$'r'c", "git -c alias.c='!git config --global' c a b",
		"echo x > ~/.config/g*/config", "echo x > ~/.config/*/config", "ln -sfn /tmp/e ~/.config"} {
		p.blocked(cmd, "coder", "")
	}
	for _, cmd := range []string{"cp /tmp/a.txt ~/a.txt", "f=/tmp/o; echo x > $f", "echo x > $(mktemp -d)/a", "echo x > $OUT/a",
		"cp .bashrc.sample /tmp/"} {
		p.allowed(cmd, "coder", "")
	}
}

func TestG2PersistSymlinkSpellingBlocked(t *testing.T) {
	p := g2NewP(t)
	tmp := g2RealTemp(t)
	for _, rel := range []string{".bashrc", ".gitconfig", ".config/git"} {
		link := filepath.Join(tmp, "l"+strings.ReplaceAll(strings.ReplaceAll(rel, "/", "_"), ".", ""))
		if err := os.Symlink(filepath.Join(p.pw, rel), link); err != nil {
			t.Fatal(err)
		}
		p.blocked("echo x > "+link, "coder", "")
		if rel == ".config/git" {
			p.blocked("echo x > "+link+"/config", "coder", "")
		} else {
			p.blocked("cp /tmp/a "+link, "coder", "")
		}
	}
}

func TestG2PersistInterpreterAndEditorNamingProtectedFileBlocked(t *testing.T) {
	p := g2NewP(t)
	for _, cmd := range []string{"python3 -c \"open('/x/.bashrc','a')\"", "python3 -c \"open('~/.gi'+'tconfig','w')\"",
		"vim ~/.zshrc", "perl -pi -e 's/a/b/' ~/.profile", "python3 - <<'EOF'\nopen('/h/.bashrc','a').write('x')\nEOF",
		"awk 'BEGIN{print 1 > \"/h/.zshenv\"}'"} {
		p.blocked(cmd, "coder", "")
	}
}

func TestG2PersistReadOnlyAndRepoLocalFormsAllowed(t *testing.T) {
	p := g2NewP(t)
	for _, role := range g2PRoles {
		for _, cmd := range []string{"git config --global --get user.name", "git config --global user.name",
			"git config --global --list", "git config --global -l", "git config --global --get-all a.b",
			"git config --global --get-regexp user", "git config --global get user.name",
			"git config --global list", "git config --global --type bool --get a.b",
			"git config --system --list", "git config -f ~/.gitconfig --get user.name",
			"git config --file ~/.gitconfig --list", "git config --get user.name",
			"git config user.name", "git config --local --get a.b",
			"git -c alias.c=config c --get a.b",
			"cat ~/.bashrc", "grep PATH ~/.zshrc", "cat ~/.gitconfig ~/.config/git/config",
			"cp ~/.bashrc /tmp/bashrc.copy", "cp ~/.config/git/config /tmp/c", "ls ~/.config/git",
			"echo x > .bashrc.sample", "echo x > notes.profile", "echo x > ~/.bashrc.d.txt",
			"echo x > /tmp/gitconfig", "echo x > ~/.config/gitx", "echo x > ~/.config/gitkraken/c",
			"sed -i s/a/b/ src/.profile.in", "echo x >> out.txt", "tee notes.txt"} {
			if g2PNoPush[role] && strings.HasPrefix(cmd, "git") && (strings.HasPrefix(cmd, "git -c") || !g2PCfgRead.MatchString(cmd)) {
				continue // T-0755: read-only roles run only the --get/--list forms of git config (allow-list)
			}
			p.allowed(cmd, role, "")
		}
	}
}

func TestG2PersistRepoLocalConfigInOwnWorktreeAllowed(t *testing.T) {
	p := g2NewP(t)
	tmp := g2RealTemp(t)
	g2Git(t, tmp, "init", "-q", tmp)
	for _, cmd := range []string{"echo x > .gitconfig", "echo x > .bashrc", "git config --get user.name",
		"git config user.name", "git config --list"} {
		p.allowed(cmd, "coder", tmp)
	}
}

func TestG2PersistGitConfigWriteWithEnvAssignmentBlocked(t *testing.T) {
	p := g2NewP(t)
	for _, role := range g2PRoles {
		for _, cmd := range []string{"GIT_CONFIG=~/.gitconfig git config core.fsmonitor x",
			"export GIT_CONFIG=~/.gitconfig; git config core.fsmonitor x",
			"GIT_CONFIG=$HOME/.config/git/config git config a.b c",
			"env GIT_CONFIG_GLOBAL=~/.gitconfig git config a.b c",
			"declare -x GIT_CONFIG=~/.gitconfig; git config a.b c",
			"HOME=/tmp git config --file .gitconfig a.b c",
			"export XDG_CONFIG_HOME=/tmp/x && git config -f x/git/config a.b c"} {
			p.blocked(cmd, role, "")
		}
	}
}

func TestG2PersistRepoLocalConfigAndReadsStayAllowed(t *testing.T) {
	p := g2NewP(t)
	for _, cmd := range []string{"git config --get a.b", "git -C . config --list", "git config --get user.name",
		"awk 1 ~/.zshrc", "awk '{print $1}' ~/.gitconfig", "echo HOME=1; git status"} {
		p.allowed(cmd, "coder", "")
	}
	p.blocked("awk -i inplace 1 ~/.zshrc", "coder", "")
}

func TestG2PersistHQModeUnchanged(t *testing.T) {
	// The Python test also compares its in-process hook with the real script; here every call is the real binary.
	p := g2NewP(t)
	for _, cmd := range []string{"git config --global user.name x", "echo x > ~/.bashrc", "cp /tmp/a ~/.gitconfig"} {
		g2WantExit(t, cmd, p.bashIn(cmd, "", true, ""), 0)
	}
	r := p.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": p.pw + "/.bashrc"}}, "", true)
	g2WantExit(t, "hq Write ~/.bashrc", r, 0)
	g2WantExit(t, "coder blocked", p.bashIn("echo x > ~/.bashrc", "coder", false, ""), 2)
	// the matrix the Python test compares in-process vs. subprocess: same verdicts through the binary
	for _, c := range []struct {
		pl   map[string]any
		role string
		code int
	}{
		{map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git config --global user.name x"}}, "qa", 2},
		{map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls -la"}}, "reviewer", 0},
		{map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": p.pw + "/.bashrc"}}, "coder", 2},
		{map[string]any{"tool_name": "Write", "tool_input": map[string]any{}}, "coder", 2},
	} {
		g2WantExit(t, fmt.Sprint(c.pl), p.hook(c.pl, c.role, false), c.code)
	}
}

// ---- PersistEditToolTests ----

func (p *g2PEnv) edit(tool, key, path, role, cwd string) g2Res {
	p.t.Helper()
	if cwd == "" {
		cwd = "/tmp"
	}
	return p.hook(map[string]any{"tool_name": tool, "cwd": cwd, "tool_input": map[string]any{key: path}}, role, false)
}

func TestG2PersistEditWritePayloadsToProtectedPathsBlocked(t *testing.T) {
	p := g2NewP(t)
	keys := map[string]string{"Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path"}
	for _, role := range g2PRoles {
		for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit"} {
			rels := g2PProtected[:3]
			if tool == "Write" {
				rels = g2PProtected
			}
			for _, rel := range rels {
				for _, spell := range []string{p.pw + "/" + rel, "~/" + rel, "$HOME/" + rel, p.pw + "/x/../" + rel} {
					r := p.edit(tool, keys[tool], spell, role, "")
					g2WantExit(t, role+" "+tool+" "+spell, r, 2)
				}
			}
		}
		g2WantExit(t, role+" /etc/gitconfig", p.edit("Write", "file_path", "/etc/gitconfig", role, ""), 2)
	}
	g2WantExit(t, "Edit .bashrc cwd=home", p.edit("Edit", "file_path", ".bashrc", "coder", p.pw), 2)
	g2WantExit(t, "Write ../.zshrc", p.edit("Write", "file_path", "../.zshrc", "coder", filepath.Join(p.pw, "factory")), 2)
}

func TestG2PersistSymlinkPayloadBlocked(t *testing.T) {
	p := g2NewP(t)
	link := filepath.Join(g2RealTemp(t), "rc")
	if err := os.Symlink(filepath.Join(p.pw, ".zshrc"), link); err != nil {
		t.Fatal(err)
	}
	g2WantExit(t, "Write via symlink", p.edit("Write", "file_path", link, "coder", ""), 2)
}

func TestG2PersistOtherPathsAllowedAndUnreadablePayloadFailsClosed(t *testing.T) {
	p := g2NewP(t)
	for _, path := range []string{"/tmp/x.txt", p.pw + "/factory/README.md", p.pw + "/.config/gitx", p.pw + "/.bashrc.bak", "notes.md"} {
		g2WantExit(t, path, p.edit("Write", "file_path", path, "coder", ""), 0)
	}
	g2WantExit(t, "Write {}", p.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{}}, "coder", false), 2)
	g2WantExit(t, "Edit 7", p.hook(map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": 7}}, "coder", false), 2)
	g2WantExit(t, "Read .bashrc", p.hook(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p.pw + "/.bashrc"}}, "coder", false), 0)
}

func TestG2PersistWorkerSettingsRouteEditToolsToTheHookAndHQSettingsDoNot(t *testing.T) {
	// The Python test renders the settings with bin/factory; the templates are plain JSON (placeholders are inside
	// strings), so the same structural facts are read from roles/settings/<role>.json.in.
	groups := func(role string) []map[string]any {
		b, err := os.ReadFile(filepath.Join(g2RepoRoot(t), "roles", "settings", role+".json.in"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Hooks struct {
				PreToolUse []map[string]any `json:"PreToolUse"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		return doc.Hooks.PreToolUse
	}
	for _, role := range g2PWorkers {
		var edit []map[string]any
		for _, g := range groups(role) {
			m, _ := g["matcher"].(string)
			parts := strings.Split(m, "|")
			sort.Strings(parts)
			if strings.Join(parts, "|") == strings.Join(g2PTools, "|") {
				edit = append(edit, g)
			}
		}
		if len(edit) != 1 {
			t.Errorf("%s: %d edit groups, want 1", role, len(edit))
			continue
		}
		hooks, _ := edit[0]["hooks"].([]any)
		h, _ := hooks[0].(map[string]any)
		cmd, _ := h["command"].(string)
		// T-0356 (M22079): worker templates call the sh shim (no .py suffix)
		if !strings.HasSuffix(cmd, "/roles/hooks/worker-guard --role "+role) {
			t.Errorf("%s: command %q", role, cmd)
		}
	}
	for _, role := range []string{"boss", "director", "lead"} {
		for _, g := range groups(role) {
			if m, _ := g["matcher"].(string); strings.Contains(m, "Edit") {
				t.Errorf("%s: has an Edit group %q", role, m)
			}
		}
	}
}

// ---- T-0957: ~/.local/bin ----

func g2LbBin(p *g2PEnv) string { return p.pw + "/.local/bin" }

func g2LbBlocked(p *g2PEnv) []string {
	b := g2LbBin(p)
	return []string{
		"echo x > ~/.local/bin/git", "echo x >> ~/.local/bin/go", "echo x > ~/.local/bin/factory",
		"echo x > $HOME/.local/bin/nice", "cp /tmp/x ~/.local/bin/git", "mv /tmp/x ~/.local/bin/git",
		"ln -s /tmp/x ~/.local/bin/git", "ln -sf /tmp/x ~/.local/bin/factory", "rm ~/.local/bin/factory",
		"rm -r ~/.local/bin", "tee ~/.local/bin/git", "cd ~/.local/bin && echo x > git",
		"cd ~/.local/bin && cp /tmp/x git", "echo x > " + b + "/git", "cp /tmp/x " + b + "/git",
		"echo x > " + b + "/../bin/git", "sed -i s/a/b/ ~/.local/bin/git",
		"python3 -c \"open('/h/.local/bin/git','w')\"",
	}
}

func g2LbAllowed(p *g2PEnv) []string {
	return []string{
		"ls ~/.local/bin", "ls -la ~/.local/bin/factory", "cp ~/.local/bin/x /tmp/x", "~/.local/bin/factory whoami",
		"grep -rn .local/bin docs", "echo x > /usr/local/bin/foo", "echo x > ~/.local/binx/foo",
		"echo x > " + p.pw + "/.local/binx/foo", "git commit -m 'note: ~/.local/bin is on PATH'",
		"factory msg lead 'subj' 'see ~/.local/bin/git'", "export PATH=~/.local/bin:$PATH",
		"cp /tmp/x ~/.local/share/foo",
	}
}

func TestG2PersistLocalBinWritesAreBlocked(t *testing.T) {
	p := g2NewP(t)
	for _, cmd := range g2LbBlocked(p) {
		r := p.bashIn(cmd, "coder", false, g2RepoRoot(t))
		g2WantExit(t, cmd, r, 2)
	}
}

func TestG2PersistLocalBinBlockMessageNamesLocalBin(t *testing.T) {
	p := g2NewP(t)
	for _, cmd := range []string{"echo x > ~/.local/bin/git", "cp /tmp/x ~/.local/bin/git", "rm ~/.local/bin/factory"} {
		if r := p.bashIn(cmd, "coder", false, g2RepoRoot(t)); !strings.Contains(r.stderr, "~/.local/bin") {
			t.Errorf("%s: stderr lacks ~/.local/bin: %q", cmd, r.stderr)
		}
	}
}

func TestG2PersistLocalBinReadsRunsLookAlikesAndTextAreAllowed(t *testing.T) {
	p := g2NewP(t)
	for _, cmd := range g2LbAllowed(p) {
		g2WantExit(t, cmd, p.bashIn(cmd, "coder", false, g2RepoRoot(t)), 0)
	}
}

func TestG2PersistLocalBinBlockedForEveryWorkerRole(t *testing.T) {
	p := g2NewP(t)
	for _, role := range []string{"coder", "qa", "reviewer", "pm"} {
		g2WantExit(t, role, p.bashIn("echo x > ~/.local/bin/git", role, false, g2RepoRoot(t)), 2)
	}
}

func (p *g2PEnv) lbEdit(tool, path, cwd string) g2Res {
	p.t.Helper()
	key := "file_path"
	if tool == "NotebookEdit" {
		key = "notebook_path"
	}
	return p.hook(map[string]any{"tool_name": tool, "tool_input": map[string]any{key: path}, "cwd": cwd}, "coder", false)
}

func TestG2PersistLocalBinEditToolsIntoLocalBinAreBlocked(t *testing.T) {
	p := g2NewP(t)
	root, b := g2RepoRoot(t), g2LbBin(p)
	for _, tool := range g2PTools {
		for _, c := range []struct{ path, cwd string }{{"~/.local/bin/git", root}, {"$HOME/.local/bin/git", root},
			{b + "/git", root}, {"git", b}, {b + "/factory", root}, {b + "/../bin/git", root}, {"/tmp/$X/.local/bin/git", root}} {
			r := p.lbEdit(tool, c.path, c.cwd)
			g2WantExit(t, tool+" "+c.path, r, 2)
			if !strings.Contains(r.stderr, "~/.local/bin") {
				t.Errorf("%s %s: stderr lacks ~/.local/bin: %q", tool, c.path, r.stderr)
			}
		}
	}
}

func TestG2PersistLocalBinSymlinkEntryWithRealpathOutsideTreeIsBlocked(t *testing.T) {
	// A fixture symlink cannot sit inside the real ~/.local/bin (never written); the spelled path of the live entry
	// is decided on its text, so the check needs no file there.
	p := g2NewP(t)
	g2WantExit(t, "Write live entry", p.lbEdit("Write", g2LbBin(p)+"/factory", g2RepoRoot(t)), 2)
}

func TestG2PersistLocalBinOtherPathsAreAllowed(t *testing.T) {
	p := g2NewP(t)
	root := g2RepoRoot(t)
	for _, tool := range g2PTools {
		for _, path := range []string{"~/.local/binx/foo", p.pw + "/.local/binx/foo", filepath.Join(root, "notes.md")} {
			g2WantExit(t, tool+" "+path, p.lbEdit(tool, path, root), 0)
		}
	}
	g2WantExit(t, "Write /usr/local/bin/foo", p.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "/usr/local/bin/foo"}}, "coder", false), 0)
}

// ---- from go/internal/factorytest/guard2/push_pin_test.go ----
// Port of tests/test_worker_guard_push_pin.py (T-0689, T-0719, T-0743, T-0755, T-0893): a coder may only
// `git push origin HEAD:factory/<own task id>`; the no-push roles never push and may run only read-only git.

const g2PpTask = "t-0689"
const g2PpOwn = "HEAD:factory/" + g2PpTask

type g2PpEnv struct {
	t     *testing.T
	tmp   string
	home  string
	wt    string
	task  string // lower-case task id
	agent string // default FACTORY_AGENT
}

// newPP is setUp: a factory home with a DB row, a real clone under worktrees/ with a local bare origin.
func g2NewPP(t *testing.T, task, group, agent string) *g2PpEnv {
	t.Helper()
	tmp := g2RealTemp(t)
	e := &g2PpEnv{t: t, tmp: tmp, home: filepath.Join(tmp, "factory"), task: task, agent: agent}
	e.wt = filepath.Join(e.home, "worktrees", group+"-"+strings.ToUpper(task))
	g2Mkdirs(t, filepath.Join(e.home, ".git"), filepath.Join(e.home, "roles"))
	e.writeDB(e.wt, strings.ToUpper(task))
	origin := filepath.Join(tmp, "origin.git")
	g2Git(t, tmp, "init", "-q", "--bare", origin)
	g2Git(t, tmp, "init", "-q", "-b", "main", e.wt)
	g2Git(t, e.wt, "commit", "-q", "--allow-empty", "-m", "i")
	g2Git(t, e.wt, "remote", "add", "origin", origin)
	g2Git(t, e.wt, "push", "-q", "origin", "main")
	g2Git(t, e.wt, "fetch", "-q", "origin")
	g2Git(t, e.wt, "checkout", "-q", "-b", "factory/"+task)
	return e
}

// writeDB replaces the DB (and the guard export built from it) with one tasks row.
func (e *g2PpEnv) writeDB(worktree, task string) {
	e.t.Helper()
	for _, p := range []string{filepath.Join(e.home, "db", "factory.db"), filepath.Join(e.home, "run", "guard-export.json")} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			e.t.Fatal(err)
		}
	}
	g2GuardDB(e.t, e.home, map[string]string{task: worktree})
}

// run: agent "-" means FACTORY_AGENT unset; "" means the default; cwd "" means the worktree.
func (e *g2PpEnv) run(cmd, role, agent, cwd string) g2Res {
	e.t.Helper()
	if role == "" {
		role = "coder"
	}
	if agent == "" {
		agent = e.agent
	}
	if cwd == "" {
		cwd = e.wt
	}
	set := map[string]string{"HOME": e.tmp, "WORKER_GUARD_LIVE_HOME": e.home, "FACTORY_HOME": e.home}
	if agent != "-" {
		set["FACTORY_AGENT"] = agent
	}
	return g2Bash(e.t, cmd, g2Opts{args: g2RoleArgs(role), cwd: cwd, drop: []string{"WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT"}, set: set})
}

// pinned is true when the pin itself refused (its message names the exact allowed push).
func (e *g2PpEnv) pinned(cmd, agent string) bool {
	r := e.run(cmd, "", agent, "")
	if r.code != 2 {
		return false
	}
	for _, w := range []string{"coders push exactly", "git push:", "before push", "environment overrides", "by another name", "whole command"} {
		if strings.Contains(r.stderr, w) {
			return true
		}
	}
	return false
}

func (e *g2PpEnv) mustExit(what string, r g2Res, code int) {
	e.t.Helper()
	g2WantExit(e.t, what, r, code)
}

func TestG2PushPinRefusedShapes(t *testing.T) {
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	own := g2PpOwn
	for _, cmd := range []string{
		"git push", "git push origin", "git push origin HEAD", "git push -u origin HEAD",
		"git push origin " + own + " HEAD:main", "git push origin '" + own + " HEAD:main'",
		"git push origin '" + own + "\tHEAD:main'", "git push origin '" + own + "\nHEAD:main'",
		"git push origin " + own + " HEAD:factory/t-0001",
		"git push origin HEAD:factory/t-0001", "git push origin HEAD:main", "git push origin HEAD:refs/heads/main",
		"git push --force origin " + own, "git push -f origin " + own, "git push --force-with-lease origin " + own,
		"git push origin +" + own, "git push origin " + own + " --force", "git push origin " + own + " -f",
		"git push --all origin", "git push --mirror origin", "git push --tags origin", "git push --tags origin " + own,
		"git push origin --delete factory/" + g2PpTask, "git push --delete origin factory/" + g2PpTask,
		"git push origin :factory/" + g2PpTask,
		"git push other " + own, "git push https://example.com/x.git " + own, "git push git@example.com:x/y.git " + own,
		"git push --repo=https://example.com/x.git " + own,
		"git -c push.default=matching push origin " + own, "git -c remote.origin.pushurl=/x push origin " + own,
		"git --git-dir=/x push origin " + own, "GIT_CONFIG_COUNT=1 git push origin " + own, "GIT_DIR=/x git push origin " + own,
		"GIT_SSH_COMMAND=true git push origin " + own, "GIT_EXEC_PATH=/x git push origin " + own,
		`export GIT_CONFIG_PARAMETERS="'remote.origin.url=/x'"; git push origin ` + own,
		"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=remote.origin.url GIT_CONFIG_VALUE_0=/x; git push origin " + own,
		"export GIT_DIR=/x; git push origin " + own, "export GIT_WORK_TREE=/x; git push origin " + own,
		"GIT_DIR=/x; git push origin " + own,
		"printf -v GIT_DIR /x; export GIT_DIR; git push origin " + own,
		"read GIT_DIR </dev/null; export GIT_DIR; git push origin " + own,
		"for GIT_DIR in /x; do export GIT_DIR; git push origin " + own + "; done",
		"mapfile -t GIT_DIR < /dev/null; export GIT_DIR; git push origin " + own,
		"GIT_DIR+=/x; export GIT_DIR; git push origin " + own,
		"x=1; export x; git push origin " + own, "declare -x y=1; git push origin " + own,
		"git push origin " + own + " # GIT_DIR",
		"git subtree push --prefix=x origin main",
		"git send-pack /x HEAD:main", "git http-push http://x/ HEAD:main",
		"FACTORY_AGENT=coder-t-0001 git push origin HEAD:factory/t-0001",
		"git push -o x origin " + own,
		`set -a; n=GI; printf -v "${n}T_CONFIG_PARAMETERS" '%s' "'remote.origin.pushurl=/x'"; git push origin ` + own,
		"f(){ local -x ${1}IT_CONFIG_PARAMETERS=x; git push origin " + own + "; }; f G",
		"BASH_ENV=/tmp/benv bash -c 'git push origin " + own + "'",
		"HOME=/tmp/h git push origin " + own,
		`set -a; a=GI; b=T_DIR; printf -v "$a$b" /x; git push origin ` + own,
		"true && git push origin " + own, "git push origin " + own + "; true", "git push origin '" + own + "'",
		"git -C $HOME push origin " + own,
	} {
		if !e.pinned(cmd, "") {
			r := e.run(cmd, "", "", "")
			t.Errorf("pin did not refuse %q: exit %d, stderr %q", cmd, r.code, r.stderr)
		}
	}
}

func TestG2PushPinUnknownTaskIDRefuses(t *testing.T) {
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, agent := range []string{"-", "coder", "qa-t-0689", "coder-t-0689x"} {
		if !e.pinned("git push origin "+g2PpOwn, agent) {
			t.Errorf("agent %q: the pin did not refuse", agent)
		}
	}
}

func TestG2PushPinOtherRolesNotPinned(t *testing.T) {
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	r := e.run("git push origin HEAD:factory/t-0001", "qa", "", "")
	if strings.Contains(r.stderr, "coders push exactly") {
		t.Errorf("qa got the coder pin message: %q", r.stderr)
	}
}

func TestG2PushPinAllowedFormIsAllowed(t *testing.T) {
	// The exact push exits 0 (real clone + local bare origin) and the pin names no refusal.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, cmd := range []string{"git push origin " + g2PpOwn, "git -C " + e.wt + " push origin " + g2PpOwn, "git -C . push origin " + g2PpOwn} {
		e.mustExit(cmd, e.run(cmd, "", "", ""), 0)
	}
}

func TestG2PushPinDashCMustBeOwnWorktree(t *testing.T) {
	// T-0719: -C into a scratch clone (another origin) is refused, also via a symlink to it; a symlink to the own one passes.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	scratch := filepath.Join(e.tmp, "scratch")
	g2Git(t, e.tmp, "init", "-q", "-b", "main", scratch)
	link := filepath.Join(e.tmp, "link")
	if err := os.Symlink(scratch, link); err != nil {
		t.Fatal(err)
	}
	for _, tgt := range []string{scratch, link, "..", filepath.Join(e.home, "worktrees", "G-018-T-0001")} {
		cmd := "git -C " + tgt + " push origin " + g2PpOwn
		r := e.run(cmd, "", "", "")
		e.mustExit(cmd, r, 2)
		if !strings.Contains(r.stderr, "own worktree") {
			t.Errorf("%s: stderr lacks 'own worktree': %q", cmd, r.stderr)
		}
	}
	// cwd in a scratch clone: even `-C .` is refused; cwd in a worktree subdir: -C <worktree root> passes
	for _, tgt := range []string{".", scratch} {
		cmd := "git -C " + tgt + " push origin " + g2PpOwn
		e.mustExit(cmd+" (cwd scratch)", e.run(cmd, "", "", scratch), 2)
	}
	sub := filepath.Join(e.wt, "sub")
	g2Mkdirs(t, sub)
	e.mustExit("-C worktree from subdir", e.run("git -C "+e.wt+" push origin "+g2PpOwn, "", "", sub), 0)
	linkOwn := filepath.Join(e.tmp, "ownlink")
	if err := os.Symlink(e.wt, linkOwn); err != nil {
		t.Fatal(err)
	}
	e.mustExit("-C symlink to own", e.run("git -C "+linkOwn+" push origin "+g2PpOwn, "", "", ""), 0)
}

func TestG2PushPinPaneInAnotherWorktreeRefused(t *testing.T) {
	// T-0743: a pane cd'd into another worktree cannot push, with or without -C, even naming its own worktree.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	other := filepath.Join(e.home, "worktrees", "G-018-T-0001")
	g2Git(t, e.tmp, "init", "-q", "-b", "main", other)
	for _, cmd := range []string{"git push origin " + g2PpOwn, "git -C . push origin " + g2PpOwn, "git -C " + other + " push origin " + g2PpOwn,
		"git -C " + e.wt + " push origin " + g2PpOwn} {
		r := e.run(cmd, "", "", other)
		e.mustExit(cmd, r, 2)
		if !strings.Contains(r.stderr, "own worktree") {
			t.Errorf("%s: stderr lacks 'own worktree': %q", cmd, r.stderr)
		}
	}
}

func TestG2PushPinUnresolvableWorktreeRefuses(t *testing.T) {
	// T-0743: no DB, no task row or an empty worktree column refuses (fail closed).
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	push := "git push origin " + g2PpOwn
	if err := os.Remove(filepath.Join(e.home, "db", "factory.db")); err != nil {
		t.Fatal(err)
	}
	r := e.run(push, "", "", "")
	e.mustExit("no DB", r, 2)
	if !strings.Contains(r.stderr, "cannot resolve the worktree") {
		t.Errorf("no DB: stderr lacks 'cannot resolve the worktree': %q", r.stderr)
	}
	e.writeDB("", "T-0689")
	e.mustExit("empty worktree column", e.run(push, "", "", ""), 2)
	e.writeDB(e.wt, "T-0001") // a DB without this task's row
	e.mustExit("no row", e.run(push, "", "", ""), 2)
}

func TestG2PushPinDBWorktreeNotCwdDecides(t *testing.T) {
	// T-0743: the DB path is the pin; a worktree-looking cwd that is not the recorded one is refused, the recorded one passes.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	other := filepath.Join(e.home, "worktrees", "G-018-T-0002")
	g2Git(t, e.tmp, "init", "-q", "-b", "main", other)
	e.writeDB(other, "T-0689")
	push := "git push origin " + g2PpOwn
	e.mustExit("cwd wt, DB other", e.run(push, "", "", ""), 2)
	e.mustExit("cwd other, DB other", e.run(push, "", "", other), 0)
}

var g2PpNoPushRoles = []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"}
var g2PpRoles = append(append([]string{}, g2PpNoPushRoles...), "researcher")

func TestG2PushPinNonCoderRolesNeverPush(t *testing.T) {
	// T-0755: every push shape is refused, even --dry-run.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	shapes := []string{
		"git push", "git push origin", "git push origin " + g2PpOwn, "git push origin HEAD:main", "git push origin +HEAD:main",
		"git push --dry-run origin HEAD:main", "git push -n origin main", "git push --all origin", "git push --mirror origin",
		"git push --tags origin", "git push --force origin main", "git -C " + e.wt + " push origin " + g2PpOwn,
		"git -c remote.origin.url=https://example.com/x.git push origin main", "git -c push.default=current push",
		"git push https://example.com/x.git main", "git send-pack origin main", "git subtree push --prefix=a origin main",
		"git -c alias.p=push p origin main",
	}
	for _, role := range g2PpNoPushRoles {
		for _, cmd := range shapes {
			r := e.run(cmd, role, role+"-t-0689", "")
			e.mustExit(role+": "+cmd, r, 2)
			if !strings.Contains(r.stderr, "may not run git push") && !strings.Contains(r.stderr, "alias") {
				t.Errorf("%s: %s: stderr names neither push nor alias: %q", role, cmd, r.stderr)
			}
		}
	}
}

func TestG2PushPinNonCoderGitOptionAndEnvBypassesRefused(t *testing.T) {
	// T-0755 retry: value-taking git global options, unknown options and env/--config-env aliases hide no push.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	shapes := []string{
		"git --attr-source HEAD push origin main", "git --attr-source HEAD push --dry-run origin main",
		"git --attr-source=HEAD push origin main", "git --namespace ns push origin main",
		"git --git-dir /x/.git push origin main", "git --work-tree /x push origin main",
		"git --super-prefix p/ push origin main", "git --no-pager push origin main", "git -p push origin main",
		"git --future-option push origin main", "git --future-option value push origin main",
		"git --config-env=alias.p=PUSHVAR p origin main", "git --config-env alias.p=PUSHVAR p origin main",
		"git -c alias.s=send-pack s origin main", "git -c alias.s=subtree s push origin",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=push git p origin main",
		`GIT_CONFIG_PARAMETERS="'alias.p=push'" git p origin main`,
		"env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=push git p origin main",
		"export GIT_CONFIG_COUNT=1; git p origin main", "export GIT_CONFIG_KEY_0=alias.p",
		"GIT_CONFIG_VALUE_0=push git status", "GIT_CONFIG_PARAMETERS=x",
	}
	for _, role := range g2PpNoPushRoles {
		for _, cmd := range shapes {
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
		}
	}
}

func TestG2PushPinNonCoderGitOptionFormsStillAllowed(t *testing.T) {
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, cmd := range []string{"git --no-pager log -1", "git -P log -1", "git --no-optional-locks status", "git --version"} {
		e.mustExit(cmd, e.run(cmd, "reviewer", "reviewer-t-0689", ""), 0)
	}
}

func TestG2PushPinNonCoderNonPushGitAllowed(t *testing.T) {
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, cmd := range []string{"git status", "git log -1", "git -C " + e.wt + " diff", "git fetch origin"} {
		e.mustExit(cmd, e.run(cmd, "qa", "qa-t-0689", ""), 0)
	}
}

// T-0755 attempt 3: the allow-list. Each of these exited 0 for a read-only role at f792b8f93 (the push deny-list).
var g2PpAllowlistRefused = []string{
	"p=push; git $p origin main", "p=push; git ${p} x", `p=push; git "$p" origin main`, "git `echo push` origin main",
	"p=push; git $p --dry-run origin main",
	"GIT_CONFIG_GLOBAL=f git p origin main", "GIT_CONFIG_SYSTEM=f git p origin main", "GIT_CONFIG=f git p origin main",
	"HOME=d git p", "env HOME=d git p", "export HOME=d; git p", "export GIT_CONFIG_GLOBAL=f; git p",
	"unset HOME; git p", "HOME=d; git p", "PATH=/x:$PATH git status",
	"git -c include.path=f p", "git -c includeIf.x.path=f p", "git -c core.pager=cat log", "git p", "git p origin main",
	"git fetch --upload-pack='x' .", "git fetch --upload-pack=x origin", "git fetch -u x origin",
	"git fetch 'ext::sh -c x'", "git fetch origin 'ext::sh -c x'", "git fetch https://example.com/x.git",
	"git fetch /tmp/other", "git ls-remote --upload-pack=x origin", "git ls-remote git@example.com:x/y.git",
	"git fetch --server-option=x origin", "git fetch $r", "git ls-remote $r",
	"o=--output=y; git diff $o", "o=--ext-diff; git log -1 $o", "git log -1 --format=x $(echo --output=y)",
	"git fetch origin main:refs/heads/x", "git fetch origin +main:main",
	"git grep -O x", "git grep -nO x", "git grep --open-files-in-pager=x y", "git grep --open-f=x y", "git grep $pat",
	"git log --ext-diff", "git diff --ext-diff", "git show --output=f", "git log --output f", "git diff --outp=f",
	"git -p log", "git --paginate log",
	"echo '[alias] p=push' >> .git/config", "tee -a .git/config < /dev/null", "cp /dev/null .git/config",
	"sed -i s/a/b/ .git/config", "echo x > .git/hooks/pre-push", "echo x > .git/info/attributes",
	"echo '[alias]' >> ~/.gitconfig", "echo x > ~/.config/git/config",
	"git config --file .git/config alias.p push",
	"printf push | xargs git", "echo push | xargs -n1 git", "find . -maxdepth 0 -exec git push \\;",
	"find . -maxdepth 0 -execdir git p \\;", "find . -ok git p \\;",
	"git-push origin main", "/usr/lib/git-core/git-push", "/usr/lib/git-core/git push origin main", "./git push",
	"$G push", "\\git p", "git rebase --exec 'git push' HEAD~1", "git rebase -x p HEAD~1", "git bisect run p",
	"git submodule foreach git push", "git filter-branch -f", "git difftool", "git archive --remote=x HEAD",
	"git push origin main", "git stash", "git commit -m x", "git checkout -b x", "git reset --hard", "git add .",
	"git branch -D x", "git branch new", "git remote add x y", "git remote set-url origin y", "git worktree add ../x",
	"git config alias.p push", "git config --unset alias.p", "git config --list --edit", "git config --global alias.p push",
	"git send-pack /x HEAD:main", "git subtree push --prefix=a origin main", "git daemon", "git var GIT_PAGER",
	"git remote update",
}

var g2PpAllowlistAllowed = []string{
	"git log --oneline -5", "git -C {wt} diff origin/main...HEAD", "git show origin/x:f", "git fetch origin",
	"git fetch origin main", "git status", "git branch -a", "git config --get user.name", "git blame f",
	"git grep -n x", "git merge-tree --write-tree origin/main HEAD", "git rev-parse HEAD", "git ls-remote origin",
	"git fetch origin refs/heads/main:refs/remotes/origin/main", "git fetch --prune origin", "git ls-remote --heads origin",
	"git diff --stat HEAD~1", "git log --no-ext-diff -1", "git remote -v", "git remote get-url origin", "git remote", "git remote show origin",
	"git worktree list", "git config --list", "git config --get-regexp remote", "git branch --list 'f*'",
	"git cat-file -p HEAD", "git show-ref", "git describe --tags --always", "git --version",
}

var g2PpRemoteWrites = []string{
	"set-url origin https://example.com/evil.git", "add x https://example.com/evil.git", "rename origin y",
	"remove origin", "rm origin", "set-head origin -a", "set-branches origin main", "prune origin", "update",
	"set-url --push origin https://example.com/evil.git", "bogus", "show", "get-url", "show origin extra",
	"get-url origin extra",
}

var g2PpRemoteReads = []string{
	"", "-v", "--verbose", "get-url origin", "get-url --push origin", "get-url --all origin", "show origin",
	"show -n origin", "-v show origin", "--verbose show -n origin",
}

func TestG2PushPinNonCoderGitRemoteWriteFormsRefused(t *testing.T) {
	// T-0755 attempt 4: every word after `remote` is checked; -v in front must not hide a write form.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, role := range g2PpRoles {
		for _, form := range g2PpRemoteWrites {
			for _, pre := range []string{"", "-v ", "--verbose "} {
				cmd := "git remote " + pre + form
				e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
			}
		}
	}
}

func TestG2PushPinNonCoderGitRemoteReadFormsAllowed(t *testing.T) {
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, role := range g2PpRoles {
		for _, form := range g2PpRemoteReads {
			cmd := strings.TrimSpace("git remote " + form)
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 0)
		}
	}
}

func TestG2PushPinNonCoderGitAllowListRefuses(t *testing.T) {
	// T-0755 acceptance 1: fail-closed allow-list (dynamic subcommand, config-file aliases, fetch helpers, pager,
	// config writes, xargs/find launchers, git-* helpers, unknown builtins).
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, role := range g2PpRoles {
		for _, cmd := range g2PpAllowlistRefused {
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
		}
	}
}

func TestG2PushPinNonCoderGitAllowListAllows(t *testing.T) {
	// T-0755 acceptance 2: the read-only forms stay allowed.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, role := range g2PpRoles {
		for _, cmd := range g2PpAllowlistAllowed {
			e.mustExit(role+": "+cmd, e.run(strings.ReplaceAll(cmd, "{wt}", e.wt), role, role+"-t-0689", ""), 0)
		}
	}
}

func TestG2PushPinNonCoderScriptFilesAreInspectedOrRefused(t *testing.T) {
	// T-0755 point 7: a script file run by a no-push role is read as sh (its body obeys the allow-list); one for
	// another interpreter is refused.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	script := func(name, body string) string {
		p := filepath.Join(e.tmp, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	push := script("push.sh", "git push origin main\n")
	py := script("py.py", "#!/usr/bin/env python3\nprint(1)\n")
	ok := script("ok.sh", "git status\n")
	for _, role := range g2PpRoles {
		for _, cmd := range []string{push, "bash " + push, "source " + push, py} {
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
		}
		e.mustExit(role+": "+ok, e.run(ok, role, role+"-t-0689", ""), 0)
	}
}

func TestG2PushPinCoderCleanerJudgeNotUnderAllowList(t *testing.T) {
	// The allow-list is for the no-push roles only: coder, cleaner and judge never get its message.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	for _, role := range []string{"coder", "cleaner", "judge"} {
		r := e.run("git p origin main", role, role+"-t-0689", "")
		if strings.Contains(r.stderr, "read-only allow-list") {
			t.Errorf("%s got the allow-list message: %q", role, r.stderr)
		}
	}
}

func TestG2PushPinJudgeHQAndCleanerUnchanged(t *testing.T) {
	// Judge and HQ are not in the no-push set (no refusal from this rule); the cleaner still gets its own messages.
	e := g2NewPP(t, g2PpTask, "G-018", "coder-"+g2PpTask)
	p := e.run("git push origin HEAD:main", "judge", "judge-1", "")
	if strings.Contains(p.stderr, "may not run git push") {
		t.Errorf("judge: %q", p.stderr)
	}
	h := g2Bash(t, "git push origin HEAD:main", g2Opts{args: []string{"--hq"}, cwd: e.wt,
		set: map[string]string{"HOME": e.tmp, "WORKER_GUARD_LIVE_HOME": e.home, "FACTORY_HOME": e.home, "FACTORY_AGENT": "lead-g-1"}})
	if strings.Contains(h.stderr, "may not run git push") {
		t.Errorf("hq: %q", h.stderr)
	}
	c := e.run("git push origin HEAD:main", "cleaner", "cleaner-1", "")
	e.mustExit("cleaner push main", c, 2)
	if !strings.Contains(c.stderr, "cleaner may only delete") {
		t.Errorf("cleaner stderr lacks 'cleaner may only delete': %q", c.stderr)
	}
	c = e.run("git push origin --delete factory/t-0001", "cleaner", "cleaner-1", "")
	if strings.Contains(c.stderr, "may not run git push") {
		t.Errorf("cleaner delete: %q", c.stderr)
	}
}

// T-0893: FACTORY_AGENT may end with -aN; the push ref still uses the task id before that suffix.
const g2ApTask = "t-0817"
const g2ApDefaultAgent = "cursor-coder-t-0817-a2"

func TestG2PushPinAttemptSuffixAllowed(t *testing.T) {
	e := g2NewPP(t, g2ApTask, "G-027", g2ApDefaultAgent)
	push := "git push origin HEAD:factory/t-0817"
	for _, agent := range []string{"cursor-coder-t-0817-a2", "cursor-coder-t-0817", "coder-t-0817-a2",
		"claude-coder-t-0817-a2", "cursor-coder-t-0817-a10"} {
		e.mustExit(agent, e.run(push, "", agent, ""), 0)
	}
	e.mustExit("-C own", e.run("git -C "+e.wt+" push origin HEAD:factory/t-0817", "", "cursor-coder-t-0817-a2", ""), 0)
	d, err := sql.Open("sqlite", filepath.Join(e.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var id, wt string
	if err := d.QueryRow("SELECT id, worktree FROM tasks WHERE id = ?", "T-0817").Scan(&id, &wt); err != nil {
		t.Fatalf("task row: %v", err)
	}
	if id != "T-0817" || wt != e.wt {
		t.Errorf("row = (%q, %q), want (T-0817, %q)", id, wt, e.wt)
	}
}

func TestG2PushPinAttemptSuffixRefused(t *testing.T) {
	e := g2NewPP(t, g2ApTask, "G-027", g2ApDefaultAgent)
	push := "git push origin HEAD:factory/t-0817"
	for _, agent := range []string{"cursor-coder-t-0817-b2", "cursor-coder-t-0817-a", "cursor-coder-t-0817-a2x",
		"cursor-coder-t-0817-a2-a3", "cursor-coder-t-0817-A2", "cursor-coder-t-0817-attempt2",
		"evil-coder-t-0817-a2", "cursor-claude-coder-t-0817-a2",
		"cursor-coder-t-a2", "coder-t-a2", "claude-coder-t-a9", "cursor-coder-t--a2"} {
		r := e.run(push, "", agent, "")
		e.mustExit(agent, r, 2)
		if !strings.Contains(r.stderr, "cannot tell this pane's task id") {
			t.Errorf("%s: stderr lacks the task-id message: %q", agent, r.stderr)
		}
	}
}

func TestG2PushPinAttemptSuffixPushUnchanged(t *testing.T) {
	e := g2NewPP(t, g2ApTask, "G-027", g2ApDefaultAgent)
	for _, cmd := range []string{"git push other HEAD:factory/t-0817", "git push origin HEAD:factory/t-0817-a2",
		"git push --force origin HEAD:factory/t-0817", "git push -u origin HEAD:factory/t-0817",
		"FACTORY_AGENT=coder-t-0001 git push origin HEAD:factory/t-0817", "GIT_DIR=/x git push origin HEAD:factory/t-0817",
		"true && git push origin HEAD:factory/t-0817", "git push origin HEAD:factory/t-0817; true"} {
		if !e.pinned(cmd, "") {
			t.Errorf("pin did not refuse %q", cmd)
		}
	}
	r := e.run("git push origin HEAD:factory/t-0817-a2", "", "", "")
	e.mustExit("-a2 ref", r, 2)
	if !strings.Contains(r.stderr, "HEAD:factory/t-0817") {
		t.Errorf("stderr lacks the allowed ref: %q", r.stderr)
	}
}

// ---- from go/internal/factorytest/guard2/repocfg_test.go ----
// Port of tests/test_worker_guard_repocfg.py (T-0226): workers cannot write repo git config (every worktree shares the
// live checkout's .git/config). Accident prevention, not a security boundary.

var g2RcRoles = []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator"}
var g2RcExecKeys = []string{"filter.x.smudge cmd", "filter.x.clean cmd", "diff.x.textconv cmd", "core.hooksPath x",
	"core.fsmonitor cmd", "core.sshCommand cmd", "remote.origin.uploadpack cmd"}

type g2RcEnv struct {
	t                             *testing.T
	tmp, home, wt, scratch, clone string
}

func g2NewRc(t *testing.T) *g2RcEnv {
	t.Helper()
	tmp := g2RealTemp(t)
	e := &g2RcEnv{t: t, tmp: tmp, home: filepath.Join(tmp, "live"), scratch: filepath.Join(tmp, "scratch")}
	e.wt = filepath.Join(e.home, "worktrees", "T-1")
	e.clone = filepath.Join(e.scratch, "clone")
	g2Mkdirs(t, e.scratch)
	g2Git(t, tmp, "init", "-q", "-b", "main", e.home)
	g2Git(t, e.home, "commit", "-q", "--allow-empty", "-m", "i")
	g2Git(t, e.home, "worktree", "add", "-q", "-b", "factory/t-1", e.wt)
	g2Git(t, tmp, "init", "-q", "-b", "main", e.clone)
	g2Git(t, tmp, "init", "-q", "--bare", filepath.Join(e.scratch, "bare.git"))
	return e
}

func (e *g2RcEnv) hook(payload map[string]any, role string) g2Res {
	e.t.Helper()
	return g2HookJSON(e.t, payload, g2Opts{args: g2RoleArgs(role),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH"},
		set:  map[string]string{"WORKER_GUARD_LIVE_HOME": e.home, "WORKER_GUARD_SCRATCH": e.scratch}})
}

func (e *g2RcEnv) bash(cmd, role, cwd string) g2Res {
	e.t.Helper()
	if cwd == "" {
		cwd = e.wt
	}
	return e.hook(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}, "cwd": cwd}, role)
}

func (e *g2RcEnv) blocked(cmd, role, cwd string) g2Res {
	e.t.Helper()
	r := e.bash(cmd, role, cwd)
	if r.code != 2 {
		e.t.Errorf("should block (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
	return r
}

func (e *g2RcEnv) allowed(cmd, role, cwd string) {
	e.t.Helper()
	if r := e.bash(cmd, role, cwd); r.code != 0 {
		e.t.Errorf("should allow (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
}

func TestG2RepoCfgExecCapableKeysBlockedFromAWorktreeForEveryRole(t *testing.T) {
	e := g2NewRc(t)
	for _, role := range g2RcRoles {
		for _, kv := range g2RcExecKeys {
			e.blocked("git config "+kv, role, "")
		}
	}
}

func TestG2RepoCfgEveryWriteFormBlocked(t *testing.T) {
	e := g2NewRc(t)
	for _, cmd := range []string{"git config --add a.b c", "git config --unset a.b", "git config --unset-all a.b",
		"git config --replace-all a.b c", "git config --rename-section a b", "git config --remove-section a",
		"git config --edit", "git config -e", "git config --local a.b c",
		"git config set a.b c", "git config unset a.b", "git config edit", "git config a.b c",
		"git config --type bool a.b true"} {
		e.blocked(cmd, "qa", "")
	}
}

func TestG2RepoCfgOtherDirectoriesAndShapesBlocked(t *testing.T) {
	e := g2NewRc(t)
	for _, role := range []string{"coder", "qa"} {
		e.blocked("git -C "+e.wt+" config core.hooksPath x", role, e.tmp)
		e.blocked("git -C "+e.home+" config core.hooksPath x", role, e.tmp)
		e.blocked("git --git-dir="+e.home+"/.git config core.hooksPath x", role, "")
		e.blocked("git --git-dir "+e.home+"/.git config core.hooksPath x", role, "")
		e.blocked("git --work-tree "+e.wt+" config core.hooksPath x", role, "")
		e.blocked("cd "+e.tmp+" ; git config core.hooksPath x", role, "")
		e.blocked("cd "+e.home+" && git config core.hooksPath x", role, "")
		e.blocked("cd /nonexistent ; git config core.hooksPath x", role, "")
		e.blocked("cd $SOMEWHERE && git config core.hooksPath x", role, "")
		e.blocked("GIT_DIR=/x/.git git config core.hooksPath x", role, "")
		e.blocked("env git config core.hooksPath x", role, "")
		e.blocked("sh -c 'git config core.hooksPath x'", role, "")
		e.blocked("git -c core.pager=cat config core.hooksPath x", role, "")
		e.blocked("git -c alias.c='config core.hooksPath x' c", role, "")
		e.blocked("git config --file "+e.home+"/.git/config core.hooksPath x", role, "")
		e.blocked("git config --file /tmp/elsewhere core.hooksPath x", role, "")
	}
}

func TestG2RepoCfgGitRemoteWritesBlockedReadsAllowed(t *testing.T) {
	e := g2NewRc(t)
	for _, role := range []string{"coder", "qa"} {
		for _, sub := range []string{"add up ~/factory", "set-url origin x", "rename origin o", "remove origin", "rm origin",
			"set-head origin main", "set-branches origin main"} {
			e.blocked("git remote "+sub, role, "")
		}
		e.blocked("cd "+e.tmp+" ; git remote add up "+e.home, role, "")
		e.blocked("git -C "+e.home+" remote add up x", role, "")
		for _, cmd := range []string{"git remote", "git remote -v", "git remote show origin", "git remote get-url origin"} {
			e.allowed(cmd, role, "")
		}
	}
}

func TestG2RepoCfgReadsAllowed(t *testing.T) {
	e := g2NewRc(t)
	for _, cmd := range []string{"git config --get user.name", "git config --get-all a.b", "git config --get-regexp user",
		"git config --list", "git config -l", "git config get user.name", "git config list",
		"git config --local --get a.b", "git config --show-origin --list", "git config user.name",
		"git -C " + e.home + " config --get remote.origin.url", "git --git-dir=" + e.home + "/.git config --list"} {
		for _, role := range []string{"coder", "judge"} { // T-0755: qa/reviewer git is read-only (allow-list)
			e.allowed(cmd, role, "")
		}
	}
}

func TestG2RepoCfgFileInsideScratchpadAllowed(t *testing.T) {
	e := g2NewRc(t)
	for _, role := range []string{"coder", "judge"} {
		e.allowed("git config --file "+e.scratch+"/x.cfg k v", role, "")
		e.allowed("git config -f "+e.scratch+"/x.cfg --add k v", role, "")
		e.allowed("cd "+e.scratch+" && git config --file x.cfg k v", role, "")
	}
}

func TestG2RepoCfgCloneInsideScratchpadAllowedLinkedWorktreeNot(t *testing.T) {
	e := g2NewRc(t)
	for _, role := range []string{"coder", "judge"} {
		e.allowed("git -C "+e.clone+" config user.name x", role, "")
		e.allowed("git -C "+e.clone+" remote add o ../elsewhere", role, "")
		e.allowed("cd "+e.clone+" && git config core.autocrlf false", role, "")
		e.allowed("git config core.autocrlf false", role, e.clone)
		e.allowed("git --git-dir "+e.clone+"/.git config a.b c", role, "")
		e.allowed("git -C "+e.scratch+"/bare.git config a.b c", role, "")
	}
	g2Git(t, e.home, "worktree", "add", "-q", "-b", "factory/t-2", e.scratch+"/lw")
	e.blocked("git -C "+e.scratch+"/lw config core.hooksPath x", "coder", "")
	e.blocked("git config core.hooksPath x", "coder", e.scratch+"/lw")
}

func TestG2RepoCfgEditWriteAndRedirectsToRepoConfigBlocked(t *testing.T) {
	e := g2NewRc(t)
	gitdir := e.home + "/.git"
	targets := []string{gitdir + "/config", e.wt + "/.git/config", e.wt + "/.git/config.worktree",
		gitdir + "/config.worktree", gitdir + "/worktrees/T-1/config.worktree", gitdir + "/info/attributes",
		e.scratch + "/bare.git/../../live/.git/config"}
	for _, role := range []string{"coder", "qa"} {
		for _, tg := range targets {
			for _, tool := range []string{"Edit", "Write", "MultiEdit"} {
				r := e.hook(map[string]any{"tool_name": tool, "tool_input": map[string]any{"file_path": tg}}, role)
				if r.code != 2 {
					t.Errorf("%s %s: exit %d\n%s", tool, tg, r.code, r.stderr)
				}
				if !strings.Contains(r.stderr, "accident prevention") {
					t.Errorf("%s %s: stderr lacks 'accident prevention': %q", tool, tg, r.stderr)
				}
			}
			for _, cmd := range []string{"echo x >> " + tg, "echo x > " + tg, "tee -a " + tg, "cp /tmp/f " + tg, "mv /tmp/f " + tg,
				"ln -sf /tmp/f " + tg, "install /tmp/f " + tg, "sed -i s/a/b/ " + tg, "dd if=/tmp/f of=" + tg} {
				e.blocked(cmd, role, "")
			}
		}
		e.blocked("echo x >> .git/config", role, e.wt)
		e.blocked("echo x >> ../../.git/config", role, e.wt)
		e.blocked("echo x >> .git/info/attributes", role, e.home)
	}
}

func TestG2RepoCfgBareRepoConfigBlockedOutsideScratchpadOnly(t *testing.T) {
	e := g2NewRc(t)
	bare := filepath.Join(e.tmp, "bare.git")
	g2Git(t, e.tmp, "init", "-q", "--bare", bare)
	e.blocked("echo x >> "+bare+"/config", "coder", "")
	e.blocked("echo x >> "+bare+"/info/attributes", "coder", "")
	e.allowed("echo x >> "+e.scratch+"/bare.git/config", "coder", "")
	e.allowed("echo x >> "+e.clone+"/.git/config", "coder", "")
	r := e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": e.clone + "/.git/config"}}, "coder")
	g2WantExit(t, "Write clone .git/config", r, 0)
}

func TestG2RepoCfgReadingAndUnrelatedConfigFilesAllowed(t *testing.T) {
	e := g2NewRc(t)
	gitdir := e.home + "/.git"
	for _, cmd := range []string{"cat " + gitdir + "/config", "grep url " + gitdir + "/config", "cp " + gitdir + "/config /tmp/c",
		"echo x > config", "echo x > config.json", "echo x > " + e.wt + "/config", "echo x > app/.git/hooks-x",
		"echo x > " + e.wt + "/docs/config.md", "echo x > ./info/attributes"} {
		e.allowed(cmd, "coder", "")
	}
}

func TestG2RepoCfgMessageExplainsTheAlternative(t *testing.T) {
	e := g2NewRc(t)
	err := e.blocked("git config core.hooksPath x", "coder", "").stderr
	for _, w := range []string{"--file", "scratchpad", "not a security boundary"} {
		if !strings.Contains(err, w) {
			t.Errorf("stderr lacks %q: %q", w, err)
		}
	}
}

// ---- from go/internal/factorytest/guard2/repoprot_test.go ----
// Port of tests/test_worker_guard_repoprot.py (R-a, T-0638): no agent changes branch protection or repository
// settings through gh.

var (
	g2RpWorkerRoles = []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator", "staff-engineer"}
	g2RpHQRoles     = []string{"boss", "director", "lead"}
)

const g2RpP = "repos/acme/hive/branches/main/protection"

var g2RpBlocked = []string{
	"gh api -X PUT " + g2RpP + " -F required_pull_request_reviews=null",
	"gh api --method PATCH " + g2RpP + "/enforce_admins",
	"gh api --method=DELETE " + g2RpP,
	"gh api -XPOST " + g2RpP + "/required_signatures",
	"gh api " + g2RpP + " -X delete",
	"gh api -H 'Accept: application/vnd.github+json' -X PUT " + g2RpP,
	"gh api -X PUT repos/acme/hive/branches/release/x/protection",
	"gh api " + g2RpP + " -f enforce_admins=false",
	"gh api -X POST repos/acme/hive/rulesets --input r.json",
	"gh api -p luke-cage -X PUT " + g2RpP,
	"gh api --preview luke-cage --method PUT " + g2RpP,
	"gh api graphql -f query='mutation{updateBranchProtectionRule(input:{}){clientMutationId}}'",
	"gh api graphql -f query='mutation{updateRepository(input:{}){clientMutationId}}'",
	"gh api graphql -F query=@q.graphql",
	"gh alias set x 'api -X PUT repos/a/b/branches/main/protection'",
	"gh api -X PUT '" + g2RpP + "#x'",
	"gh api --field=a=1 " + g2RpP,
	"gh api -X PUT '" + g2RpP + "?x=1'",
	"gh api -X PUT repos/acme/hive/rules/branches/main",
	"gh repo edit acme/hive --default-branch x",
	"gh repo edit --visibility private",
	"gh -R acme/hive repo edit --enable-issues=false",
	"gh repo archive acme/hive --yes",
	"gh api -X PATCH repos/acme/hive -f private=true",
	"gh api --method PATCH /repos/acme/hive",
	"gh api -X DELETE repos/acme/hive",
	"env GH_TOKEN=x gh repo edit acme/hive",
	"sh -c 'gh api -X PUT repos/acme/hive/branches/main/protection'",
	"gh api -X=DELETE " + g2RpP,
	"gh api --method=PATCH " + g2RpP + "/enforce_admins",
	"gh api -iX DELETE " + g2RpP,
	"gh api -iXDELETE " + g2RpP,
	"gh api -i -X=PUT " + g2RpP,
	"gh api -X DELETE repos/acme/hive/branches/main/%70rotection",
	"gh api -X PUT repos/acme/hive/branches/main/%50ROTECTION",
	"gh api -X PATCH repos/acme/hive%2F",
}

var g2RpAllowed = []string{
	"gh api " + g2RpP,
	"gh api -X GET " + g2RpP,
	"gh api --method=GET " + g2RpP + "/enforce_admins",
	"gh api -X=GET " + g2RpP,
	"gh api -iX GET " + g2RpP,
	"gh api -iXGET " + g2RpP,
	"gh api repos/acme/hive/branches/main/%70rotection",
	"gh api repos/acme/hive",
	"gh api repos/acme/hive --jq .private",
	"gh repo view acme/hive --json name",
	"gh api -X POST repos/acme/hive/issues/3/comments -f body=hello",
	"gh api -X PATCH repos/acme/hive/pulls/3 -f title=x",
	"gh api -X PUT repos/acme/hive/topics",
	"gh api graphql -f query='query{viewer{login}}'",
	"gh alias list",
	"gh pr view 3",
	"echo gh repo edit",
	"grep -n 'gh api -X PUT repos/a/b/branches/main/protection' notes.txt",
}

func g2RpRun(t *testing.T, tmp, cmd, role string) g2Res {
	t.Helper()
	args := g2RoleArgs(role)
	for _, h := range g2RpHQRoles {
		if role == h {
			args = []string{"--hq"}
		}
	}
	// FACTORY_HOME has no DB: the public-text rule stays off, so only R-a decides.
	return g2Bash(t, cmd, g2Opts{args: args, cwd: g2RepoRoot(t),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH"}, set: map[string]string{"FACTORY_HOME": tmp}})
}

func TestG2RepoProtEveryRoleIsRefusedEachChangingShape(t *testing.T) {
	tmp := t.TempDir()
	for _, role := range append(append([]string{}, g2RpWorkerRoles...), g2RpHQRoles...) {
		for _, cmd := range g2RpBlocked {
			r := g2RpRun(t, tmp, cmd, role)
			if r.code != 2 {
				t.Errorf("should block (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
			}
			if !strings.Contains(r.stderr, "branch protection and repository settings") {
				t.Errorf("%s: %q: stderr lacks the R-a reason: %q", role, cmd, r.stderr)
			}
		}
	}
}

func TestG2RepoProtReadsAndUnrelatedWritesStayAllowed(t *testing.T) {
	tmp := t.TempDir()
	for _, role := range []string{"coder", "judge", "director"} {
		for _, cmd := range g2RpAllowed {
			r := g2RpRun(t, tmp, cmd, role)
			if r.code != 0 {
				t.Errorf("should allow (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
			}
		}
	}
}
