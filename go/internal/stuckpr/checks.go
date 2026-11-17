package stuckpr

import (
	"sort"
	"strings"
)

// checkFinished is true when a rollup check is done. A non-empty conclusion and completedAt count as
// finished even when status is still IN_PROGRESS (G-050 stuck-PR rule).
func checkFinished(c map[string]any) bool {
	_, conclTruthy, conclOK := strField(c, "conclusion")
	_, compTruthy, compOK := strField(c, "completedAt")
	if conclOK && conclTruthy && compOK && compTruthy {
		return true
	}
	if tn, tnTruthy, ok := strField(c, "__typename"); ok && tnTruthy && tn == "StatusContext" {
		state, stateTruthy, stOK := strField(c, "state")
		if stOK && stateTruthy {
			return state == "SUCCESS" || state == "FAILURE" || state == "ERROR"
		}
	}
	status, statusTruthy, stOK := strField(c, "status")
	return stOK && statusTruthy && strings.EqualFold(status, "COMPLETED")
}

type checkEntry struct {
	raw map[string]any
	ts  string
	idx int
}

func latestChecks(rollup []any) ([]map[string]any, bool) {
	latest := map[string]checkEntry{}
	for i, e := range rollup {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		tn, _, ok1 := strField(m, "__typename")
		ctxv, ctxT, ok2 := strField(m, "context")
		name, nameT, ok3 := strField(m, "name")
		wf, _, ok4 := strField(m, "workflowName")
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return nil, false
		}
		var ident string
		if tn == "StatusContext" || (ctxT && !nameT) {
			ident = "ctx\x00" + ctxv
			if _, present := m["context"]; !present || m["context"] == nil {
				ident = "ctx\x00<none>"
			}
		} else {
			ident = "run\x00" + wf + "\x00"
			if _, present := m["name"]; !present || m["name"] == nil {
				ident += "<none>"
			} else {
				ident += "s:" + name
			}
		}
		ts := ""
		for _, k := range []string{"startedAt", "completedAt", "createdAt"} {
			s, truthy, ok := strField(m, k)
			if !ok {
				return nil, false
			}
			if truthy {
				ts = s
				break
			}
		}
		if old, ok := latest[ident]; !ok || ts >= old.ts {
			latest[ident] = checkEntry{raw: m, ts: ts, idx: i}
		}
	}
	list := make([]checkEntry, 0, len(latest))
	for _, e := range latest {
		list = append(list, e)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].idx < list[b].idx })
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = e.raw
	}
	return out, true
}

// ciVerdict classifies CI on a PR head: pending while any check is not finished; otherwise green or red.
func ciVerdict(rollup []any) (verdict string, failed []string, stillRunning bool, ok bool) {
	checks, ok := latestChecks(rollup)
	if !ok {
		return "", nil, false, false
	}
	if len(checks) == 0 {
		return "pending", nil, false, true
	}
	failed = nil
	allDone := true
	for _, c := range checks {
		if !checkFinished(c) {
			allDone = false
			continue
		}
		name := checkName(c)
		if checkFailed(c) {
			failed = append(failed, name)
		}
	}
	if !allDone {
		return "pending", nil, true, true
	}
	if len(failed) > 0 {
		return "red", failed, false, true
	}
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = checkName(c)
	}
	return "green", names, false, true
}

func checkName(c map[string]any) string {
	if s, truthy, ok := strField(c, "name"); ok && truthy {
		return s
	}
	if s, truthy, ok := strField(c, "context"); ok && truthy {
		return s
	}
	return "?"
}

func checkFailed(c map[string]any) bool {
	if tn, truthy, ok := strField(c, "__typename"); ok && truthy && tn == "StatusContext" {
		state, _, _ := strField(c, "state")
		switch strings.ToUpper(state) {
		case "FAILURE", "ERROR":
			return true
		case "SUCCESS":
			return false
		default:
			return true
		}
	}
	concl, _, _ := strField(c, "conclusion")
	switch strings.ToUpper(concl) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return false
	case "":
		return true
	default:
		return true
	}
}

func strField(m map[string]any, k string) (string, bool, bool) {
	v, present := m[k]
	if !present || v == nil {
		return "", false, true
	}
	s, isStr := v.(string)
	if !isStr {
		return "", false, false
	}
	return s, s != "", true
}
