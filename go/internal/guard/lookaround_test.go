package guard

import (
	"strings"
	"testing"
)

// Table tests for the hand-written lookaround matchers in lookaround.go. Every expected value was produced by
// the Python regex of roles/hooks/worker-guard.py (CPython 3.13), loaded like ci/guard_golden.py loads the hook:
//
//	m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
//	m.PRIV_CODE.search(t) / m.PERSIST_CODE.search(t) / m.CFG_ENV_ASSIGN.search(t) / m.CFG_CODE.search(t) / m.R1.search(t)
//	m.R3_REDIR.search(t) / m.HERDR_WORD.search(t) / m.EVAL_HOME_PREFIX.match(t) / m.persist_code(t) / m.Analyzer.cfg_code(t)
//	re.search(r"(?<![\w.-])(sudo|su|doas|pkexec|docker)(?![\w-])", t)      (priv_subst, line 397)
//	re.sub(r"(?<![\w$])\d+(?=[<>])", " ", t)                                (line 989)
//	re.sub(r"^(~|\$HOME|\$\{HOME\})(?=/|$)", "/H", t)                        (lines 1229, 3055)
//
// Cases cover the boundary characters of each lookaround, the end/start of the text, and non-ASCII neighbours
// (Python's \w, \d, \s and \b are Unicode-aware).

type strCase struct {
	in   string
	want bool
}

type matchCase struct {
	in   string
	want string
	ok   bool
}

type textCase struct{ in, want string }

func runBool(t *testing.T, name string, f func(string) bool, cases []strCase) {
	t.Helper()
	if len(cases) < 6 {
		t.Fatalf("%s: only %d cases", name, len(cases))
	}
	for _, c := range cases {
		if got := f(c.in); got != c.want {
			t.Errorf("%s(%q) = %v, want %v", name, c.in, got, c.want)
		}
	}
}

// TestLookaroundPersistCode: PERSIST_CODE (worker-guard.py:151-152)
func TestLookaroundPersistCode(t *testing.T) {
	runBool(t, "hasPersistCode", hasPersistCode, []strCase{
		{".bashrc", true},
		{"cat .bashrc", true},
		{"x.bashrc", false},
		{"-.bashrc", false},
		{"_.bashrc", false},
		{".bashrc2", false},
		{".bashrc.bak", false},
		{".bashrc-x", false},
		{".bashrc/", true},
		{".profile", true},
		{".bash_profile", true},
		{".zprofile", true},
		{".zshenv", true},
		{".zshrc", true},
		{".gitconfig", true},
		{".gitconfig.d", false},
		{"ï.bashrc", false},
		{".bashrcé", false},
		{"日.bashrc", false},
		{".config/git", true},
		{".config/git/x", true},
		{".config/gitx", false},
		{".config/git-x", false},
		{".config/git_x", false},
		{"x.config/git", true},
		{"/etc/gitconfig", true},
		{"/etc/gitconfigx", true},
		{".config/gité", false},
		{"a/.bashrc", true},
		{"~/.bashrc;", true},
		{".bashrc ", true},
		{".profiles", false},
		{".xprofile", false},
		{"my.profile", false},
	})
}

// TestLookaroundPersistCodeFn: persist_code (worker-guard.py:219-220)
func TestLookaroundPersistCodeFn(t *testing.T) {
	runBool(t, "PersistCode", PersistCode, []strCase{
		{".bashrc", true},
		{".ba'shrc", true},
		{".ba\"shrc", true},
		{".ba+shrc", true},
		{"'.bashrc'", true},
		{"x+.bashrc", true},
		{"a.b'ashrc", false},
		{".config/g'it", true},
		{"/etc/git'config", true},
		{"ls", false},
		{".bashrc2", false},
		{".bash'rc2", false},
		{".ba'shrc'x", false},
	})
}

// TestLookaroundCfgEnvAssign: CFG_ENV_ASSIGN (worker-guard.py:153)
func TestLookaroundCfgEnvAssign(t *testing.T) {
	runBool(t, "HasCfgEnvAssign", HasCfgEnvAssign, []strCase{
		{"HOME=/x", true},
		{"x HOME=/x", true},
		{"xHOME=/x", false},
		{"_HOME=/x", false},
		{"XDG_CONFIG_HOME=/x", true},
		{"GIT_CONFIG=/x", true},
		{"GIT_CONFIG_GLOBAL=/x", true},
		{"GIT_CONFIGé=/x", true},
		{"GIT_CONFIG_=", true},
		{"GIT_CONFIG =", false},
		{"HOME =", false},
		{"ïHOME=", false},
		{"日HOME=", false},
		{"-HOME=", true},
		{".HOME=", true},
		{"export HOME=", true},
		{"A_XDG_CONFIG_HOME=", false},
		{"GIT_CONFIG", false},
		{"HOMEX=", false},
		{"ZGIT_CONFIG=", false},
		{"1HOME=", false},
		{"(HOME=", true},
		{"GIT_CONFIG_COUNT=1", true},
	})
}

