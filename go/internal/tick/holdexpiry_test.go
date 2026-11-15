package tick

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func tableHas(name string) bool {
	for _, u := range walkUnits() {
		if u.Name == name {
			return true
		}
	}
	return false
}

// TestHoldExpiryRegistersOnlyWithTheRow pins the two-step shape: the unit is registered exactly when the unit table
// has a hold_expiry row.
func TestHoldExpiryRegistersOnlyWithTheRow(t *testing.T) {
	_, registered := goUnits[holdExpiryUnit]
	if registered != tableHas(holdExpiryUnit) {
		t.Fatalf("at start-up: registered=%v but table row present=%v", registered, tableHas(holdExpiryUnit))
	}
	savedUnits, savedFn := Units, goUnits[holdExpiryUnit]
	t.Cleanup(func() {
		Units = savedUnits
		delete(goUnits, holdExpiryUnit)
		if registered {
			goUnits[holdExpiryUnit] = savedFn
		}
	})

	// a table without the row: nothing is registered
	var without []Unit
	for _, u := range savedUnits {
		if u.Name != holdExpiryUnit {
			without = append(without, u)
		}
	}
	Units = without
	delete(goUnits, holdExpiryUnit)
	if registerHoldExpiry() {
		t.Error("registerHoldExpiry() = true on a table without the row")
	}
	if _, ok := goUnits[holdExpiryUnit]; ok {
		t.Error("the unit is registered on a table without the row")
	}

	// a table with the row: registered
	Units = append(append([]Unit{}, without...), Unit{holdExpiryUnit, holdExpiryUnit, "flow"})
	if !registerHoldExpiry() {
		t.Error("registerHoldExpiry() = false on a table with the row")
	}
	if _, ok := goUnits[holdExpiryUnit]; !ok {
		t.Error("the unit is not registered on a table with the row")
	}
}

// TestHoldExpiryUnitReportsFailureAndGoesOn: a failing run is printed and does not end the tick.
func TestHoldExpiryUnitReportsFailureAndGoesOn(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db")) // no tables: every read fails
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out strings.Builder
	if err := runHoldExpiry(&UnitCtx{DB: db, Stdout: func(s string) { out.WriteString(s) }}); err != nil {
		t.Fatalf("runHoldExpiry = %v, want nil (the tick goes on)", err)
	}
	if !strings.HasPrefix(out.String(), "judge-hold-expiry: failed, holds kept: ") || !strings.Contains(out.String(), "config") {
		t.Fatalf("output %q does not report the failure", out.String())
	}
}
