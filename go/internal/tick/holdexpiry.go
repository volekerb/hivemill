package tick

import (
	"context"
	"database/sql"
	"fmt"

	"factory/go/internal/holdexpiry"
)

// holdExpiryUnit is the unit name of the judge hold expiry (docs/judge-hold-expiry.md). init registers
// it only when the unit table has a row of that name (it has one since T-1610).
const holdExpiryUnit = "hold_expiry"

func init() { registerHoldExpiry() }

// registerHoldExpiry registers the Go unit when the walk contains its name and reports whether it did.
func registerHoldExpiry() bool {
	for _, u := range walkUnits() {
		if u.Name == holdExpiryUnit {
			registerGo(holdExpiryUnit, runHoldExpiry)
			return true
		}
	}
	return false
}

// runHoldExpiry is the Go tick unit: it runs the rule once through RunHoldExpiryOnce.
func runHoldExpiry(u *UnitCtx) error {
	RunHoldExpiryOnce(u.Ctx, u.DB, u.Stdout)
	return nil
}

// RunHoldExpiryOnce runs the rule once and prints one line per cleared or undecidable hold. A failure is printed and
// does not end the tick: a task whose transaction failed keeps its hold and is tried again next tick, and the units
// after this one (dispatch, nudges, export) must still run. It is shared by the Go unit and by `factory-go
// hold-expiry`, which the Python tick runs (docs/judge-hold-expiry.md). A nil out discards the lines.
func RunHoldExpiryOnce(ctx context.Context, db *sql.DB, out func(string)) {
	if out == nil {
		out = func(string) {}
	}
	res, err := holdexpiry.Run(ctx, holdexpiry.Deps{DB: db})
	for _, e := range res.Expired {
		out(fmt.Sprintf("judge-hold-expired %s: held %d min, set by %s, marker %s, report to %s\n",
			e.Task, e.HeldMin, e.SetBy, e.Marker, e.To))
	}
	for _, s := range res.Skipped {
		out(fmt.Sprintf("judge-hold-expiry %s: hold kept, cannot decide: %s\n", s.Task, s.Why))
	}
	if err != nil {
		out(fmt.Sprintf("judge-hold-expiry: failed, holds kept: %v\n", err))
	}
}
