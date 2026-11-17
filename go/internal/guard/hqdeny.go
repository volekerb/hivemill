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
func hqDenyHit(word, name string, rest []string, role string) string {
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
			if role == "boss" && bossMergeOK(rest) {
				return ""
			}
			return "gh pr merge"
		}
	case "sqlite3":
		return "sqlite3"
	}
	return ""
}

// bossMergeRepos is worker-guard.py:BOSS_MERGE_REPOS.
var bossMergeRepos = map[string]struct{}{"volekerb/hivemill": {}, "volekerb/hivemill-www": {}}

// bossMergeOK is worker-guard.py:boss_merge_ok (T-1803, owner decision M40734): `gh pr merge <N> --squash --delete-branch`,
// each flag once, optionally --repo|-R|--repo= naming hivemill or hivemill-www; anything else stays denied.
func bossMergeOK(rest []string) bool {
	var args, repos []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--repo" || a == "-R":
			if i+1 >= len(rest) {
				return false
			}
			repos = append(repos, rest[i+1])
			i++
		case strings.HasPrefix(a, "--repo="):
			repos = append(repos, strings.TrimPrefix(a, "--repo="))
		default:
			args = append(args, a)
		}
	}
	if len(repos) > 1 {
		return false
	}
	for _, r := range repos {
		if !in(bossMergeRepos, r) {
			return false
		}
	}
	var flags, pos []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	if len(flags) != 2 || len(pos) != 3 || pos[0] != "pr" || pos[1] != "merge" {
		return false
	}
	if !((flags[0] == "--squash" && flags[1] == "--delete-branch") || (flags[0] == "--delete-branch" && flags[1] == "--squash")) {
		return false
	}
	for _, c := range pos[2] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return pos[2] != ""
}

// IsHqDenyBlock reports whether err is an HqDenyBlock.
func IsHqDenyBlock(err error) bool {
	var h *HqDenyBlock
	return errors.As(err, &h)
}
