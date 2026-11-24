package hm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"factory/go/internal/kit/spend"
)

func TestSessionCostsParallel(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-home-x-factory")
	_ = os.MkdirAll(dir, 0o755)
	var ids []string
	for i := 0; i < 9; i++ {
		sid := fmt.Sprintf("s%d", i)
		ids = append(ids, sid)
		usage := map[string]any{"input_tokens": 1000 * (i + 1), "output_tokens": 200, "cache_read_input_tokens": 5000,
			"cache_creation_input_tokens": 0}
		b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-11-24T12:00:00Z", "requestId": "q" + sid[:1] + fmt.Sprint(i%3),
			"message": map[string]any{"id": "m" + sid[:1] + fmt.Sprint(i%3), "model": "claude-sonnet-5-5", "usage": usage}})
		_ = os.WriteFile(filepath.Join(dir, sid+".jsonl"), append(b, '\n'), 0o644)
	}
	got := SessionCosts(home, ids)
	for k, b := range spend.SessionCostsN(filepath.Join(home, ".claude", "projects"), ids, 1) {
		if want := math.Round(b.Cost*10000) / 10000; got[k] != want {
			t.Errorf("%s: parallel %v, serial %v", k, got[k], want)
		}
	}
	if len(got) != 9 {
		t.Errorf("got %d sessions", len(got))
	}
}
