package cli

import (
	"factory/go/internal/digest"
	"factory/go/internal/identity"
	"factory/go/internal/store"
)

// digest [--date D] [--hours N] and digest --send [--goal G] [--date D] [--hours N] (Python build_digest without a goal,
// cmd_digest --send; G-041 S2t). The goal form without --send stays in cmd_digest.go (S2s). The tick's morning_digest stays Python.

type digestAllCmd struct {
	goal, date, hours string
	send              bool
}

func parseDigestAll(rest []string) (any, bool) {
	c := digestAllCmd{hours: "24"}
	ok := flagParser{vals: map[string]*string{"--goal": &c.goal, "--date": &c.date, "--hours": &c.hours},
		bools: map[string]*bool{"--send": &c.send}}.parse(rest)
	return c, ok
}

func identDigestAll(p any) identity.Cmd {
	a, _ := p.(digestAllCmd)
	return identity.Cmd{Name: "digest", Send: a.send}
}

func runDigestAll(c *ctx, p any) (int, error) {
	a := p.(digestAllCmd)
	if a.send {
		if _, err := requireRole(c, "director"); err != nil {
			return 0, err
		}
	}
	hours, ok, err := pyIntStr(a.hours)
	if err != nil {
		return 0, err
	}
	if !ok || hours < 0 || hours > digest.HoursMax {
		return 0, errDelegate // argparse's usage error, or arithmetic Python may refuse
	}
	text, err := digest.Build(&store.Store{DB: c.db}, digestEnv(c), a.goal, a.date, int(hours))
	if err != nil {
		return 0, err
	}
	if !a.send {
		c.out.WriteString(text)
		return 0, nil
	}
	day, err := digest.Day(digestEnv(c), a.date)
	if err != nil {
		return 0, err
	}
	var goal *string
	if a.goal != "" {
		goal = strp(a.goal)
	}
	mid, rcpt, err := msgSend(c, "boss", "daily digest "+day, strp(text), "report", goal, nil, "")
	if err != nil {
		return 0, err
	}
	c.out.WriteString(digest.Line(mid, rcpt))
	return 0, nil
}

// digestEnv is the process view the digest reads: TZ and FACTORY_NOW from the command's environment, its clock.
func digestEnv(c *ctx) digest.Env { return digest.Env{Getenv: c.env.Getenv, Now: c.env.Now} }
