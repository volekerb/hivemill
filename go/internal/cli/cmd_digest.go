package cli

import "factory/go/internal/identity"

// digest --goal G [--date YYYY-MM-DD] [--hours N] (Python cmd_digest, line 6779, no --send). The factory-wide digest and
// --send are other tasks' and run in Python; so does any argv that argparse would treat differently.

func init() {
	register("digest", "", handler{parse: parseDigest, run: runDigest,
		ident: func(any) identity.Cmd { return identity.Cmd{Name: "digest"} }})
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
