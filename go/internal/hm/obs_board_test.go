package hm

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pageBody(t *testing.T, r *Runner, target string) string {
	t.Helper()
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", target, nil)
	req.Host = "127.0.0.1:7461"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET %s: %d", target, w.Code)
	}
	return w.Body.String()
}

func boardOf(t *testing.T, r *Runner, target string) *Board {
	t.Helper()
	var b Board
	if err := json.Unmarshal([]byte(pageBody(t, r, target)), &b); err != nil {
		t.Fatal(err)
	}
	return &b
}

func (b *Board) col(id string) BoardColumn {
	for _, c := range b.Columns {
		if c.ID == id {
			return c
		}
	}
	return BoardColumn{}
}

func TestBoardPutsEveryTaskInItsColumnWithItsCause(t *testing.T) {
	r, s := summaryRunner(t)
	_ = s.SetCfg("projects", "www")
	// T-0001 is the fixture's task; give T-3 a run record that says why its merge waits, and a title
	if _, err := s.db.Exec(`INSERT INTO tasks(id,goal_id,title,spec,status,acceptance) VALUES('T-3','G-001','Runner channel per project','spec','in_review','1. x. cmd:true => exit 0')`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(&Run{Task: "T-3", Project: "www", State: Merging, MergeHold: "hold: main's push run for its head is pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(&Run{Task: "T-1", Project: "www", State: Working, IdleSince: time.Now().UTC().Format(time.RFC3339), IdleNudges: 1}); err != nil {
		t.Fatal(err)
	}
	b := boardOf(t, r, "/api/obs/v1/board")
	var ids []string
	for _, c := range b.Columns {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "queued,working,ci,merge,held,done" || b.API != ObsAPI {
		t.Fatalf("columns: %v", ids)
	}
	merge := b.col("merge")
	if merge.Count != 1 || merge.Groups[0].Cause != "Main's checks still running" || merge.Groups[0].Cards[0].Title != "Runner channel per project" || merge.Groups[0].Cards[0].Key != "box-a/T-3" {
		t.Fatalf("merge: %+v", merge)
	}
	if !merge.Groups[0].Bad {
		t.Fatal("6h in merge is over the stage limit, so the group is marked")
	}
	if w := b.col("working"); w.Count != 1 || w.Groups[0].Cause != "Idle after a nudge" || !w.Groups[0].Bad {
		t.Fatalf("working: %+v", w)
	}
	if c := b.col("ci"); c.Count != 1 || c.Groups[0].Cause != "" {
		t.Fatalf("ci: %+v", c)
	}
	held := b.col("held")
	if held.Count != 1 || held.Groups[0].Cards[0].Note != "tier T3 is above T2; the owner merges" || held.Groups[0].Cards[0].PR == "" {
		t.Fatalf("held: %+v", held)
	}
	done := b.col("done")
	if b.Done.Failed != 1 || b.Done.Merged != 0 || done.Count != 1 || done.Groups[0].Cause != "Failed, not re-filed" || done.Groups[0].Cards[0].Note != "stalled" {
		t.Fatalf("done: %+v %+v", b.Done, done)
	}
	if q := b.col("queued"); q.Count != 0 {
		t.Fatalf("the fixture's only todo task has a run: %+v", q)
	}
	if other := boardOf(t, r, "/api/obs/v1/board?project=none-such"); other.col("working").Count+other.col("merge").Count != 0 || len(other.Projects) != 1 {
		t.Fatalf("project filter: %+v", other)
	}
}

func TestMergeCauseNamesWhatTheRunWaitsFor(t *testing.T) {
	for hold, want := range map[string]string{
		"": "Merging",
		"hold: main's push run for its head is pending":                              "Main's checks still running",
		"hold: main is red; merges wait for a green push run":                        "Main is red",
		"hold: main's push run for its head failed":                                  "Main is red",
		"refresh: the head does not contain the base branch; a merge of it is clean": "Behind main, refreshing",
		"resume: the branch conflicts with the base branch":                          "Conflicts with main",
	} {
		if got, _ := mergeCause(hold); got != want {
			t.Errorf("%q: %q, want %q", hold, got, want)
		}
	}
}

func TestTraceV1CarriesTheTitleAndPromptText(t *testing.T) {
	r, s := summaryRunner(t)
	if _, err := s.db.Exec(`INSERT INTO tasks(id,goal_id,title,spec,status,acceptance) VALUES('T-4','G-001','Owner hard limits','spec','in_review','1. x. cmd:true => exit 0')`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.Env.StateDir(), "prompts", "T-4")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "001-first.txt"), []byte("You are the coder for T-4."), 0o600)
	_ = os.WriteFile(filepath.Join(r.Env.StateDir(), "secret.txt"), []byte("outside"), 0o600)
	at := time.Now().UTC().Add(-80 * time.Minute).Format(time.RFC3339)
	writeEvents(t, r.Log.Path,
		ev(at, "prompt.sent", "T-4", "G-1", "www", "", "task", map[string]any{"kind": "first", "path": "prompts/T-4/001-first.txt"}),
		ev(at, "prompt.sent", "T-4", "G-1", "www", "", "task", map[string]any{"kind": "other", "path": "prompts/../secret.txt"}))
	var tr TraceV1
	if err := json.Unmarshal([]byte(pageBody(t, r, "/api/obs/v1/task?k=box-a%2FT-4")), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Title != "Owner hard limits" || tr.Stage != "held" || tr.Task != "T-4" || len(tr.Steps) < 3 {
		t.Fatalf("trace: %+v", tr)
	}
	if len(tr.Prompts) != 1 {
		t.Fatalf("one prompt is readable, the one outside prompts/ is not served: %+v", tr.Prompts)
	}
	for i, p := range tr.Prompts {
		if p.Text != "You are the coder for T-4." || p.Kind != "first" || tr.Steps[i].Kind != "prompt.sent" {
			t.Fatalf("prompt: %+v", p)
		}
	}
}