// TestLookaroundCfgCode: CFG_CODE (worker-guard.py:225)
func TestLookaroundCfgCode(t *testing.T) {
	runBool(t, "hasCfgCode", hasCfgCode, []strCase{
		{".config/factory", true},
		{"x.config/factory", true},
		{"run/settings", true},
		{"xrun/settings", true},
		{"live_home", true},
		{"live_homes", false},
		{"xlive_home", false},
		{"_live_home", false},
		{"live_home_", false},
		{"live_home1", false},
		{"live_home.", true},
		{"(live_home)", true},
		{"ïlive_home", true},
		{"live_homeé", true},
		{"日live_home", true},
		{"live-home", false},
		{"live_ home", false},
		{"~/.config/factoryx", true},
		{"a live_home", true},
	})
}

// TestLookaroundCfgCodeWord: Analyzer.cfg_code (worker-guard.py:1232-1235)
func TestLookaroundCfgCodeWord(t *testing.T) {
	runBool(t, "CfgCode", CfgCode, []strCase{
		{"'live_'+'home'", true},
		{"live_ home", true},
		{"live_,home", true},
		{"\"live\"+\"_home\"", true},
		{".con fig/factory", true},
		{"run/ settings", true},
		{"a live_home b", true},
		{"live_homes", false},
		{"xlive_home", false},
		{"'.config/'+'factory'", true},
		{"nothing", false},
		{"live _home", true},
		{"live\n_home", true},
		{"live_\"home\"x", false},
	})
}

// TestLookaroundR1: R1 (worker-guard.py:254)
func TestLookaroundR1(t *testing.T) {
	runBool(t, "MatchR1", MatchR1, []strCase{
		{"herdr server stop", true},
		{"herdr serverx", false},
		{"herdr server", false},
		{"herdr server  ", false},
		{"herdr server x", true},
		{"herdr agent start", true},
		{"herdr pane kill", true},
		{"herdr tab close", true},
		{"herdr workspace create", true},
		{"herdr worktree remove", true},
		{"herdr pane kills", false},
		{"herdr pane 'start'", true},
		{"herdr pane \"start\"", true},
		{"herdr pane\\start", true},
		{"tests/stubs/herdr server stop", false},
		{"xtests/stubs/herdr server stop", false},
		{"xherdr server stop", false},
		{"herdrx server stop", false},
		{"herdr_ server stop", false},
		{"ïherdr server stop", false},
		{"herdrï server stop", false},
		{"日本語herdr server stop", false},
		{"herdr\nserver stop", false},
		{"herdr x\nserver stop", false},
		{"herdr a server x", true},
		{"herdr aserver x", false},
		{"herdr a-server x", true},
		{"herdr a_server x", false},
		{"herdr xpane start", false},
		{"herdr pane  start", true},
		{"herdr pane\tnew", true},
		{"herdr panes start", false},
		{"herdr pane starté", false},
		{"herdr pane start_", false},
		{"herdr pane start-x", true},
		{"herdr pane start.", true},
		{"herdr  server x", true},
		{"herdr server x", true},
		{"herdr server　　x", true},
		{"a herdr b server c", true},
		{"herdr tests/stubs/herdr server x", true},
		{"herdr x tests/stubs/herdr server x", true},
		{"tests/stubs/herdr x herdr pane run", true},
		{"herdr; ls; herdr pane send", true},
		{"herdrserver x", false},
		{"herdr agentstart", false},
		{"herdr agent'start", true},
		{"herdr agent\"start\"", true},
		{"herdr tab\\\nnew", true},
		{"herdr server\n x", true},
	})
}

