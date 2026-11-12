// Package cli is the Go side of the factory CLI (G-041 S1/S2). The commands it answers are exactly the
// registrations: each cmd_<x>.go calls register in init(), so there is no per-command list to keep in sync here.
// Anything not registered (another command, -h/--help, an unknown flag, a write, a database this binary does not
// understand) is handed to Python by execv: python3 <dir of the running bin/factory>/factory with
// FACTORY_CLI_IMPL=python. Output and exit codes of the commands handled here match bin/factory_cli.py byte for byte.
//
// All output is buffered and only written once the command succeeded or died on purpose, so a late decision to
// delegate never leaves half an answer behind.
package cli

//go:generate go run ./gen
