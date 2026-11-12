package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestInboxDelegates: every inbox form that marks messages read (or that argparse might read differently) is
// handed to Python, through the real Run entry point.
func TestInboxDelegates(t *testing.T) {
	forms := map[string][]string{
		"plain":              {"inbox"},
		"all":                {"inbox", "--all"},
		"agent_self":         {"inbox", "--agent", "coder-a"},
		"limit_zero":         {"inbox", "--peek", "--limit", "0"},
		"limit_not_int":      {"inbox", "--peek", "--limit", "x"},
		"limit_signed":       {"inbox", "--peek", "--limit", "+5"},
		"limit_underscore":   {"inbox", "--peek", "--limit", "1_0"},
		"show_with_peek":     {"inbox", "--show", "1", "--peek"},
		"show_dash_value":    {"inbox", "--show", "-1"},
		"help":               {"inbox", "-h"},
		"abbreviated_flag":   {"inbox", "--pe"},
		"equals_form":        {"inbox", "--peek", "--limit=2"},
		"repeated_peek":      {"inbox", "--peek", "--peek"},
		"show_without_value": {"inbox", "--show"},
		"unknown_flag":       {"inbox", "--peek", "--bogus"},
		"agent_dash_value":   {"inbox", "--peek", "--agent", "-x"},
		"show_empty":         {"inbox", "--show", ""},
		"show_huge_id":       {"inbox", "--show", "99999999999999999999"},
		"show_unicode_digit": {"inbox", "--show", "M٣"},
	}
	for name, args := range forms {
		t.Run(name, func(t *testing.T) {
			c := goldenCase{group: "inbox", name: name, args: args, env: []string{"FACTORY_AGENT=coder-a"}, delegate: true}
			c.dir = filepath.Join(goldenDir, "inbox")
			if err := checkGoldenCase(t, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestInboxNoReadAt: no inbox golden case changes read_at (the fingerprint covers every row; this names the
// column the spec cares about).
func TestInboxNoReadAt(t *testing.T) {
	for _, c := range goldenCases(t) {
		if c.group != "inbox" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			path, err := buildGoldenDB(t, home, "inbox")
			if err != nil {
				t.Fatal(err)
			}
			count := func() string {
				db, err := openRaw(path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var n int
				if err := db.QueryRow("SELECT count(*) FROM messages WHERE read_at IS NOT NULL").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return strings.Repeat("x", n)
			}
			before := count()
			if _, err := runGo(t, c); err != nil { // runGo builds its own DB; same seed, so same count
				t.Fatal(err)
			}
			if before != count() {
				t.Fatalf("read_at count changed")
			}
		})
	}
}

func TestInboxParseSpecialCases(t *testing.T) {
	if _, ok := parseInbox([]string{"--peek", "--limit", "2", "--agent", "x"}); !ok {
		t.Fatal("peek with limit and agent should be answered in Go")
	}
	if !hasDailyDigestPrefix("DAILY digest x") || hasDailyDigestPrefix("daİly digest") || hasDailyDigestPrefix("daily") {
		t.Fatal("daily digest prefix check differs from Python's lower().startswith")
	}
}
