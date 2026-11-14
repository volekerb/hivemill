package tick

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The fake child is this test binary re-executed: TestMain sees TICKFAKE_SCRIPT, plays step N of the script (N =
// number of children that ran before) and exits. It stands where python3 + bin/factory stand in production.

type step struct {
	Exit     int             `json:"exit"`
	Signal   bool            `json:"signal"`
	Laps     [][2]any        `json:"laps"`
	Counters json.RawMessage `json:"counters"`
	Tail     float64         `json:"tail"`
	PreReset *float64        `json:"pre_reset"`
	Stop     bool            `json:"stop"`
	State    map[string]any  `json:"state"`
	Garble   bool            `json:"garble"`
	Delete   bool            `json:"delete"`
	Extra    map[string]any  `json:"extra"`
}

type call struct {
	Units   string   `json:"units"`
	Impl    string   `json:"impl"`
	Handoff string   `json:"handoff"`
	Stdin   string   `json:"stdin"`
	DocRaw  string   `json:"doc"`
	Argv    []string `json:"argv"`
}

func TestMain(m *testing.M) {
	if sp := os.Getenv("TICKFAKE_SCRIPT"); sp != "" {
		os.Exit(fakeChild(sp))
	}
	os.Exit(m.Run())
}

func fakeChild(scriptPath string) int {
	var script []step
	b, _ := os.ReadFile(scriptPath)
	if err := json.Unmarshal(b, &script); err != nil {
		fmt.Fprintln(os.Stderr, "fake: bad script", err)
		return 99
	}
	logPath := os.Getenv("TICKFAKE_LOG")
	n := 0
	if lb, err := os.ReadFile(logPath); err == nil {
		n = strings.Count(string(lb), "\n")
	}
	hp := os.Getenv("FACTORY_TICK_HANDOFF")
	doc, _ := os.ReadFile(hp)
	stdin, _ := os.ReadFile("/dev/stdin")
	rec := call{Units: os.Args[len(os.Args)-1], Impl: os.Getenv("FACTORY_CLI_IMPL"), Handoff: hp, Stdin: string(stdin),
		DocRaw: string(doc), Argv: os.Args[1:]}
	lf, _ := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	lj, _ := json.Marshal(rec)
	lf.Write(append(lj, '\n'))
	lf.Close()
	if n >= len(script) {
		fmt.Fprintln(os.Stderr, "fake: unexpected extra child", rec.Units)
		return 98
	}
	st := script[n]
	fmt.Printf("child%d\n", n)
	if st.Signal {
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	if st.Exit != 0 {
		return st.Exit
	}
	var in map[string]json.RawMessage
	json.Unmarshal(doc, &in)
	if st.Delete {
		os.Remove(hp)
		return 0
	}
	if st.Garble {
		os.WriteFile(hp, []byte("{not json"), 0o600)
		return 0
	}
	state := in["state"]
	if st.State != nil {
		state, _ = json.Marshal(st.State)
	}
	laps := st.Laps
	if laps == nil {
		laps = [][2]any{}
	}
	ctr := st.Counters
	if len(ctr) == 0 || string(ctr) == "null" {
		ctr = json.RawMessage("{}")
	}
	out := map[string]any{"v": 1, "pid": os.Getppid(), "timers": true, "state": json.RawMessage(state), "laps": laps,
		"counters": ctr, "tail": st.Tail, "stop": st.Stop}
	if st.PreReset != nil {
		out["pre_reset"] = *st.PreReset
	}
	for k, v := range st.Extra {
		out[k] = v
	}
	ob, _ := json.Marshal(out)
	os.WriteFile(hp, ob, 0o600)
	return 0
}

type rig struct {
	t      *testing.T
	home   string
	out    bytes.Buffer
	errb   bytes.Buffer
	log    string
	timers bool
	goFns  map[string]GoFunc
}

func newRig(t *testing.T, script []step) *rig {
	t.Helper()
	r := &rig{t: t, home: t.TempDir(), goFns: map[string]GoFunc{}}
	sp := filepath.Join(t.TempDir(), "script.json")
	b, _ := json.Marshal(script)
	os.WriteFile(sp, b, 0o644)
	r.log = filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("TICKFAKE_SCRIPT", sp)
	t.Setenv("TICKFAKE_LOG", r.log)
	return r
}

// run executes the tick with a fake clock that advances one second per reading.
func (r *rig) run() int {
	r.t.Helper()
	self, _ := os.Executable()
	tm := 0.0
	return Run(Config{
		Home: r.home, Stdout: &r.out, Stderr: &r.errb, Environ: os.Environ(), Python: self, Entry: "bin/factory",
		Clock:    func() float64 { tm++; return tm },
		Getpid:   os.Getpid,
		TimersOn: func() bool { return r.timers },
		Go:       r.goFns,
	})
}

func (r *rig) calls() []call {
	r.t.Helper()
	b, err := os.ReadFile(r.log)
	if err != nil {
		return nil
	}
	var cs []call
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var c call
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			r.t.Fatalf("log line %q: %v", l, err)
		}
		cs = append(cs, c)
	}
	return cs
}

