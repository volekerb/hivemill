package hm

import (
	"strings"
	"testing"
)

func TestConfigLines(t *testing.T) {
	cfg := func(k string) string {
		if k == "max_concurrent" {
			return "4"
		}
		return Defaults[k]
	}
	lines := ConfigLines(cfg)
	if len(lines) != len(Defaults) {
		t.Fatalf("%d lines for %d keys", len(lines), len(Defaults))
	}
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "max_concurrent=4 (default 2)\n") || !strings.Contains(all, "model=claude-sonnet-5-5\n") {
		t.Errorf("lines:\n%s", all)
	}
}
