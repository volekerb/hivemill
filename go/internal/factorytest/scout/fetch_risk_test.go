package scout

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	sc "factory/go/internal/scout"
)

// Port of the RiskTests class of tests/test_scout_fetch.py (see fetch_test.go for the whole module and ported_scout.txt).
// The Python cls() helper builds a tree and a reader keyed by path; fxCls does the same over sc.Classify.
// not ported: the four newline scopes of test_zsh_expansion_and_precommand_prefixes_are_open call the private
// risk._unrestricted_bash directly; Go keeps unrestrictedBash unexported, so only the _open() half is ported.

const fxSkillMD = "---\nname: x\ndescription: d\n---\nDo the thing.\n"

type fxSpec map[string][2]string // path -> (mode, type) overrides of fxTree

func fxTree(paths []string, special fxSpec) []sc.Entry {
	out := make([]sc.Entry, 0, len(paths))
	for i, p := range paths {
		mode, typ := "100644", "blob"
		if s, ok := special[p]; ok {
			mode, typ = s[0], s[1]
		}
		out = append(out, sc.Entry{Path: p, Mode: mode, Type: typ, Sha: "s" + itoa(i)})
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }

// fxRead serves texts by path; a path that is not served is a ReadError (Python raises KeyError there, and no
// passing case reads such a path). nil texts means no reader at all.
func fxRead(texts map[string]string) sc.ReadFunc {
	if texts == nil {
		return nil
	}
	return func(e sc.Entry) (string, error) {
		if s, ok := texts[e.Path]; ok {
			return s, nil
		}
		return "", &sc.ReadError{Msg: "not served: " + e.Path}
	}
}

func fxClsX(paths []string, texts map[string]string, prefix string, special fxSpec) sc.Result {
	return sc.Classify(fxTree(paths, special), prefix, fxRead(texts), false)
}

func fxCls(paths []string, texts map[string]string) sc.Result { return fxClsX(paths, texts, "", nil) }

func fxOne(path, text string) sc.Result { return fxCls([]string{path}, map[string]string{path: text}) }

func fxP(paths ...string) []string { return paths }

func fxHas(t testing.TB, what string, list []string, want string) {
	t.Helper()
	for _, x := range list {
		if x == want {
			return
		}
	}
	t.Fatalf("%s: %q not in %q", what, want, list)
}

func fxRiskIs(t testing.TB, what string, res sc.Result, want string) {
	t.Helper()
	if res.Risk != want {
		t.Fatalf("%s: risk = %q (%q), want %q", what, res.Risk, res.Detail, want)
	}
}

func fxFlags(res sc.Result) []string { return strings.Split(res.Risk, ",") }

func fxContainsFlag(t testing.TB, what string, res sc.Result, flag string) {
	t.Helper()
	fxHas(t, what+" risk "+res.Risk, fxFlags(res), flag)
}

func fxDetail(res sc.Result) string { return strings.Join(res.Detail, "\n") }

const fxAsanaSetup = "---\ndescription: Set up Asana MCP\n---\n# Asana setup\nRun:\n\n```bash\n" +
	"claude mcp add --transport http --client-id ID asana https://mcp.asana.com/v2/mcp\n```\n" +
	"Then complete the OAuth flow.\n"
const fxAsanaPlugin = `{"name": "asana", "description": "Asana's V2 MCP server (https://mcp.asana.com/v2/mcp)"}`

func fxFixture(t testing.TB, name string) []byte {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "tests", "fixtures", "scout", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestScoutFetchRiskC0022AsanaLayoutIsMcpAndNetwork(t *testing.T) {
	files := fxP(".claude-plugin/plugin.json", "README.md", "commands/asana-setup.md")
	res := fxCls(files, map[string]string{files[0]: fxAsanaPlugin, files[1]: "# Asana\n", files[2]: fxAsanaSetup})
	if res.Risk == "prompt-only" {
		t.Fatal("asana layout is prompt-only")
	}
	fxRiskIs(t, "asana", res, "mcp,network")
	fxHas(t, "detail", res.Detail, "mcp: commands/asana-setup.md")
	fxHas(t, "detail", res.Detail, "mcp: .claude-plugin/plugin.json")
}

func TestScoutFetchRiskMcpInstructionsInMarkdown(t *testing.T) {
	for _, text := range []string{"run claude mcp add foo -- npx x", "mcp add --transport sse x", "claude mcp add-json x {}",
		"set mcpServers in settings", "see https://example.com/mcp/docs",
		"endpoint HTTPS://MCP.Example.com/x", "RUN MCP  ADD x", "[x](https://mcp.foo.com)",
		"https://example.com/api?server=mcp"} {
		res := fxOne("SKILL.md", "---\nname: a\n---\n"+text+"\n")
		fxContainsFlag(t, text, res, "mcp")
	}
}

func TestScoutFetchRiskMcpProseOnlyStaysPromptOnly(t *testing.T) {
	text := "---\nname: a\n---\nMCP is the Model Context Protocol. See https://modelcontextprotocol.io/docs " +
		"and https://example.com/guide for background.\n"
	fxRiskIs(t, "prose", fxOne("SKILL.md", text), "prompt-only")
}

func TestScoutFetchRiskMcpInPluginJSONTextWithoutKey(t *testing.T) {
	fxRiskIs(t, "plugin.json", fxOne(".claude-plugin/plugin.json", fxAsanaPlugin), "mcp,network")
}

func TestScoutFetchRiskCommandsInSubdirectoryAreRead(t *testing.T) {
	res := fxOne("commands/git/commit.md", "x\n!`rm -rf ~`\n")
	contains(t, "risk", res.Risk, "shell-in-markdown")
	contains(t, "detail", fxDetail(res), "commands/git/commit.md")
}

func TestScoutFetchRiskOversizeMarkdownIsUnknownNeverPromptOnly(t *testing.T) {
	big := strings.Repeat("filler\n", 10000) + "!`whoami`\n"
	res := fxOne("SKILL.md", big)
	contains(t, "risk", res.Risk, "unknown")
	if res.Risk == "prompt-only" {
		t.Fatal("oversize markdown is prompt-only")
	}
	tree := fxTree(fxP("SKILL.md"), nil)
	tree[0].Size = sc.ReadCap + 1
	got := sc.Classify(tree, "", func(sc.Entry) (string, error) { return "short", nil }, false)
	contains(t, "risk of a blob over the cap", got.Risk, "unknown")
}

func TestScoutFetchRiskHooksKeyInFrontmatter(t *testing.T) {
	for _, path := range []string{"SKILL.md", "commands/a.md", "agents/a.md"} {
		res := fxOne(path, "---\nname: a\nhooks:\n  PreToolUse: []\n---\nbody\n")
		contains(t, path+" risk", res.Risk, "hooks")
	}
	fxRiskIs(t, "hooks in prose", fxOne("SKILL.md", "---\nname: hooks-guide\n---\nhooks: no\n"), "prompt-only")
}

func TestScoutFetchRiskAgentToolsNetworkAndBash(t *testing.T) {
	agent := func(tools string) string {
		p := "agents/team/a.md"
		return fxOne(p, "---\nname: a\n"+tools+"\n---\nbody\n").Risk
	}
	for tools, want := range map[string]string{"tools: Read, WebFetch": "network", "tools: WebSearch": "network",
		"tools: Read, Bash": "network", "tools:\n  - Read\n  - Bash": "network", "tools: Read, Grep": "prompt-only",
		"tools: Read, Bash(git status:*)": "prompt-only"} {
		if got := agent(tools); got != want {
			t.Errorf("%q: risk = %q, want %q", tools, got, want)
		}
	}
}

func TestScoutFetchRiskUnrestrictedBashInAllowedTools(t *testing.T) {
	for _, path := range []string{"SKILL.md", "commands/a.md"} {
		fxRiskIs(t, path, fxOne(path, "---\nallowed-tools: Read, Bash\n---\nbody\n"), "network")
		fxRiskIs(t, path, fxOne(path, "---\nallowed-tools: Read, Bash(ls:*)\n---\nbody\n"), "prompt-only")
	}
}

func fxScopeRisk(scope string) string {
	return fxOne("SKILL.md", "---\nallowed-tools: Read, "+scope+"\n---\nbody\n").Risk
}

func fxOpen(scope string) bool { return fxScopeRisk(scope) == "shell-in-markdown,network" }

func TestScoutFetchRiskOpenBashScopesAreUnrestricted(t *testing.T) {
	for _, scope := range []string{"Bash(*)", "Bash(sh:*)", "Bash(bash:*)", "Bash(python*:*)", "Bash(python3:*)", "Bash()",
		"Bash(/bin/sh:*)", "Bash(/usr/bin/env:*)", "Bash(sh*)", "Bash(b*:*)", "Bash(?sh:*)"} {
		if got := fxScopeRisk(scope); got != "shell-in-markdown,network" {
			t.Errorf("%s: risk = %q", scope, got)
		}
	}
	for _, scope := range []string{"Bash(git status:*)", "Bash(ls:*)", "Bash(shasum:*)", "Bash(pythonista:*)"} {
		if got := fxScopeRisk(scope); got != "prompt-only" {
			t.Errorf("%s: risk = %q", scope, got)
		}
	}
}

func TestScoutFetchRiskWrappersAndRunnersAreUnrestricted(t *testing.T) {
	for _, cmd := range []string{"sudo", "doas", "timeout", "nohup", "command", "nice", "stdbuf", "setsid", "xargs", "source", ".",
		"pwsh", "npx", "uv", "uvx", "deno", "bun", "awk", "gawk", "find", "git", "make", "/usr/bin/git"} {
		for _, scope := range []string{"Bash(" + cmd + ":*)", "Bash(" + cmd + ")"} {
			if got := fxScopeRisk(scope); got != "shell-in-markdown,network" {
				t.Errorf("%s: risk = %q", scope, got)
			}
		}
	}
	for _, scope := range []string{"Bash(git -c core.pager=x:*)", "Bash(make -f x:*)"} {
		if got := fxScopeRisk(scope); got != "shell-in-markdown,network" {
			t.Errorf("%s: risk = %q", scope, got)
		}
	}
	for _, scope := range []string{"Bash(git status:*)", "Bash(/usr/bin/ls:*)", "Bash(findings:*)"} {
		if got := fxScopeRisk(scope); got != "prompt-only" {
			t.Errorf("%s: risk = %q", scope, got)
		}
	}
}

func fxAssertOpen(t *testing.T, want bool, scopes ...string) {
	t.Helper()
	for _, s := range scopes {
		if got := fxOpen(s); got != want {
			t.Errorf("%q: open = %v, want %v (risk %q)", s, got, want, fxScopeRisk(s))
		}
	}
}

func TestScoutFetchRiskGitAllowlistAndNoMakeException(t *testing.T) {
	fxAssertOpen(t, true, "Bash(git grep:*)", "Bash(git grep -O sh:*)", "Bash(git grep:-O)", "Bash(make test:*)",
		"Bash(make --eval x:*)", "Bash(make:*)", "Bash(git rebase -x:*)", "Bash(git rebase:*)",
		"Bash(git bisect run:*)", "Bash(git submodule foreach:*)", "Bash(git -c x=y status:*)",
		"Bash(git status --output=x:*)", "Bash(git log -O:*)", "Bash(git status;sh:*)", "Bash(git:*)")
	for _, sub := range []string{"status", "log", "diff", "show", "rev-parse", "ls-files", "blame"} {
		fxAssertOpen(t, false, "Bash(git "+sub+":*)")
	}
	fxAssertOpen(t, false, "Bash(git diff main:*)")
}

func TestScoutFetchRiskExpansionBuiltCommandWordIsOpen(t *testing.T) {
	fxAssertOpen(t, true, "Bash($'\\x73h':*)", "Bash($SHELL:*)", "Bash(${SHELL}:*)", "Bash(`echo sh`:*)",
		"Bash($(echo sh):*)", "Bash(/bin/s[h]:*)", "Bash(/bin/{sh,}:*)", "Bash(~/x:*)")
	fxAssertOpen(t, false, "Bash(ls:*)")
}

func TestScoutFetchRiskZshExpansionAndPrecommandPrefixesAreOpen(t *testing.T) {
	fxAssertOpen(t, true, "Bash(=sh:*)", "Bash(=ls:*)", "Bash(time sh:*)", "Bash(noglob sh:*)", "Bash(! sh:*)",
		"Bash(builtin exec:*)", "Bash(nocorrect sh:*)", "Bash(coproc sh:*)", "Bash(if sh:*)",
		"Bash(command sh:*)", "Bash(exec sh:*)", "Bash(eval x:*)", "Bash(. x:*)", "Bash(source x:*)",
		"Bash(nohup sh:*)", "Bash(env sh:*)", "Bash(doas sh:*)", "Bash(- sh:*)",
		"Bash(while x:*)", "Bash(for x:*)", "Bash(case x:*)", "Bash(/usr/bin/env sh:*)",
		"Bash(time:*)", "Bash(a@b:*)", "Bash(a!b:*)")
	fxAssertOpen(t, false, "Bash(ls:*)", "Bash(rg:*)", "Bash(/usr/bin/ls:*)", "Bash(python3.11-config:*)", "Bash(git log:*)")
}

func TestScoutFetchRiskGitAllowlistChecksScopeAfterColon(t *testing.T) {
	fxAssertOpen(t, true, "Bash(git log:--output=x)", "Bash(git diff:--output=/x)", "Bash(git log:$(x))", "Bash(git log:-p;sh)")
	fxAssertOpen(t, false, "Bash(git log:*)", "Bash(git diff:main)")
}

func TestScoutFetchRiskQuotedCommandWordIsStripped(t *testing.T) {
	fxAssertOpen(t, true, "Bash(\"sh\":*)", "Bash('bash' -c:*)", "Bash('sh':*)", "Bash(\"git\" grep:*)", "Bash(s\"h\":*)", "Bash(\\sh:*)")
	fxAssertOpen(t, false, "Bash(\"ls\":*)", "Bash('git' status:*)")
}

func TestScoutFetchRiskEnvAssignmentPrefixIsOpen(t *testing.T) {
	fxAssertOpen(t, true, "Bash(FOO=1 sh:*)", "Bash(FOO=1 ls:*)", "Bash(PATH=/x git status:*)", "Bash(A=b:*)")
}

func TestScoutFetchRiskMoreLaunchersAreOpen(t *testing.T) {
	for _, cmd := range []string{"ssh", "scp", "rsync", "busybox", "chroot", "unshare", "nsenter", "script", "expect", "tclsh",
		"lua", "R", "Rscript", "julia", "/usr/bin/ssh"} {
		fxAssertOpen(t, true, "Bash("+cmd+":*)", "Bash("+cmd+" -x:*)")
	}
	fxAssertOpen(t, false, "Bash(scripts:*)", "Bash(rg:*)")
}

func TestScoutFetchRiskCaseInsensitiveDirsAndExtensions(t *testing.T) {
	for _, path := range []string{"COMMANDS/a.md", "Agents/a.md", "commands/A.MD", "skill.md"} {
		contains(t, path, fxOne(path, "x\n!`rm -rf ~`\n").Risk, "shell-in-markdown")
	}
	contains(t, "Hooks/run.md", fxOne("Hooks/run.md", "x").Risk, "hooks")
	if !sc.HasManifest(fxTree(fxP("COMMANDS/A.MD"), nil), "") || !sc.HasManifest(fxTree(fxP("Agents/a.md"), nil), "") {
		t.Fatal("HasManifest is case sensitive")
	}
}

// Unicode parity: Python str.strip() drops NBSP U+00A0, EM SPACE U+2003 and a BOM before the frontmatter.
func TestScoutFetchRiskBomAndLeadingWhitespaceBeforeFrontmatter(t *testing.T) {
	body := "---\nname: a\nhooks:\n  PreToolUse: []\nallowed-tools: Bash(*)\n---\nbody\n"
	for _, prefix := range []string{"\ufeff", "\n\n", "  \t", "\ufeff\r\n", "\u00a0", "\u2003\u00a0\ufeff\u200b"} {
		res := fxOne("SKILL.md", prefix+body)
		contains(t, "hooks "+fmt.Sprintf("%q", prefix), res.Risk, "hooks")
		contains(t, "network "+fmt.Sprintf("%q", prefix), res.Risk, "network")
	}
	crlf := "\ufeff" + strings.ReplaceAll(body, "\n", "\r\n")
	contains(t, "crlf", fxOne("SKILL.md", crlf).Risk, "hooks")
}

func TestScoutFetchRiskUnparseableInputIsUnknown(t *testing.T) {
	res := fxOne("SKILL.md", "---\nname: a\nhooks:\n  x: 1\nbody never closed\n")
	contains(t, "risk", res.Risk, "unknown")
	contains(t, "risk", res.Risk, "hooks")
	contains(t, "bad json", fxOne(".claude-plugin/plugin.json", "{not json").Risk, "unknown")
	contains(t, "json list", fxOne(".claude-plugin/plugin.json", "[1]").Risk, "unknown")
	fxRiskIs(t, "ok json", fxOne(".claude-plugin/plugin.json", `{"name": "a"}`), "prompt-only")
}

func TestScoutFetchRiskEntryTargetPrefixRemoval(t *testing.T) {
	repo := sc.MarketplaceRepo
	for _, c := range []struct {
		src, path string
		ok        bool
	}{{"./plugins/x", "plugins/x", true}, {"./", "", true}, {"../x", "", false}, {"./../x", "", false}, {"./.hidden/x", ".hidden/x", true}} {
		r, p, ok := sc.EntryTarget(c.src)
		if ok != c.ok || (ok && (r != repo || p != c.path)) {
			t.Errorf("EntryTarget(%q) = %q %q %v, want %q %q %v", c.src, r, p, ok, repo, c.path, c.ok)
		}
	}
}

func fxTreeFixture(t testing.TB, name string) []sc.Entry {
	t.Helper()
	var doc struct {
		Tree []sc.Entry `json:"tree"`
	}
	if err := json.Unmarshal(fxFixture(t, name), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Tree
}

func TestScoutFetchRiskFixturePromptOnly(t *testing.T) {
	tree := fxTreeFixture(t, "tree_prompt_only.json")
	texts := map[string]string{"r1": "# readme\n", "r5": "{}", "r3": `{"name": "x"}`, "r4": fxSkillMD, "r7": "Review $ARGUMENTS carefully.\n"}
	res := sc.Classify(tree, "", func(e sc.Entry) (string, error) {
		if s, ok := texts[e.Sha]; ok {
			return s, nil
		}
		return "", &sc.ReadError{Msg: "not served: " + e.Sha}
	}, false)
	fxRiskIs(t, "fixture", res, "prompt-only")
	if len(res.Detail) != 0 {
		t.Fatalf("detail = %q, want none", res.Detail)
	}
}

func TestScoutFetchRiskFixtureExecutables(t *testing.T) {
	tree := fxTreeFixture(t, "tree_executables.json")
	res := sc.Classify(tree, "", func(sc.Entry) (string, error) { return fxSkillMD, nil }, false)
	fxRiskIs(t, "fixture", res, "scripts,hooks,mcp,symlinks")
	detail := fxDetail(res)
	for _, p := range []string{"run.sh", "tool.py", "a.js", "b.cjs", "c.mjs", "d.ts", "blob.bin", "exec.md"} {
		contains(t, "detail", detail, "scripts: "+p)
	}
	contains(t, "detail", detail, "hooks: hooks/hooks.json")
	contains(t, "detail", detail, "mcp: .mcp.json")
	contains(t, "detail", detail, "symlinks: link.md")
	contains(t, "detail", detail, "symlinks: sub")
	notContains(t, "detail", detail, "SKILL.md")
}

func TestScoutFetchRiskSingleFlags(t *testing.T) {
	md := map[string]string{"commands/a.md": "---\ndescription: x\n---\nRun:\n!`git status`\n"}
	exe := sc.Classify([]sc.Entry{{Path: "a.md", Mode: "100755", Type: "blob", Sha: "x"}}, "", func(sc.Entry) (string, error) { return "t", nil }, false)
	cases := []struct {
		want string
		res  sc.Result
	}{
		{"scripts", fxCls(fxP("SKILL.md", "x.sh"), map[string]string{"SKILL.md": fxSkillMD})},
		{"scripts", exe},
		{"scripts", fxCls(fxP("package.json", "SKILL.md"), map[string]string{"package.json": "{}", "SKILL.md": fxSkillMD})},
		{"hooks", fxCls(fxP("hooks/x.md"), map[string]string{"hooks/x.md": "t"})},
		{"hooks", fxOne(".claude-plugin/plugin.json", `{"hooks": {"Stop": []}}`)},
		{"mcp", fxOne(".mcp.json", "{}")},
		{"mcp", fxOne(".claude-plugin/plugin.json", `{"mcpServers": {"a": {}}}`)},
		{"symlinks", fxClsX(fxP("a.md"), map[string]string{"a.md": "t"}, "", fxSpec{"a.md": {"120000", "blob"}})},
		{"symlinks", fxClsX(fxP("vendor"), map[string]string{"vendor": ""}, "", fxSpec{"vendor": {"160000", "commit"}})},
		{"shell-in-markdown", fxCls(fxP("commands/a.md"), md)},
		{"shell-in-markdown", fxOne("commands/b.md", "---\nallowed-tools: Bash("+"cu"+"rl:*)\n---\nx")},
		{"network", fxOne("SKILL.md", "---\nname: x\nallowed-tools: WebFetch\n---\nx")},
		{"network", fxOne("SKILL.md", "Fetch the list from https://example.invalid/list.json first.")},
	}
	for i, c := range cases {
		if c.res.Risk != c.want || len(c.res.Detail) == 0 {
			t.Errorf("case %d: risk = %q detail %q, want %q with a detail", i, c.res.Risk, c.res.Detail, c.want)
		}
	}
	fxRiskIs(t, "backtick in prose", fxOne("commands/a.md", "use `ls` here! `x`"), "prompt-only")
}

func TestScoutFetchRiskPromptOnlyOnlyAlone(t *testing.T) {
	res := fxCls(fxP("SKILL.md", "run.sh"), map[string]string{"SKILL.md": fxSkillMD})
	notContains(t, "risk", res.Risk, "prompt-only")
	res = fxCls(fxP("SKILL.md", "README.md", "LICENSE", "NOTICE", "skills/a/SKILL.md", "skills/a/d.json",
		"data.csv", "t.txt", "plugin.json", "marketplace.json"),
		map[string]string{"SKILL.md": fxSkillMD, "skills/a/SKILL.md": fxSkillMD, "plugin.json": "{}", "README.md": "# r", "skills/a/d.json": "{}"})
	fxRiskIs(t, "inert tree", res, "prompt-only")
}

func TestScoutFetchRiskJSONOutsideASkillIsInertButPackageJSONIsAScript(t *testing.T) {
	res := fxCls(fxP("config.json", "x.yaml", "y.toml"), map[string]string{"config.json": "{}", "x.yaml": "a: 1", "y.toml": "a = 1"})
	notContains(t, "risk", res.Risk, "scripts")
	fxRiskIs(t, "package.json", fxCls(fxP("package.json"), map[string]string{}), "scripts")
	fxRiskIs(t, "pyproject.toml", fxCls(fxP("pyproject.toml"), map[string]string{}), "scripts")
}

func TestScoutFetchRiskBConfigWithHooksOrMcpContentIsFlagged(t *testing.T) {
	for _, c := range []struct{ path, text, want string }{
		{"lefthook.d/x.yaml", "hooks:\n  pre-commit: {}", "hooks"},
		{"conf/a.json", `{"hooks": {"Stop": []}}`, "hooks"},
		{"conf/b.toml", "[hooks]\nx = 1", "hooks"},
		{"mcp.json", `{"mcpServers": {"a": {"command": "x"}}}`, "mcp"},
		{".cursor/servers.json", `{"url": "https://mcp.example.com/sse"}`, "mcp"},
	} {
		res := fxCls(fxP(c.path, "SKILL.md"), map[string]string{c.path: c.text, "SKILL.md": fxSkillMD})
		fxContainsFlag(t, c.path, res, c.want)
	}
	skillOnly := func(e sc.Entry) (string, error) {
		if e.Path != "SKILL.md" {
			return "", &sc.ReadError{Msg: "not served"}
		}
		return fxSkillMD, nil
	}
	res := sc.Classify(fxTree(fxP("conf/a.json", "SKILL.md"), nil), "", skillOnly, false) // unreadable config is not inert
	contains(t, "risk", res.Risk, "unknown")
}

func TestScoutFetchRiskBReadmeUnderCommandsIsStillACommand(t *testing.T) {
	contains(t, "risk", fxOne("commands/README.md", "x\n!`rm -rf ~`\n").Risk, "shell-in-markdown")
}

// --- T-0377 part A: residual MCP gaps ---

func fxMcpFlags(path, text string) []string { return fxFlags(fxOne(path, text)) }

func TestScoutFetchRiskA1ReadmeIsScanned(t *testing.T) {
	res := fxCls(fxP("SKILL.md", "README.md"), map[string]string{"SKILL.md": fxSkillMD, "README.md": "Run claude mcp add x https://mcp.foo.com/v1\n"})
	fxRiskIs(t, "readme", res, "mcp,network")
	fxHas(t, "detail", res.Detail, "mcp: README.md")
	fxRiskIs(t, "prose readme", fxCls(fxP("SKILL.md", "README.md"),
		map[string]string{"SKILL.md": fxSkillMD, "README.md": "curl is great. MCP is a protocol.\n"}), "prompt-only")
}

func TestScoutFetchRiskA2LineContinuedMcpAdd(t *testing.T) {
	fxHas(t, "flags", fxMcpFlags("SKILL.md", "---\nname: a\n---\nclaude mcp \\\n  add foo -- x\n"), "mcp")
}

func TestScoutFetchRiskA3NpxMcpLaunchers(t *testing.T) {
	for _, text := range []string{"npx -y mcp-remote https://x.example/sse", "uvx some-mcp-server", "npx @modelcontextprotocol/server-fs /tmp"} {
		flags := fxMcpFlags("SKILL.md", "---\nname: a\n---\n"+text+"\n")
		fxHas(t, text, flags, "mcp")
		fxHas(t, text, flags, "network")
	}
	eqStrings(t, "flags", fxMcpFlags("SKILL.md", "---\nname: a\n---\nnpx prettier --write .\nmention mcp-remote later\n"), []string{"prompt-only"})
}

func TestScoutFetchRiskA4JSONEscapedURL(t *testing.T) {
	fxRiskIs(t, "escaped url", fxOne(".claude-plugin/plugin.json", `{"url": "https:\/\/mcp.example\/v1"}`), "mcp,network")
}

func TestScoutFetchRiskA5ParenInURLPath(t *testing.T) {
	fxHas(t, "flags", fxMcpFlags("SKILL.md", "---\nname: a\n---\nhttps://example.com/a_(b)/mcp\n"), "mcp")
}

func TestScoutFetchRiskA6AllowedToolsMcp(t *testing.T) {
	for _, tools := range []string{"allowed-tools: Read, mcp__asana__create_task", "allowed-tools:\n  - mcp__github"} {
		fxHas(t, tools, fxMcpFlags("commands/a.md", "---\n"+tools+"\n---\nbody\n"), "mcp")
	}
	eqStrings(t, "prose", fxMcpFlags("commands/a.md", "---\nallowed-tools: Read\n---\nuse mcp__x in prose\n"), []string{"prompt-only"})
}

func TestScoutFetchRiskA7McpserversRemoteURLWithoutMcpInIt(t *testing.T) {
	text := `{"mcpServers": {"a": {"type": "http", "url": "https://api.example.com/v1"}}}`
	fxRiskIs(t, "remote", fxOne(".claude-plugin/plugin.json", text), "mcp,network")
	fxRiskIs(t, "local", fxOne(".claude-plugin/plugin.json", `{"mcpServers": {"a": {"command": "x"}}}`), "mcp")
}

// --- T-0377 part B: 'scripts' by real executability ---

func TestScoutFetchRiskBInertFilesDoNotFlagScripts(t *testing.T) {
	for _, path := range []string{".gitignore", ".gitattributes", "assets/logo.svg", "a.png", "LICENSE.txt", "LICENSE", "fonts/a.woff2",
		"config.yaml", "settings.toml", "data.json"} {
		res := fxCls(fxP(path, "SKILL.md"), map[string]string{path: "x", "SKILL.md": fxSkillMD})
		notContains(t, path+" risk", res.Risk, "scripts")
		notContains(t, path+" risk", res.Risk, "unknown")
	}
}

func TestScoutFetchRiskBMcpExampleJSONIsMcpNotScripts(t *testing.T) {
	res := fxCls(fxP(".mcp.example.json", "SKILL.md"), map[string]string{"SKILL.md": fxSkillMD})
	fxRiskIs(t, "mcp example", res, "mcp")
	fxHas(t, "detail", res.Detail, "mcp: .mcp.example.json")
}

func TestScoutFetchRiskBExecutablesStillFlagScripts(t *testing.T) {
	for _, path := range []string{"x.sh", "tool.py", "a.js", "bin.exe", "lib.so", "x.wasm"} {
		fxRiskIs(t, path, fxCls(fxP(path, "SKILL.md"), map[string]string{"SKILL.md": fxSkillMD}), "scripts")
	}
	tree := fxTree(fxP("SKILL.md", "bin/run"), fxSpec{"bin/run": {"100755", "blob"}})
	fxRiskIs(t, "mode 755", sc.Classify(tree, "", func(sc.Entry) (string, error) { return fxSkillMD, nil }, false), "scripts")
	res := fxCls(fxP("SKILL.md", "bin/run"), map[string]string{"SKILL.md": fxSkillMD, "bin/run": "#!/usr/bin/env bash\necho hi\n"})
	fxRiskIs(t, "shebang", res, "scripts")
	fxHas(t, "detail", res.Detail, "scripts: bin/run")
}

func TestScoutFetchRiskBCommandLaunchingConfigsAreNeverPromptOnly(t *testing.T) {
	lsp := `{"go": {"command": "gopls"}}`
	for _, c := range []struct{ name, path, text, want string }{
		{".lsp.json", ".lsp.json", lsp, "scripts"},
		{"lspServers file", "lsp/servers.json", lsp, "scripts"},
		{".vscode/mcp.json", ".vscode/mcp.json", "{\"servers\": {\"x\": {\n\"command\": \"npx\"\n}}}", "scripts,mcp"},
		{"codex toml", ".codex/config.toml", "[mcp_servers.x]\ncommand = \"npx\"\n", "scripts,mcp"},
	} {
		fxRiskIs(t, c.name, fxCls(fxP(c.path, "SKILL.md"), map[string]string{c.path: c.text, "SKILL.md": fxSkillMD}), c.want)
	}
	res := fxCls(fxP(".claude-plugin/plugin.json", "SKILL.md"),
		map[string]string{".claude-plugin/plugin.json": `{"name": "p", "lspServers": "./lsp/servers.json"}`, "SKILL.md": fxSkillMD})
	contains(t, "lspServers", res.Risk, "scripts")
	fxRiskIs(t, "inert", fxCls(fxP("data.json", "SKILL.md"), map[string]string{"data.json": `{"a": 1}`, "SKILL.md": fxSkillMD}), "prompt-only")
}

// fxLinearLimit is 1 s in Python; Go under -race on a loaded box needs slack. A quadratic scan of these 65k-char inputs takes tens of seconds, so 5 s still catches it.
const fxLinearLimit = 5 * time.Second

// fxLinear fails when classifying text takes a second or more: the scanners must be linear on hostile input.
func fxLinear(t *testing.T, what string, paths []string, texts map[string]string) sc.Result {
	t.Helper()
	start := time.Now()
	res := fxCls(paths, texts)
	if d := time.Since(start); d >= fxLinearLimit {
		t.Fatalf("%s: took %v, want < %v", what, d, fxLinearLimit)
	}
	return res
}

func TestScoutFetchRiskBMcpLauncherIsLinearOnHostileInput(t *testing.T) {
	for _, blob := range []string{strings.Repeat("npx ", 16384), strings.Repeat("pipx ", 13107), "npx" + strings.Repeat(" ", 65000)} {
		res := fxLinear(t, blob[:8], fxP("SKILL.md"), map[string]string{"SKILL.md": blob})
		fxRiskIs(t, blob[:8], res, "prompt-only")
	}
	contains(t, "real launcher", fxOne("SKILL.md", "npx -y mcp-remote https://x/sse").Risk, "mcp")
}

func TestScoutFetchRiskBEscapedJSONCommandKeyIsScripts(t *testing.T) {
	for _, c := range []struct{ name, path, text, want string }{
		{"vscode", ".vscode/mcp.json", `{"servers": {"x": {"\u0063ommand": "npx", "args": ["y"]}}}`, "scripts,mcp"},
		{"plain", "conf/servers.json", `{"servers": {"x": {"\u0063ommand": "gopls"}}}`, "scripts"},
		{"in list", "conf/list.json", `{"a": [{"\u0063ommand": "x"}]}`, "scripts"},
		{"jsonc comment", "conf/c.jsonc", "// c\n{\"x\": {\"\\u0063ommand\": \"x\"}}", "scripts"},
	} {
		fxRiskIs(t, c.name, fxCls(fxP(c.path, "SKILL.md"), map[string]string{c.path: c.text, "SKILL.md": fxSkillMD}), c.want)
	}
}

func TestScoutFetchRiskBTomlCommandKeyInlineOrQuotedIsScripts(t *testing.T) {
	for name, text := range map[string]string{"inline": `servers = { x = { command = "npx" } }`, "quoted key": "[servers.x]\n\"command\" = \"npx\""} {
		fxRiskIs(t, name, fxCls(fxP("config.toml", "SKILL.md"), map[string]string{"config.toml": text, "SKILL.md": fxSkillMD}), "scripts")
	}
}

func TestScoutFetchRiskBTomlYamlDottedEscapedAndBomCommandKeysAreScripts(t *testing.T) {
	for _, c := range []struct{ name, path, text string }{
		{"toml dotted", "c.toml", `x.command = "npx"`},
		{"toml inline dotted", "c.toml", `a = { b.command = "x" }`},
		{"toml escaped", "c.toml", `"\u0063ommand" = "x"`},
		{"toml bom", "c.toml", "\ufeffcommand = \"x\""},
		{"yaml escaped", "c.yaml", `"\u0063ommand": x`},
		{"yaml bom", "c.yaml", "\ufeffcommand: x"},
	} {
		fxRiskIs(t, c.name, fxCls(fxP(c.path, "SKILL.md"), map[string]string{c.path: c.text, "SKILL.md": fxSkillMD}), "scripts")
	}
}

func TestScoutFetchRiskBYamlCommandKeyBlockListOrFlowIsScripts(t *testing.T) {
	for _, c := range []struct{ name, path, text, want string }{
		{"block", "mcp.yaml", "servers:\n  x:\n    command: npx\n    args: [y]", "scripts,mcp"},
		{"list item", "servers.yml", "servers:\n  - name: x\n    command: npx", "scripts"},
		{"flow", "servers.yml", "servers: {x: {command: npx}}", "scripts"},
	} {
		fxRiskIs(t, c.name, fxCls(fxP(c.path, "SKILL.md"), map[string]string{c.path: c.text, "SKILL.md": fxSkillMD}), c.want)
	}
}

func TestScoutFetchRiskBConfigWithoutACommandKeyStaysPromptOnly(t *testing.T) {
	for path, text := range map[string]string{
		"skills/a/data.json": `{"items": [1,2], "note": "run the command later"}`,
		"skills/a/meta.yaml": "name: a\ndescription: explains the command line\n",
		"skills/a/meta.toml": "name = \"a\"\ncommands = [\"x\"]\n",
	} {
		res := fxCls(fxP("skills/a/SKILL.md", path), map[string]string{"skills/a/SKILL.md": fxSkillMD, path: text})
		fxRiskIs(t, path, res, "prompt-only")
	}
}

func TestScoutFetchRiskBCommandKeyScanIsLinearOnHostileConfigs(t *testing.T) {
	blobs := []string{strings.Repeat("{command", 8192), strings.Repeat("- ", 32768), strings.Repeat(`\u0063`, 10922), strings.Repeat("[", 60000)}
	for _, ext := range []string{".json", ".yaml", ".toml"} {
		for _, blob := range blobs {
			path := "skills/a/c" + ext
			fxLinear(t, ext+blob[:8], fxP("skills/a/SKILL.md", path), map[string]string{"skills/a/SKILL.md": fxSkillMD, path: blob})
		}
	}
}

func TestScoutFetchRiskBUnrecognisedTypeIsUnknown(t *testing.T) {
	res := fxCls(fxP("SKILL.md", "thing.xyz", "bin/tool"), map[string]string{"SKILL.md": fxSkillMD, "thing.xyz": "data", "bin/tool": "plain"})
	fxRiskIs(t, "unrecognised", res, "unknown")
	fxHas(t, "detail", res.Detail, "unknown: unrecognised file type: thing.xyz")
	if n := strings.Count(sc.Classify(fxTree(fxP("SKILL.md", "thing.xyz"), nil), "", nil, false).Risk, "unknown"); n != 1 {
		t.Fatalf("unknown appears %d times, want 1", n)
	}
}

func TestScoutFetchRiskScopePrefix(t *testing.T) {
	paths := fxP("skills/a/SKILL.md", "skills/b/SKILL.md", "skills/b/x.sh", "tool.py")
	texts := map[string]string{"skills/a/SKILL.md": fxSkillMD, "skills/b/SKILL.md": fxSkillMD}
	fxRiskIs(t, "a", fxClsX(paths, texts, "skills/a", nil), "prompt-only")
	fxRiskIs(t, "b", fxClsX(paths, texts, "skills/b", nil), "scripts")
	fxRiskIs(t, "c", fxClsX(paths, texts, "skills/c", nil), "unknown")
}

func TestScoutFetchRiskUndecidableIsUnknownNeverPromptOnly(t *testing.T) {
	tree := fxTree(fxP("SKILL.md"), nil)
	fxRiskIs(t, "no reader", sc.Classify(tree, "", nil, false), "unknown")
	res := sc.Classify(tree, "", func(sc.Entry) (string, error) { return "", &sc.ReadError{Msg: "boom"} }, false)
	fxRiskIs(t, "boom", res, "unknown")
	contains(t, "detail", res.Detail[0], "unreadable")
	fxRiskIs(t, "truncated", sc.Classify(tree, "", func(sc.Entry) (string, error) { return fxSkillMD, nil }, true), "unknown")
}

func TestScoutFetchRiskReadLimitsAndDetailCap(t *testing.T) {
	reads := 0
	var paths []string
	for i := 0; i < 14; i++ {
		paths = append(paths, fmt.Sprintf("commands/c%02d.md", i))
	}
	res := sc.Classify(fxTree(paths, nil), "", func(sc.Entry) (string, error) { reads++; return "text", nil }, false)
	if reads != 10 {
		t.Fatalf("reads = %d, want 10", reads)
	}
	fxRiskIs(t, "14 commands", res, "unknown")
	var sh []string
	for i := 0; i < 30; i++ {
		sh = append(sh, fmt.Sprintf("s%02d.sh", i))
	}
	if n := len(sc.Classify(fxTree(sh, nil), "", nil, false).Detail); n != 20 {
		t.Fatalf("detail lines = %d, want 20", n)
	}
}

func TestScoutFetchRiskKind(t *testing.T) {
	for _, c := range []struct {
		paths []string
		texts map[string]string
		want  string
	}{
		{fxP("SKILL.md"), map[string]string{"SKILL.md": fxSkillMD}, "skill"},
		{fxP("skills/a/SKILL.md", "skills/b/SKILL.md"), map[string]string{"skills/a/SKILL.md": "", "skills/b/SKILL.md": ""}, "collection"},
		{fxP(".claude-plugin/plugin.json"), map[string]string{".claude-plugin/plugin.json": "{}"}, "plugin"},
		{fxP("agents/a.md"), map[string]string{"agents/a.md": "x"}, "agent"},
	} {
		if got := fxCls(c.paths, c.texts).Kind; got != c.want {
			t.Errorf("%v: kind = %q, want %q", c.paths, got, c.want)
		}
	}
}

func TestScoutFetchRiskHasManifest(t *testing.T) {
	for _, c := range []struct {
		paths []string
		want  bool
	}{{fxP("README.md", "src/a.py"), false}, {fxP("SKILL.md"), true}, {fxP("x/y/SKILL.md"), true},
		{fxP(".claude-plugin/plugin.json"), true}, {fxP("agents/a.md"), true}, {fxP("commands/a.md"), true},
		{fxP("docs/commands/a.txt"), false}} {
		if got := sc.HasManifest(fxTree(c.paths, nil), ""); got != c.want {
			t.Errorf("HasManifest(%v) = %v, want %v", c.paths, got, c.want)
		}
	}
	if sc.HasManifest(fxTree(fxP("skills/a/SKILL.md"), nil), "skills/b") {
		t.Error("HasManifest ignores the scope prefix")
	}
}
