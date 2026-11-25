//go:build linux

package sandbox2

// Go port of tests/test_nsbox_allow.py (46 cases): the allow-list mode of the nsbox view (additive make_cfg options,
// pid=False, keep_env, rw/ro/masks view, fail closed). Group A needs no namespaces; group B runs real stage-1
// namespaces and skips with the reason where user namespaces are unavailable.
//
// Go-side differences, each case still has a real test:
//   - The Python probes (`python -c ...`) are the in-box probe of nsbox_helpers_test.go (this test binary is stage 1
//     and the probe, so no python3).
//   - Go's Opts cannot tell TmpfsSize "" from "not given": "" as the whole-box size means the default there, so
//     TestNsboxAllowBadTmpfsSizeIsRefused checks "" through TmpfsSizes and SizeOK.
//   - Go has no Python-style patchable seams inside stage 1: the three "swapped after the sources are open" and
//     "_open_chain" cases are run against the real stage 1 with the swap done between MakeCfg and the run (the
//     window the Python hook widened); the assertion is the same: refused, path named, target never ran.
//   - The Python golden dict has python/nsbox/egress paths; Go's Cfg has one Self key, so the golden is the Cfg value.
//   - TestNsboxAllowWithoutTheMaskTheHostSocketIsReachable picks a host /run socket this process can connect to (the
//     Python test assumed /run/docker.sock).
//
// not ported: (none) - every case has a Go test.

import (
	"os"
	"reflect"
	"strings"
	"testing"

	sbx "factory/go/internal/sandbox"
)

// --- group A: no namespaces ---

// nsbGolden is the Cfg origin/main's make_cfg returned for these inputs, written out by hand.
func nsbGolden(trial, trialsRoot, home, runtime string) *sbx.Cfg {
	return &sbx.Cfg{
		Unshare: nsbWhich("unshare"), Setpriv: nsbWhich("setpriv"), Self: "/nsb/self", UID: os.Getuid(), GID: os.Getgid(),
		Trial: trial, TrialsRoot: trialsRoot,
		HideDirs:  []string{home + "/.claude", home + "/.config", home + "/.ssh", home + "/.gnupg", home + "/factory", runtime},
		HideFiles: []string{home + "/.git-credentials", home + "/.netrc"},
		Keep:      []string{trial}, KeepRO: nil, EnvSet: map[string]string{},
		Scrub: append([]string{"SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"}, sbx.NetEnv...),
	}
}

func nsbWhich(name string) string {
	if p, err := lookPath(name); err == nil {
		return p
	}
	return name
}

func nsbGoldenOpts(t *testing.T) (root, trial string, o sbx.Opts) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	root = nsbRoot(t)
	trial = root + "/trials/t1"
	o = sbx.NewOpts(trial)
	o.Home, o.Self = "/h", "/nsb/self"
	return root, trial, o
}

func nsbDiff(t *testing.T, got, want *sbx.Cfg) {
	t.Helper()
	// nil and empty slices are the same JSON ([]); normalise through the wire shape before comparing
	g, w := *got, *want
	for _, c := range []*sbx.Cfg{&g, &w} {
		if len(c.KeepRO) == 0 {
			c.KeepRO = nil
		}
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("cfg mismatch\n got: %+v\nwant: %+v", g, w)
	}
}

func TestNsboxAllowRunContainedArgsReproduceMain(t *testing.T) {
	root, trial, o := nsbGoldenOpts(t)
	cwd := trial + "/w"
	o.TrialsRoot, o.VendorDir, o.Keep, o.Net, o.Cwd = root+"/trials", "/v", []string{cwd}, true, cwd
	got := nsbMake(t, o)
	want := nsbGolden(trial, root+"/trials", "/h", "/run/user/4242")
	want.Net, want.KeepRO, want.Keep, want.Cwd = true, []string{"/v"}, []string{trial, cwd}, &cwd
	nsbDiff(t, got, want)
}

func TestNsboxAllowWriteShimArgsReproduceMain(t *testing.T) {
	root, trial, o := nsbGoldenOpts(t)
	claude := "/opt/c/claude"
	o.RealClaude, o.TrialsRoot, o.Forward = claude, "", true
	got := nsbMake(t, o)
	want := nsbGolden(trial, root+"/trials", "/h", "/run/user/4242")
	sock := trial + "/proxy.sock"
	want.Keep = []string{trial, claude, sbx.Realpath(claude)}
	want.Claude, want.ForwardSock = &claude, &sock
	want.Scrub = []string{"SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"}
	want.EnvSet = map[string]string{"HTTPS_PROXY": sbx.ProxyURL, "https_proxy": sbx.ProxyURL, "NO_PROXY": "", "no_proxy": ""}
	nsbDiff(t, got, want)
}

func nsbJSONKeys(t *testing.T, cfg *sbx.Cfg) map[string]any {
	t.Helper()
	b, err := cfg.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	m := map[string]any{}
	if err := jsonUnmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal cfg json: %v", err)
	}
	return m
}