func allUnits() []string {
	var n []string
	for _, u := range walkUnits() {
		n = append(n, u.Name)
	}
	return n
}

func indexOf(l []string, s string) int {
	for i, x := range l {
		if x == s {
			return i
		}
	}
	return -1
}

// childUnitLists splits the walk where TICK_UNITS has offline between judge_ci_prune and scout.
func childUnitLists() (first, second string) {
	all := allUnits()
	i := indexOf(all, pruneUnit)
	return strings.Join(all[:i+1], ","), strings.Join(all[i+1:], ",")
}

func TestTickLock(t *testing.T) {
	r := newRig(t, []step{{}})
	f, busy, err := TakeLock(r.home)
	if err != nil || busy {
		t.Fatal(err, busy)
	}
	if code := r.run(); code != 0 || r.out.String() != "another tick/up is running; skipping\n" {
		t.Fatalf("busy: exit %d out %q", code, r.out.String())
	}
	if len(r.calls()) != 0 {
		t.Fatal("a child started while the lock was busy")
	}
	f.Close()

	// free: held while a unit runs, released at return; the file mode is 0666 minus the umask
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	os.Remove(filepath.Join(r.home, "run", LockName))
	held := false
	r.goFns["judge_ci_prune"] = func(u *UnitCtx) error {
		g, busy, _ := TakeLock(r.home)
		if g != nil {
			g.Close()
		}
		held = busy
		return nil
	}
	r.out.Reset()
	// script: child 0 ok, child 1 ok
	b, _ := json.Marshal([]step{{}, {}})
	os.WriteFile(os.Getenv("TICKFAKE_SCRIPT"), b, 0o644)
	if code := r.run(); code != 0 {
		t.Fatalf("exit %d %s", code, r.errb.String())
	}
	if !held {
		t.Fatal("the lock was not held while a unit ran")
	}
	fi, err := os.Stat(filepath.Join(r.home, "run", LockName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("lock mode %v, want 0644 (0666 minus umask 022)", fi.Mode().Perm())
	}
	g, busy, _ := TakeLock(r.home)
	if busy {
		t.Fatal("the lock is still held after Run returned")
	}
	g.Close()
}

func TestSegmentsOneChildWithoutGoUnits(t *testing.T) {
	segs := segments(map[string]GoFunc{})
	if len(segs) != 1 || segs[0].units[len(segs[0].units)-1] != "util" || indexOf(segs[0].units, OfflineUnit) < 0 {
		t.Fatalf("want one all-Python segment ending at util with the offline row inside, got %+v", segs)
	}
}

func TestAgentsNullHandoff(t *testing.T) {
	h, err := ParseHandoff([]byte(`{"v":1,"state":{"agents":null},"laps":[],"counters":{},"tail":0,"stop":false}`))
	if err != nil || !h.State.AgentsNull() {
		t.Fatalf("parse %v agentsNull=%v", err, h.State.AgentsNull())
	}
}

func TestTickChildren(t *testing.T) {
	for _, timers := range []bool{false, true} {
		r := newRig(t, []step{{}})
		r.timers = timers
		if code := r.run(); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, r.errb.String())
		}
		cs := r.calls()
		all := allUnits()
		i := indexOf(all, pruneUnit)
		// Python --phases forbids gaps, so the offline row rides along between judge_ci_prune and side
		full := append(append(append([]string(nil), all[:i+1]...), OfflineUnit), all[i+1:]...)
		if timers {
			full = append(full, EmitUnit)
		}
		want := phaseSpec(full)
		if len(cs) != 1 || cs[0].Units != want {
			t.Fatalf("timers=%v: calls %+v, want one child with --phases %s", timers, cs, want)
		}
	}
	// a Go unit at judge_ci_prune: two children around it
	all := allUnits()
	i := indexOf(all, pruneUnit)
	r := newRig(t, []step{{State: map[string]any{"agents": []any{}, "t": 1.5}}, {}})
	var seen string
	r.goFns["judge_ci_prune"] = func(u *UnitCtx) error {
		seen = string(u.State["t"])
		u.Stdout("go-unit\n")
		return nil
	}
	if code := r.run(); code != 0 {
		t.Fatalf("exit %d: %s", code, r.errb.String())
	}
	cs := r.calls()
	if len(cs) != 2 {
		t.Fatalf("want 2 children, got %+v", cs)
	}
	if cs[0].Units != phaseSpec(all[:i]) || !strings.HasSuffix(cs[0].Units, "test_orphans") ||
		cs[1].Units != phaseSpec(all[i+1:]) || !strings.HasPrefix(cs[1].Units, "side,") || !strings.HasSuffix(cs[1].Units, ",util") {
		t.Fatalf("lists %q / %q", cs[0].Units, cs[1].Units)
	}
	if seen != "1.5" {
		t.Fatalf("the Go unit did not see the child's state: %q", seen)
	}
	for _, c := range cs {
		if c.Impl != "python" || c.Handoff != filepath.Join(r.home, "run", HandoffName) || c.Stdin != "" {
			t.Fatalf("env/stdin of child %+v", c)
		}
		var d map[string]any
		json.Unmarshal([]byte(c.DocRaw), &d)
		if d["v"].(float64) != 1 || int(d["pid"].(float64)) != os.Getpid() {
			t.Fatalf("handoff %s", c.DocRaw)
		}
	}
	if !strings.Contains(cs[1].DocRaw, `"t":1.5`) {
		t.Fatalf("state not handed to the second child: %s", cs[1].DocRaw)
	}
	if got := r.out.String(); got != "child0\ngo-unit\nchild1\n" {
		t.Fatalf("stdout interleaving: %q", got)
	}
}

