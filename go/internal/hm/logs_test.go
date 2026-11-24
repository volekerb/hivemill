package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLogs(t *testing.T) {
	r, _ := testRunner(t, "")
	dir := filepath.Join(r.Env.StateDir(), "runs", "T-0001", "round-1")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "proof-2.log"), []byte("exit 1\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "gate.json"), []byte("{}"), 0o600)
	files, err := r.RunLogs("T-0001")
	if err != nil || strings.Join(files, ",") != "round-1/gate.json,round-1/proof-2.log" {
		t.Fatalf("%v %v", files, err)
	}
	text, err := r.RunLog("T-0001", "round-1/proof-2.log")
	if err != nil || text != "exit 1\n" {
		t.Errorf("%q %v", text, err)
	}
	if _, err := r.RunLog("T-0001", "round-1/missing.log"); err == nil {
		t.Error("a missing file is an error")
	}
}
