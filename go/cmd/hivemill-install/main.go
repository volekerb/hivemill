// Command hivemill-install is the non-interactive installer for the hivemill factory: the Go port of
// plugins/hivemill/scripts/install.py. The plugin's verifier shim (plugins/hivemill/scripts/install) downloads the
// pinned release build of this binary, checks its sha256 and runs it.
package main

import (
	"fmt"
	"os"

	"factory/go/internal/install"
)

func main() {
	host, err := install.NewOSHost()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hivemill-install:", err)
		os.Exit(1)
	}
	os.Exit(install.Main(os.Args[1:], host, os.Stdout, os.Stderr))
}
