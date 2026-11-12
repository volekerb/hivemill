package tick

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// HandoffName is the handoff file under FACTORY_HOME/run (contract H2).
const HandoffName = "tick-handoff.json"

// State is the cross-unit state: agents (list or null), t (float), hc_back (bool), starting (list of names). A key is
// absent until a unit computed it; values stay raw JSON so Go never reinterprets what Python wrote.
type State map[string]json.RawMessage

// AgentsNull reports whether herdr_list ran and the pane server was unreachable ("agents": null).
func (s State) AgentsNull() bool {
	v, ok := s["agents"]
	return ok && bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

// Handoff is the v1 document. Unknown top-level keys are kept in Extra and written back.
type Handoff struct {
	Pid      int
	Elapsed  float64
	Timers   bool
	State    State
	Laps     []Lap
	Counters Counters
	Tail     float64
	PreReset *float64
	Total    float64
	Stop     bool
	Extra    map[string]json.RawMessage
}

var knownKeys = map[string]bool{"v": true, "pid": true, "elapsed": true, "timers": true, "state": true, "laps": true,
	"counters": true, "tail": true, "pre_reset": true, "total": true, "stop": true}

// Marshal writes the document with the known keys; a key Go does not use is kept through Extra.
func (h Handoff) Marshal() ([]byte, error) {
	m := map[string]any{"v": 1, "pid": h.Pid, "elapsed": h.Elapsed, "timers": h.Timers, "state": h.State,
		"laps": lapsJSON(h.Laps), "counters": h.Counters, "tail": h.Tail, "total": h.Total, "stop": h.Stop}
	if m["state"].(State) == nil {
		m["state"] = State{}
	}
	if h.PreReset != nil {
		m["pre_reset"] = *h.PreReset
	}
	for k, v := range h.Extra {
		if !knownKeys[k] {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

func lapsJSON(l []Lap) [][2]any {
	out := make([][2]any, 0, len(l))
	for _, x := range l {
		out = append(out, [2]any{x.Name, x.Sec})
	}
	return out
}

// ParseHandoff reads a document a child rewrote. Any shape problem is an error; the caller prints the H7 text.
func ParseHandoff(b []byte) (*Handoff, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var v int
	if err := json.Unmarshal(raw["v"], &v); err != nil || v != 1 {
		return nil, errors.New("v is not 1")
	}
	h := &Handoff{Extra: map[string]json.RawMessage{}}
	for k, r := range raw {
		if !knownKeys[k] {
			h.Extra[k] = r
		}
	}
	get := func(key string, dst any, required bool) error {
		r, ok := raw[key]
		if !ok {
			if required {
				return fmt.Errorf("key %q missing", key)
			}
			return nil
		}
		if err := json.Unmarshal(r, dst); err != nil {
			return fmt.Errorf("key %q: %w", key, err)
		}
		return nil
	}
	var laps [][]json.RawMessage
	for _, step := range []error{
		get("pid", &h.Pid, false), get("elapsed", &h.Elapsed, false), get("timers", &h.Timers, false),
		get("state", &h.State, true), get("laps", &laps, true), get("counters", &h.Counters, true),
		get("tail", &h.Tail, true), get("total", &h.Total, false), get("stop", &h.Stop, true),
		get("pre_reset", &h.PreReset, false),
	} {
		if step != nil {
			return nil, step
		}
	}
	if h.State == nil {
		return nil, errors.New("state is null")
	}
	for _, l := range laps {
		var name string
		var sec float64
		if len(l) != 2 || json.Unmarshal(l[0], &name) != nil || json.Unmarshal(l[1], &sec) != nil {
			return nil, errors.New("laps entry is not [name, seconds]")
		}
		h.Laps = append(h.Laps, Lap{name, sec})
	}
	return h, nil
}

// WriteHandoff writes run/tick-handoff.json atomically (temp file in run/, mode 0600, rename).
func WriteHandoff(home string, h Handoff) (string, error) {
	run := filepath.Join(home, "run")
	b, err := h.Marshal()
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(run, ".tick-handoff-*")
	if err != nil {
		return "", fmt.Errorf("cannot write the tick handoff: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("cannot write the tick handoff: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("cannot write the tick handoff: %w", err)
	}
	path := filepath.Join(run, HandoffName)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("cannot write the tick handoff: %w", err)
	}
	return path, nil
}

// ReadHandoff loads run/tick-handoff.json.
func ReadHandoff(path string) (*Handoff, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseHandoff(b)
}
