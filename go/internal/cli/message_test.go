package cli

import (
	"path/filepath"
	"testing"

	"factory/go/internal/factorydb"
)

// msgTestCtx opens the msg golden database (schema + seed) as who.
func msgTestCtx(t *testing.T, who string) *ctx {
	t.Helper()
	path, err := buildGoldenDB(t, t.TempDir(), "msg")
	if err != nil {
		t.Fatal(err)
	}
	db, err := factorydb.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &ctx{db: db.DB, who: who}
}

// TestResolveRecipient covers resolve_recipient (bin/factory_cli.py 3946): HQ singletons per org, lead:<team>, agent
// names, roles (newest live first), and the refusals.
func TestResolveRecipient(t *testing.T) {
	tests := []struct {
		name, to, org, want, die string
		prefixOne                bool
		dieCode                  int
	}{
		{name: "owner is the boss", to: "owner", org: "claude", want: "boss"},
		{name: "bare director own org", to: "director", org: "claude", want: "director"},
		{name: "bare director other org", to: "director", org: "cursor", want: "cursor-director"},
		{name: "boss other org", to: "boss", org: "cursor", want: "cursor-boss"},
		{name: "owner in other org", to: "owner", org: "cursor", want: "cursor-boss"},
		{name: "staff-engineer not live stays unprefixed", to: "staff-engineer", org: "claude", want: "staff-engineer"},
		{name: "staff-engineer other org", to: "staff-engineer", org: "cursor", want: "cursor-staff-engineer"},
		{name: "staff-engineer with prefix on", to: "staff-engineer", org: "claude", prefixOne: true, want: "claude-staff-engineer"},
		{name: "director with prefix on finds legacy live name", to: "director", org: "claude", prefixOne: true, want: "director"},
		{name: "unknown org falls back to default", to: "director", org: "nope", want: "director"},
		{name: "lead team", to: "lead:TM-1", org: "claude", want: "lead-a"},
		{name: "lead team without lead", to: "lead:TM-3", org: "claude", die: "team TM-3 has no lead", dieCode: 1},
		{name: "lead of unknown team", to: "lead:TM-404", org: "claude", die: "team TM-404 has no lead", dieCode: 1},
		{name: "agent name", to: "coder-a", org: "claude", want: "coder-a"},
		{name: "exited agent name still resolves", to: "qa-old", org: "claude", want: "qa-old"},
		{name: "role picks newest live", to: "qa", org: "claude", want: "qa-b"},
		{name: "unknown recipient", to: "nobody-here", org: "claude", die: "unknown recipient nobody-here", dieCode: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := msgTestCtx(t, "owner")
			if tc.prefixOne {
				if _, err := c.db.Exec("UPDATE config SET value='1' WHERE key='org_prefix_default'"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := msgResolveRecipient(c, tc.to, tc.org)
			if tc.die != "" {
				ee := exitOf(t, err)
				if ee.Msg != tc.die || ee.Code != tc.dieCode {
					t.Fatalf("got exit %d %q, want %d %q", ee.Code, ee.Msg, tc.dieCode, tc.die)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestMsgSpecWords(t *testing.T) {
	for s, want := range map[string]bool{
		"the spec": true, "Acceptance": true, "criteria here": true, "requirements": true, "scope": true,
		"inspect": false, "specific": false, "descope": false, "": false,
	} {
		if got := msgSpecWords.MatchString(s); got != want {
			t.Errorf("%q: got %v, want %v", s, got, want)
		}
	}
}
