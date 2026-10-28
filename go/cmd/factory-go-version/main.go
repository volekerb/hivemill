// Command factory-go-version prints the factory Go module version.
package main

import (
	"fmt"

	"factory/go/internal/buildinfo"
)

func main() {
	fmt.Println(buildinfo.String())
}