func TestNsboxAllowNewKeysOnlyWhenNonDefault(t *testing.T) {
	_, trial, o := nsbGoldenOpts(t)
	o.Home = "/h"
	base := nsbJSONKeys(t, nsbMake(t, o))
	for _, k := range []string{"mode", "rw", "ro", "masks", "pid", "tmpfs_size", "tmpfs_sizes"} {
		if _, ok := base[k]; ok {
			t.Errorf("deny-mode cfg has key %q: %v", k, base)
		}
	}
	d := nsbOpts(trial, "/h", "")
	d.TmpfsSize, d.TmpfsSizes = "8m", map[string]string{}
	dflt := nsbMake(t, d)
	if dflt.TmpfsSize != "" || dflt.TmpfsSizes != nil {
		t.Errorf("default sizes appear in cfg: %q %v", dflt.TmpfsSize, dflt.TmpfsSizes)
	}
	keys := nsbJSONKeys(t, dflt)
	for _, k := range []string{"tmpfs_size", "tmpfs_sizes", "pid"} {
		if _, ok := keys[k]; ok {
			t.Errorf("allow cfg with default sizes has key %q", k)
		}
	}
	a := nsbOpts(trial, "/h", "")
	a.Rw, a.Ro, a.Masks, a.NoPid, a.NoRunMask = []string{"/a"}, []string{"/b"}, []string{"/b/m"}, true, true
	allow := nsbMake(t, a)
	if allow.Mode != "allow" || allow.NoPid != true {
		t.Errorf("mode=%q pid-off=%v", allow.Mode, allow.NoPid)
	}
	nsbEq(t, "rw", allow.Rw, []string{"/a"})
	nsbEq(t, "ro", allow.Ro, []string{"/b"})
	nsbEq(t, "masks", allow.Masks, []string{"/b/m"})
	if m := nsbJSONKeys(t, allow); m["pid"] != false {
		t.Errorf("json pid = %v, want false", m["pid"])
	}
}

func TestNsboxAllowTmpfsSizeKeysAppearOnlyWhenSetAndNeedAllowMode(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := nsbOpts(trial, "/h", "")
	o.TmpfsSize, o.TmpfsSizes = "16m", map[string]string{"/h/x": "1g"}
	cfg := nsbMake(t, o)
	if cfg.TmpfsSize != "16m" || !reflect.DeepEqual(cfg.TmpfsSizes, map[string]string{"/h/x": "1g"}) {
		t.Fatalf("sizes = %q %v", cfg.TmpfsSize, cfg.TmpfsSizes)
	}
	d := sbx.NewOpts(trial)
	d.Home, d.TmpfsSize = "/h", "16m"
	nsbRefused(t, "tmpfs_size in deny mode", d)
	d = sbx.NewOpts(trial)
	d.Home, d.TmpfsSizes = "/h", map[string]string{"/h": "16m"}
	nsbRefused(t, "tmpfs_sizes in deny mode", d)
}

func TestNsboxAllowBadTmpfsSizeIsRefused(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	bad := []string{"", "big", "8 m", "8m,mode=0777", "-1",
		"0", "00", "0k", "0m", "0g", "0%", // zero is an unlimited tmpfs
		"17179869184g", "18446744073709551616", "1000000g", // wrap to 0 in the kernel / absurdly big
		"17g", "16385m", "101%", "1t"}
	for _, b := range bad {
		if b != "" { // TmpfsSize "" is "not given" in Go's Opts: covered by TmpfsSizes and SizeOK below
			o := nsbOpts(trial, "/h", "")
			o.TmpfsSize = b
			nsbRefused(t, "TmpfsSize "+b, o)
		}
		o := nsbOpts(trial, "/h", "")
		o.TmpfsSizes = map[string]string{"/h": b}
		nsbRefused(t, "TmpfsSizes "+b, o)
		if sbx.SizeOK(b) {
			t.Errorf("SizeOK(%q) = true", b)
		}
	}
}

func TestNsboxAllowGoodTmpfsSizesAreAccepted(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	for _, ok := range []string{"8m", "512k", "1g", "16g", "16384M", "1%", "100%", "1"} {
		o := nsbOpts(trial, "/h", "")
		o.TmpfsSize = ok
		cfg := nsbMake(t, o)
		got := cfg.TmpfsSize
		if got == "" {
			got = "8m"
		}
		if got != ok {
			t.Errorf("tmpfs_size for %q = %q", ok, got)
		}
	}
}

func TestNsboxAllowRwWithoutAllowModeIsRefused(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := sbx.NewOpts(trial)
	o.Home, o.Rw = "/h", []string{"/a"}
	nsbRefused(t, "rw in deny mode", o)
	o = sbx.NewOpts(trial)
	o.Home, o.Mode = "/h", "bogus"
	nsbRefused(t, "mode bogus", o)
}

func TestNsboxAllowPidFalseWithForwardIsRefused(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := sbx.NewOpts(trial)
	o.Home, o.NoPid, o.Forward = "/h", true, true
	nsbRefused(t, "pid=False forward", o)
	a := nsbOpts(trial, "/h", "")
	a.Rw, a.NoPid, a.Forward = []string{trial}, true, true
	nsbRefused(t, "allow pid=False forward", a)
	n := sbx.NewOpts(trial)
	n.Home, n.NoPid, n.Net = "/h", true, true
	if cfg := nsbMake(t, n); !cfg.NoPid || cfg.PidNS() {
		t.Errorf("pid=False net=True: NoPid=%v", cfg.NoPid)
	}
	f := sbx.NewOpts(trial)
	f.Home, f.Forward = "/h", true
	if cfg := nsbMake(t, f); cfg.ForwardSock == nil || *cfg.ForwardSock == "" {
		t.Errorf("pid=True forward=True has no forward socket")
	}
}

func TestNsboxAllowVendorDirAndKeepInAllowModeAreRefused(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	a := nsbOpts(trial, "/h", "")
	a.VendorDir = "/v"
	nsbRefused(t, "vendor_dir in allow", a)
	a = nsbOpts(trial, "/h", "")
	a.Keep = []string{"/k"}
	nsbRefused(t, "keep in allow", a)
	d := sbx.NewOpts(trial)
	d.Home, d.Mode, d.VendorDir, d.Keep = "/h", "deny", "/v", []string{"/k"}
	nsbMake(t, d)
}

