//go:build !linux && !darwin

package procx

import "errors"

// Unsupported platforms answer Unknown / error, never Pass.
func CwdInside(dir string) Result   { return unknown("unsupported OS") }
func HeldOpen(path string) Result   { return unknown("unsupported OS") }
func Processes() ([]Process, error) { return nil, errors.New("unsupported OS") }
