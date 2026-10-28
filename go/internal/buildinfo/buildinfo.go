// Package buildinfo holds the version string of the factory Go tools.
package buildinfo

// Version is the module version string reported by factory-go-version.
const Version = "0.1.0"

// String returns the version line printed by factory-go-version.
func String() string {
	return "factory-go " + Version
}
