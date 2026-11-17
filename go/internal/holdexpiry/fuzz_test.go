package holdexpiry_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"factory/go/internal/factorytest"
	"factory/go/internal/holdexpiry"
)

const maxExpiryMin = 60 * 24 * 365

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// FuzzParseExpiry: any config value gives 0..one year of minutes; a value other than the default is exactly the
// whole number the text spells, so 0 (off) only comes from zeros.
func FuzzParseExpiry(f *testing.F) {
	for _, s := range []string{"90", " 90 ", "abc", "", "-5", "1.5", "99999999999999999999", "0", "000", "525600",
		"525601", "+7", "٩٠", "9\x000", "1e3", "0x10", "12\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := holdexpiry.ParseExpiry(s)
		if got < 0 || got > maxExpiryMin {
			t.Fatalf("ParseExpiry(%q) = %d: out of range", s, got)
		}
		trimmed := strings.TrimSpace(s)
		want := holdexpiry.DefaultExpiryMin
		if isDigits(trimmed) {
			if n, err := strconv.ParseUint(trimmed, 10, 64); err == nil && n <= maxExpiryMin {
				want = int(n)
			}
		}
		if got != want {
			t.Fatalf("ParseExpiry(%q) = %d, want %d", s, got, want)
		}
	})
}

// markerIDs is an independent reading of the "boss-approved M<digits>" markers of a hold text, in order.
func markerIDs(text string) []string {
	const lit = "boss-approved M"
	var out []string
	for {
		i := strings.Index(text, lit)
		if i < 0 {
			return out
		}
		text = text[i+len(lit):]
		n := 0
		for n < len(text) && text[n] >= '0' && text[n] <= '9' {
			n++
		}
		if n > 0 {
			out = append(out, text[:n])
		}
		text = text[n:]
	}
}

// FuzzApproval: over arbitrary hold text the marker scan never fails, and the hold is approved only when the text
// names the one seeded boss decision about the task (M41). M42 (a director's decision), M43 (a boss report) and M44
// (a boss decision about another task) never approve; without approval the first marker is reported.
func FuzzApproval(f *testing.F) {
	e := factorytest.New(f)
	db := e.RawDB()
	f.Cleanup(func() { db.Close() })
	e.SeedAgent(factorytest.AgentRow{Name: "claude-boss", Role: "boss"})
	e.SeedAgent(factorytest.AgentRow{Name: "claude-director", Role: "director"})
	for _, m := range []struct {
		id                 int
		sender, kind, task string
	}{{41, "claude-boss", "decision", taskID}, {42, "claude-director", "decision", taskID},
		{43, "claude-boss", "report", taskID}, {44, "claude-boss", "decision", "T-0002"}} {
		if _, err := db.Exec(`INSERT INTO messages(id,sender,recipient,kind,subject,body,task_id,created)
			VALUES(?,?,'claude-director',?,'hold','release freeze',?,?)`, m.id, m.sender, m.kind, m.task, nowISO); err != nil {
			f.Fatal(err)
		}
	}
	for _, s := range []string{"", "hold until PR 703 merges", "boss-approved M41: release freeze", "boss-approved M41",
		"boss-approved M42", "boss-approved M43", "boss-approved M44", "boss-approved M99", "boss-approved M42 boss-approved M41",
		"Boss-Approved M41", "boss-approved m41", "boss-approved M", "boss-approved M 41", "boss-approved M-41",
		"boss-approved M041", "boss-approved M4１", "boss-approved M99999999999999999999999", "xboss-approved M41x",
		"boss-approved M41\x00\xff", "boss-approved Mboss-approved M41"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, reason string) {
		valid, invalid, err := holdexpiry.Approval(context.Background(), db, taskID, reason)
		if err != nil {
			t.Fatalf("Approval(%q): %v", reason, err)
		}
		ids := markerIDs(reason)
		wantValid := false
		for _, id := range ids {
			if n, err := strconv.ParseInt(id, 10, 64); err == nil && n == 41 {
				wantValid = true
			}
		}
		wantInvalid := ""
		if !wantValid && len(ids) > 0 {
			wantInvalid = "M" + ids[0]
		}
		if valid != wantValid || invalid != wantInvalid {
			t.Fatalf("Approval(%q) = %v, %q; want %v, %q", reason, valid, invalid, wantValid, wantInvalid)
		}
	})
}

// FuzzPRNumber: a PR number is only read off a URL that ends in /pull/<digits> (one trailing slash allowed).
func FuzzPRNumber(f *testing.F) {
	for _, s := range []string{"", factorytest.PRURL, "https://github.com/o/r/pull/725/", " https://github.com/o/r/pull/7 \n",
		"https://github.com/o/r/pull/725/files", "https://github.com/o/r/pull/", "/pull/0", "/pull/007",
		"/pull/99999999999999999999999", "/pull/-1", "/pull/1//", "/pull/١"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := holdexpiry.PRNumber(s)
		u := strings.TrimSuffix(strings.TrimSpace(s), "/")
		i := strings.LastIndex(u, "/pull/")
		want := 0
		if i >= 0 && isDigits(u[i+len("/pull/"):]) {
			if n, err := strconv.Atoi(u[i+len("/pull/"):]); err == nil {
				want = n
			}
		}
		if got != want {
			t.Fatalf("PRNumber(%q) = %d, want %d", s, got, want)
		}
	})
}