// TestLookaroundR3Redir: R3_REDIR (worker-guard.py:267)
func TestLookaroundR3Redir(t *testing.T) {
	runBool(t, "MatchR3Redir", MatchR3Redir, []strCase{
		{"< herdr", false},
		{"< /usr/bin/herdr", true},
		{"</usr/bin/herdr", true},
		{"< \"/usr/bin/herdr\"", true},
		{"< '/usr/bin/herdr'", true},
		{"<'/x/herdr'", true},
		{"< /x/herdrx", false},
		{"< /x/herdr-x", true},
		{"< /x/herdr.bin", true},
		{"< /x/tests/stubs/herdr", false},
		{"< tests/stubs/herdr", false},
		{"< /x/herdr tests/stubs/", true},
		{"< $HERDR_BIN_PATH", true},
		{"< ${HERDR_BIN_PATH}", true},
		{"<$HERDR_BIN_PATH", true},
		{"< \"$HERDR_BIN_PATH\"", true},
		{"< /a/${HERDR_BIN_PATH", true},
		{"< $ HERDR_BIN_PATH", false},
		{"< /x/herdré", false},
		{"< /x/herdr日", false},
		{"< /x/herdr_", false},
		{"cat < /x/herdr && ls", true},
		{"cat < /x/herdr", true},
		{"cat <\n/x/herdr", true},
		{"cat < /x/herdr", true},
		{"cat <　/x/herdr", true},
		{"<< /x/herdr", true},
		{"<<< /x/herdr", true},
		{"< a /x/herdr", false},
		{"< 'a b/herdr'", false},
		{"< \"/x/tests/stubs/herdr\"", false},
		{"< x/tests/stubs/y/herdr", false},
		{"< /x/herdr/", true},
		{"< /x/herdr\"", true},
		{"< /herdr /herdrx", true},
		{"< /herdrx/herdr", true},
		{"< /herdrx/herdr/y", true},
		{"< \"tests/stubs/x\" /herdr", false},
		{"< /x/herdr < /y", true},
	})
}

// TestLookaroundHerdrWord: HERDR_WORD (worker-guard.py:268)
func TestLookaroundHerdrWord(t *testing.T) {
	runBool(t, "HasHerdrWord", HasHerdrWord, []strCase{
		{"herdr", true},
		{"xherdr", false},
		{"herdrx", false},
		{"herdr_", false},
		{"_herdr", false},
		{"-herdr", true},
		{"herdr-x", true},
		{"tests/stubs/herdr", false},
		{"xtests/stubs/herdr", false},
		{"tests/stubs/herdrx", false},
		{"a tests/stubs/herdr herdr", true},
		{"ïherdr", false},
		{"herdrï", false},
		{"日本語herdr", false},
		{"herdr日本語", false},
		{"herdr.sock", true},
		{"/usr/bin/herdr", true},
		{"'herdr'", true},
		{"herdr1", false},
		{"1herdr", false},
		{"tests/stubs/ herdr", true},
		{"tests/stubs//herdr", true},
		{"TESTS/stubs/herdr", true},
		{"x/tests/stubs/herdr", false},
		{"herd", false},
		{"herdr x", true},
		{"a herdr", true},
	})
}

// TestLookaroundEvalHomePrefix: EVAL_HOME_PREFIX (worker-guard.py:2243)
func TestLookaroundEvalHomePrefix(t *testing.T) {
	runBool(t, "MatchEvalHomePrefix", MatchEvalHomePrefix, []strCase{
		{"FACTORY_HOME=/tmp/x factory ls", true},
		{"  FACTORY_HOME=/tmp/x factory ls", true},
		{"\nFACTORY_HOME=/tmp/x factory ls", true},
		{"FACTORY_HOME=\"/tmp/a b\" factory x", true},
		{"FACTORY_HOME='/tmp/a b' factory x", true},
		{"FACTORY_HOME=/tmp/x  factory x", true},
		{"FACTORY_HOME=/tmp/x\tfactory x", true},
		{"FACTORY_HOME=/tmp/x\nfactory x", true},
		{"FACTORY_HOME=/tmp/x factory", false},
		{"FACTORY_HOME=/tmp/x factory\n", true},
		{"FACTORY_HOME=/tmp/x factoryx", false},
		{"FACTORY_HOME=/tmp/x factory x", true},
		{"FACTORY_HOME=/tmp/x factory　x", true},
		{"FACTORY_HOME= factory x", false},
		{"FACTORY_HOME=\"\" factory x", true},
		{"FACTORY_HOME='' factory x", true},
		{"x FACTORY_HOME=/tmp/x factory x", false},
		{"FACTORY_HOME=/tmp/$X factory x", false},
		{"FACTORY_HOME=\"/tmp/$X\" factory x", false},
		{"FACTORY_HOME=\"/tmp/a\\b\" factory x", false},
		{"FACTORY_HOME='a\"b' factory x", true},
		{"FACTORY_HOME=\"a'b\" factory x", true},
		{"FACTORY_HOME=a;b factory x", false},
		{"FACTORY_HOME=/tmp/ï factory x", true},
		{"FACTORY_HOME=\"/tmp/日本\" factory x", true},
		{"FACTORY_HOME=/tmp/x factory x; FACTORY_HOME=/y factory z", true},
		{"FACTORY_HOME=\"/tmp/x\"factory x", false},
		{"FACTORY_HOME='x'  \t factory  ls", true},
		{" FACTORY_HOME=/t factory x", true},
		{"FACTORY_HOME=/t `x` factory x", false},
		{"FACTORY_HOME=\"a\" b factory x", false},
		{"FACTORY_HOME=a'b' factory x", false},
		{"FACTORY_HOME=\"a factory x", false},
		{"FACTORY_HOME='a factory x", false},
	})
}

