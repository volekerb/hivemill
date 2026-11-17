package spend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// WorkerEnv caps every agent except the boss: a 200k window, compaction at 60% of it, and short tool output. The
// factory's role settings (roles/settings/*.json.in) carry the same values; docs/token-discipline.md explains them.
var WorkerEnv = map[string]string{
	"CLAUDE_CODE_DISABLE_1M_CONTEXT":          "1",
	"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE":         "60",
	"BASH_MAX_OUTPUT_LENGTH":                  "20000",
	"MAX_MCP_OUTPUT_TOKENS":                   "8000",
	"CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS": "15000",
	"CLAUDE_CODE_DISABLE_ADVISOR_TOOL":        "1",
}

// BossEnv overrides WorkerEnv for the one agent that holds the whole picture: the 1M window, compaction at 40%, and
// larger reads. It is a separate settings file passed with `claude --settings <file>`, which outranks the user file.
// It leaves the advisor setting alone.
var BossEnv = map[string]string{
	"CLAUDE_CODE_DISABLE_1M_CONTEXT":          "0",
	"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE":         "40",
	"BASH_MAX_OUTPUT_LENGTH":                  "40000",
	"MAX_MCP_OUTPUT_TOKENS":                   "8000",
	"CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS": "25000",
}

// Change is one env key a settings file lacks or holds with another value.
type Change struct {
	File string `json:"file"`
	Key  string `json:"key"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new"`
}

// ErrNotObject means a settings file is not a JSON object; nothing is written then.
var ErrNotObject = errors.New("not a JSON object")

// Plan lists what Apply would change in path to make its "env" hold want. A missing file plans every key.
func Plan(path string, want map[string]string) ([]Change, error) {
	doc, err := load(path)
	if err != nil {
		return nil, err
	}
	env, _ := doc["env"].(map[string]any)
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Change
	for _, k := range keys {
		old, has := env[k].(string)
		if has && old == want[k] {
			continue
		}
		if k == "CLAUDE_CODE_DISABLE_ADVISOR_TOOL" && old == "1" {
			continue // never turn an advisor back on that someone turned off
		}
		out = append(out, Change{File: path, Key: k, Old: old, New: want[k]})
	}
	return out, nil
}

// Apply writes the planned keys into path's "env", keeping every other setting. The first write keeps a copy of the
// old file as <path>.bak-hm-spend. The write is atomic (temp file + rename) and keeps the file mode.
func Apply(path string, want map[string]string) ([]Change, error) {
	changes, err := Plan(path, want)
	if err != nil || len(changes) == 0 {
		return changes, err
	}
	doc, err := load(path)
	if err != nil {
		return nil, err
	}
	env, _ := doc["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	for _, c := range changes {
		env[c.Key] = c.New
	}
	doc["env"] = env
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
		if bak := path + ".bak-hm-spend"; !exists(bak) {
			old, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(bak, old, mode); err != nil {
				return nil, err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hm-spend-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return changes, os.Rename(tmp.Name(), path)
}

func load(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("%s: %w", path, ErrNotObject)
	}
	if env, has := doc["env"]; has {
		if _, ok := env.(map[string]any); !ok {
			return nil, fmt.Errorf("%s: \"env\" is %w", path, ErrNotObject)
		}
	}
	return doc, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
