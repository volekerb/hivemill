package guard

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// Go port of roles/hooks/boss-inbox.py (G-048 P2a): the UserPromptSubmit hook of the boss pane. It runs
// `factory inbox --limit 1000` and hands the output to the model as additionalContext. It always exits 0 and never
// blocks the owner's prompt; an empty inbox, a missing or failing factory, or a timeout prints nothing.

const (
	bossInboxLimit  = 8000
	bossInboxHeader = "Factory inbox (read automatically with this owner message; act on it and report in this same reply):"
	bossInboxTail   = "\n(truncated; run factory inbox --all)"
)

// InboxRunner returns the stdout and exit code of `factory inbox --limit 1000`, or an error when it cannot run.
type InboxRunner func() (stdout []byte, code int, err error)

// ExecInbox runs the real factory CLI with the hook's 8 second timeout.
func ExecInbox() ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "factory", "inbox", "--limit", "1000")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok && ctx.Err() == nil {
		return out.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), 0, err
}

// BossInbox is `worker-guard --boss-inbox`. The result is always exit 0.
func BossInbox(stdin io.Reader, stdout io.Writer, run InboxRunner) (code int) {
	defer func() {
		if recover() != nil { // the hook never blocks the owner's prompt
			code = 0
		}
	}()
	_, _ = io.Copy(io.Discard, stdin) // content ignored; bad or empty stdin must not matter
	raw, code, err := run()
	if err != nil || !utf8.Valid(raw) {
		return 0
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
	out := strings.TrimFunc(text, pyIsSpace)
	if code != 0 || out == "" || out == "inbox empty" {
		return 0
	}
	body := bossInboxHeader + "\n" + out
	if r := []rune(body); len(r) > bossInboxLimit {
		body = string(r[:bossInboxLimit]) + bossInboxTail
	}
	fmt.Fprintln(stdout, `{"hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": `+pyJSONString(body)+`}}`)
	return 0
}
