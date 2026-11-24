package tick

import (
	"encoding/json"
	"strings"
	"testing"
)

// Parser -> fuzz target table (go checklist a). Every parser or decoder this task adds has one target:
//
//	classifyDialog / dialogBlock / optParse / promptMatch   FuzzClassifyDialog
//	hasUnsentInput                                         FuzzHasUnsentInput
//	parseHCRemote                                          FuzzParseHCRemote
//	hcTerminalRow / hcWord (decoded hc terminal JSON)      FuzzHCTerminalRow
//	startedAge / stillStarting (agents.started text)       FuzzStillStarting
//
// Seeds are the parity fixtures of dialog_act_test.go (dlgWarn, dlgPerm, dlgUnknown) plus the Unicode cases below.

const (
	uniNBSP   = "Bash command\n  cat /etc/hosts\n\nDo you want to proceed?\n❯ 1. Yes\n  2. No\n"
	uniVT     = "Bash command\n  cat /etc/hosts\n\nDo you want to proceed?\n❯ 1.\x0bYes\n  2. No\n"
	uniArabic = "Bash command\n  cat /etc/hosts\n\nDo you want to proceed?\n❯ ١. Yes\n  ٢. No\n"
	uniMixed  = "Bash command\n  cat /etc/hosts\n\nDo you want to proceed?\n❯ 1. Yes\n  ٢. No\n"
)

func FuzzClassifyDialog(f *testing.F) {
	for _, s := range []string{dlgWarn, dlgPerm, dlgUnknown, uniNBSP, uniVT, uniArabic, uniMixed, "", "\x00", "❯"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := classifyDialog(s)
		if err != nil {
			return // a decode refusal is a defined outcome; panics are the failure
		}
		switch c.kind {
		case "unknown", "permission", "settings-warning":
		default:
			t.Fatalf("kind %q", c.kind)
		}
	})
}

func FuzzHasUnsentInput(f *testing.F) {
	for _, s := range []string{dlgWarn, dlgPerm, dlgUnknown, uniNBSP, uniVT, uniArabic, "> typed text\n", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = hasUnsentInput(s)
	})
}

func FuzzParseHCRemote(f *testing.F) {
	for _, s := range []string{
		`{"terminals":[{"name":"a","terminal_id":"t1","status":"idle"}]}`,
		`[{"name":"a"}]`, `{"terminals":null}`, `not json`, ``, `{"terminals":[1,"x",null]}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parseHCRemote(s)
	})
}

func FuzzHCTerminalRow(f *testing.F) {
	for _, s := range []string{
		`{"name":"a","terminal_id":"t1","status":"idle","agent":"claude-code"}`,
		`{"status":5}`, `{}`, `{"name":null}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) != nil {
			return
		}
		_ = hcTerminalRow(m, "k")
		_ = hcWord(m)
	})
}

func FuzzStillStarting(f *testing.F) {
	for _, s := range []string{"2026-11-17T08:00:00+00:00", "2026-11-17T08:00:00", "", "garbage", "2026-11-17T08:00:00Z", "٢026-11-17"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = stillStarting(s)
		_, _ = startedAge(s)
	})
}

// TestDialogUnicodeParity: the verdicts below are what Python's classify_dialog / has_unsent_input return for the
// same text (probed against bin/factory_cli.py): NBSP and \v count as whitespace, an Arabic-Indic digit is not
// accepted as an option number in a line of its own (unknown, unsent input).
func TestDialogUnicodeParity(t *testing.T) {
	cases := []struct {
		name, text, kind string
		unsent           bool
	}{
		{"nbsp after the cursor", uniNBSP, "permission", false},
		{"vertical tab in the option", uniVT, "permission", false},
		{"arabic-indic digits only", uniArabic, "unknown", true},
		{"ascii 1 with arabic 2", uniMixed, "permission", false},
		{"nbsp in a settings warning", strings.Replace(dlgWarn, "❯ 1.", "❯\u00a01.", 1), "settings-warning", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := classifyDialog(c.text)
			if err != nil {
				t.Fatal(err)
			}
			if got.kind != c.kind {
				t.Errorf("kind = %s, python has %s", got.kind, c.kind)
			}
			un, err := hasUnsentInput(c.text)
			if err != nil {
				t.Fatal(err)
			}
			if un != c.unsent {
				t.Errorf("unsent = %v, python has %v", un, c.unsent)
			}
		})
	}
}
