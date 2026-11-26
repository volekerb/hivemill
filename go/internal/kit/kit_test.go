package kit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cfg struct {
	SchemaVersion int    `json:"schemaVersion"`
	Name          string `json:"name"`
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name, body, wantErr string
	}{
		{"valid", `{"schemaVersion":1,"name":"a"}`, ""},
		{"older than max is fine", `{"schemaVersion":1}`, ""},
		{"unknown field refused", `{"schemaVersion":1,"nope":true}`, "unknown field"},
		{"missing schemaVersion refused", `{"name":"a"}`, "schemaVersion is required"},
		{"zero refused", `{"schemaVersion":0}`, "integer >= 1"},
		{"fractional refused", `{"schemaVersion":1.5}`, "integer >= 1"},
		{"string refused", `{"schemaVersion":"1"}`, "integer >= 1"},
		{"above max refused", `{"schemaVersion":3}`, "schemaVersion 3 is newer than this binary supports (max 2); upgrade hivemill"},
		{"newer wins over unknown field", `{"schemaVersion":3,"nope":1}`, "is newer than this binary supports"},
		{"not an object", `[1]`, "not a JSON object"},
		{"trailing data", `{"schemaVersion":1} {}`, "not a JSON object"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c cfg
			err := LoadConfig(writeCfg(t, tc.body), 2, &c)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	var c cfg
	if err := LoadConfig(filepath.Join(t.TempDir(), "absent.json"), 1, &c); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

func TestVersion(t *testing.T) {
	var b bytes.Buffer
	if !HandleVersion(&b, "hm-x", []string{"--version"}) || b.String() != "hm-x dev\n" {
		t.Fatalf("got %q", b.String())
	}
	b.Reset()
	if HandleVersion(&b, "hm-x", []string{"--version", "x"}) || HandleVersion(&b, "hm-x", nil) || b.Len() != 0 {
		t.Fatal("must only handle a lone --version")
	}
}

func TestExitCodes(t *testing.T) {
	if OK != 0 || Err != 1 || Usage != 2 || Refused != 3 {
		t.Fatal("exit codes are fixed by ADR 0007")
	}
}