func TestNsboxAllowSymlinkLeafInRwOrRoIsRefused(t *testing.T) {
	root := nsbRoot(t)
	trial := root + "/trials/t1"
	real, link := root+"/real", root+"/link"
	nsbMkdir(t, trial, real)
	nsbSymlink(t, real, link)
	for _, kw := range []string{"rw", "ro"} {
		o := nsbOpts(trial, "/h", "")
		if kw == "rw" {
			o.Rw = []string{link}
		} else {
			o.Ro = []string{link}
		}
		nsbRefused(t, kw+" symlink leaf", o)
	}
	o := nsbOpts(trial, "/h", "")
	o.Rw = []string{real}
	nsbEq(t, "rw", nsbMake(t, o).Rw, []string{real})
}

func TestNsboxAllowSymlinkParentInRwOrRoIsRefused(t *testing.T) {
	root := nsbRoot(t)
	trial := root + "/trials/t1"
	real, link := root+"/realdir", root+"/linkdir"
	nsbMkdir(t, trial, real+"/sub")
	nsbSymlink(t, real, link)
	for _, kw := range []string{"rw", "ro"} {
		o := nsbOpts(trial, "/h", "")
		if kw == "rw" {
			o.Rw = []string{link + "/sub"}
		} else {
			o.Ro = []string{link + "/sub"}
		}
		nsbHas(t, kw+" error", nsbRefused(t, kw+" symlink parent", o), link)
	}
	o := nsbOpts(trial, "/h", "")
	o.Rw = []string{real + "/sub"}
	nsbEq(t, "rw", nsbMake(t, o).Rw, []string{real + "/sub"})
}

func TestNsboxAllowClaudeLinkLeafIsStillAcceptedButNotASymlinkParent(t *testing.T) {
	root := nsbRoot(t)
	trial := root + "/trials/t1"
	versions := root + "/versions"
	nsbMkdir(t, trial, root+"/bin")
	nsbWrite(t, versions+"/claude", "#!/bin/sh\n")
	link := root + "/bin/claude"
	nsbSymlink(t, versions+"/claude", link)
	o := nsbOpts(trial, "/h", "")
	o.RealClaude, o.NoRunMask = link, true
	nsbEq(t, "ro", nsbMake(t, o).Ro, []string{link, versions + "/claude"})
	viadir := root + "/viadir"
	nsbSymlink(t, root+"/bin", viadir)
	o = nsbOpts(trial, "/h", "")
	o.RealClaude = viadir + "/claude"
	nsbRefused(t, "claude through a symlinked dir", o)
}

// The Python test calls nsbox._open_chain directly; Go's openChain is unexported, so the same three outcomes are
// checked through the real stage 1 with a cfg whose lists were edited after MakeCfg (MakeCfg itself refuses them).
func TestNsboxAllowOpenChainRefusesSymlinkParentAndReportsLeafLink(t *testing.T) {
	nsbNeedUserns(t)
	root := nsbRoot(t)
	trial := root + "/trials/t1"
	real := root + "/r"
	nsbMkdir(t, trial, real+"/d")
	nsbSymlink(t, real, root+"/l")
	nsbSymlink(t, real+"/d", real+"/leaf")
	run := func(ro string) nsbResult {
		o := nsbOpts(trial, root+"/home", root+"/trials")
		o.Self, o.HideDirs, o.HideFiles, o.NoRunMask = nsbExe(t), nil, nil, true
		cfg := nsbMake(t, o)
		cfg.Ro = []string{ro}
		return nsbRun(t, cfg, []string{"/bin/true"})
	}
	if r := run(root + "/l/d"); r.RC == 0 || !strings.Contains(r.Err, "symlink") {
		t.Errorf("symlink parent: rc=%d stderr=%q, want a refusal naming a symlink", r.RC, r.Err)
	}
	if r := run(real + "/leaf"); r.RC != 0 {
		t.Errorf("leaf symlink (the claude link) must be accepted: rc=%d stderr=%q", r.RC, r.Err)
	}
	if r := run(real + "/d"); r.RC != 0 {
		t.Errorf("plain dir: rc=%d stderr=%q", r.RC, r.Err)
	}
	if r := run(real + "/nope"); r.RC == 0 || !strings.Contains(r.Err, "missing") {
		t.Errorf("missing source: rc=%d stderr=%q, want a refusal saying missing", r.RC, r.Err)
	}
}

