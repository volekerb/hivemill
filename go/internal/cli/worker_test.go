package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/identity"
	"time"
)

// fakeProc writes <root>/<pid>/{stat,cmdline}.
type fakeProc struct{ root string }

func (f fakeProc) add(t *testing.T, pid, ppid int, argv ...string) {
	t.Helper()
	d := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (a (b) c) S " + strconv.Itoa(ppid) + " 1 1 0\n"
	if err := os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCallerIsWorker(t *testing.T) {
	const own, parent = 500, 400
	cases := []struct {
		name  string
		build func(t *testing.T, f fakeProc)
		want  string
	}{
		{"settings flag, coder", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude", "--settings", "/x/roles/settings/coder.json")
		}, "coder"},
		{"settings=, qa", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "/usr/bin/claude", "--settings=/x/settings/qa.json")
		}, "qa"},
		{"claude without settings", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude")
		}, ""},
		{"lead is not a worker role", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude", "--settings", "/x/settings/lead.json")
		}, ""},
		{"the nearest claude decides: a worker above it does not count", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude")
			f.add(t, 300, 200, "claude", "--settings", "/x/settings/coder.json")
		}, ""},
		{"claude within the first three argv words", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "node", "--flag", "/opt/claude", "--settings", "/s/settings/judge.json")
		}, "judge"},
		{"claude as the fourth word is not claude", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 1, "a", "b", "c", "claude", "--settings", "/s/settings/judge.json")
		}, ""},
		{"no claude: the chain ends at pid 1", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 1, "zsh")
		}, ""},
		{"own pid absent: start at the parent", func(t *testing.T, f fakeProc) {
			f.add(t, parent, 300, "claude", "--settings", "/x/settings/reviewer.json")
		}, "reviewer"},
		{"the first settings value that names a worker wins", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude", "--settings", "/x/settings/lead.json", "--settings=/x/settings/pm.json")
		}, "pm"},
		{"non-ASCII dir before the role file", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude", "--settings", "/settings/é/settings/coder.json")
		}, "coder"},
		// Go \w is ASCII, Python \w is Unicode: both give "" only because every worker role is ASCII.
		{"non-ASCII role name is not a worker", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude", "--settings", "/settings/cödér.json")
		}, ""},
		{"trailing newline after the role file (Python $)", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude", "--settings", "/x/settings/coder.json\n")
		}, "coder"},
		{"a settings path that is not settings/<role>.json", func(t *testing.T, f fakeProc) {
			f.add(t, own, parent, "factory")
			f.add(t, parent, 300, "claude", "--settings", "/x/coder.json")
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeProc{t.TempDir()}
			tc.build(t, f)
			if got := ancestryWorker(f.root, own, parent); got != tc.want {
				t.Fatalf("ancestryWorker = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("no claude within 64 hops", func(t *testing.T) {
		f := fakeProc{t.TempDir()}
		for i := 0; i < 70; i++ { // a chain of 70 shells with claude at the end
			pid := 1000 + i
			if i == 69 {
				f.add(t, pid, 2, "claude", "--settings", "/x/settings/coder.json")
			} else {
				f.add(t, pid, pid+1, "sh")
			}
		}
		if got := ancestryWorker(f.root, 1000, 999); got != "" {
			t.Fatalf("claude beyond 64 hops must not count, got %q", got)
		}
		if got := ancestryWorker(f.root, 1007, 999); got != "coder" { // 62 hops away
			t.Fatalf("claude within 64 hops must count, got %q", got)
		}
	})
}

func testCtx(t *testing.T, r *rig, who string) (*ctx, func()) {
	t.Helper()
	db := dbtest.Raw(t, r.dbPath)
	env := identity.Env{
		Getenv:    func(k string) string { return r.env[k] },
		LookupEnv: func(k string) (string, bool) { v, ok := r.env[k]; return v, ok },
		Uid:       os.Getuid(),
		PwDir:     func(int) (string, error) { return r.pw, nil },
		Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
	}
	return &ctx{db: db, who: who, env: env, deps: &Deps{Getpid: func() int { return 500 }, Getppid: func() int { return 400 }}}, func() { db.Close() }
}

// TestCallerIsWorkerOrder: ancestry, FACTORY_ROLE, DB role, then the pane row, as in _detect_worker.
func TestCallerIsWorkerOrder(t *testing.T) {
	proc := t.TempDir()
	r := newRig(t, map[string]string{"FACTORY_AGENT": "claude-director", "FACTORY_PROC_DIR": proc})
	r.sql("INSERT INTO agents(name,role,status,pane_id) VALUES('claude-director','director','live','p2'),('claude-coder-x','coder','live','p1')")
	check := func(who, wantRole string, wantDel bool) {
		t.Helper()
		c, done := testCtx(t, r, who)
		defer done()
		if role, del := callerIsWorker(c); role != wantRole || del != wantDel {
			t.Fatalf("%s: got %q delegate=%v, want %q delegate=%v", who, role, del, wantRole, wantDel)
		}
	}
	check("claude-director", "", false)
	r.env["FACTORY_ROLE"] = "qa"
	check("claude-director", "qa", false)
	r.env["FACTORY_ROLE"] = "lead" // not a worker role: ignored
	check("claude-coder-x", "coder", false)
	r.env["HERDR_PANE_ID"] = "p1"
	check("claude-director", "", true) // a live coder row on the pane: Python's path may write events
	r.env["HERDR_PANE_ID"] = "p2"
	check("claude-director", "", false)
	fakeProc{proc}.add(t, 400, 1, "claude", "--settings", "/s/settings/cleaner.json")
	check("claude-director", "cleaner", false)
}

// TestProcDirIgnoredOnLiveHome: a set FACTORY_PROC_DIR on a live home makes Python record an event; Go delegates.
func TestProcDirIgnoredOnLiveHome(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner", "FACTORY_PROC_DIR": t.TempDir()})
	taskTables(r)
	r.sql("INSERT INTO tasks(id,goal_id,title,created,updated) VALUES('T-1','G-1','t','x','x')")
	live := filepath.Join(r.pw, "factory") // the rig's fake passwd home: <pw>/factory is the live home
	if err := os.Rename(r.home, live); err != nil {
		t.Fatal(err)
	}
	r.env["FACTORY_HOME"] = live
	if code := r.run("task", "show", "T-1"); code != 0 || len(r.execs) != 1 {
		t.Fatalf("code %d execs %d out %q err %q: want one hand-over to python", code, len(r.execs), r.out.String(), r.errb.String())
	}
}

func TestWorkerDetectRolesMatchPython(t *testing.T) {
	src, err := os.ReadFile(repoFile(t, "bin/factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^WORKER_ROLES = \{([^}]*)\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("WORKER_ROLES literal not found in bin/factory_cli.py")
	}
	var py, gos []string
	for _, w := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		py = append(py, string(w[1]))
	}
	for r := range workerRoles {
		gos = append(gos, r)
	}
	sort.Strings(py)
	sort.Strings(gos)
	if strings.Join(py, ",") != strings.Join(gos, ",") {
		t.Fatalf("workerRoles %v differ from Python WORKER_ROLES %v", gos, py)
	}
}

// FuzzAncestryWorker feeds arbitrary stat/cmdline bytes through procStatPPid and ancestryWorker: no panic, and the
// answer is "" or a worker role.
func FuzzAncestryWorker(f *testing.F) {
	f.Add([]byte("500 (a (b) c) S 400 1 1 0\n"), []byte("claude\x00--settings\x00/x/settings/coder.json\x00"))
	f.Add([]byte(""), []byte(""))
	f.Add([]byte(")"), []byte("\x00\x00claude"))
	f.Add([]byte("1 (x) S abc"), []byte("--settings=/settings/\xff.json"))
	f.Fuzz(func(t *testing.T, stat, cmdline []byte) {
		root := t.TempDir()
		d := filepath.Join(root, "500")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "stat"), stat, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "cmdline"), cmdline, 0o644); err != nil {
			t.Fatal(err)
		}
		procStatPPid(root, 500)
		got := ancestryWorker(root, 500, 400)
		if got != "" && !workerRoles[got] {
			t.Fatalf("ancestryWorker = %q, not empty and not a worker role", got)
		}
	})
}
