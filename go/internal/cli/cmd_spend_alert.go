package cli

import (
	"context"
	"fmt"
	"time"

	"factory/go/internal/identity"
	"factory/go/internal/spendalert"
)

// spend-alert (T-1824, owner order M41393): a Go-only command the Python tick runs from its util unit. It checks
// `hm-spend report` against the spike rules (docs/spend-alert.md) at most every 15 minutes and mails claude-boss at
// most once per clock hour. It never fails the tick: a failing hm-spend is one event, and the exit code stays 0.

func init() {
	register("spend-alert", "", handler{parse: parseSpendAlert, run: runSpendAlertCmd, ident: spendAlertIdent})
}

type spendAlertArgs struct{ extra bool }

func parseSpendAlert(rest []string) (any, bool) { return spendAlertArgs{extra: len(rest) > 0}, true }

func spendAlertIdent(any) identity.Cmd { return identity.Cmd{Name: "whoami"} }

// spendAlertRun is the hm-spend boundary; tests replace it.
var spendAlertRun = spendalert.ExecSpend

func runSpendAlertCmd(c *ctx, p any) (int, error) {
	if c.who != "owner" {
		return 0, dieErr(2, "spend-alert: owner only (it is run by the tick); identity is "+c.who)
	}
	if p.(spendAlertArgs).extra {
		return 0, dieErr(2, "usage: factory-go spend-alert (no arguments)")
	}
	res, err := spendalert.Run(context.Background(), spendalert.Deps{DB: c.db, Now: time.Now, Spend: spendAlertRun})
	if err != nil {
		fmt.Fprintf(c.out, "spend-alert: failed: %v\n", err)
		return 0, nil
	}
	if res.Alerted {
		fmt.Fprintf(c.out, "spend-alert: sent to %s: $%.2f in the last hour (24 h average $%.2f/h)\n", spendalert.Recipient, res.Hour1, res.Avg24)
	}
	return 0, nil
}
