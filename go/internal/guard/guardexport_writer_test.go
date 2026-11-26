package guard_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"factory/go/internal/guard"
	"factory/go/internal/hm"
)

// The file hivemill writes into a home loads with the guard's own reader: no stale, parse or layout error.
func TestHivemillExportLoadsInTheGuard(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if _, err := hm.InitHome(home, hm.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := hm.NewRunner(hm.Env{Home: home, UserHome: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Store.Close()
	p := filepath.Join(home, "run", "guard-export.json")
	_ = os.Remove(p)
	if err := r.WriteGuardExport(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := guard.ReadExportForTest(p); err != nil {
		t.Fatalf("the guard refuses the export: %v", err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v %v", st, err)
	}
}
