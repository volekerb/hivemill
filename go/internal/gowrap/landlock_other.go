//go:build !linux

package gowrap

import "errors"

// No Landlock off Linux: the ABI probe says unavailable, so Run refuses (fails closed).
func landlockABI() (int, error) { return 0, nil }

func landlockApply(int, []string, []string) error { return errors.New("landlock needs linux") }
