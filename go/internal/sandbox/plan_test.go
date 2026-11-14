package sandbox

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func workerPlan(t *testing.T, f *fixture, rwCwd bool) (*Cfg, []Op) {
	t.Helper()
	cfg, err := WorkerCfg(WorkerCfgArgs{AgentDir: f.agent, Cwd: f.fh + "/worktrees/wt1", FactoryHome: f.fh, DBDir: f.fh + "/db",
		RealClaude: f.claude, RwCwd: rwCwd, Home: f.home, Runtime: f.run, Go: GoPaths(f.home, f.agent, "", ""), Seams: f.seams()})
	must(t, err)
	ops, err := PlanAllow(cfg)
	must(t, err)
	return cfg, ops
}

func kinds(ops []Op, k OpKind) []string {
	var out []string
	for _, o := range ops {
		if o.Kind == k {
			out = append(out, o.Path)
		}
	}
	return out
}

// TestMountsReadOnly: the live factory home and ~/.local/lib/factory are never mounted writable; the worktree is the
// only writable bind besides the agent's scratch (its claude config dir, the Go cache dir) and the db dir, which is
// the same list as scout/workerbox.py make_cfg: rw = [db_dir, <agent>/claude, <agent>/go (when Go is present), cwd].
func TestMountsReadOnly(t *testing.T) {
	f := newFixture(t)
	for _, rwCwd := range []bool{true, false} {
		name := "ro cwd"
		if rwCwd {
			name = "rw cwd"
		}
		t.Run(name, func(t *testing.T) {
			cfg, ops := workerPlan(t, f, rwCwd)
			rw, ro := kinds(ops, OpBindRW), kinds(ops, OpBindRO)
			wantRW := []string{f.fh + "/db", f.agent + "/claude", f.agent + "/go"}
			if rwCwd {
				wantRW = append(wantRW, f.fh+"/worktrees/wt1")
			}
			if got := sortedCopy(rw); !reflect.DeepEqual(got, sortedCopy(wantRW)) {
				t.Errorf("writable binds = %v, want exactly %v", got, wantRW)
			}
			if !contains(ro, f.fh) {
				t.Errorf("the live factory home %s is not bound read-only: ro = %v", f.fh, ro)
			}
			if !rwCwd && !contains(ro, f.fh+"/worktrees/wt1") {
				t.Errorf("the read-only cwd is not bound read-only: ro = %v", ro)
			}
			protected := []string{f.fh, f.fh + "/bin", f.fh + "/roles", f.home, f.home + "/.local/lib/factory", f.home + "/.config", f.home + "/.local",
				f.fh + "/worktrees"}
			if err := RefuseWritable(ops, protected); err != nil {
				t.Errorf("%v", err)
			}
			for _, w := range rw {
				if w == f.home+"/.local/lib/factory" || strings.HasPrefix(w, f.home+"/.local/lib/factory/") {
					t.Errorf("~/.local/lib/factory is writable: %s", w)
				}
			}
			if !contains(kinds(ops, OpTmpfs), f.home) {
				t.Errorf("the real home is not hidden: tmpfs = %v", kinds(ops, OpTmpfs))
			}
			if got := cfg.TmpfsSizes[Realpath("/tmp")]; got != WorkerTmpSize {
				t.Errorf("/tmp tmpfs size = %q", got)
			}
		})
	}
	t.Run("worktrees is masked so siblings stay hidden", func(t *testing.T) {
		_, ops := workerPlan(t, f, true)
		if got := kinds(ops, OpMask); !reflect.DeepEqual(got, []string{f.fh + "/worktrees"}) {
			t.Errorf("masks = %v", got)
		}
		// the mask comes before the cwd bind: shorter paths first
		var maskAt, cwdAt int
		for i, o := range ops {
			if o.Kind == OpMask {
				maskAt = i
			}
			if o.Path == f.fh+"/worktrees/wt1" {
				cwdAt = i
			}
		}
		if maskAt > cwdAt {
			t.Errorf("mask at %d after the cwd bind at %d", maskAt, cwdAt)
		}
	})
	t.Run("RefuseWritable rejects a writable factory home", func(t *testing.T) {
		ops := []Op{{Kind: OpBindRW, Path: f.fh}}
		if err := RefuseWritable(ops, []string{f.fh}); err == nil {
			t.Error("a writable bind of the live factory home was accepted")
		}
	})
	t.Run("RefuseWritable rejects a writable parent of the lib", func(t *testing.T) {
		ops := []Op{{Kind: OpBindRW, Path: f.home + "/.local"}}
		if err := RefuseWritable(ops, []string{f.home + "/.local/lib/factory"}); err == nil {
			t.Error("a writable parent of ~/.local/lib/factory was accepted")
		}
	})
	t.Run("RefuseWritable rejects a writable home", func(t *testing.T) {
		if err := RefuseWritable([]Op{{Kind: OpBindRW, Path: f.home}}, []string{f.home}); err == nil {
			t.Error("a writable home was accepted")
		}
	})
	t.Run("RefuseWritable allows a listed path inside", func(t *testing.T) {
		if err := RefuseWritable([]Op{{Kind: OpBindRW, Path: f.fh + "/db"}}, []string{f.fh}); err != nil {
			t.Errorf("%v", err)
		}
	})
	t.Run("a cfg listing the factory home rw is refused by the plan check", func(t *testing.T) {
		cfg, _ := workerPlan(t, f, true)
		cfg.Ro = removeStr(cfg.Ro, f.fh)
		cfg.Rw = append(cfg.Rw, f.fh)
		ops, err := PlanAllow(cfg)
		must(t, err)
		if err := RefuseWritable(ops, []string{f.fh}); err == nil {
			t.Error("a writable live factory home passed the check")
		}
	})
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func removeStr(l []string, s string) []string {
	var out []string
	for _, x := range l {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func TestPlanAllow(t *testing.T) {
	d := Realpath(t.TempDir())
	for _, p := range []string{"/h/.ssh", "/h/a/b", "/rw1/sub", "/ro1", "/mask/deep", "/other"} {
		must(t, os.MkdirAll(d+p, 0o755))
	}
	touch(t, d+"/h/secret", 0o600)
	must(t, os.Symlink(d+"/h/secret", d+"/h/link-secret"))
	base := func() *Cfg {
		return &Cfg{Mode: "allow", Trial: d, TrialsRoot: d, HideDirs: []string{d + "/h"}}
	}
	t.Run("tmpfs first then the listed binds", func(t *testing.T) {
		c := base()
		c.Rw, c.Ro = []string{d + "/rw1"}, []string{d + "/ro1"}
		ops, err := PlanAllow(c)
		must(t, err)
		want := []Op{{OpTmpfs, d + "/h", "8m"}, {OpBindRW, d + "/rw1", ""}, {OpBindRO, d + "/ro1", ""}}
		if !reflect.DeepEqual(ops, want) {
			t.Errorf("ops = %v\nwant %v", ops, want)
		}
	})
	t.Run("shorter paths first, equal depth keeps the caller order", func(t *testing.T) {
		c := base()
		c.Rw = []string{d + "/rw1/sub", d + "/other"}
		c.Ro = []string{d + "/rw1"}
		ops, err := PlanAllow(c)
		must(t, err)
		got := []string{}
		for _, o := range ops[1:] {
			got = append(got, string(o.Kind)+":"+strings.TrimPrefix(o.Path, d))
		}
		want := []string{"bind-rw:/other", "bind-ro:/rw1", "bind-rw:/rw1/sub"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})
	t.Run("the same path in rw and ro is refused", func(t *testing.T) {
		c := base()
		c.Rw, c.Ro = []string{d + "/rw1"}, []string{d + "/rw1"}
		if _, err := PlanAllow(c); err == nil || !strings.Contains(err.Error(), "both rw and ro") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a missing mask dir is refused", func(t *testing.T) {
		c := base()
		c.Masks = []string{d + "/nope"}
		if _, err := PlanAllow(c); err == nil || !strings.Contains(err.Error(), "mask dir missing") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a mask becomes a tmpfs op after its parent", func(t *testing.T) {
		c := base()
		c.Masks = []string{d + "/mask"}
		ops, err := PlanAllow(c)
		must(t, err)
		if ops[len(ops)-1].Kind != OpMask || ops[len(ops)-1].Size != "8m" {
			t.Errorf("ops = %v", ops)
		}
	})
	t.Run("hide dirs nested in another hide dir are dropped", func(t *testing.T) {
		c := base()
		c.HideDirs = []string{d + "/h/.ssh", d + "/h", d + "/h/a"}
		ops, _ := PlanAllow(c)
		if got := kinds(ops, OpTmpfs); !reflect.DeepEqual(got, []string{d + "/h"}) {
			t.Errorf("tmpfs = %v", got)
		}
	})
	t.Run("duplicate hide dirs collapse", func(t *testing.T) {
		c := base()
		c.HideDirs = []string{d + "/h", d + "/h"}
		ops, _ := PlanAllow(c)
		if len(kinds(ops, OpTmpfs)) != 1 {
			t.Errorf("ops = %v", ops)
		}
	})
	t.Run("a missing hide dir and / are skipped", func(t *testing.T) {
		c := base()
		c.HideDirs = []string{d + "/gone", "/"}
		ops, _ := PlanAllow(c)
		if len(ops) != 0 {
			t.Errorf("ops = %v", ops)
		}
	})
	t.Run("per-tree and default tmpfs sizes", func(t *testing.T) {
		c := base()
		c.HideDirs = []string{d + "/h", d + "/other"}
		c.TmpfsSize, c.TmpfsSizes = "32m", map[string]string{d + "/h": "64m"}
		ops, _ := PlanAllow(c)
		got := map[string]string{}
		for _, o := range ops {
			got[o.Path] = o.Size
		}
		if got[d+"/h"] != "64m" || got[d+"/other"] != "32m" {
			t.Errorf("sizes = %v", got)
		}
	})
	t.Run("hide files only when present", func(t *testing.T) {
		c := base()
		c.HideFiles = []string{d + "/h/secret", d + "/h/link-secret", d + "/h/none"}
		ops, _ := PlanAllow(c)
		if got := kinds(ops, OpHideFile); !reflect.DeepEqual(got, []string{d + "/h/secret", d + "/h/link-secret"}) {
			t.Errorf("hide files = %v", got)
		}
	})
	t.Run("hide files come last", func(t *testing.T) {
		c := base()
		c.Rw, c.HideFiles = []string{d + "/rw1"}, []string{d + "/h/secret"}
		ops, _ := PlanAllow(c)
		if ops[len(ops)-1].Kind != OpHideFile {
			t.Errorf("ops = %v", ops)
		}
	})
	t.Run("an empty cfg plans nothing", func(t *testing.T) {
		ops, err := PlanAllow(&Cfg{Mode: "allow"})
		if err != nil || len(ops) != 0 {
			t.Errorf("ops = %v, err = %v", ops, err)
		}
	})
	t.Run("Writable lists only rw binds", func(t *testing.T) {
		c := base()
		c.Rw, c.Ro = []string{d + "/rw1"}, []string{d + "/ro1"}
		ops, _ := PlanAllow(c)
		if got := Writable(ops); !reflect.DeepEqual(got, []string{d + "/rw1"}) {
			t.Errorf("writable = %v", got)
		}
	})
	t.Run("denyDirs reduces to the outermost real dirs", func(t *testing.T) {
		c := &Cfg{HideDirs: []string{d + "/h", d + "/h/a", d + "/gone"}}
		got := denyDirs(c, []string{d + "/other", d + "/h/a/b"})
		if !reflect.DeepEqual(got, []string{d + "/h", d + "/other"}) {
			t.Errorf("denyDirs = %v", got)
		}
	})
}
