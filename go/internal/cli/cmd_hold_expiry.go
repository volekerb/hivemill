package cli

import (
	"context"

	"factory/go/internal/identity"
	"factory/go/internal/tick"
)

// hold-expiry (T-1631, G-058): a Go-only command with no Python twin. The Python tick runs it as a subprocess in its
// hold_expiry unit (`factory-go hold-expiry`, docs/judge-hold-expiry.md) so the 2 h judge-hold rule has one
// implementation (go/internal/holdexpiry). It is meant for the tick only: no arguments, owner identity only.
//
// It does NOT take the tick lock: the Python tick that calls it already holds it, and taking it here would deadlock
// the tick against its own child. It exits 0 even when the rule reports a failure (the holds are kept and the tick
// must go on); the lines are the same ones the Go tick unit prints.

func init() {
	register("hold-expiry", "", handler{parse: parseHoldExpiry, run: runHoldExpiryCmd, ident: holdExpiryIdent})
}

type holdExpiryArgs struct{ extra bool }

// parseHoldExpiry always accepts: an argument is a usage error (exit 2) from run, not a delegation to Python, which
// has no such command.
func parseHoldExpiry(rest []string) (any, bool) { return holdExpiryArgs{extra: len(rest) > 0}, true }

// holdExpiryIdent reports a read so an unknown identity reaches runHoldExpiryCmd, which refuses every non-owner.
func holdExpiryIdent(any) identity.Cmd { return identity.Cmd{Name: "whoami"} }

func runHoldExpiryCmd(c *ctx, p any) (int, error) {
	if c.who != "owner" {
		return 0, dieErr(2, "hold-expiry: owner only (it is run by the tick); identity is "+c.who)
	}
	if p.(holdExpiryArgs).extra {
		return 0, dieErr(2, "usage: factory-go hold-expiry (no arguments)")
	}
	tick.RunHoldExpiryOnce(context.Background(), c.db, func(s string) { c.out.WriteString(s) })
	return 0, nil
}
