// Package macfacts records read-only facts about a Mac for the ADR 0003
// "[untested]" claims. Every probe runs through a Runner so the logic is
// testable on Linux with a fake. Nothing here writes outside the run's own
// temp dir, uses sudo, or reads the contents of secret locations.
package macfacts
