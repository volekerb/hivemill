package scout

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

type fakeAPI struct {
	n      int64
	sent   []map[string]string
	events [][]string
}

func (f *fakeAPI) Clock() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }
func (f *fakeAPI) Send(_ *sql.DB, to, subject, body, kind, sender string) (int64, error) {
	f.n++
	f.sent = append(f.sent, map[string]string{"to": to, "subject": subject, "body": body, "kind": kind, "sender": sender})
	return 100 + f.n, nil
}
func (f *fakeAPI) Event(_ *sql.DB, entity, id, action, detail string) error {
	f.events = append(f.events, []string{entity, id, action, detail})
	return nil
}

func reportDB(t *testing.T) *sql.DB {
	db := newDB(t)
	sqlText, err := os.ReadFile("testdata/report_fixture.sql")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "PRAGMA foreign_keys=OFF") // the fixture has an orphan trial on purpose (single connection: the pragma sticks)
	mustExec(t, db, string(sqlText))
	return db
}

type reportGolden struct {
	Windows []struct {
		Since, Until, Want string
	} `json:"windows"`
	Send struct {
		IDs   []int64             `json:"ids"`
		Again []int64             `json:"again"`
		Sent  []map[string]string `json:"sent"`
		Evts  [][]string          `json:"events"`
		Runs  []struct {
			Slot        string
			ReportedMsg *int64 `json:"reported_msg"`
		} `json:"runs"`
	} `json:"send"`
}

func loadReportGolden(t *testing.T) reportGolden {
	raw, err := os.ReadFile("testdata/report_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g reportGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestReportGolden: the section text equals scout/report.py on the same DB rows, for every window.
func TestReportGolden(t *testing.T) {
	db := reportDB(t)
	g := loadReportGolden(t)
	if len(g.Windows) < 5 {
		t.Fatal("goldens missing")
	}
	for _, w := range g.Windows {
		since, ok1 := TS(w.Since)
		until, ok2 := TS(w.Until)
		if !ok1 || !ok2 {
			t.Fatalf("bad window %v", w)
		}
		got, err := BuildSkillScoutSection(db, since, until)
		if err != nil {
			t.Fatal(err)
		}
		if got != w.Want {
			t.Errorf("window %s..%s:\n--- got\n%s\n--- want\n%s", w.Since, w.Until, got, w.Want)
		}
	}
}

// TestReportGoldenSend: SendPending sends the same messages, once, and records reported_msg.
func TestReportGoldenSend(t *testing.T) {
	db := reportDB(t)
	g := loadReportGolden(t)
	api := &fakeAPI{}
	ids, err := SendPending(db, api)
	if err != nil {
		t.Fatal(err)
	}
	again, err := SendPending(db, api)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, g.Send.IDs) || len(again) != 0 {
		t.Errorf("ids %v again %v, want %v and none", ids, again, g.Send.IDs)
	}
	if !reflect.DeepEqual(api.sent, g.Send.Sent) {
		t.Errorf("sent mismatch:\n%v\nwant\n%v", api.sent, g.Send.Sent)
	}
	if !reflect.DeepEqual(api.events, g.Send.Evts) {
		t.Errorf("events %v want %v", api.events, g.Send.Evts)
	}
	for _, r := range g.Send.Runs {
		var got sql.NullInt64
		if err := db.QueryRow("SELECT reported_msg FROM scout_runs WHERE slot=?", r.Slot).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (r.ReportedMsg == nil) == got.Valid || (got.Valid && got.Int64 != *r.ReportedMsg) {
			t.Errorf("run %s reported_msg %v want %v", r.Slot, got, r.ReportedMsg)
		}
	}
}

func TestSubject(t *testing.T) {
	if Subject("2026-10-25T09") != "skill scout 2026-10-25 09" {
		t.Fatal(Subject("2026-10-25T09"))
	}
}