func TestTickChildExit(t *testing.T) {
	for _, code := range []int{1, 2, 3} {
		r := newRig(t, []step{{Exit: code}, {}})
		r.timers = true
		r.goFns["judge_ci_prune"] = func(*UnitCtx) error { t.Error("a Go unit ran after a failed child"); return nil }
		if got := r.run(); got != code {
			t.Fatalf("child exit %d: Go exit %d", code, got)
		}
		if n := len(r.calls()); n != 1 {
			t.Fatalf("child exit %d: %d children, want 1 (no later child, no emit)", code, n)
		}
	}
	r := newRig(t, []step{{Signal: true}})
	if got := r.run(); got != 1 || !strings.Contains(r.errb.String(), "factory(go): tick child killed by killed") {
		t.Fatalf("signal: exit %d stderr %q", got, r.errb.String())
	}
}

func TestTickStop(t *testing.T) {
	// a Go unit at the end splits the walk; the first child says stop: nothing runs after it
	r := newRig(t, []step{{Stop: true}, {}})
	r.goFns["util"] = func(*UnitCtx) error { t.Error("unit ran after stop"); return nil }
	if got := r.run(); got != 0 {
		t.Fatalf("exit %d %s", got, r.errb.String())
	}
	if n := len(r.calls()); n != 1 {
		t.Fatalf("stop=true: %d children, want 1", n)
	}
	// timers on and the stopping child had no emit: exactly one emit child follows
	r2 := newRig(t, []step{{Stop: true}, {}})
	r2.timers = true
	r2.goFns["util"] = func(*UnitCtx) error { return nil }
	if got := r2.run(); got != 0 {
		t.Fatalf("exit %d %s", got, r2.errb.String())
	}
	if cs := r2.calls(); len(cs) != 2 || cs[1].Units != "emit" {
		t.Fatalf("calls %+v", cs)
	}
}

func TestTickOfflineAfterGoPrune(t *testing.T) {
	r := newRig(t, []step{{}, {Stop: true}, {}})
	r.timers = true
	r.goFns["judge_ci_prune"] = func(u *UnitCtx) error {
		u.State["agents"] = json.RawMessage("null")
		return nil
	}
	if got := r.run(); got != 0 {
		t.Fatalf("exit %d %s", got, r.errb.String())
	}
	wantPre := "boot,digest,tmp_alert,remote_url_alert,main_protection,tmp_clean,tmp_harness,red_main,issue_intake,reap_serves,reap_orphans,test_orphans"
	if cs := r.calls(); len(cs) != 2 || cs[0].Units != wantPre || cs[1].Units != "offline" {
		t.Fatalf("agents null after the Go prune unit: calls %+v", cs)
	}
}

