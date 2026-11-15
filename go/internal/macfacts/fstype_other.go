//go:build !darwin

package macfacts

import "errors"

// StatfsType is only meaningful on macOS.
func StatfsType(path string) (string, error) {
	return "", errors.New("statfs fstype name is only read on darwin")
}
