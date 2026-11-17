package scout

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	sc "factory/go/internal/scout"
)

// Port of tests/test_scout_pending_order.py (5 cases; see ported_scout.txt).

const first0 = "2026-10-01T00:00:00+00:00"

type cand struct {
	kind, risk string // "" = NULL
	sources    []string
	spd        *float64
	first      string
}

func nul(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func addCand(t *testing.T, d *sql.DB, n int, c cand) {
	t.Helper()
	if c.first == "" {
		c.first = first0
	}
	src, _ := json.Marshal(c.sources)
	var spd any
	if c.spd != nil {
		spd = *c.spd
	}
	exec(t, d, "INSERT INTO scout_candidates(id,key,kind,install_risk,sources,stars_per_day,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?)",
		fmt.Sprintf("C-%04d", n), fmt.Sprintf("gh:o/r%d", n), nul(c.kind), nul(c.risk), string(src), spd, c.first, c.first)
}

func pendingIDs(t *testing.T, d *sql.DB, limit int) []string {
	t.Helper()
	rows, err := sc.Pending(d, limit)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func fp(x float64) *float64 { return &x }

var skills, mkt = []string{"skills"}, []string{"marketplace"}

func TestPendingOrderSkillsBeforeMarketplacePlugins(t *testing.T) {
	d := mem(t)
	addCand(t, d, 1, cand{kind: "plugin", risk: "prompt-only", sources: mkt, first: "2026-09-01T00:00:00+00:00"})
	addCand(t, d, 2, cand{kind: "mcp", risk: "prompt-only", sources: mkt, first: "2026-09-02T00:00:00+00:00"})
	addCand(t, d, 3, cand{risk: "prompt-only", sources: mkt, first: "2026-09-03T00:00:00+00:00"})
	addCand(t, d, 4, cand{kind: "agent", risk: "prompt-only", sources: skills, first: "2026-10-05T00:00:00+00:00"})
	addCand(t, d, 5, cand{kind: "collection", risk: "prompt-only", sources: skills, first: "2026-10-06T00:00:00+00:00"})
	addCand(t, d, 6, cand{kind: "skill", risk: "prompt-only", sources: skills, first: "2026-10-07T00:00:00+00:00"})
	eqStrings(t, "order", pendingIDs(t, d, 50), []string{"C-0004", "C-0005", "C-0006", "C-0001", "C-0002", "C-0003"})
}

func TestPendingOrderPromptOnlyBeforeFlagged(t *testing.T) {
	d := mem(t)
	addCand(t, d, 1, cand{kind: "skill", risk: "hooks,scripts", sources: skills, first: "2026-09-01T00:00:00+00:00"})
	addCand(t, d, 2, cand{kind: "skill", sources: skills, first: "2026-09-02T00:00:00+00:00"})
	addCand(t, d, 3, cand{kind: "skill", risk: "prompt-only", sources: skills, first: "2026-10-01T00:00:00+00:00"})
	eqStrings(t, "order", pendingIDs(t, d, 50), []string{"C-0003", "C-0001", "C-0002"})
}

func TestPendingOrderNonMarketplaceSourceBeforeMarketplaceOnly(t *testing.T) {
	d := mem(t)
	addCand(t, d, 1, cand{kind: "skill", risk: "prompt-only", sources: mkt, first: "2026-09-01T00:00:00+00:00"})
	addCand(t, d, 2, cand{kind: "skill", risk: "prompt-only", sources: []string{"marketplace", "awesome"}, first: "2026-10-01T00:00:00+00:00"})
	addCand(t, d, 3, cand{kind: "skill", risk: "prompt-only", sources: []string{}, first: "2026-09-02T00:00:00+00:00"})
	got := pendingIDs(t, d, 50)
	if got[0] != "C-0002" { // empty sources counts as marketplace-only: first_seen decides
		t.Fatalf("first = %s, want C-0002", got[0])
	}
	eqStrings(t, "order", got, []string{"C-0002", "C-0001", "C-0003"})
}

func TestPendingOrderStarsPerDayThenFirstSeenThenID(t *testing.T) {
	d := mem(t)
	base := func(spd *float64, first string) cand {
		return cand{kind: "skill", risk: "prompt-only", sources: skills, spd: spd, first: first}
	}
	addCand(t, d, 1, base(nil, "2026-09-01T00:00:00+00:00"))
	addCand(t, d, 2, base(fp(1.5), "2026-10-01T00:00:00+00:00"))
	addCand(t, d, 3, base(fp(9.0), "2026-10-02T00:00:00+00:00"))
	addCand(t, d, 5, base(fp(1.5), "2026-09-15T00:00:00+00:00"))
	addCand(t, d, 4, base(fp(1.5), "2026-09-15T00:00:00+00:00"))
	want := []string{"C-0003", "C-0004", "C-0005", "C-0002", "C-0001"}
	eqStrings(t, "order", pendingIDs(t, d, 50), want)
	eqStrings(t, "order again", pendingIDs(t, d, 50), want)
}

func TestPendingOrderLimitAppliesAndNothingIsDropped(t *testing.T) {
	d := mem(t)
	for n := 1; n <= 7; n++ {
		c := cand{kind: "skill", risk: "prompt-only", sources: mkt}
		if n%2 == 1 {
			c.kind = "plugin"
		}
		if n%3 == 0 {
			c.sources = []string{"hn"}
		}
		addCand(t, d, n, c)
	}
	full := pendingIDs(t, d, 50)
	if len(full) != 7 {
		t.Fatalf("full = %v, want 7 ids", full)
	}
	eqStrings(t, "limit 3", pendingIDs(t, d, 3), full[:3])
	var seen []string
	for { // triage the head of the queue, then page again: every candidate comes out once
		batch, err := sc.Pending(d, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			exec(t, d, "INSERT INTO scout_triage(id,candidate_id,purpose,target_roles,overlap,recommendation,rationale,eval_suggested,author,created) VALUES(?,?,'p','coder','o','watch','w',0,'a','t')",
				len(seen)+1, r.ID)
			seen = append(seen, r.ID)
		}
	}
	eqStrings(t, "paged", seen, full)
}
