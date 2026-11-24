package hm

import "testing"

func TestConfigLines(t *testing.T) {
	cfg := func(k string) string {
		switch k {
		case "max_concurrent":
			return "4"
		case "backup_target":
			return "file:///x"
		}
		return Defaults[k]
	}
	lines := ConfigLines(cfg)
	if len(lines) != len(Defaults) {
		t.Fatalf("%d lines for %d keys", len(lines), len(Defaults))
	}
	want := map[string]bool{
		"max_concurrent=4 (default " + Defaults["max_concurrent"] + ")": false,
		`backup_target=file:///x (default "")`:                          false,
		"model=" + Defaults["model"]:                                    false,
	}
	for i, ln := range lines {
		if _, ok := want[ln]; ok {
			want[ln] = true
		}
		if i > 0 && lines[i-1] >= ln {
			t.Errorf("not sorted: %q before %q", lines[i-1], ln)
		}
	}
	for ln, seen := range want {
		if !seen {
			t.Errorf("missing line %q", ln)
		}
	}
}
