package sandbox2

// Go port of tests/test_nsbox_resolv_keep.py (13 cases): the /run keep-list derived from the real path of resolv.conf
// (T-0697). sbx.RunKeepRO(resolvPath) resolves resolvPath and returns its parent dir only when that is strictly under
// /run (never /run itself, never /run/user/...).
//
// Go-side differences: Python pinned os.path.realpath(/etc/resolv.conf) to a made-up target; Go's RunKeepRO takes
// the path itself, so the made-up /run targets are passed directly (they need not exist) and the "plain /etc file"
// case uses a real temp file. The hardcoded-constant case checks MakeCfg's allow-mode ro list instead of a constant.
//
// not ported: (none) - every case has a Go test.

import (
	"os"
	"strconv"
	"testing"

	sbx "factory/go/internal/sandbox"
)

func nsbKeep(t *testing.T, resolv string, want ...string) {
	t.Helper()
	got := sbx.RunKeepRO(resolv)
	nsbEq(t, "RunKeepRO("+resolv+")", got, want)
}

func TestNsboxResolvKeepFunctionExistsAndCallable(t *testing.T) {
	var fn func(string) []string = sbx.RunKeepRO
	if fn == nil {
		t.Fatal("sandbox.RunKeepRO is nil")
	}
	_ = fn("/etc/resolv.conf") // must not panic on the real host path
}

func TestNsboxResolvKeepASystemdResolvedStub(t *testing.T) {
	nsbKeep(t, "/run/systemd/resolve/stub-resolv.conf", "/run/systemd/resolve")
}

func TestNsboxResolvKeepBResolvconf(t *testing.T) {
	nsbKeep(t, "/run/resolvconf/resolv.conf", "/run/resolvconf")
}

func TestNsboxResolvKeepCNetworkManager(t *testing.T) {
	nsbKeep(t, "/run/NetworkManager/resolv.conf", "/run/NetworkManager")
}

func TestNsboxResolvKeepDPlainEtcFileKeepsNothing(t *testing.T) {
	root := nsbRoot(t)
	nsbWrite(t, root+"/etc/resolv.conf", "nameserver 127.0.0.1\n")
	nsbKeep(t, root+"/etc/resolv.conf")
}

func TestNsboxResolvKeepD2OtherNonRunTargetKeepsNothing(t *testing.T) {
	nsbKeep(t, "/var/lib/dns/resolv.conf")
}

func TestNsboxResolvKeepD3RunPrefixLookalikeIsNotUnderRun(t *testing.T) {
	nsbKeep(t, "/runner/dns/resolv.conf")
}

func TestNsboxResolvKeepEFileDirectlyInRunKeepsNothing(t *testing.T) {
	// parent would be /run itself: never re-expose the whole mask
	nsbKeep(t, "/run/resolv.conf")
}

func TestNsboxResolvKeepE2UnderRunUserKeepsNothing(t *testing.T) {
	uid := os.Getuid()
	for _, p := range []string{
		"/run/user/" + strconv.Itoa(uid) + "/resolv.conf", "/run/user/" + strconv.Itoa(uid) + "/systemd/resolve/resolv.conf",
		"/run/user/1000/resolv.conf", "/run/user/resolv.conf",
	} {
		nsbKeep(t, p)
	}
}

func TestNsboxResolvKeepOnlyOneEntryAndNothingElseUnderRun(t *testing.T) {
	got := sbx.RunKeepRO("/run/systemd/resolve/stub-resolv.conf")
	if len(got) != 1 {
		t.Fatalf("entries = %q, want exactly one", got)
	}
	if nsbContains(got, "/run") {
		t.Errorf("%q contains /run itself", got)
	}
	for _, k := range got {
		if k == "/run/user" || len(k) > 9 && k[:10] == "/run/user/" {
			t.Errorf("%q has an entry under /run/user", got)
		}
	}
}

func TestNsboxResolvKeepReturnsTuple(t *testing.T) {
	// the Go counterpart of "a tuple of dirs" is a non-nil []string for a hit and an empty one for a miss
	if got := sbx.RunKeepRO("/run/resolvconf/resolv.conf"); got == nil || len(got) != 1 {
		t.Errorf("hit = %#v, want a one-element slice", got)
	}
	if got := sbx.RunKeepRO("/run/resolv.conf"); len(got) != 0 {
		t.Errorf("miss = %#v, want empty", got)
	}
}

func TestNsboxResolvKeepRealSymlinkChainIsFollowed(t *testing.T) {
	root := nsbRoot(t)
	target := root + "/resolv.conf"
	nsbWrite(t, target, "nameserver 127.0.0.1\n")
	link := root + "/link"
	nsbSymlink(t, target, link)
	// the path is a symlink to a file outside /run: it is resolved, nothing is kept
	nsbKeep(t, link)
}

func TestNsboxResolvKeepHardcodedSystemdEntryGoneFromConstant(t *testing.T) {
	// no hardcoded /run/systemd/resolve: with a resolv.conf that is not under /run, allow mode re-exposes nothing
	root := nsbRoot(t)
	nsbWrite(t, root+"/resolv.conf", "nameserver 127.0.0.1\n")
	o := nsbOpts(root+"/trials/t1", "/h", "")
	o.ResolvConf = root + "/resolv.conf"
	cfg := nsbMake(t, o)
	if nsbContains(cfg.Ro, "/run/systemd/resolve") {
		t.Errorf("ro = %q must not hardcode /run/systemd/resolve", cfg.Ro)
	}
	nsbKeep(t, root+"/resolv.conf")
}
