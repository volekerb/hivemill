package guard2

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Port of tests/test_worker_guard_persist.py (T-0334, T-0957): workers cannot persist config into the live box
// (global/system git config, ~/.config/git, shell init files, ~/.local/bin). Every call runs the real Go guard binary.

var (
	pRoles   = []string{"coder", "qa", "reviewer"}
	pNoPush  = map[string]bool{"qa": true, "reviewer": true} // T-0755: read-only git allow-list roles
	pCfgRead = regexp.MustCompile(`--get|--list| -l(?: |$)`)
	pWorkers = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout", "evaluator"}

	pProtected = []string{".gitconfig", ".config/git/config", ".config/git/attributes", ".config/git/ignore", ".bashrc", ".zshrc",
		".zshenv", ".profile", ".bash_profile", ".zprofile"}
	pTools = []string{"Edit", "MultiEdit", "NotebookEdit", "Write"} // sorted
)

// pw is the account home the guard resolves (the hook reads only the text of a command: nothing here writes it).
func pw(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	p, err := filepath.EvalSymlinks(u.HomeDir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// pEnv is one test's view: an empty temp FACTORY_HOME (T-0636: the result must not depend on the caller's live home).
type pEnv struct {
	t    *testing.T
	home string
	pw   string
}

func newP(t *testing.T) *pEnv {
	t.Helper()
	return &pEnv{t: t, home: realTemp(t), pw: pw(t)}
}

func (p *pEnv) hook(payload map[string]any, role string, hq bool) res {
	p.t.Helper()
	args := roleArgs(role)
	if hq {
		args = []string{"--hq"}
	}
	return hookJSON(p.t, payload, hookOpts{args: args, cwd: repoRoot(p.t), set: map[string]string{"FACTORY_HOME": p.home}})
}

func (p *pEnv) bashIn(cmd, role string, hq bool, cwd string) res {
	p.t.Helper()
	pl := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
	if cwd != "" {
		pl["cwd"] = cwd
	}
	return p.hook(pl, role, hq)
}

func (p *pEnv) blocked(cmd, role, cwd string) {
	p.t.Helper()
	r := p.bashIn(cmd, role, false, cwd)
	if r.code != 2 {
		p.t.Errorf("should block (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
}

func (p *pEnv) allowed(cmd, role, cwd string) {
	p.t.Helper()
	r := p.bashIn(cmd, role, false, cwd)
	if r.code != 0 {
		p.t.Errorf("should allow (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
}

func TestPersistGitConfigGlobalAndSystemWritesBlockedForEveryWorkerRole(t *testing.T) {
	p := newP(t)
	for _, role := range pRoles {
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

func TestPersistGitConfigFileToProtectedPathBlocked(t *testing.T) {
	p := newP(t)
	for _, role := range pRoles {
		for _, rel := range pProtected {
			p.blocked(fmt.Sprintf("git config -f %s/%s core.fsmonitor x", p.pw, rel), role, "")
		}
	}
	for _, cmd := range []string{"git config --file ~/.gitconfig a.b c", "git config --file=$HOME/.config/git/config a.b c",
		"git config -f${HOME}/.gitconfig a.b c", "git config -f /etc/gitconfig a.b c",
		"git config -f ~/x/../.gitconfig a.b c", "git config --file ~/.gitconfig --unset a.b"} {
		p.blocked(cmd, "coder", "")
	}
}

func TestPersistAliasRoutesToGlobalConfigBlocked(t *testing.T) {
	p := newP(t)
	p.blocked("git -c alias.c=config c --global a.b c", "coder", "")
	p.blocked("git -c 'alias.c=!git config --global a.b c' c", "coder", "")
	p.blocked("git -c 'alias.c=!echo x >> ~/.bashrc' c", "coder", "")
}

func TestPersistBashWritesToEveryProtectedPathBlocked(t *testing.T) {
	p := newP(t)
	tmpls := []string{"echo x > {p}", "echo x >> {p}", "echo x | tee {p}", "cp /tmp/a {p}", "mv /tmp/a {p}",
		"ln -sf /tmp/a {p}", "install -m644 /tmp/a {p}", "sed -i s/a/b/ {p}", "dd if=/tmp/a of={p}", "bash -c 'echo x > {p}'"}
	for _, role := range pRoles { // every spelling and tool for the coder; qa and reviewer share the same code path
		rels := pProtected[:1]
		if role == "coder" {
			rels = pProtected
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

func TestPersistRelativeAndCdSpellingsBlocked(t *testing.T) {
	p := newP(t)
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

func TestPersistSelfReviewBypassesBlocked(t *testing.T) {
	p := newP(t)
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

func TestPersistSymlinkSpellingBlocked(t *testing.T) {
	p := newP(t)
	tmp := realTemp(t)
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

func TestPersistInterpreterAndEditorNamingProtectedFileBlocked(t *testing.T) {
	p := newP(t)
	for _, cmd := range []string{"python3 -c \"open('/x/.bashrc','a')\"", "python3 -c \"open('~/.gi'+'tconfig','w')\"",
		"vim ~/.zshrc", "perl -pi -e 's/a/b/' ~/.profile", "python3 - <<'EOF'\nopen('/h/.bashrc','a').write('x')\nEOF",
		"awk 'BEGIN{print 1 > \"/h/.zshenv\"}'"} {
		p.blocked(cmd, "coder", "")
	}
}

func TestPersistReadOnlyAndRepoLocalFormsAllowed(t *testing.T) {
	p := newP(t)
	for _, role := range pRoles {
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
			if pNoPush[role] && strings.HasPrefix(cmd, "git") && (strings.HasPrefix(cmd, "git -c") || !pCfgRead.MatchString(cmd)) {
				continue // T-0755: read-only roles run only the --get/--list forms of git config (allow-list)
			}
			p.allowed(cmd, role, "")
		}
	}
}

func TestPersistRepoLocalConfigInOwnWorktreeAllowed(t *testing.T) {
	p := newP(t)
	tmp := realTemp(t)
	git(t, tmp, "init", "-q", tmp)
	for _, cmd := range []string{"echo x > .gitconfig", "echo x > .bashrc", "git config --get user.name",
		"git config user.name", "git config --list"} {
		p.allowed(cmd, "coder", tmp)
	}
}

func TestPersistGitConfigWriteWithEnvAssignmentBlocked(t *testing.T) {
	p := newP(t)
	for _, role := range pRoles {
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

func TestPersistRepoLocalConfigAndReadsStayAllowed(t *testing.T) {
	p := newP(t)
	for _, cmd := range []string{"git config --get a.b", "git -C . config --list", "git config --get user.name",
		"awk 1 ~/.zshrc", "awk '{print $1}' ~/.gitconfig", "echo HOME=1; git status"} {
		p.allowed(cmd, "coder", "")
	}
	p.blocked("awk -i inplace 1 ~/.zshrc", "coder", "")
}

func TestPersistHQModeUnchanged(t *testing.T) {
	// The Python test also compares its in-process hook with the real script; here every call is the real binary.
	p := newP(t)
	for _, cmd := range []string{"git config --global user.name x", "echo x > ~/.bashrc", "cp /tmp/a ~/.gitconfig"} {
		wantExit(t, cmd, p.bashIn(cmd, "", true, ""), 0)
	}
	r := p.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": p.pw + "/.bashrc"}}, "", true)
	wantExit(t, "hq Write ~/.bashrc", r, 0)
	wantExit(t, "coder blocked", p.bashIn("echo x > ~/.bashrc", "coder", false, ""), 2)
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
		wantExit(t, fmt.Sprint(c.pl), p.hook(c.pl, c.role, false), c.code)
	}
}

// ---- PersistEditToolTests ----

func (p *pEnv) edit(tool, key, path, role, cwd string) res {
	p.t.Helper()
	if cwd == "" {
		cwd = "/tmp"
	}
	return p.hook(map[string]any{"tool_name": tool, "cwd": cwd, "tool_input": map[string]any{key: path}}, role, false)
}

func TestPersistEditWritePayloadsToProtectedPathsBlocked(t *testing.T) {
	p := newP(t)
	keys := map[string]string{"Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path"}
	for _, role := range pRoles {
		for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit"} {
			rels := pProtected[:3]
			if tool == "Write" {
				rels = pProtected
			}
			for _, rel := range rels {
				for _, spell := range []string{p.pw + "/" + rel, "~/" + rel, "$HOME/" + rel, p.pw + "/x/../" + rel} {
					r := p.edit(tool, keys[tool], spell, role, "")
					wantExit(t, role+" "+tool+" "+spell, r, 2)
				}
			}
		}
		wantExit(t, role+" /etc/gitconfig", p.edit("Write", "file_path", "/etc/gitconfig", role, ""), 2)
	}
	wantExit(t, "Edit .bashrc cwd=home", p.edit("Edit", "file_path", ".bashrc", "coder", p.pw), 2)
	wantExit(t, "Write ../.zshrc", p.edit("Write", "file_path", "../.zshrc", "coder", filepath.Join(p.pw, "factory")), 2)
}

func TestPersistSymlinkPayloadBlocked(t *testing.T) {
	p := newP(t)
	link := filepath.Join(realTemp(t), "rc")
	if err := os.Symlink(filepath.Join(p.pw, ".zshrc"), link); err != nil {
		t.Fatal(err)
	}
	wantExit(t, "Write via symlink", p.edit("Write", "file_path", link, "coder", ""), 2)
}

func TestPersistOtherPathsAllowedAndUnreadablePayloadFailsClosed(t *testing.T) {
	p := newP(t)
	for _, path := range []string{"/tmp/x.txt", p.pw + "/factory/README.md", p.pw + "/.config/gitx", p.pw + "/.bashrc.bak", "notes.md"} {
		wantExit(t, path, p.edit("Write", "file_path", path, "coder", ""), 0)
	}
	wantExit(t, "Write {}", p.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{}}, "coder", false), 2)
	wantExit(t, "Edit 7", p.hook(map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": 7}}, "coder", false), 2)
	wantExit(t, "Read .bashrc", p.hook(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p.pw + "/.bashrc"}}, "coder", false), 0)
}

func TestPersistWorkerSettingsRouteEditToolsToTheHookAndHQSettingsDoNot(t *testing.T) {
	// The Python test renders the settings with bin/factory; the templates are plain JSON (placeholders are inside
	// strings), so the same structural facts are read from roles/settings/<role>.json.in.
	groups := func(role string) []map[string]any {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "roles", "settings", role+".json.in"))
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
	for _, role := range pWorkers {
		var edit []map[string]any
		for _, g := range groups(role) {
			m, _ := g["matcher"].(string)
			parts := strings.Split(m, "|")
			sort.Strings(parts)
			if strings.Join(parts, "|") == strings.Join(pTools, "|") {
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

func lbBin(p *pEnv) string { return p.pw + "/.local/bin" }

func lbBlocked(p *pEnv) []string {
	b := lbBin(p)
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

func lbAllowed(p *pEnv) []string {
	return []string{
		"ls ~/.local/bin", "ls -la ~/.local/bin/factory", "cp ~/.local/bin/x /tmp/x", "~/.local/bin/factory whoami",
		"grep -rn .local/bin docs", "echo x > /usr/local/bin/foo", "echo x > ~/.local/binx/foo",
		"echo x > " + p.pw + "/.local/binx/foo", "git commit -m 'note: ~/.local/bin is on PATH'",
		"factory msg lead 'subj' 'see ~/.local/bin/git'", "export PATH=~/.local/bin:$PATH",
		"cp /tmp/x ~/.local/share/foo",
	}
}

func TestPersistLocalBinWritesAreBlocked(t *testing.T) {
	p := newP(t)
	for _, cmd := range lbBlocked(p) {
		r := p.bashIn(cmd, "coder", false, repoRoot(t))
		wantExit(t, cmd, r, 2)
	}
}

func TestPersistLocalBinBlockMessageNamesLocalBin(t *testing.T) {
	p := newP(t)
	for _, cmd := range []string{"echo x > ~/.local/bin/git", "cp /tmp/x ~/.local/bin/git", "rm ~/.local/bin/factory"} {
		if r := p.bashIn(cmd, "coder", false, repoRoot(t)); !strings.Contains(r.stderr, "~/.local/bin") {
			t.Errorf("%s: stderr lacks ~/.local/bin: %q", cmd, r.stderr)
		}
	}
}

func TestPersistLocalBinReadsRunsLookAlikesAndTextAreAllowed(t *testing.T) {
	p := newP(t)
	for _, cmd := range lbAllowed(p) {
		wantExit(t, cmd, p.bashIn(cmd, "coder", false, repoRoot(t)), 0)
	}
}

func TestPersistLocalBinBlockedForEveryWorkerRole(t *testing.T) {
	p := newP(t)
	for _, role := range []string{"coder", "qa", "reviewer", "pm"} {
		wantExit(t, role, p.bashIn("echo x > ~/.local/bin/git", role, false, repoRoot(t)), 2)
	}
}

func (p *pEnv) lbEdit(tool, path, cwd string) res {
	p.t.Helper()
	key := "file_path"
	if tool == "NotebookEdit" {
		key = "notebook_path"
	}
	return p.hook(map[string]any{"tool_name": tool, "tool_input": map[string]any{key: path}, "cwd": cwd}, "coder", false)
}

func TestPersistLocalBinEditToolsIntoLocalBinAreBlocked(t *testing.T) {
	p := newP(t)
	root, b := repoRoot(t), lbBin(p)
	for _, tool := range pTools {
		for _, c := range []struct{ path, cwd string }{{"~/.local/bin/git", root}, {"$HOME/.local/bin/git", root},
			{b + "/git", root}, {"git", b}, {b + "/factory", root}, {b + "/../bin/git", root}, {"/tmp/$X/.local/bin/git", root}} {
			r := p.lbEdit(tool, c.path, c.cwd)
			wantExit(t, tool+" "+c.path, r, 2)
			if !strings.Contains(r.stderr, "~/.local/bin") {
				t.Errorf("%s %s: stderr lacks ~/.local/bin: %q", tool, c.path, r.stderr)
			}
		}
	}
}

func TestPersistLocalBinSymlinkEntryWithRealpathOutsideTreeIsBlocked(t *testing.T) {
	// A fixture symlink cannot sit inside the real ~/.local/bin (never written); the spelled path of the live entry
	// is decided on its text, so the check needs no file there.
	p := newP(t)
	wantExit(t, "Write live entry", p.lbEdit("Write", lbBin(p)+"/factory", repoRoot(t)), 2)
}

func TestPersistLocalBinOtherPathsAreAllowed(t *testing.T) {
	p := newP(t)
	root := repoRoot(t)
	for _, tool := range pTools {
		for _, path := range []string{"~/.local/binx/foo", p.pw + "/.local/binx/foo", filepath.Join(root, "notes.md")} {
			wantExit(t, tool+" "+path, p.lbEdit(tool, path, root), 0)
		}
	}
	wantExit(t, "Write /usr/local/bin/foo", p.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "/usr/local/bin/foo"}}, "coder", false), 0)
}
