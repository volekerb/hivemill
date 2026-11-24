package tick

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Tests for auto_clear_dialog (factory_cli.py:17510) and finish_pending_nudges (factory_cli.py:14631) with a PATH herdr
// stub. The pane texts are the fixtures of tests/test_auto_clear.py.

const (
	dlgWarn = "Settings Warning\n\n /srv/u/.claude/settings.json\n  └ permissions.deny: Invalid value\n\n" +
		"The values listed above were skipped; the rest of the file is in effect.\n\n" +
		"❯ 1. Continue\n  2. Exit and fix manually\n"
	dlgPerm    = "Bash command\n  cat /etc/hosts\n\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, and don't ask again\n  3. No\n"
	dlgUnknown = "Choose a theme:\n❯ 1. Dark\n  2. Light\n"
)

// TestPythonConstants quotes the Python constants the port must carry (factory_cli.py:17354, 17355, 14628).
func TestPythonConstants(t *testing.T) {
	src, err := os.ReadFile("../../../bin/factory_cli.py")
	if err != nil {
		t.Skipf("python source not in this tree: %v", err)
	}
	for _, c := range []struct {
		line string
		want int
		got  int
	}{
		{"SETTINGS_ESC_COOLDOWN_S = 300", 300, settingsEscCooldownS},
		{"SETTINGS_ESC_MAX_PER_DAY = 3", 3, settingsEscMaxPerDay},
		{"NUDGE_PENDING_TTL_S = 600", 600, nudgePendingTTLS},
		{"STARTUP_GRACE_S = 180", 180, startupGraceS},
		{"CURSOR_START_LOG_MAX_S = 600", 600, cursorStartLogMaxS},
	} {
		if !strings.Contains(string(src), c.line) {
			t.Errorf("python no longer has %q", c.line)
		}
		if c.got != c.want {
			t.Errorf("%s: go has %d", c.line, c.got)
		}
	}
	if escKey != "esc" {
		t.Errorf("escKey = %q", escKey)
	}
}

type dlgFix struct {
	*unitFix
}

func newDlgFix(t *testing.T) *dlgFix {
	t.Helper()
	d := &dlgFix{newUnitFix(t)}
	t.Setenv("FACTORY_AUTO_CLEAR_NOW", "")
	return d
}

// pane makes `herdr agent list` report name blocked and `agent read` return text.
func (d *dlgFix) pane(name, role, text string, blockedSince any) {
	d.t.Helper()
	b, _ := json.Marshal(map[string]any{"text": text})
	d.stub.set("agent_read", "out", string(b))
	d.stub.set("agent_list", "out", `{"agents":[{"name":"`+name+`","agent":"claude","pane_id":"p-1","agent_status":"blocked"}]}`)
	d.exec("INSERT OR REPLACE INTO agents(name,role,status,task_id,pane_id,started,blocked_since,spawn_args) VALUES(?,?,?,?,?,?,?,'{}')",
		name, role, "live", nil, "p-1", "2026-11-17T08:00:00+00:00", blockedSince)
}

func (d *dlgFix) clear(name string) bool {
	d.t.Helper()
	r, err := newReapDB(d.db)
	if err != nil {
		d.t.Fatal(err)
	}
	c, _ := newHerdrClient(r)
	a, err := readDlgAgent(d.db, name)
	if err != nil || a == nil {
		d.t.Fatalf("agent %s: %v %v", name, a, err)
	}
	return autoClearDialog(r, c, &UnitCtx{DB: d.db, State: State{}, Stdout: func(string) {}}, a, d.now)
}

func (d *dlgFix) keys() []string {
	var out []string
	for _, c := range d.stub.calls() {
		if strings.HasPrefix(c, "agent send-keys") {
			out = append(out, c)
		}
	}
	return out
}

// onlyEsc asserts the stub log holds exactly n send-keys calls and every one is `send-keys <name> esc`.
func (d *dlgFix) onlyEsc(name string, n int) {
	d.t.Helper()
	for _, k := range d.keys() {
		if k != "agent send-keys "+name+" esc" {
			d.t.Errorf("key %q is not `send-keys %s esc`", k, name)
		}
	}
	if len(d.keys()) != n {
		d.t.Errorf("%d keys %q, want %d", len(d.keys()), d.keys(), n)
	}
}

