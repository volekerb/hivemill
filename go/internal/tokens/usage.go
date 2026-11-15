package tokens

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func defaultProjects() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// SessionTokens is session_tokens: input + output + cache creation tokens of one session (subagent logs included),
// 0 if none or if the id is not a plain id. Cache reads are not counted. root "" = ~/.claude/projects.
func SessionTokens(session, root string) int64 {
	if !plainID.MatchString(session) {
		return 0
	}
	n, _ := sessionSum(session, root)
	return n
}

// SessionTokensKnown is session_tokens_known: ok=false (Python None) when the id is bad, the session has no log, or no
// usage line in it; 0 with ok=true is a log that was read and shows no tokens.
func SessionTokensKnown(session, root string) (int64, bool) {
	if !plainID.MatchString(session) {
		return 0, false
	}
	return sessionSum(session, root)
}

func sessionSum(session, root string) (int64, bool) {
	ix := NewTokenIndex(root, nil, "", session)
	ix.Refresh()
	rows := ix.Snapshot().Rows
	var n int64
	for _, r := range rows {
		n += r.Input + r.Output + r.CacheWrite5m + r.CacheWrite1h
	}
	return n, len(rows) > 0
}

// LatestContext is latest_context: the context size (input + cache read + cache creation tokens) of the session's
// latest main-thread request, ok=false when the session has no log or no usage line. Only the tail (tailBytes, 0 =
// 1 MiB) of the log is read; subagent logs are not looked at.
func LatestContext(session, root string, tailBytes int64) (int64, bool) {
	if !plainID.MatchString(session) {
		return 0, false
	}
	if tailBytes <= 0 {
		tailBytes = 1 << 20
	}
	if root == "" {
		root = defaultProjects()
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return 0, false
	}
	var best struct {
		mtime float64
		n     int64
		set   bool
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, pn := range names {
		proj := filepath.Join(root, pn)
		f := filepath.Join(proj, session+".jsonl")
		if !isDir(proj) {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		st, err := fh.Stat()
		if err != nil {
			fh.Close()
			continue
		}
		off := max(0, st.Size()-tailBytes)
		fh.Seek(off, io.SeekStart)
		data, err := io.ReadAll(fh)
		fh.Close()
		if err != nil {
			continue
		}
		mtime := float64(st.ModTime().UnixNano()) / 1e9
		lines := bytes.Split(data, []byte("\n"))
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			if !bytes.Contains(line, []byte(`"usage"`)) {
				continue
			}
			d, ok := decodeLine(line)
			if !ok {
				continue // the first line of the tail may be cut
			}
			m, isMap := d.(map[string]any)
			if !isMap {
				continue
			}
			msg, _ := m["message"].(map[string]any)
			var u map[string]any
			if msg != nil {
				u, _ = msg["usage"].(map[string]any)
			}
			if t, _ := m["type"].(string); t != "assistant" || u == nil || truthy(m["isSidechain"]) {
				continue
			}
			n := pyInt(u["input_tokens"]) + pyInt(u["cache_read_input_tokens"]) + pyInt(u["cache_creation_input_tokens"])
			if !best.set || mtime >= best.mtime {
				best.mtime, best.n, best.set = mtime, n, true
			}
			break
		}
	}
	return best.n, best.set
}

// UsageTotalsResult is the dict usage_totals returns.
type UsageTotalsResult struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// UsageTotals is usage_totals: summed usage of every JSONL under root (subagent logs included); a missing dir is zeros.
func UsageTotals(root string) UsageTotalsResult {
	ix := NewTokenIndex(root, nil, "", "")
	ix.Refresh()
	var t UsageTotalsResult
	for _, r := range ix.Snapshot().Rows {
		t.Input += r.Input
		t.Output += r.Output
		t.CacheRead += r.CacheRead
		t.CacheWrite += r.CacheWrite5m + r.CacheWrite1h
	}
	return t
}