// TestLookaroundPrivCode: PRIV_CODE (group 0) (worker-guard.py:99)
func TestLookaroundPrivCode(t *testing.T) {
	cases := []matchCase{
		{"sudo ls", "sudo", true},
		{"x sudo", "sudo", true},
		{"xsudo", "", false},
		{"sudoers", "", false},
		{"sudo-x", "", false},
		{"./sudo", "", false},
		{"a/sudo", "", false},
		{"a-sudo", "", false},
		{"a.sudo", "", false},
		{"(sudo)", "sudo", true},
		{"ïsudo", "", false},
		{"sudoé", "", false},
		{"日本語sudo", "", false},
		{"docker run", "docker", true},
		{"x docker", "docker", true},
		{"doas", "doas", true},
		{"pkexec x", "pkexec", true},
		{"su -", "su", true},
		{"su root", "su", true},
		{"su  -l", "su", true},
		{"su\n-", "su", true},
		{"su root2", "", false},
		{"su rooté", "", false},
		{"su", "", false},
		{"su x", "", false},
		{"xsu -", "", false},
		{"su-", "", false},
		{"'su'", "'su'", true},
		{"\"su\"", "\"su\"", true},
		{"'su\"", "'su\"", true},
		{"\"su", "", false},
		{"su'", "", false},
		{"echo 'su' x", "'su'", true},
		{"a su -", "su", true},
		{"ésu -", "", false},
		{"su -é", "su", true},
		{"sudo", "sudo", true},
		{"dockers", "", false},
		{"docker-compose", "", false},
		{"docker_x", "", false},
		{"_sudo", "", false},
		{"1sudo", "", false},
		{"x=sudo", "sudo", true},
		{"'sudo'", "sudo", true},
		{"$(sudo)", "sudo", true},
	}
	for _, c := range cases {
		if got, ok := FindPrivCode(c.in); got != c.want || ok != c.ok {
			t.Errorf("FindPrivCode(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestLookaroundPrivSubst: priv_subst (group 1) (worker-guard.py:397)
func TestLookaroundPrivSubst(t *testing.T) {
	cases := []matchCase{
		{"$(sudo ls)", "sudo", true},
		{"`su`", "su", true},
		{"$(echo sudo)", "sudo", true},
		{"$(xsudo)", "", false},
		{"$(a.sudo)", "", false},
		{"$(sudo-x)", "", false},
		{"$(sudoers)", "", false},
		{"$(su -)", "su", true},
		{"$(docker)", "docker", true},
		{"$(ïsudo)", "", false},
		{"$(sudoé)", "", false},
		{"$(a/sudo)", "sudo", true},
		{"$(a-docker)", "", false},
		{"$(pkexec)", "pkexec", true},
		{"$(doas x)", "doas", true},
		{"sudo", "sudo", true},
		{"su", "su", true},
		{"$(sudo", "sudo", true},
		{"ssu", "", false},
		{"$(_su)", "", false},
	}
	for _, c := range cases {
		if got, ok := FindPrivSubst(c.in); got != c.want || ok != c.ok {
			t.Errorf("FindPrivSubst(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestLookaroundBlankRedirectFds: digits before a redirect (worker-guard.py:989)
func TestLookaroundBlankRedirectFds(t *testing.T) {
	cases := []textCase{
		{"2>&1", " >&1"},
		{"ls 2>x", "ls  >x"},
		{"a2>x", "a2>x"},
		{"$2>x", "$2>x"},
		{"_2>x", "_2>x"},
		{"é2>x", "é2>x"},
		{"22>x", " >x"},
		{"x 22>x", "x  >x"},
		{" 22>x", "  >x"},
		{"2<x", " <x"},
		{"2 >x", "2 >x"},
		{"12", "12"},
		{"2", "2"},
		{">2", ">2"},
		{"x>2>y", "x> >y"},
		{"1>&2 2>&1", " >&2  >&1"},
		{"٣>x", " >x"},
		{"a ٣>x", "a  >x"},
		{"x٣>x", "x٣>x"},
		{"ab٣٣>x", "ab٣٣>x"},
		{"a 12>x 3>y", "a  >x  >y"},
		{"$(2>x)", "$( >x)"},
		{"(2>x)", "( >x)"},
		{"a;2>x", "a; >x"},
		{"a&2>x", "a& >x"},
		{"echo 2>&-", "echo  >&-"},
		{"2>", " >"},
		{"<2<3", "< <3"},
		{"à2>x", "à2>x"},
		{"日2>x", "日2>x"},
		{"日 2>x", "日  >x"},
	}
	for _, c := range cases {
		if got := BlankRedirectFds(c.in); got != c.want {
			t.Errorf("BlankRedirectFds(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLookaroundExpandHome: ~ / $HOME prefix (worker-guard.py:1229, 3055)
func TestLookaroundExpandHome(t *testing.T) {
	cases := []textCase{
		{"~", "/H"},
		{"~/x", "/H/x"},
		{"~x", "~x"},
		{"$HOME", "/H"},
		{"$HOME/x", "/H/x"},
		{"$HOMEx", "$HOMEx"},
		{"${HOME}", "/H"},
		{"${HOME}/x", "/H/x"},
		{"${HOME}x", "${HOME}x"},
		{"x~", "x~"},
		{" ~", " ~"},
		{"~\n", "/H\n"},
		{"~\n\n", "~\n\n"},
		{"~/", "/H/"},
		{"$HOME\n", "/H\n"},
		{"$HOME\nx", "$HOME\nx"},
		{"~~", "~~"},
		{"~é", "~é"},
		{"$HOMEé", "$HOMEé"},
		{"~ ", "~ "},
		{"", ""},
		{"\n~", "\n~"},
		{"${HOME", "${HOME"},
		{"$HOME/", "/H/"},
		{"$HOME//", "/H//"},
	}
	for _, c := range cases {
		if got := ExpandHome(c.in, "/H"); got != c.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Helpers for the \b / \w regexes of preprocess (worker-guard.py:~700). Expected values: the Python regexes
// re.search(r"\b(python[\d.]*|perl|ruby|node|php|lua)\b", t), INTERPRETERS.search(t) and
// re.search(r"\b(sh|bash|...|eval|source)\b", t).
func TestWordBoundaryHelpers(t *testing.T) {
	runBool(t, "hasInterpreterWord", hasInterpreterWord, []strCase{
		{"python", true},
		{"python3", true},
		{"python3.12", true},
		{"pythonü", false},
		{"éperl", false},
		{"python3é", false},
		{"xnode", false},
		{"node", true},
		{"perl ruby", true},
		{"ruby.", true},
		{"nodejs", false},
		{"python٣", true},
		{"x python3 y", true},
		{"(perl)", true},
		{"lua5", false},
		{"bun", false},
		{"日本語php", false},
		{"php日本語", false},
		{"python..", true},
		{"python3.", true},
		{"python 3", true},
		{"", false},
	})
	runBool(t, "interpreterFull", interpreterFull, []strCase{
		{"python", true},
		{"python3", true},
		{"python3.12", true},
		{"pythonü", false},
		{"perl", true},
		{"node\n", true},
		{"nodejs", true},
		{"deno", true},
		{"bun", true},
		{"x", false},
		{"", false},
		{"python3 ", false},
		{"python٣", true},
		{"perl ruby", false},
	})
	runBool(t, "hasWord", func(s string) bool { return hasWord(s, Shells, "eval", "source") }, []strCase{
		{"bash -c x", true},
		{"xbash", false},
		{"bashé", false},
		{"ébash", false},
		{"eval", true},
		{"evals", false},
		{"source x", true},
		{"a.sh", true},
		{"sh", true},
		{"/bin/sh", true},
		{"日本語sh", false},
		{"sh日本語", false},
		{"ls | sh", true},
		{"re_sh", false},
		{"", false},
	})
	for in, want := range map[string]string{"a.b c": "a.b|c", "x-y": "x|y", "é.ü ï": "é.ü|ï", "日本 語": "日本|語", ".": ".", "": ""} {
		if got := strings.Join(wordDotRuns(in), "|"); got != want {
			t.Errorf("wordDotRuns(%q) = %q, want %q", in, got, want)
		}
	}
}
