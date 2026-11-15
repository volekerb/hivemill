package guard

import (
	"errors"
	"strings"
)

// HqDenyBlock is worker-guard.py:HqDenyBlock, a PrivBlock: HQ mode only (T-1500), a command the boss/director/lead
// settings deny by prefix, in any spelling the PRIV pass follows.
type HqDenyBlock struct{ Msg string }

func (b *HqDenyBlock) Error() string { return b.Msg }

type hqDenyRow struct {
	word string
	args []string
}

var hqHerdrRows = [][]string{{"server", "stop"}, {"workspace", "close"}, {"workspace", "create"}, {"tab", "close"},
	{"tab", "create"}, {"pane", "close"}, {"pane", "split"}, {"agent", "start"}}

// hqDeny is worker-guard.py:HQ_DENY: one row per roles/settings deny entry, herdr-core with the hc rows.
var hqDeny = func() []hqDenyRow {
	var out []hqDenyRow
	for _, a := range hqHerdrRows {
		out = append(out, hqDenyRow{"herdr", a})
	}
	hc := append(append([][]string{}, hqHerdrRows...), []string{"kill"}, []string{"rm"}, []string{"run"})
	for _, w := range []string{"hc", "herdr-core"} {
		for _, a := range hc {
			out = append(out, hqDenyRow{w, a})
		}
	}
	return append(out, hqDenyRow{"claude", nil}, hqDenyRow{"gh", []string{"pr", "merge"}}, hqDenyRow{"sqlite3", nil})
}()

// hqDenyHit is worker-guard.py:hq_deny_hit: what the HQ deny rule names, "" when the command is not denied. The basename
// is compared exactly, no glob expansion.
func hqDenyHit(word, name string, rest []string) string {
	switch name {
	case "herdr", "hc", "herdr-core":
		if IsStub(word) {
			return ""
		}
		var pos []string
		for _, x := range rest {
			if !strings.HasPrefix(x, "-") {
				pos = append(pos, x)
			}
		}
		for _, row := range hqDeny {
			if row.word != name || len(pos) < len(row.args) {
				continue
			}
			ok := true
			for i, x := range row.args {
				if pos[i] != x {
					ok = false
					break
				}
			}
			if ok {
				return strings.Join(append([]string{name}, row.args...), " ")
			}
		}
	case "claude":
		return "claude"
	case "npx", "bunx", "pnpx", "pnpm", "npm", "yarn", "node", "nodejs", "bun", "deno":
		var a Analyzer
		if err := a.claudeLaunchers(name, rest); err != nil {
			return "claude"
		}
	case "gh":
		pos, _, _, _, _ := ghParse(rest)
		if len(pos) >= 2 && pos[0] == "pr" && pos[1] == "merge" {
			return "gh pr merge"
		}
	case "sqlite3":
		return "sqlite3"
	}
	return ""
}

// IsHqDenyBlock reports whether err is an HqDenyBlock.
func IsHqDenyBlock(err error) bool {
	var h *HqDenyBlock
	return errors.As(err, &h)
}
