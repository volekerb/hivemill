package sandbox1

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Port of tests/test_testcg.py (12 cases; see ported_sandbox1.txt). scout/testcg.py has no Go port and `factory test`
// is handed to bin/factory, so every case drives the real CLI as a subprocess (NeedPython). The two Unit cases of
// Python call testcg.limits and testcg.default_parent directly; here they assert the same values through the files
// and the error text of `factory test`.

func tbFakeParent(t *testing.T, b *tbBox, controllers string) string {
	t.Helper()
	p := filepath.Join(b.e.Home, "cgparent")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	tbWrite(t, p+"/cgroup.controllers", controllers+"\n", 0o644) // what Delegate= makes available
	tbWrite(t, p+"/cgroup.subtree_control", "", 0o644)           // systemd leaves it empty; check_parent must enable them
	b.cfg("test_cgroup", "1")
	b.cfg("test_cgroup_parent", p)
	return p
}

func tbKids(t *testing.T, parent string) []string {
	t.Helper()
	ents, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, filepath.Join(parent, e.Name()))
		}
	}
	return out
}

func TestTestcgFlagOffByDefaultRunsDirectly(t *testing.T) {
	b := tbNew(t)
	tbEq(t, "exit", b.ftest(nil, "--", "sh", "-c", "exit 7").Exit, 7)
}