func TestTickHandoffGarbled(t *testing.T) {
	cases := map[string]step{
		"garbled":      {Garble: true},
		"deleted":      {Delete: true},
		"bad lap":      {Laps: [][2]any{{"x", "y"}}},
		"bad counters": {Counters: json.RawMessage(`{"a":"s"}`)},
	}
	for name, st := range cases {
		r := newRig(t, []step{st, {}})
		r.goFns["util"] = func(*UnitCtx) error { return nil }
		got := r.run()
		want := "factory(go): tick handoff unreadable after boot,hygiene,offline,side,reap,flow,start,nudge,publish,recycle,relaunches\n"
		if got != 1 || !strings.HasSuffix(r.errb.String(), want) {
			t.Errorf("%s: exit %d stderr %q", name, got, r.errb.String())
		}
		if n := len(r.calls()); n < 1 {
			t.Errorf("%s: %d children, want at least 1", name, n)
		}
	}
}

func TestTickGoUnitError(t *testing.T) {
	r := newRig(t, []step{{}, {}})
	r.goFns["judge_ci_prune"] = func(*UnitCtx) error { return fmt.Errorf("boom") }
	if got := r.run(); got != 1 || r.errb.String() != "factory(go): boom\n" || len(r.calls()) != 1 {
		t.Fatalf("exit %d stderr %q calls %d", got, r.errb.String(), len(r.calls()))
	}
	r = newRig(t, []step{{}, {}})
	r.goFns["judge_ci_prune"] = func(*UnitCtx) error { panic("oops") }
	if got := r.run(); got != 1 || !strings.Contains(r.errb.String(), "factory(go): unit judge_ci_prune panicked: oops") {
		t.Fatalf("panic: exit %d stderr %q", got, r.errb.String())
	}
}

func TestHandoffKeepsUnknownKeys(t *testing.T) {
	h, err := ParseHandoff([]byte(`{"v":1,"state":{},"laps":[],"counters":{},"tail":0,"stop":false,"future":{"a":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.Marshal()
	if !strings.Contains(string(b), `"future":{"a":1}`) {
		t.Fatalf("unknown key dropped: %s", b)
	}
	if _, err := ParseHandoff([]byte(`{"v":2,"state":{},"laps":[],"counters":{},"tail":0,"stop":false}`)); err == nil {
		t.Fatal("v=2 must be refused")
	}
}

func TestUnitsMatchPython(t *testing.T) {
	want, err := os.ReadFile("testdata/units.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := formatTable(); got != string(want) {
		t.Fatalf("unit table differs from testdata/units.txt:\n%s\nwant:\n%s", got, want)
	}
	if n := len(walkUnits()); n != 42 {
		t.Fatalf("walk has %d units, want 42 (43 numbered rows less emit, plus none)", n)
	}
	if b := strings.Join(Blocks(), ","); b != "boot,hygiene,offline,side,reap,flow,start,nudge,publish,tail" {
		t.Fatalf("blocks %s", b)
	}
}

func TestRegisterGoRejects(t *testing.T) {
	for _, bad := range []string{"nope", OfflineUnit, EmitUnit} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registerGo(%q) did not panic", bad)
				}
			}()
			registerGo(bad, func(*UnitCtx) error { return nil })
		}()
	}
}

func TestTickEmitChildGetsMergedLaps(t *testing.T) {
	pre := 0.5
	r := newRig(t, []step{
		{PreReset: &pre, Laps: [][2]any{{"migrate_db", 1.0}}, Counters: json.RawMessage(`{"a":1,"b":0.5}`), State: map[string]any{"agents": []any{}}},
		{Laps: [][2]any{{"scout", 2.0}}, Counters: json.RawMessage(`{"b":0.25,"c":3}`)},
		{},
	})
	r.timers = true
	r.goFns["judge_ci_prune"] = func(*UnitCtx) error { return nil }
	r.goFns["util"] = func(*UnitCtx) error { return nil }
	if got := r.run(); got != 0 {
		t.Fatalf("exit %d %s", got, r.errb.String())
	}
	cs := r.calls()
	if len(cs) != 3 || cs[2].Units != "emit" {
		t.Fatalf("calls %+v", cs)
	}
	var d struct {
		Laps     [][2]any
		Counters json.RawMessage
		Total    float64
	}
	if err := json.Unmarshal([]byte(cs[2].DocRaw), &d); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, l := range d.Laps {
		names = append(names, l[0].(string))
	}
	if strings.Join(names, ",") != "migrate_db,judge_ci_prune,scout,util" || string(d.Counters) != `{"a":1,"b":0.75,"c":3}` || d.Total <= 0 {
		t.Fatalf("emit handoff: %s", cs[2].DocRaw)
	}
}
