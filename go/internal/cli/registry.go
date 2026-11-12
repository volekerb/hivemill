package cli

import (
	"bytes"
	"database/sql"
	"fmt"

	"factory/go/internal/identity"
)

// ctx is what a handler's run gets: the open database, the resolved identity and the buffers Run flushes.
type ctx struct {
	db   *sql.DB
	who  string
	env  identity.Env
	deps *Deps
	out  *bytes.Buffer
	errb *bytes.Buffer
}

// handler answers one argv shape. parse sees the arguments after the command (and action) words; ok=false means
// "delegate to Python". run returns the exit code, or errDelegate / *identity.ExitError / *rawExit. ident is the
// identity.Cmd reported to RequireKnown (nil: the registered name and action, a read).
type handler struct {
	parse func(rest []string) (any, bool)
	run   func(c *ctx, parsed any) (int, error)
	ident func(parsed any) identity.Cmd

	name, action string
}

func (h handler) identity(parsed any) identity.Cmd {
	if h.ident != nil {
		return h.ident(parsed)
	}
	return identity.Cmd{Name: h.name, Action: h.action}
}

type key struct{ name, action string }

var registry = map[key]handler{}

// register adds a handler for argv[0] (action "") or for argv[0] argv[1]. Called from init() in each cmd_<x>.go; a
// duplicate key is a programming error and panics at start-up.
func register(name, action string, h handler) {
	k := key{name, action}
	if _, dup := registry[k]; dup {
		panic(fmt.Sprintf("cli: duplicate registration for %q %q", name, action))
	}
	h.name, h.action = name, action
	registry[k] = h
}

// lookup finds the handler for args and parses the rest. ok=false: not ours, delegate.
func lookup(args []string) (handler, any, bool) {
	if len(args) == 0 {
		return handler{}, nil, false
	}
	h, rest, found := handler{}, []string(nil), false
	if len(args) >= 2 {
		h, found = registry[key{args[0], args[1]}]
		rest = args[2:]
	}
	if !found {
		h, found = registry[key{args[0], ""}]
		rest = args[1:]
	}
	if !found {
		return handler{}, nil, false
	}
	p, ok := h.parse(rest)
	if !ok {
		return handler{}, nil, false
	}
	return h, p, true
}