func TestNsboxAllowStage1ArgvIdenticalForPidUnsetAndTrue(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := sbx.NewOpts(trial)
	o.Home, o.Self = "/h", "/nsb/self"
	a, err := sbx.ComposeStage1(nsbMake(t, o), []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	o.NoPid = false
	b, err := sbx.ComposeStage1(nsbMake(t, o), []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	nsbEq(t, "argv", a, b)
	nsbEq(t, "argv[1:8]", a[1:8], []string{"-r", "-n", "-m", "--pid", "--fork", "--kill-child", "--mount-proc"})
}

func TestNsboxAllowPidFalseDropsThePidNamespaceFlags(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := sbx.NewOpts(trial)
	o.Home, o.Self, o.NoPid = "/h", "/nsb/self", true
	argv, err := sbx.ComposeStage1(nsbMake(t, o), []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"--pid", "--fork", "--kill-child", "--mount-proc"} {
		if nsbContains(argv, f) {
			t.Errorf("argv has %s: %q", f, argv)
		}
	}
	nsbEq(t, "argv[1:6]", argv[1:6], []string{"-r", "-n", "-m", "--propagation", "private"})
}

func TestNsboxAllowKeepEnvExemptsOnlyTheNamedVariable(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := sbx.NewOpts(trial)
	o.Home, o.Net, o.KeepEnv = "/h", true, []string{"CLAUDE_CODE_OAUTH_TOKEN"}
	scrub := nsbMake(t, o).Scrub
	if nsbContains(scrub, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("kept variable is scrubbed: %q", scrub)
	}
	if !nsbContains(scrub, "ANTHROPIC_API_KEY") {
		t.Errorf("other variable is not scrubbed: %q", scrub)
	}
	o.KeepEnv = nil
	if !nsbContains(nsbMake(t, o).Scrub, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("without keep_env the token must be scrubbed")
	}
}

func TestNsboxAllowAllowModeMasksRunAndKeepsOnlyResolveRo(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := nsbOpts(trial, "/h", "")
	o.HideDirs = []string{"/h"}
	cfg := nsbMake(t, o)
	nsbEq(t, "hide_dirs", cfg.HideDirs, []string{"/h", "/run"})
	nsbEq(t, "ro", cfg.Ro, nsbRunKeepDirs())
	o.NoRunMask = true
	off := nsbMake(t, o)
	nsbEq(t, "hide_dirs without the run mask", off.HideDirs, []string{"/h"})
	nsbEq(t, "ro without the run mask", off.Ro, nil)
	d := sbx.NewOpts(trial)
	d.Home = "/h"
	if nsbContains(nsbMake(t, d).HideDirs, "/run") {
		t.Errorf("deny mode must not mask /run")
	}
}

func TestNsboxAllowAllowModeDoesNotMakeTrialOrCwdWritable(t *testing.T) {
	_, trial, _ := nsbGoldenOpts(t)
	o := nsbOpts(trial, "/h", "")
	o.Cwd, o.Rw = "/c", []string{"/w"}
	cfg := nsbMake(t, o)
	nsbEq(t, "rw", cfg.Rw, []string{"/w"})
	nsbEq(t, "keep", cfg.Keep, nil)
	nsbEq(t, "keep_ro", cfg.KeepRO, nil)
}

// --- group B: real stage-1 namespaces ---

// nsbBox is the fixture of AllowModeNamespaceTests: a fake home with secrets, a runtime dir, a trial.
type nsbBox struct {
	t                          *testing.T
	root, trial, home, runtime string
}

func nsbNewBox(t *testing.T) *nsbBox {
	t.Helper()
	nsbNeedUserns(t)
	b := &nsbBox{t: t, root: nsbRoot(t)}
	b.trial, b.home, b.runtime = b.root+"/trials/t1", b.root+"/home", b.root+"/run"
	nsbMkdir(t, b.trial, b.home+"/.ssh", b.home+"/secret", b.runtime)
	nsbWrite(t, b.home+"/secret/file", "S")
	nsbWrite(t, b.home+"/.ssh/key", "K")
	nsbWrite(t, b.home+"/.zshrc", "orig\n")
	return b
}

// cfg is the allow-mode cfg with home and runtime hidden and no hide files; mod edits the Opts.
func (b *nsbBox) cfg(mod func(o *sbx.Opts)) *sbx.Cfg {
	b.t.Helper()
	o := nsbOpts(b.trial, b.home, b.root+"/trials")
	o.Self, o.HideDirs, o.HideFiles = nsbExe(b.t), []string{b.home, b.runtime}, nil
	if mod != nil {
		mod(&o)
	}
	return nsbMake(b.t, o)
}

func TestNsboxAllowHidesHomeRuntimeAndSocketsAndIsolatesWrites(t *testing.T) {
	b := nsbNewBox(t)
	nsbSockets(t, b.home+"/agent.sock", b.runtime+"/bus")
	h, r := b.home, b.runtime
	probes := []string{"r:" + h + "/secret/file", "r:" + h + "/.ssh/key", "c:" + h + "/agent.sock", "c:" + r + "/bus"}
	got := nsbProbeRun(t, b.cfg(nil), probes...)
	for _, p := range probes {
		if got[p] != "blocked" {
			t.Errorf("%s = %q, want blocked", p, got[p])
		}
	}
	w := nsbRun(t, b.cfg(nil), []string{"/bin/sh", "-c", "echo pwned > '" + h + "/.zshrc'"})
	if w.RC != 0 {
		t.Fatalf("write into the tmpfs-hidden home: rc=%d %s", w.RC, w.Err)
	}
	if got := nsbRead(t, h+"/.zshrc"); got != "orig\n" {
		t.Errorf("host .zshrc = %q, want it untouched", got)
	}
	if got := nsbRead(t, h+"/secret/file"); got != "S" {
		t.Errorf("host secret = %q", got)
	}
}

func TestNsboxAllowRwWritableAndVisibleOutsideRoIsErofs(t *testing.T) {
	b := nsbNewBox(t)
	rwd, rod := b.root+"/rwd", b.root+"/rod"
	nsbMkdir(t, rwd, rod)
	nsbWrite(t, rod+"/f", "ro-data")
	cfg := b.cfg(func(o *sbx.Opts) { o.Rw, o.Ro = []string{rwd}, []string{rod} })
	got := nsbProbeRun(t, cfg, "w:"+rwd+"/new", "w:"+rod+"/g", "r:"+rod+"/f")
	if got["w:"+rwd+"/new"] != "ok" || got["w:"+rod+"/g"] != "EROFS" || got["r:"+rod+"/f"] != "readable" {
		t.Errorf("probes = %v", got)
	}
	// the writable bind is the host dir: write through it and see the file outside
	r := nsbRun(t, cfg, []string{"/bin/sh", "-c", "echo -n hello > '" + rwd + "/new'; cat '" + rod + "/f'"})
	if r.RC != 0 || r.Out != "ro-data" {
		t.Fatalf("rc=%d out=%q err=%q", r.RC, r.Out, r.Err)
	}
	if got := nsbRead(t, rwd+"/new"); got != "hello" {
		t.Errorf("rw file outside = %q, want hello", got)
	}
	if nsbExists(rod + "/g") {
		t.Errorf("a file appeared in the ro dir")
	}
}

func TestNsboxAllowOrderingRoParentRwChildMaskAndReboundChild(t *testing.T) {
	b := nsbNewBox(t)
	p := b.root + "/P"
	nsbMkdir(t, p+"/c", p+"/m/own", p+"/m/other")
	nsbWrite(t, p+"/top", "t")
	// listed shortest-last on purpose: the order comes from the path depth, not from the list
	cfg := b.cfg(func(o *sbx.Opts) {
		o.Rw, o.Ro, o.Masks = []string{p + "/m/own", p + "/c"}, []string{p}, []string{p + "/m"}
	})
	got := nsbProbeRun(t, cfg, "w:"+p+"/x", "w:"+p+"/c/x", "w:"+p+"/m/own/x", "l:"+p+"/m")
	want := map[string]string{"w:" + p + "/x": "EROFS", "w:" + p + "/c/x": "ok", "w:" + p + "/m/own/x": "ok", "l:" + p + "/m": "own"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("probes = %v, want %v", got, want)
	}
	ents, _ := os.ReadDir(p + "/m")
	if len(ents) != 2 || ents[0].Name() != "other" || ents[1].Name() != "own" {
		t.Errorf("host %s/m = %v, want other and own", p, ents)
	}
}

func TestNsboxAllowMissingBindSourceFailsClosedNamingThePath(t *testing.T) {
	b := nsbNewBox(t)
	marker, missing := b.root+"/ran", b.root+"/nope"
	r := nsbRun(t, b.cfg(func(o *sbx.Opts) { o.Rw = []string{missing} }), nsbMarkerArgv(marker))
	if r.RC == 0 {
		t.Errorf("rc = 0, want a failure")
	}
	nsbHas(t, "stderr", r.Err, missing)
	if nsbExists(marker) {
		t.Errorf("the target ran")
	}
}

func TestNsboxAllowParentSymlinkSwappedInAfterMakeCfgFailsClosed(t *testing.T) {
	b := nsbNewBox(t)
	marker, parent, decoy := b.root+"/ran", b.root+"/swap", b.root+"/decoy"
	nsbMkdir(t, parent+"/sub", decoy+"/sub")
	cfg := b.cfg(func(o *sbx.Opts) { o.Rw = []string{parent + "/sub"} }) // accepted: parent is a real dir now
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	nsbSymlink(t, decoy, parent) // the swap, after the checks
	r := nsbRun(t, cfg, nsbMarkerArgv(marker))
	if r.RC == 0 {
		t.Errorf("rc = 0, want a failure")
	}
	nsbHas(t, "stderr", r.Err, parent)
	if nsbExists(marker) {
		t.Errorf("the target ran")
	}
}

func TestNsboxAllowMissingMaskFailsClosed(t *testing.T) {
	b := nsbNewBox(t)
	marker := b.root + "/ran"
	r := nsbRun(t, b.cfg(func(o *sbx.Opts) { o.Rw, o.Masks = []string{b.root}, []string{b.root + "/nomask"} }), nsbMarkerArgv(marker))
	if r.RC == 0 {
		t.Errorf("rc = 0, want a failure")
	}
	nsbHas(t, "stderr", r.Err, "nomask")
	if nsbExists(marker) {
		t.Errorf("the target ran")
	}
}

func TestNsboxAllowNoCapabilitiesAndMountFails(t *testing.T) {
	b := nsbNewBox(t)
	got := nsbProbeRun(t, b.cfg(func(o *sbx.Opts) { o.Rw = []string{b.root} }), "caps")
	if got["caps"] != "0 -1" {
		t.Errorf("CapEff and mount rc = %q, want \"0 -1\"", got["caps"])
	}
}

func TestNsboxAllowTrialNotListedIsReadOnlyAndHiddenCwdFallsBackToFirstRw(t *testing.T) {
	b := nsbNewBox(t)
	rwd := b.root + "/rwd"
	nsbMkdir(t, rwd)
	cfg := b.cfg(func(o *sbx.Opts) { o.Rw, o.Ro, o.Cwd = []string{rwd}, []string{b.trial}, b.home })
	got := nsbProbeRun(t, cfg, "w:"+b.trial+"/x", "cwd")
	if got["w:"+b.trial+"/x"] != "EROFS" || got["cwd"] != rwd {
		t.Errorf("probes = %v, want EROFS and cwd %s", got, rwd)
	}
}

func TestNsboxAllowTrialUnlistedIsNotBoundBackAtAllWhenHidden(t *testing.T) {
	b := nsbNewBox(t)
	cfg := b.cfg(func(o *sbx.Opts) {
		o.HideDirs = []string{b.home, b.runtime, b.root + "/trials"}
		o.Rw = []string{b.home + "/secret"}
	})
	if got := nsbProbeRun(t, cfg, "x:"+b.trial); got["x:"+b.trial] != "false" {
		t.Errorf("trial exists inside = %q, want false", got["x:"+b.trial])
	}
}

func TestNsboxAllowDefaultHideTmpfsIs8m(t *testing.T) {
	b := nsbNewBox(t)
	if got := nsbFill(t, b.cfg(nil), b.home, 12); got != "ENOSPC" {
		t.Errorf("12 MiB into the default hide tmpfs = %q, want ENOSPC", got)
	}
}

func TestNsboxAllowTmpfsSizeIsHonouredAndCapped(t *testing.T) {
	b := nsbNewBox(t)
	cfg := b.cfg(func(o *sbx.Opts) { o.TmpfsSize = "16m" })
	if got := nsbFill(t, cfg, b.home, 12); got != "ok" {
		t.Errorf("12 MiB into a 16m tmpfs = %q, want ok", got)
	}
	if got := nsbFill(t, cfg, b.home, 20); got != "ENOSPC" {
		t.Errorf("20 MiB into a 16m tmpfs = %q, want ENOSPC", got)
	}
}

func TestNsboxAllowTmpCapAtWorkerbox512mIsEnforced(t *testing.T) {
	b := nsbNewBox(t)
	// the workerbox cap on /tmp itself: 512m is the real size, filled with dd (the probe binary lives in /tmp too, so
	// it is gone inside the box); about 512 MB of RAM briefly
	tmp := sbx.Realpath("/tmp")
	cfg := b.cfg(func(o *sbx.Opts) {
		o.HideDirs, o.TmpfsSizes = []string{tmp, b.runtime}, map[string]string{tmp: "512m"}
	})
	r := nsbRun(t, cfg, []string{"/bin/sh", "-c", "dd if=/dev/zero of='" + tmp + "/big' bs=1M count=700 2>/dev/null; stat -c %s '" + tmp + "/big'"})
	if r.RC != 0 {
		t.Fatalf("rc=%d stderr=%s", r.RC, r.Err)
	}
	size, err := nsbAtoi(strings.TrimSpace(r.Out))
	if err != nil {
		t.Fatalf("size %q: %v", r.Out, err)
	}
	if n := size >> 20; n < 500 || n > 513 { // capped at 512 MiB, well past the 8m default and short of the 700 asked
		t.Errorf("wrote %d MiB before ENOSPC, want 500..513", n)
	}
}

func TestNsboxAllowTmpfsSizesOverrideOneTreeAndMask(t *testing.T) {
	b := nsbNewBox(t)
	mask := b.root + "/maskdir"
	nsbMkdir(t, mask)
	cfg := b.cfg(func(o *sbx.Opts) {
		o.TmpfsSizes, o.Masks = map[string]string{b.home: "32m", mask: "16m"}, []string{mask}
	})
	for _, c := range []struct {
		dir  string
		mib  int
		want string
	}{{b.home, 20, "ok"}, {b.home, 40, "ENOSPC"}, {b.runtime, 12, "ENOSPC"}, // not listed: default size
		{mask, 12, "ok"}, {mask, 20, "ENOSPC"}} {
		if got := nsbFill(t, cfg, c.dir, c.mib); got != c.want {
			t.Errorf("%d MiB into %s = %q, want %s", c.mib, c.dir, got, c.want)
		}
	}
}

// --- NoFollowTargetTests (T-0256) ---

type nsbSwap struct {
	*nsbBox
	parent, decoy string
}

func nsbNewSwap(t *testing.T) *nsbSwap {
	t.Helper()
	b := nsbNewBox(t)
	s := &nsbSwap{nsbBox: b, parent: b.root + "/swap", decoy: b.root + "/decoy"}
	nsbMkdir(t, s.parent+"/sub", s.decoy+"/sub")
	return s
}

// cfg0 is the cfg of the NoFollow classes: no hide dirs, no hide files, no /run mask.
func (s *nsbSwap) cfg0(mod func(o *sbx.Opts)) *sbx.Cfg {
	s.t.Helper()
	return s.cfg(func(o *sbx.Opts) {
		o.Home, o.HideDirs, o.HideFiles, o.NoRunMask = s.root+"/home", nil, nil, true
		if mod != nil {
			mod(o)
		}
	})
}

func (s *nsbSwap) swap() {
	s.t.Helper()
	if err := os.RemoveAll(s.parent); err != nil {
		s.t.Fatal(err)
	}
	nsbSymlink(s.t, s.decoy, s.parent)
}

func (s *nsbSwap) refused(cfg *sbx.Cfg, named string) {
	s.t.Helper()
	marker := s.root + "/ran"
	r := nsbRun(s.t, cfg, nsbMarkerArgv(marker))
	if r.RC == 0 {
		s.t.Errorf("rc = 0, want a failure")
	}
	nsbHas(s.t, "stderr", r.Err, named)
	if nsbExists(marker) {
		s.t.Errorf("the target ran")
	}
}

func TestNsboxAllowTargetParentSwappedAfterSourcesAreOpenIsRefused(t *testing.T) {
	s := nsbNewSwap(t)
	cfg := s.cfg0(func(o *sbx.Opts) { o.Rw = []string{s.parent + "/sub"} })
	s.swap()
	s.refused(cfg, s.parent)
}

func TestNsboxAllowMaskParentSwappedIsRefused(t *testing.T) {
	s := nsbNewSwap(t)
	cfg := s.cfg0(func(o *sbx.Opts) { o.Rw, o.Masks = []string{s.root + "/trials"}, []string{s.parent + "/sub"} })
	s.swap()
	s.refused(cfg, s.parent)
}

func TestNsboxAllowSymlinkedMaskLeafIsRefusedBeforeExec(t *testing.T) {
	s := nsbNewSwap(t)
	nsbSymlink(t, s.decoy, s.root+"/m")
	cfg := s.cfg0(func(o *sbx.Opts) { o.Rw, o.Masks = []string{s.root + "/trials"}, []string{s.root + "/m"} })
	s.refused(cfg, s.root+"/m")
}

func TestNsboxAllowMissingTargetsUnderAHiddenTreeAreCreatedAndBound(t *testing.T) {
	s := nsbNewSwap(t)
	home := s.root + "/home"
	nsbWrite(t, home+"/a/b/f", "F")
	cfg := s.cfg0(func(o *sbx.Opts) {
		o.HideDirs, o.Rw, o.Ro = []string{home}, []string{home + "/a/b"}, []string{home + "/a/b/f"}
	})
	r := nsbRun(t, cfg, []string{"/bin/cat", home + "/a/b/f"})
	if r.RC != 0 || strings.TrimSpace(r.Out) != "F" {
		t.Errorf("rc=%d out=%q err=%q, want F", r.RC, r.Out, r.Err)
	}
}

// --- NoFollowHideTests (T-0462) ---

func nsbNewHideSwap(t *testing.T) *nsbSwap {
	t.Helper()
	s := nsbNewSwap(t)
	nsbWrite(t, s.parent+"/f", "S")
	nsbWrite(t, s.decoy+"/f", "S")
	return s
}

func TestNsboxAllowHideDirParentSwappedAfterMakeCfgIsRefused(t *testing.T) {
	s := nsbNewHideSwap(t)
	cfg := s.cfg0(func(o *sbx.Opts) { o.Rw, o.HideDirs = []string{s.root + "/trials"}, []string{s.parent + "/sub"} })
	s.swap()
	s.refused(cfg, s.parent)
}

func TestNsboxAllowHideDirLeafSymlinkIsRefused(t *testing.T) {
	s := nsbNewHideSwap(t)
	nsbSymlink(t, s.decoy, s.root+"/link")
	cfg := s.cfg0(func(o *sbx.Opts) { o.Rw, o.HideDirs = []string{s.root + "/trials"}, []string{s.root + "/link"} })
	s.refused(cfg, s.root+"/link")
}

func TestNsboxAllowHideFileParentSwappedAfterMakeCfgIsRefused(t *testing.T) {
	s := nsbNewHideSwap(t)
	cfg := s.cfg0(func(o *sbx.Opts) { o.Rw, o.HideFiles = []string{s.root + "/trials"}, []string{s.parent + "/f"} })
	s.swap()
	s.refused(cfg, s.parent)
}

func TestNsboxAllowPlainHideDirAndFileAreStillHidden(t *testing.T) {
	s := nsbNewHideSwap(t)
	cfg := s.cfg0(func(o *sbx.Opts) {
		o.Rw, o.HideDirs, o.HideFiles = []string{s.root + "/trials"}, []string{s.parent + "/sub"}, []string{s.decoy + "/f"}
	})
	r := nsbRun(t, cfg, []string{"/bin/sh", "-c", "echo \"[$(cat '" + s.decoy + "/f')] [$(ls -A '" + s.parent + "/sub')]\""})
	if r.RC != 0 || strings.TrimSpace(r.Out) != "[] []" {
		t.Errorf("rc=%d out=%q err=%q, want an empty file and an empty dir", r.RC, r.Out, r.Err)
	}
}

// --- PidAndEnvNamespaceTests ---

func nsbPidCfg(t *testing.T, mod func(o *sbx.Opts)) *sbx.Cfg {
	t.Helper()
	nsbNeedUserns(t)
	root := nsbRoot(t)
	o := sbx.NewOpts(root)
	o.Home, o.TrialsRoot, o.Self, o.Unshare = root, root, nsbExe(t), nsbUnshare()
	o.HideDirs, o.HideFiles = nil, nil
	mod(&o)
	return nsbMake(t, o)
}

func TestNsboxAllowPidFalseTargetKeepsTheStage1Pid(t *testing.T) {
	cfg := nsbPidCfg(t, func(o *sbx.Opts) { o.NoPid = true })
	argv, err := sbx.ComposeStage1(cfg, []string{"/bin/sh", "-c", "echo $$"})
	if err != nil {
		t.Fatal(err)
	}
	pid, out, err := nsbStart(argv)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := nsbAtoi(strings.TrimSpace(out)); err != nil || got != pid {
		t.Errorf("target pid = %q (%v), want the stage-1 pid %d", out, err, pid)
	}
}

func TestNsboxAllowPidTrueTargetIsPidInNewNamespace(t *testing.T) {
	cfg := nsbPidCfg(t, func(o *sbx.Opts) {})
	r := nsbRun(t, cfg, []string{"/bin/sh", "-c", "echo $$"})
	n, err := nsbAtoi(strings.TrimSpace(r.Out))
	if err != nil || n == 0 || n >= 1000 {
		t.Errorf("pid in the new namespace = %q (%v) stderr=%q, want 1..999", r.Out, err, r.Err)
	}
}

func TestNsboxAllowKeepEnvTokenReachesTargetAndIsScrubbedWithoutIt(t *testing.T) {
	env := []string{"CLAUDE_CODE_OAUTH_TOKEN=dummy-token", "ANTHROPIC_API_KEY=dummy-key"}
	argv := []string{"/bin/sh", "-c", "echo ${CLAUDE_CODE_OAUTH_TOKEN-None} ${ANTHROPIC_API_KEY-None}"}
	kept := nsbRun(t, nsbPidCfg(t, func(o *sbx.Opts) { o.Net, o.KeepEnv = true, []string{"CLAUDE_CODE_OAUTH_TOKEN"} }), argv, env...)
	if got := strings.TrimSpace(kept.Out); got != "dummy-token None" {
		t.Errorf("with keep_env = %q (stderr %q), want \"dummy-token None\"", got, kept.Err)
	}
	scrubbed := nsbRun(t, nsbPidCfg(t, func(o *sbx.Opts) { o.Net = true }), argv, env...)
	if got := strings.TrimSpace(scrubbed.Out); got != "None None" {
		t.Errorf("without keep_env = %q (stderr %q), want \"None None\"", got, scrubbed.Err)
	}
}

// --- RunMaskTests (T-0673) ---

var nsbRunSocks = []string{"/run/docker.sock", "/var/run/docker.sock", "/run/ssh-unix-local/socket"}

// nsbRunIn is run_in: allow mode, no hide dirs or files, trial rw.
func nsbRunIn(t *testing.T, mod func(o *sbx.Opts)) (*sbx.Cfg, string) {
	t.Helper()
	nsbNeedUserns(t)
	root := nsbRoot(t)
	trial := root + "/trials/t1"
	nsbMkdir(t, trial)
	o := nsbOpts(trial, root+"/home", root+"/trials")
	o.Self, o.HideDirs, o.HideFiles, o.Rw = nsbExe(t), nil, nil, []string{trial}
	if mod != nil {
		mod(&o)
	}
	return nsbMake(t, o), root
}

func TestNsboxAllowRunSocketsCannotBeConnectedAndRunIsEmptyButResolve(t *testing.T) {
	cfg, _ := nsbRunIn(t, nil)
	var probes []string
	for _, p := range nsbRunSocks {
		probes = append(probes, "c:"+p)
	}
	probes = append(probes, "l:/run")
	got := nsbProbeRun(t, cfg, probes...)
	for _, p := range nsbRunSocks {
		if got["c:"+p] != "blocked" {
			t.Errorf("%s = %q, want blocked", p, got["c:"+p])
		}
	}
	seen := map[string]bool{}
	var want []string
	for _, d := range nsbRunKeepDirs() {
		if n := strings.Split(d, "/")[2]; !seen[n] {
			seen[n] = true
			want = append(want, n)
		}
	}
	if got["l:/run"] != strings.Join(want, ",") {
		t.Errorf("/run lists %q, want %q", got["l:/run"], strings.Join(want, ","))
	}
}

func TestNsboxAllowWithoutTheMaskTheHostSocketIsReachable(t *testing.T) {
	sock := ""
	for _, p := range nsbRunSocks {
		if nsbCanConnect(p) {
			sock = p
			break
		}
	}
	if sock == "" {
		t.Skip("no host /run socket this process can connect to")
	}
	// control: proves the probe can connect at all, so "blocked" above is the mask and not a broken probe
	cfg, _ := nsbRunIn(t, func(o *sbx.Opts) { o.NoRunMask = true })
	if got := nsbProbeRun(t, cfg, "c:"+sock); got["c:"+sock] != "connected" {
		t.Errorf("%s without the mask = %q, want connected", sock, got["c:"+sock])
	}
}

func TestNsboxAllowResolvConfStillReadableAndReadOnly(t *testing.T) {
	const stub = "/run/systemd/resolve/stub-resolv.conf"
	if fi, err := os.Stat(stub); err != nil || !fi.Mode().IsRegular() {
		t.Skip("no systemd-resolved stub file")
	}
	cfg, _ := nsbRunIn(t, nil)
	r := nsbRun(t, cfg, []string{"/bin/grep", "-c", "nameserver", "/etc/resolv.conf"})
	if n, err := nsbAtoi(strings.TrimSpace(r.Out)); r.RC != 0 || err != nil || n < 1 {
		t.Errorf("/etc/resolv.conf inside: rc=%d out=%q err=%q, want a nameserver line", r.RC, r.Out, r.Err)
	}
	got := nsbProbeRun(t, cfg, "a:"+stub)
	if got["a:"+stub] == "ok" {
		t.Errorf("the stub file opened for write inside the box: %v", got)
	}
}

func TestNsboxAllowEvalProfileRunSocketsUnreachable(t *testing.T) {
	nsbNeedUserns(t)
	var socks []string
	for _, p := range nsbRunSocks {
		if nsbCanConnect(p) { // control: connectable outside, or not asserted
			socks = append(socks, p)
		}
	}
	if len(socks) == 0 {
		t.Skip("no connectable host /run socket on this box")
	}
	root := nsbRoot(t)
	trial, home := root+"/trials/t1", root+"/home"
	nsbMkdir(t, trial+"/work", trial+"/bin", trial+"/scout", trial+"/role", home+"/.local/bin")
	// the eval profile hides /tmp, where this test binary lives: put a copy in the trial's read-only bin/
	self := trial + "/bin/self"
	if err := nsbCopyFile(nsbExe(t), self, 0o755); err != nil {
		t.Fatal(err)
	}
	var probes []string
	for _, p := range socks {
		probes = append(probes, "c:"+p)
	}
	// the eval profile execs claude itself: a probe stands in for it
	claude := home + "/.local/bin/claude"
	nsbWrite(t, claude, "#!/bin/sh\nexec '"+self+"' --nsb-probe "+strings.Join(probes, " ")+"\n")
	if err := os.Chmod(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := sbx.EvalNsCfg(trial, home, claude, "", nsbUnshare(), sbx.Opts{Self: self, UID: os.Getuid(), GID: os.Getgid()})
	if err != nil {
		t.Fatalf("EvalNsCfg: %v", err)
	}
	r := nsbRun(t, cfg, nil)
	if r.RC != 0 {
		t.Fatalf("rc=%d stderr=%s stdout=%s", r.RC, r.Err, r.Out)
	}
	got := nsbParse(r.Out)
	if len(got) != len(socks) {
		t.Fatalf("probe output %q, want %d results", r.Out, len(socks))
	}
	for k, v := range got {
		if v != "blocked" {
			t.Errorf("%s = %q, want blocked", k, v)
		}
	}
}
