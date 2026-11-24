package cursor

import (
	"testing"

	ch "factory/go/internal/cursorharness"
)

// Port of tests/test_cursor_attribution.py (1 case; see ../ported_cursor.txt).

func TestCursorAttributionMaterializedCLIJSONDisablesAgentAttribution(t *testing.T) {
	info := materialize(t, t.TempDir(), "coder", ch.MaterializeOpts{Deny: []string{"Bash(sudo:*)"}, Allow: []string{}})
	attr := sub(t, readJSON(t, info.CLI), "attribution")
	for _, k := range []string{"attributeCommitsToAgent", "attributePRsToAgent"} {
		v, isBool := attr[k].(bool)
		if !isBool || v {
			t.Fatalf("attribution.%s = %v, want false", k, attr[k])
		}
	}
}