func (d *dlgFix) noKeys() {
	d.t.Helper()
	if got := d.keys(); len(got) != 0 {
		d.t.Errorf("keys sent: %q", got)
	}
	if d.stub.count("agent read") == 0 {
		d.t.Error("the pane was never read: the no-keys assertion is vacuous")
	}
}

func TestAutoClearDialog(t *testing.T) {
	t.Run("settings warning gets Esc only", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("coder-1", "coder", dlgWarn, nil)
		if !d.clear("coder-1") {
			t.Fatal("no key sent")
		}
		d.onlyEsc("coder-1", 1)
		if n := d.col("SELECT count(*) FROM events WHERE action='settings-warning-dismissed' AND entity_id='coder-1'"); n != "1" {
			t.Errorf("dismissed events = %s", n)
		}
		if n := d.col("SELECT count(*) FROM messages WHERE subject LIKE 'Settings Warning on coder-1%' AND recipient='director' AND kind='alert'"); n != "1" {
			t.Errorf("director alerts = %s", n)
		}
	})
	t.Run("HQ permission dialog past hq_unblock_s gets Esc only", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("zz-lead", "lead", dlgPerm, d.now-100000)
		if !d.clear("zz-lead") {
			t.Fatal("no key sent")
		}
		d.onlyEsc("zz-lead", 1)
		if n := d.col("SELECT count(*) FROM events WHERE action='unblock' AND entity_id='zz-lead'"); n != "1" {
			t.Errorf("unblock events = %s", n)
		}
	})
	t.Run("worker permission dialog past blocked_alert_s is denied with an order", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("coder-1", "coder", dlgPerm, d.now-100000)
		if !d.clear("coder-1") {
			t.Fatal("not handled")
		}
		if n := d.col("SELECT count(*) FROM messages WHERE subject='UNBLOCK' AND recipient='coder-1'"); n != "1" {
			t.Errorf("UNBLOCK orders = %s", n)
		}
		for _, k := range d.keys() {
			if k != "agent send-keys coder-1 esc" {
				t.Errorf("a key other than Esc was sent: %q", k)
			}
		}
	})
	t.Run("permission dialog before the timeout gets no key", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("coder-1", "coder", dlgPerm, d.now-10)
		if d.clear("coder-1") {
			t.Fatal("sent")
		}
		d.noKeys()
	})
	t.Run("unknown dialog gets no key", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("coder-1", "coder", dlgUnknown, d.now-100000)
		if d.clear("coder-1") {
			t.Fatal("sent")
		}
		d.noKeys()
	})
	t.Run("dialog only in scrollback gets no key", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("coder-1", "coder", dlgWarn+strings.Repeat("output line\n", 40), nil)
		if d.clear("coder-1") {
			t.Fatal("sent")
		}
		d.noKeys()
	})
	t.Run("boss pane with unsent input gets no key", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("boss-1", "boss", dlgWarn+"\n> half typed message\n", nil)
		if d.clear("boss-1") {
			t.Fatal("sent")
		}
		d.noKeys()
	})
	t.Run("an unreadable pane is one auto-clear-failed event, never an error", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("coder-1", "coder", dlgWarn, nil)
		d.stub.set("agent_read", "out", `"not an object"`)
		if d.clear("coder-1") {
			t.Fatal("sent")
		}
		if n := d.col("SELECT count(*) FROM events WHERE action='auto-clear-failed' AND entity_id='coder-1'"); n != "1" {
			t.Errorf("auto-clear-failed events = %s", n)
		}
		if len(d.keys()) != 0 {
			t.Errorf("keys = %q", d.keys())
		}
	})
}

