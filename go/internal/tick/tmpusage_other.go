//go:build !linux

package tick

import "errors"

// statvfs fails closed off Linux: the /tmp watch reads "unknown" (tmp_usage None) and raises no alert.
func statvfs(path string) (total, free int64, err error) {
	return 0, 0, errors.New("statvfs is only ported for linux")
}
