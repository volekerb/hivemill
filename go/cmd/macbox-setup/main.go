// Command macbox-setup prints the macOS owner setup and uninstall scripts of
// ADR 0003 section 3.2. It only prints; the owner reads and runs the script.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"factory/go/internal/macbox"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "print-setup" && args[0] != "print-uninstall") {
		fmt.Fprintln(stderr, "usage: macbox-setup print-setup|print-uninstall --owner-user U --owner-home H --factory-home F [--launcher P]")
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o macbox.SetupOpts
	fs.StringVar(&o.OwnerUser, "owner-user", "", "the owner's login name")
	fs.StringVar(&o.OwnerHome, "owner-home", "", "the owner's home directory")
	fs.StringVar(&o.FactoryHome, "factory-home", "", "the factory home directory")
	fs.StringVar(&o.LauncherPath, "launcher", macbox.DefaultLauncher, "install path of the launcher")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	gen := macbox.SetupScript
	if args[0] == "print-uninstall" {
		gen = macbox.UninstallScript
	}
	s, err := gen(o)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprint(stdout, s)
	return 0
}
