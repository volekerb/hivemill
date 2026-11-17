package sandbox

import "fmt"

// unsupportedOS is the refusal every non-Linux stub returns: the namespace sandbox needs Linux, so nothing runs.
func unsupportedOS(goos string) error {
	return fmt.Errorf("%w: the Linux namespace sandbox does not run on %s (macOS workers use go/internal/macbox)", ErrSandboxRefused, goos)
}
