package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"factory/go/internal/identity"
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

// TestInboxNoReadAt: no inbox golden case changes read_at. Run() is pointed at the very database the
// count is taken on (same Deps shape as runGo).
func TestInboxNoReadAt(t *testing.T) {
	for _, c := range goldenCases(t) {
		if c.group != "inbox" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			home, procDir := t.TempDir(), t.TempDir()
			path, err := buildGoldenDB(t, home, "inbox")
			if err != nil {
				t.Fatal(err)
			}
			count := func() int {
				db, err := openRaw(path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var n int
				if err := db.QueryRow("SELECT count(*) FROM messages WHERE read_at IS NOT NULL").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := count()
			env := goldenEnv(home, procDir, "", c.env)
			m := envMap(env)
			argv0, err := filepath.Abs(filepath.Join("..", "..", "..", "bin", "factory"))
			if err != nil {
				t.Fatal(err)
			}
			var out, errb bytes.Buffer
			Run(Deps{
				Args: c.args, Argv0: argv0, Environ: append(append([]string{}, env...), "FACTORY_CLI_IMPL=go"),
				Env: identity.Env{
					Getenv:    func(k string) string { return m[k] },
					LookupEnv: func(k string) (string, bool) { v, ok := m[k]; return v, ok },
					Uid:       os.Getuid(),
					PwDir:     func(int) (string, error) { return home, nil },
					Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
				},
				Stdout: &out, Stderr: &errb,
				Getpid: func() int { return 4242 }, Getppid: func() int { return 4241 },
				LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
				Exec:     func(string, []string, []string) error { return nil },
			})
			if after := count(); before != after {
				t.Fatalf("read_at count changed %d -> %d", before, after)
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
