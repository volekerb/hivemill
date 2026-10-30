package guard

import (
	"strings"
	"testing"
	"time"
)

// T-0352 review nit: a ~190 KB command must not go quadratic (it took 34 s before the append/rune-slice fixes; now ~0.4 s).
func TestLongCommandIsNotQuadratic(t *testing.T) {
	for name, cmd := range map[string]string{
		"words":      "echo " + strings.Repeat("word ", 38000),
		"statements": strings.Repeat("ls -la /tmp; ", 17000),
	} {
		start := time.Now()
		if err := Check(cmd, t.TempDir(), "coder", false); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if d := time.Since(start); d > 15*time.Second {
			t.Errorf("%s: %d-byte command took %s, want well under 15s", name, len(cmd), d)
		}
	}
}
