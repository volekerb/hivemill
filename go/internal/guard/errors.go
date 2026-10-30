package guard

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The exception classes of worker-guard.py below Block (Block itself is in frontend.go). Python's class
// hierarchy is kept as predicates: UnitBlock and RepoProtBlock are PrivBlocks, DynamicBlock is a ClaudeBlock.

// PrivBlock is worker-guard.py:PrivBlock; Word is the privilege word the rule found.
type PrivBlock struct{ Word string }

func (b *PrivBlock) Error() string { return b.Word }

// UnitBlock is worker-guard.py:UnitBlock: mutating systemctl (HQ mode) or systemd-run (every mode).
type UnitBlock struct{ Msg string }

func (b *UnitBlock) Error() string { return b.Msg }

// ClaudeBlock is worker-guard.py:ClaudeBlock: a `claude` start from a shell.
type ClaudeBlock struct{ Msg string }

func (b *ClaudeBlock) Error() string { return b.Msg }

// DynamicBlock is worker-guard.py:DynamicBlock: a command word that could expand to herdr or claude.
type DynamicBlock struct{ Msg string }

func (b *DynamicBlock) Error() string { return b.Msg }

// IsPrivBlock reports whether err is a PrivBlock or one of its Python subclasses.
func IsPrivBlock(err error) bool {
	var p *PrivBlock
	var u *UnitBlock
	var r *RepoProtBlock
	return errors.As(err, &p) || errors.As(err, &u) || errors.As(err, &r)
}

// IsClaudeBlock reports whether err is a ClaudeBlock or a DynamicBlock.
func IsClaudeBlock(err error) bool {
	var c *ClaudeBlock
	var d *DynamicBlock
	return errors.As(err, &c) || errors.As(err, &d)
}

// isPublicBlock reports whether err is a PublicBlock (never swallowed by HQ mode).
func isPublicBlock(err error) bool {
	var p *PublicBlock
	return errors.As(err, &p)
}

// Explain is the stderr text of worker-guard.py:main for err (one line, newline included).
func Explain(err error) string {
	var u *UnitBlock
	var d *DynamicBlock
	var c *ClaudeBlock
	var p *PrivBlock
	var b *Block
	var rp *RepoProtBlock
	var pb *PublicBlock
	switch {
	case errors.As(err, &pb):
		return fmt.Sprintf("worker-guard: blocked (%s)\n", pb.Msg)
	case errors.As(err, &u):
		return fmt.Sprintf("worker-guard: blocked (`%s`): mutating systemctl/systemd-run is not allowed for "+
			"factory agents; the owner manages units (M2108)\n", u.Msg)
	case errors.As(err, &p):
		return fmt.Sprintf("worker-guard: blocked: privilege escalation (`%s`) is not allowed for factory agents "+
			"(owner M803)\n", p.Word)
	case errors.As(err, &rp):
		return fmt.Sprintf("worker-guard: blocked (`%s`): branch protection and repository settings are changed only by "+
			"the owner (ci/publish_repo.py, rule R-a); agents may only read them with a plain GET. "+
			"Tell your lead if this is wrong.\n", rp.Msg)
	case errors.As(err, &d):
		return fmt.Sprintf("worker-guard: blocked (%s). Workers may not run herdr, and only factory spawn "+
			"starts Claude (lesson M347); use the factory CLI (factory done, factory msg, ...) "+
			"and ask your lead.\n", d.Msg)
	case errors.As(err, &c):
		return fmt.Sprintf("worker-guard: blocked (%s). only factory spawn starts Claude (lesson M347); "+
			"ask your lead.\n", c.Msg)
	case errors.As(err, &b):
		return fmt.Sprintf("worker-guard: blocked (%s). Workers may not run herdr or manage factory/herdr systemd units "+
			"directly; use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n", b.Msg)
	}
	return fmt.Sprintf("worker-guard: blocked (hook error, failing closed: %v). Workers may not run herdr or manage "+
		"factory/herdr systemd units directly; use the factory CLI (factory done, factory msg, ...). "+
		"Tell your lead if this is wrong.\n", err)
}

// pyRepr is repr(s) of a Python str: the quote choice and escapes of CPython.
func pyRepr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == rune(q) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(q)
	return b.String()
}
