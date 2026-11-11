// Package cli is the Go side of the factory CLI (G-041 S1-b): whoami, flags, agents and the read forms of config.
// Anything else (another command, -h/--help, an unknown flag, a config write, a database this binary does not
// understand) is handed to Python by execv: python3 <dir of the running bin/factory>/factory with
// FACTORY_CLI_IMPL=python. Output and exit codes of the commands handled here match bin/factory_cli.py byte for byte.
//
// All output is buffered and only written once the command succeeded or died on purpose, so a late decision to
// delegate never leaves half an answer behind.
package cli

//go:generate go run ./gen
