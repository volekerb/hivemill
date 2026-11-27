package hm

import (
	"testing"
	"time"
)

func TestObsAlertReopensTheFeedRow(t *testing.T) {
	r, s := testRunner(t, "")
	_, _ = s.db.Exec(`CREATE TABLE owner_alert_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, org TEXT NOT NULL, kind TEXT NOT NULL,
		ref TEXT NOT NULL, since TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL, closed TEXT, sent TEXT)`)
	now := time.Now()
	for i, why := range []string{"down", "", "down again"} {
		sent, err := r.ObsAlert(now, why)
		if err != nil || sent != (i == 0) {
			t.Fatalf("check %d: sent %t, %v", i, sent, err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-obs-down' AND closed IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("open rows: %d, %v", n, err)
	}
}
