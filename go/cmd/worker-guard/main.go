// Command worker-guard is the Go port of roles/hooks/worker-guard.py (G-016 SC4, flag-gated; not wired
// anywhere yet). It keeps the hook's I/O contract: argv (--role ROLE | --hq), a PreToolUse JSON payload on
// stdin, exit 0 = allow, exit 2 = block with the reason on stderr.
//
// Every rule is ported, the public-text rule (T-0887) included. The decisions that read the factory DB (own goal,
// test_sandbox, public-text repos, the coder's task worktree) are made from the guard export. One rule still
// re-runs a payload through the Python hook (--py-hook <path>): the archive member list of a no-push tar/unzip
// extraction (T-1001); the delegation also stays for a DB value the export has no row for. --py-hook still names
// the checkout the hook belongs to (ci/scrub_allowlist.txt for a push check).
// A panic or any internal error exits 2. The code lives in internal/guard (HookMain), which `hivemill guard` also runs.
package main

import (
	"io"
	"os"

	"factory/go/internal/guard"
)

// maxInput caps the stdin payload (guard.MaxHookInput).
const maxInput = guard.MaxHookInput

// testPanicEnv makes the hook panic, to prove the recover fails closed (guard.HookTestPanicEnv).
const testPanicEnv = guard.HookTestPanicEnv

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// run is guard.HookMain.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return guard.HookMain(args, stdin, stdout, stderr)
}

// parseArgs is guard.ParseHookArgs.
func parseArgs(args []string) (string, bool) { return guard.ParseHookArgs(args) }
