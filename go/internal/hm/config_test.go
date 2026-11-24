package hm

import "testing"

func TestValidateCfg(t *testing.T) {
	for _, c := range []struct {
		key, value string
		ok         bool
	}{
		{"max_concurrent", "3", true},
		{"max_concurrent", "0", true},
		{"max_concurrent", "-1", false},
		{"gate_wait_s", " 900 ", true},
		{"max_concurent", "3", false},
		{"model", "claude-sonnet-5-5", true},
		{"backup_target", "file:///mnt/nas", true},
	} {
		if err := ValidateCfg(c.key, c.value); (err == nil) != c.ok {
			t.Errorf("%s=%q: err %v, want ok=%v", c.key, c.value, err, c.ok)
		}
	}
}
