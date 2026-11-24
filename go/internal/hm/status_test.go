package hm

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStatusJSON(t *testing.T) {
	r, s := testRunner(t, "")
	var buf bytes.Buffer
	r.Out = &buf
	_ = s.PutRun(&Run{Task: "T-0001", State: Working, Round: 2, PRURL: "https://github.com/zz-test/www/pull/1"})
	_ = s.PutRun(&Run{Task: "T-0002", State: Failed, Round: 1, FailReason: "gate blocked 3 times"})
	if err := r.StatusJSON(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 || rows[0]["task"] != "T-0001" || rows[0]["state"] != Working || rows[1]["state"] != Failed {
		t.Errorf("rows %v", rows)
	}
}
