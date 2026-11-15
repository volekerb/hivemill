package merge

import (
	"fmt"
	"sort"
	"strings"
)

// Rollup is a PR's statusCheckRollup: the decoded JSON array of check objects.
type Rollup []map[string]any

// PendingStates are the check states that mean "not finished yet" (PENDING_CHECK_STATES).
var PendingStates = []string{"PENDING", "IN_PROGRESS", "QUEUED", "WAITING", "REQUESTED", "EXPECTED", "NONE"}

// str is c.get(key) when it is a string (ok), "" for absent or null; ok=false for any other JSON type, which Python
// would handle in ways this port does not copy.
func str(c map[string]any, key string) (string, bool) {
	switch v := c[key].(type) {
	case nil:
		return "", true
	case string:
		return v, true
	}
	return "", false
}

// orStr is Python's `a or b` over c.get(ka), c.get(kb) when both are strings or null: the first non-empty one.
func orStr(c map[string]any, ka, kb string) (string, bool) {
	a, ok := str(c, ka)
	if !ok {
		return "", false
	}
	if a != "" {
		return a, true
	}
	return str(c, kb)
}

// LatestChecks is latest_checks: the newest rollup entry per check identity ((workflowName, name) for check runs,
// context for status contexts), in list order. Entries of an unexpected shape return ErrUnsupported.
func LatestChecks(rollup Rollup) (Rollup, error) {
	type slot struct {
		ts string
		i  int
		c  map[string]any
	}
	latest := map[string]slot{}
	for i, c := range rollup {
		tn, ok := str(c, "__typename")
		if !ok {
			return nil, ErrUnsupported
		}
		ctxv, ok := str(c, "context")
		if !ok {
			return nil, ErrUnsupported
		}
		name, ok := str(c, "name")
		if !ok {
			return nil, ErrUnsupported
		}
		var ident string
		if tn == "StatusContext" || (ctxv != "" && name == "") {
			if c["context"] == nil {
				ident = "ctx|null"
			} else {
				ident = "ctx|s" + ctxv
			}
		} else {
			wf, ok := str(c, "workflowName")
			if !ok {
				return nil, ErrUnsupported
			}
			if c["name"] == nil {
				ident = "run|" + wf + "|null"
			} else {
				ident = "run|" + wf + "|s" + name
			}
		}
		ts := ""
		for _, k := range []string{"startedAt", "completedAt", "createdAt"} {
			v, ok := str(c, k)
			if !ok {
				return nil, ErrUnsupported
			}
			if v != "" {
				ts = v
				break
			}
		}
		if old, seen := latest[ident]; !seen || ts > old.ts || (ts == old.ts && i >= old.i) {
			latest[ident] = slot{ts, i, c}
		}
	}
	slots := make([]slot, 0, len(latest))
	for _, s := range latest {
		slots = append(slots, s)
	}
	sort.Slice(slots, func(a, b int) bool { return slots[a].i < slots[b].i })
	out := make(Rollup, len(slots))
	for i, s := range slots {
		out[i] = s.c
	}
	return out, nil
}

// ChecksOK is checks_ok: "name=STATE" for every latest check that is not SUCCESS, SKIPPED or NEUTRAL.
func ChecksOK(rollup Rollup) ([]string, error) {
	latest, err := LatestChecks(rollup)
	if err != nil {
		return nil, err
	}
	bad := []string{}
	for _, c := range latest {
		concl, ok := orStr(c, "conclusion", "state")
		if !ok {
			return nil, ErrUnsupported
		}
		concl = pyUpper(concl)
		switch concl {
		case "SUCCESS", "SKIPPED", "NEUTRAL":
			continue
		}
		name, ok := orStr(c, "name", "context")
		if !ok {
			return nil, ErrUnsupported
		}
		shown := name
		if name == "" && c["name"] == nil && c["context"] == nil {
			shown = "None"
		}
		tail := concl
		if tail == "" {
			s, ok := pyScalar(c["status"])
			if !ok {
				return nil, ErrUnsupported
			}
			tail = s
		}
		bad = append(bad, shown+"="+tail)
	}
	return bad, nil
}

// Pending is _judge_pending: some check is not green and every one of them is only unfinished.
func Pending(bad []string) bool {
	if len(bad) == 0 {
		return false
	}
	for _, b := range bad {
		state := b
		if i := strings.LastIndex(b, "="); i >= 0 {
			state = b[i+1:]
		}
		state = pyUpper(state)
		found := false
		for _, p := range PendingStates {
			if p == state {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// MissingChecks is missing_checks: "required check N missing" for each name of the comma list `required` that no
// rollup entry (any, not only the latest) carries as name or context.
func MissingChecks(required string, rollup Rollup) ([]string, error) {
	seen := map[string]bool{}
	for _, c := range rollup {
		n, ok := orStr(c, "name", "context")
		if !ok {
			return nil, ErrUnsupported
		}
		seen[n] = true
	}
	out := []string{}
	for _, n := range strings.Split(required, ",") {
		n = pyStrip(n)
		if n != "" && !seen[n] {
			out = append(out, fmt.Sprintf("required check %s missing", n))
		}
	}
	return out, nil
}
