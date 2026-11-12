package cli

import (
	"regexp"
	"strings"
	"testing"
)

// TestRuleKeysMatchPython: ruleKeys is the key set _config_value_rules of bin/factory_cli.py validates.
func TestRuleKeysMatchPython(t *testing.T) {
	src := pySource(t)
	i := strings.Index(src, "def _config_value_rules(")
	j := strings.Index(src[i:], "\ndef ")
	if i < 0 || j < 0 {
		t.Fatal("_config_value_rules not found in the Python source")
	}
	body := src[i : i+j]
	want := map[string]bool{}
	for _, m := range regexp.MustCompile(`a\.key (?:==|in) (\("[^)]*\)|"[^"]*")`).FindAllStringSubmatch(body, -1) {
		for _, k := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
			want[k[1]] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("no keys parsed from _config_value_rules")
	}
	for k := range want {
		if !ruleKeys[k] {
			t.Errorf("ruleKeys lacks %q, which Python validates", k)
		}
	}
	for k := range ruleKeys {
		if !want[k] {
			t.Errorf("ruleKeys has %q, which Python does not validate", k)
		}
	}
}
