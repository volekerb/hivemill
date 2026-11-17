package tick

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"factory/go/internal/herdr"
	"factory/go/internal/store"
)

// The cross-unit state the reap units read and write, in the handoff types Python writes (H2): agents list or null, t
// float, hc_back bool, hc_by_name / tui_by_name / tui_by_pane objects of agent rows, hc_list_ok / tui_list_ok bool.
// by_name and by_pane are NOT handoff keys (Python recomputes them in unit 20); reapIndex recomputes them the same way.

func stateRaw(s State, key string) (json.RawMessage, error) {
	r, ok := s[key]
	if !ok {
		return nil, fmt.Errorf("tick state has no %q: unit herdr_list must run before the reap units", key)
	}
	return r, nil
}

// stateAgents decodes state["agents"]. null is true when herdr_list stored null (the host could not be asked).
func stateAgents(s State) (agents []herdr.Agent, null bool, err error) {
	r, err := stateRaw(s, "agents")
	if err != nil {
		return nil, false, err
	}
	if bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
		return nil, true, nil
	}
	if err := json.Unmarshal(r, &agents); err != nil {
		return nil, false, fmt.Errorf("tick state agents is not a list of objects: %w", err)
	}
	return agents, false, nil
}

// stateBool decodes a bool key.
func stateBool(s State, key string) (bool, error) {
	r, err := stateRaw(s, key)
	if err != nil {
		return false, err
	}
	var b bool
	if err := json.Unmarshal(r, &b); err != nil {
		return false, fmt.Errorf("tick state %s is not a bool: %w", key, err)
	}
	return b, nil
}

// stateBoolOr is st.get(key, def) for a bool: an absent or null key is def.
func stateBoolOr(s State, key string, def bool) (bool, error) {
	r, ok := s[key]
	if !ok || bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
		return def, nil
	}
	return stateBool(s, key)
}

// stateFloat decodes a number key.
func stateFloat(s State, key string) (float64, error) {
	r, err := stateRaw(s, key)
	if err != nil {
		return 0, err
	}
	var f float64
	if err := json.Unmarshal(r, &f); err != nil {
		return 0, fmt.Errorf("tick state %s is not a number: %w", key, err)
	}
	return f, nil
}

// setStateFloat stores a float the way Python's json.dumps writes it (an integral value keeps ".0"), so a child that
// reads the handoff sees a float.
func setStateFloat(s State, key string, f float64) {
	s[key] = json.RawMessage(store.PyFloatRepr(f))
}

// stateAgentMap decodes an object of agent rows (hc_by_name, tui_by_name, tui_by_pane). An absent or null key is an
// empty map, as Python's `st.get(key) or {}`.
func stateAgentMap(s State, key string) (map[string]herdr.Agent, error) {
	r, ok := s[key]
	if !ok || bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
		return map[string]herdr.Agent{}, nil
	}
	var m map[string]herdr.Agent
	if err := json.Unmarshal(r, &m); err != nil {
		return nil, fmt.Errorf("tick state %s is not an object of agent rows: %w", key, err)
	}
	if m == nil {
		m = map[string]herdr.Agent{}
	}
	return m, nil
}

// reapIndex is the first lines of _tick_u_pending_nudges: by_name and by_pane from the agents list (herdr.Index is
// _agents_index) and, when the factory is not hc-backed, the hc rows laid over by_name (`{**by_name, **hc_by_name}`).
func reapIndex(s State) (byName, byPane map[string]herdr.Agent, err error) {
	agents, _, err := stateAgents(s)
	if err != nil {
		return nil, nil, err
	}
	hcBack, err := stateBool(s, "hc_back")
	if err != nil {
		return nil, nil, err
	}
	byName, byPane = herdr.Index(agents)
	if !hcBack {
		hc, err := stateAgentMap(s, "hc_by_name")
		if err != nil {
			return nil, nil, err
		}
		for k, v := range hc {
			byName[k] = v
		}
	}
	return byName, byPane, nil
}

// reapStore is the herdr.Store over the factory DB: config rows, agents rows and events written as the tick. The Store
// interface has no error returns, so the first failure is kept and Err reports it after the call.
type reapStore struct {
	r   *reapDB
	err error
}

func (s *reapStore) note(err error) {
	if err != nil && s.err == nil {
		s.err = err
	}
}

// Err is the first DB error any Store call met, or nil.
func (s *reapStore) Err() error { return s.err }

// Config implements herdr.Store: the row, with no default (the client applies its own).
func (s *reapStore) Config(key string) (string, bool) {
	v, ok, err := s.r.CfgRow(key)
	s.note(err)
	return v, ok
}

// AgentRow implements herdr.Store.
func (s *reapStore) AgentRow(name string) *herdr.Row {
	var sa, pane sql.NullString
	err := s.r.DB.QueryRow("SELECT spawn_args, pane_id FROM agents WHERE name=?", name).Scan(&sa, &pane)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		s.note(fmt.Errorf("read agent %s: %w", name, err))
		return nil
	}
	return &herdr.Row{Name: name, SpawnArgs: sa.String, PaneID: pane.String}
}

// Event implements herdr.Store: the detail string is stored as is (Python event() stores a str unchanged).
func (s *reapStore) Event(entity, id, action, detail string) {
	s.note(s.r.event(entity, id, action, detail))
}

// newHerdrClient is the herdr client the reap units share: the owner's tick is never a worker, so the socket is the
// configured one.
func newHerdrClient(r *reapDB) (*herdr.Client, *reapStore) {
	st := &reapStore{r: r}
	return &herdr.Client{Store: st}, st
}