func TestTestcgBadFlagValueIsErrorAndNotRun(t *testing.T) {
	b := tbNew(t)
	b.rawCfg("test_cgroup", "yes")
	r := b.ftest(nil, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	tbContains(t, "stderr", r.Stderr, "factory config test_cgroup 0")
	if tbExists(b.clone + "/ran") {
		t.Fatal("the command ran")
	}
}

func TestTestcgMissingParentWithFlagOnDoesNotRun(t *testing.T) {
	for _, sb := range []string{"0", "1"} {
		b := tbNew(t)
		if sb == "1" {
			tbNeedUserns(t)
		}
		b.cfg("test_sandbox", sb)
		b.cfg("test_cgroup", "1")
		b.cfg("test_cgroup_parent", filepath.Join(b.e.Home, "nope"))
		r := b.ftest(nil, "touch", "ran")
		tbEq(t, "exit (test_sandbox="+sb+")", r.Exit, 3)
		tbContains(t, "stderr", r.Stderr, "is missing")
		tbContains(t, "stderr", r.Stderr, "factory config test_cgroup 0")
		if tbExists(b.clone + "/ran") {
			t.Fatalf("ran uncapped with test_sandbox=%s", sb)
		}
	}
}

func TestTestcgDefaultParentMissingOnAHostWithoutTheUnit(t *testing.T) {
	b := tbNew(t)
	b.cfg("test_cgroup", "1")
	if fi, err := os.Stat(tbDefaultParent(os.Getuid())); err == nil && fi.IsDir() {
		t.Skip("the factory-tests unit is installed on this host")
	}
	r := b.ftest(nil, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	if tbExists(b.clone + "/ran") {
		t.Fatal("the command ran")
	}
}

func TestTestcgParentWithoutDelegatedControllersDoesNotRun(t *testing.T) {
	b := tbNew(t)
	tbFakeParent(t, b, "cpu")
	r := b.ftest(nil, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	tbContains(t, "stderr", r.Stderr, "does not delegate memory pids")
	if tbExists(b.clone + "/ran") {
		t.Fatal("the command ran")
	}
}

func TestTestcgBadLimitValuesDoNotRun(t *testing.T) {
	b := tbNew(t)
	tbFakeParent(t, b, "cpu memory pids")
	good := map[string]string{"test_cpu_quota": "400", "test_mem_max": "3G", "test_tasks_max": "512"}
	for _, kv := range [][2]string{{"test_cpu_quota", "0"}, {"test_mem_max", "lots"}, {"test_tasks_max", "-1"}} {
		k, v := kv[0], kv[1]
		b.cfg(k, v)
		r := b.ftest(nil, "touch", "ran")
		if r.Exit != 3 {
			t.Fatalf("%s=%s: exit %d, want 3: %s", k, v, r.Exit, r.Stderr)
		}
		tbContains(t, k+" stderr", r.Stderr, k)
		if tbExists(b.clone + "/ran") {
			t.Fatalf("%s=%s: the command ran", k, v)
		}
		b.cfg(k, good[k])
	}
}

func TestTestcgChildCgroupGetsCapsAndTheRunIsMovedIn(t *testing.T) {
	b := tbNew(t)
	parent := tbFakeParent(t, b, "cpu memory pids")
	b.cfg("test_cpu_quota", "250")
	b.cfg("test_mem_max", "2g")
	b.cfg("test_tasks_max", "99")
	tbEq(t, "exit", b.ftest(nil, "--", "sh", "-c", "exit 5").Exit, 5) // the command's exit code
	kids := tbKids(t, parent)
	if len(kids) != 1 { // one child per run (plain dir here: rmdir cannot drop its files)
		t.Fatalf("kids = %v", kids)
	}
	k := kids[0]
	tbEq(t, "cpu.max", tbRead(t, k+"/cpu.max"), "250000 100000")
	tbEq(t, "memory.max", tbRead(t, k+"/memory.max"), "2G")
	tbEq(t, "memory.swap.max", tbRead(t, k+"/memory.swap.max"), "0")
	tbEq(t, "pids.max", tbRead(t, k+"/pids.max"), "99")
	tbEq(t, "subtree_control", tbRead(t, parent+"/cgroup.subtree_control"), "+cpu +memory +pids") // enabled by check_parent
	tbEq(t, "cgroup.procs", tbRead(t, k+"/cgroup.procs"), "0")                                    // the run moved itself in before exec
}

func TestTestcgDefaultsAre400Percent3G512(t *testing.T) {
	b := tbNew(t)
	parent := tbFakeParent(t, b, "cpu memory pids")
	tbEq(t, "exit", b.ftest(nil, "true").Exit, 0)
	kids := tbKids(t, parent)
	if len(kids) == 0 {
		t.Fatal("no child cgroup")
	}
	k := kids[0]
	tbEq(t, "cpu.max", tbRead(t, k+"/cpu.max"), "400000 100000")
	tbEq(t, "memory.max", tbRead(t, k+"/memory.max"), "3G")
	tbEq(t, "pids.max", tbRead(t, k+"/pids.max"), "512")
}

func TestTestcgWorksWithTheSandboxOn(t *testing.T) {
	tbNeedUserns(t)
	b := tbNew(t)
	parent := tbFakeParent(t, b, "cpu memory pids")
	b.cfg("test_sandbox", "1")
	r := b.ftest(nil, "sh", "-c", "exit 4")
	tbEq(t, "exit", r.Exit, 4)
	tbEq(t, "kids", len(tbKids(t, parent)), 1)
}

// tbDelegatedParent is a fresh real cgroup below the user manager with cpu memory pids delegated, or "" and the reason.
func tbDelegatedParent() (string, string) {
	uid := os.Getuid()
	userCG := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service", uid, uid)
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Sprintf("no random source: %v", err)
	}
	d := userCG + "/t0681-" + hex.EncodeToString(rnd[:])
	if err := os.Mkdir(d, 0o755); err != nil {
		return "", fmt.Sprintf("cannot create a cgroup under %s: %v", userCG, err)
	}
	b, err := os.ReadFile(d + "/cgroup.controllers")
	if err != nil {
		os.Remove(d) // best effort: the directory is ours and empty
		return "", fmt.Sprintf("cannot read %s/cgroup.controllers: %v", d, err)
	}
	avail := " " + strings.Join(strings.Fields(string(b)), " ") + " "
	for _, c := range []string{"cpu", "memory", "pids"} {
		if !strings.Contains(avail, " "+c+" ") {
			os.Remove(d) // best effort: the directory is ours and empty
			return "", fmt.Sprintf("cpu memory pids not available in %s (have:%s)", d, strings.TrimRight(avail, " "))
		}
	}
	return d, ""
}

func TestTestcgMemoryMaxIsEnforced(t *testing.T) {
	parent, why := tbDelegatedParent()
	if parent == "" {
		t.Skip(why)
	}
	t.Cleanup(func() {
		for i := 0; i < 50; i++ {
			if os.Remove(parent) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	b := tbNew(t)
	b.cfg("test_cgroup", "1")
	b.cfg("test_cgroup_parent", parent)
	b.cfg("test_mem_max", "64M")
	tbEq(t, "subtree_control", strings.TrimSpace(tbRead(t, parent+"/cgroup.subtree_control")), "") // nothing pre-enabled
	ok := b.ftest(nil, "python3", "-c", "x = bytearray(16 * 1024 * 1024); print('small ok')")
	if ok.Exit == 127 && strings.Contains(ok.Stderr, "cannot move the run into") && strings.Contains(ok.Stderr, "errno 13") {
		// e.g. a CI runner whose own cgroup is outside the user manager: the kernel refuses the move; factory test failed closed.
		if strings.Contains(ok.Stdout, "small ok") {
			t.Fatal("the command ran although the move was refused")
		}
		t.Skipf("this process may not move into cgroups below the user manager: %s", strings.TrimSpace(ok.Stderr))
	}
	if ok.Exit != 0 || strings.TrimSpace(ok.Stdout) != "small ok" {
		t.Fatalf("small run: exit %d out %q err %q", ok.Exit, ok.Stdout, ok.Stderr)
	}
	big := b.ftest(nil, "python3", "-c", "x = bytearray(400 * 1024 * 1024); print('survived')")
	tbEq(t, "big exit", big.Exit, 137) // SIGKILL from the cgroup OOM killer
	if strings.Contains(big.Stdout, "survived") {
		t.Fatal("the allocation survived the cap")
	}
	if k := tbKids(t, parent); len(k) != 0 {
		t.Fatalf("child cgroups are not removed after the run: %v", k)
	}
}

// tbDefaultParent is testcg.default_parent.
func tbDefaultParent(uid int) string {
	return fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/factory-tests.slice/factory-tests.service", uid, uid)
}

// Unit case test_limits_text: limits("400","3G","512")["cpu.max"] == "400000 100000"; limits("50","512m","8")["memory.max"] ==
// "512M"; limits("1.5","3G","8") raises. Asserted through the child cgroup files and the refusal of `factory test`.
func TestTestcgUnitLimitsText(t *testing.T) {
	b := tbNew(t)
	parent := tbFakeParent(t, b, "cpu memory pids")
	b.cfg("test_cpu_quota", "400")
	b.cfg("test_mem_max", "3G")
	b.cfg("test_tasks_max", "512")
	tbEq(t, "exit", b.ftest(nil, "true").Exit, 0)
	tbEq(t, "cpu.max", tbRead(t, tbKids(t, parent)[0]+"/cpu.max"), "400000 100000")
	b2 := tbNew(t)
	p2 := tbFakeParent(t, b2, "cpu memory pids")
	b2.cfg("test_cpu_quota", "50")
	b2.cfg("test_mem_max", "512m")
	b2.cfg("test_tasks_max", "8")
	tbEq(t, "exit", b2.ftest(nil, "true").Exit, 0)
	tbEq(t, "memory.max", tbRead(t, tbKids(t, p2)[0]+"/memory.max"), "512M")
	b3 := tbNew(t)
	tbFakeParent(t, b3, "cpu memory pids")
	b3.rawCfg("test_cpu_quota", "1.5")
	b3.rawCfg("test_tasks_max", "8")
	r := b3.ftest(nil, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	tbContains(t, "stderr", r.Stderr, "test_cpu_quota")
	if tbExists(b3.clone + "/ran") {
		t.Fatal("the command ran")
	}
}

// Unit case test_default_parent_is_the_unit_cgroup: the default parent path shows in the refusal when no parent is configured.
func TestTestcgUnitDefaultParentIsTheUnitCgroup(t *testing.T) {
	b := tbNew(t)
	b.cfg("test_cgroup", "1")
	want := tbDefaultParent(os.Getuid())
	if fi, err := os.Stat(want); err == nil && fi.IsDir() {
		t.Skip("the factory-tests unit is installed on this host: the default parent exists and no refusal names it")
	}
	r := b.ftest(nil, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	tbContains(t, "stderr", r.Stderr, want)
}
