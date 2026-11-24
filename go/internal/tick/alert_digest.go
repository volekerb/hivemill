package tick

import (
	"errors"
	"fmt"
	"os"

	"factory/go/internal/digest"
	"factory/go/internal/identity"
)

// Unit digest (_tick_u_digest -> morning_digest): once a day, from digest_hour on, the director sends the digest to the
// boss of every enabled org. Never breaks the tick: a failure is one digest-failed event per day.

func init() {
	registerGo("digest", digestUnit)
	registerGo("tmp_alert", tmpAlertUnit)
	registerGo("remote_url_alert", remoteURLAlertUnit)
	registerGo("main_protection", mainProtectionUnit)
}

func digestUnit(u *UnitCtx) error {
	a, closeDB, err := openAlert(u)
	if err != nil {
		return err
	}
	defer closeDB()
	return morningDigest(a)
}

// morningDigest is morning_digest: any failure of the run is the digest-failed event, once per day.
func morningDigest(a *alertEnv) error {
	now := alertWall()
	today := now.Format("2006-01-02")
	err := morningDigestRun(a, now.Hour(), today)
	if err == nil {
		return nil
	}
	// except Exception: the tick must go on
	failed, ok, ferr := configRow(a.S.DB, "last_digest_fail_date")
	if ferr != nil {
		return nil // the inner try/except pass
	}
	if ok && failed == today {
		return nil
	}
	if eerr := a.S.EventAs(pruneActor, "digest", "-", "digest-failed", err.Error(), ""); eerr != nil {
		return nil
	}
	_ = setConfig(a.S.DB, "last_digest_fail_date", today) // inner try/except pass: a failed marker write only repeats the event tomorrow
	return nil
}

func morningDigestRun(a *alertEnv, hour int, today string) error {
	s := a.S
	dh, err := s.Cfg("digest_hour")
	if err != nil {
		return err
	}
	h, ok, perr := digest.PyIntStr(dh)
	if perr != nil {
		return fmt.Errorf("digest_hour %s is not an ASCII integer: %w", pyRepr(dh), perr)
	}
	if !ok {
		return fmt.Errorf("invalid literal for int() with base 10: %s", pyRepr(dh))
	}
	if int64(hour) < h {
		return nil
	}
	last, has, err := configRow(s.DB, "last_digest_date")
	if err != nil {
		return err
	}
	if has && last == today {
		return nil
	}
	env := digest.Env{Getenv: os.Getenv, Now: alertWall}
	text, err := digest.Build(s, env, "", "", 24)
	if err != nil {
		if errors.Is(err, identity.ErrDelegate) {
			return fmt.Errorf("the Go digest cannot reproduce this input byte for byte; set tick_impl to python: %w", err)
		}
		return err
	}
	orgs, err := digest.EnabledOrgs(s.DB)
	if err != nil {
		return err
	}
	for _, o := range orgs {
		sender, err := s.Oname(o, "director")
		if err != nil {
			return err
		}
		if _, _, err := digest.Send(s, today, text, "", sender, o); err != nil {
			return err
		}
	}
	return setConfig(s.DB, "last_digest_date", today)
}
