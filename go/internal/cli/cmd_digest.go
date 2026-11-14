package cli

// digest registration. The goal form without --send (S2s) lives here; the factory-wide digest and --send are cmd_digest_all.go (S2t),
// and any argv that argparse would treat differently runs in Python.

func init() {
	register("digest", "", handler{parse: parseDigestAll, run: runDigestAll, ident: identDigestAll})
}

type digestCmd struct{ goal, date, hours string }

// dgHoursMax bounds --hours so the window arithmetic cannot overflow where Python's would raise.
const dgHoursMax = 100000

func parseDigest(rest []string) (any, bool) {
	c := digestCmd{hours: "24"}
	ok := flagParser{vals: map[string]*string{"--goal": &c.goal, "--date": &c.date, "--hours": &c.hours}}.parse(rest)
	if !ok || c.goal == "" {
		return c, false
	}
	return c, true
}

func runDigest(c *ctx, p any) (int, error) {
	a := p.(digestCmd)
	hours, ok, err := pyIntStr(a.hours)
	if err != nil {
		return 0, err
	}
	if !ok || hours < 0 || hours > dgHoursMax {
		return 0, errDelegate // argparse's usage error, or arithmetic Python may refuse
	}
	text, err := dgBuild(c, a.goal, a.date, int(hours))
	if err != nil {
		return 0, err
	}
	c.out.WriteString(text)
	return 0, nil
}