// TestAutoClearCooldownAndDailyCap is Python's test_persisting_warning_esc_bounded_then_one_stuck_alert with the clock
// pinned by FACTORY_AUTO_CLEAR_NOW: Esc at 0, none at +10 s, Esc at 310 and 620, none at 930 or 940, one stuck alert.
func TestAutoClearCooldownAndDailyCap(t *testing.T) {
	d := newDlgFix(t)
	d.pane("zz-lead", "lead", dlgWarn, nil)
	at := func(sec int) bool {
		t.Setenv("FACTORY_AUTO_CLEAR_NOW", fmt.Sprintf("2026-11-17T10:%02d:%02d+00:00", sec/60, sec%60))
		return d.clear("zz-lead")
	}
	steps := []struct {
		sec  int
		sent bool
	}{{0, true}, {10, false}, {310, true}, {620, true}, {930, false}, {940, false}}
	for _, s := range steps {
		if got := at(s.sec); got != s.sent {
			t.Errorf("at +%ds: sent=%v, want %v", s.sec, got, s.sent)
		}
	}
	d.onlyEsc("zz-lead", settingsEscMaxPerDay)
	if n := d.col("SELECT count(*) FROM messages WHERE subject LIKE 'Settings Warning stuck%'"); n != "1" {
		t.Errorf("stuck alerts = %s, want 1", n)
	}
}

// ---- pending nudges ----

const (
	nudgeFresh  = "2026-11-17T11:58:00+00:00"
	nudgeDetail = `{"nudge": "continue", "pane_id": "p-1", "started": "2026-11-17T08:00:00+00:00"}`
)

func (d *dlgFix) pending(name, ts, detail string) int64 {
	d.t.Helper()
	res, err := d.db.Exec("INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES(?,?,?,?,?,?)",
		ts, "tick", "agent", name, "nudge-pending", detail)
	if err != nil {
		d.t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		d.t.Fatal(err)
	}
	return id
}

func (d *dlgFix) nudge(status string) {
	d.t.Helper()
	prev := reapNow
	reapNow = func() time.Time { return d.clock }
	defer func() { reapNow = prev }()
	st := agentsState(d.t, map[string]any{"name": "coder-1", "pane_id": "p-1", "agent": "claude", "agent_status": status})
	st["hc_back"] = json.RawMessage("false")
	if err := d.run("pending_nudges", st); err != nil {
		d.t.Fatalf("pending_nudges: %v", err)
	}
}

func (d *dlgFix) liveCoder() {
	d.exec("INSERT OR REPLACE INTO agents(name,role,status,pane_id,started,spawn_args) VALUES('coder-1','coder','live','p-1','2026-11-17T08:00:00+00:00','{}')")
}

