package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"factory/go/internal/retro"
	"factory/go/internal/scout"
)

// retro packet | file - | show [--day D] (Python cmd_retro). Registered for P4-L: the Python dispatcher answers until
// "retro" is added to cli_go_cmds.

func init() {
	register("retro", "packet", handler{parse: parseRetroPacket, run: runRetroPacket})
	register("retro", "file", handler{parse: parseRetroFile, run: runRetroFile})
	register("retro", "show", handler{parse: parseRetroShow, run: runRetroShow})
}

type retroShowCmd struct{ day string }

func parseRetroPacket(rest []string) (any, bool) { return nil, len(rest) == 0 }

func parseRetroFile(rest []string) (any, bool) { return nil, len(rest) == 1 && rest[0] == "-" }

func parseRetroShow(rest []string) (any, bool) {
	var c retroShowCmd
	return c, flagParser{vals: map[string]*string{"--day": &c.day}}.parse(rest)
}

// retroClock is digest_clock for the retro commands.
func retroClock(c *ctx) (time.Time, error) {
	loc, err := sbLocation(c.env.Getenv)
	if err != nil {
		return time.Time{}, err
	}
	t, err := sbClock(c.env.Getenv, c.env.Now, loc)
	return t, err
}

func runRetroPacket(c *ctx, _ any) (int, error) {
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	switch role {
	case "retro", "director", "boss", "lead":
	default:
		if c.who != "owner" {
			return 0, dieErr(3, fmt.Sprintf("role '%s' (%s) may not run `factory retro packet`", role, c.who))
		}
	}
	clock, err := retroClock(c)
	if err != nil {
		return 0, err
	}
	p, err := retro.BuildPacket(c.db, clock)
	if err != nil {
		return 0, fmt.Errorf("retro packet: %w", err)
	}
	fmt.Fprintln(c.out, p.Text())
	return 0, nil
}

func runRetroFile(c *ctx, _ any) (int, error) {
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	if role != "retro" {
		return 0, dieErr(3, fmt.Sprintf("role '%s' (%s) may not run `factory retro file`; only the retro agent files reports", role, c.who))
	}
	raw, err := io.ReadAll(c.deps.stdin())
	if err != nil {
		return 0, fmt.Errorf("read stdin: %w", err)
	}
	text := string(raw)
	if text == "" {
		text = "{}"
	}
	body, err := retro.DecodeOrdered([]byte(text))
	if err != nil {
		return 0, dieErr(2, "stdin is not valid JSON: "+err.Error())
	}
	day, norm, err := retro.ValidateFile(c.db, body)
	if err != nil {
		var ve *retro.ValueError
		if errors.As(err, &ve) {
			return 0, dieErr(2, ve.Msg)
		}
		return 0, err
	}
	clock, err := retroClock(c)
	if err != nil {
		return 0, err
	}
	if err := retro.StoreReport(c.db, day, norm, c.who, scout.Stamp(clock)); err != nil {
		return 0, err
	}
	fmt.Fprintf(c.out, "filed retro report for %s\n", day)
	return 0, nil
}

func runRetroShow(c *ctx, p any) (int, error) {
	a := p.(retroShowCmd)
	if _, err := requireRole(c, "retro", "director", "boss", "lead"); err != nil {
		return 0, err
	}
	day := a.day
	if day == "" {
		clock, err := retroClock(c)
		if err != nil {
			return 0, err
		}
		day = clock.Format("2006-01-02")
	}
	r, ok, err := retro.LoadReport(c.db, day)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, dieErr(2, "no retro report for "+day)
	}
	text, err := r.ShowText()
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(c.out, text)
	return 0, nil
}