func TestFinishPendingNudges(t *testing.T) {
	t.Run("sent only when the host reports idle or done", func(t *testing.T) {
		for _, c := range []struct {
			status string
			sent   bool
		}{{"idle", true}, {"done", true}, {"working", false}, {"blocked", false}} {
			d := newDlgFix(t)
			d.liveCoder()
			d.pending("coder-1", nudgeFresh, nudgeDetail)
			d.nudge(c.status)
			want := 0
			if c.sent {
				want = 1
			}
			if got := d.stub.count("agent prompt"); got != want {
				t.Errorf("%s: %d prompt calls, want %d", c.status, got, want)
			}
			if got := d.col("SELECT count(*) FROM events WHERE action='nudge-done'"); got != fmt.Sprint(want) {
				t.Errorf("%s: nudge-done rows = %s", c.status, got)
			}
		}
	})
	t.Run("nudge-done row and the single host call match Python", func(t *testing.T) {
		d := newDlgFix(t)
		d.liveCoder()
		id := d.pending("coder-1", nudgeFresh, nudgeDetail)
		d.nudge("idle")
		got := d.col("SELECT actor||'|'||entity||'|'||entity_id||'|'||detail FROM events WHERE action='nudge-done'")
		if want := fmt.Sprintf(`tick|agent|coder-1|{"pending": %d, "outcome": "sent"}`, id); got != want {
			t.Errorf("row = %s, want %s", got, want)
		}
		if c := d.stub.calls(); len(c) != 1 || c[0] != "agent prompt coder-1 continue" {
			t.Errorf("host calls = %q, want one prompt", c)
		}
	})
	t.Run("dropped when the agent is gone, not live or the nudge is too old", func(t *testing.T) {
		cases := []struct {
			name  string
			setup func(d *dlgFix)
			ts    string
		}{
			{"gone", func(d *dlgFix) {}, nudgeFresh},
			{"not live", func(d *dlgFix) { d.liveCoder(); d.exec("UPDATE agents SET status='exited'") }, nudgeFresh},
			{"too old", func(d *dlgFix) { d.liveCoder() }, "2026-11-17T11:49:00+00:00"},
		}
		for _, c := range cases {
			d := newDlgFix(t)
			c.setup(d)
			id := d.pending("coder-1", c.ts, nudgeDetail)
			d.nudge("idle")
			if d.stub.count("agent prompt") != 0 {
				t.Errorf("%s: a nudge was sent", c.name)
			}
			got := d.col("SELECT detail FROM events WHERE action='nudge-done'")
			if want := fmt.Sprintf(`{"pending": %d, "outcome": "dropped"}`, id); got != want {
				t.Errorf("%s: detail = %s, want %s", c.name, got, want)
			}
		}
	})
	t.Run("a respawned pane is a mismatch event and a drop", func(t *testing.T) {
		d := newDlgFix(t)
		d.liveCoder()
		d.exec("UPDATE agents SET pane_id='p-2'")
		d.pending("coder-1", nudgeFresh, nudgeDetail)
		d.nudge("idle")
		if d.stub.count("agent prompt") != 0 {
			t.Error("a nudge was sent to a different pane")
		}
		if n := d.col("SELECT count(*) FROM events WHERE action='nudge-mismatch'"); n != "1" {
			t.Errorf("nudge-mismatch rows = %s", n)
		}
	})
	t.Run("an already finished nudge is not redone", func(t *testing.T) {
		d := newDlgFix(t)
		d.liveCoder()
		id := d.pending("coder-1", nudgeFresh, nudgeDetail)
		d.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)", nudgeFresh, "tick", "agent", "coder-1",
			"nudge-done", fmt.Sprintf(`{"pending": %d, "outcome": "sent"}`, id))
		d.nudge("idle")
		if len(d.stub.calls()) != 0 {
			t.Errorf("host calls = %q", d.stub.calls())
		}
	})
}

// TestAutoClearRecheckAndBoss: dialog_now re-checks the host status and the pane before a key, and a boss pane without
// unsent input is not skipped (the accepting twin of the unsent-input case).
func TestAutoClearRecheckAndBoss(t *testing.T) {
	now := func(d *dlgFix, name, role, want string) bool {
		t.Helper()
		r, err := newReapDB(d.db)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := newHerdrClient(r)
		ok, err := dialogNow(r, c, name, role, want)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	d := newDlgFix(t)
	d.pane("coder-1", "coder", dlgWarn, nil)
	if !now(d, "coder-1", "coder", "settings-warning") {
		t.Error("blocked host and matching pane: want true")
	}
	if now(d, "coder-1", "coder", "permission") {
		t.Error("the pane is a settings warning, not a permission dialog: want false")
	}
	d.stub.set("agent_list", "out", `{"agents":[{"name":"coder-1","agent":"claude","pane_id":"p-1","agent_status":"idle"}]}`)
	if now(d, "coder-1", "coder", "settings-warning") {
		t.Error("the host no longer reports blocked: want false")
	}
	d.pane("coder-1", "coder", dlgUnknown, nil)
	if now(d, "coder-1", "coder", "settings-warning") {
		t.Error("the pane changed to an unknown dialog: want false")
	}
	t.Run("boss pane without unsent input gets Esc", func(t *testing.T) {
		d := newDlgFix(t)
		d.pane("boss-1", "boss", dlgWarn, nil)
		if !d.clear("boss-1") {
			t.Fatal("no key sent")
		}
		d.onlyEsc("boss-1", 1)
	})
}
