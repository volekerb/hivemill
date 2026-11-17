package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestUnsupportedOSRefuses(t *testing.T) {
	err := unsupportedOS("darwin")
	if !errors.Is(err, ErrSandboxRefused) {
		t.Fatalf("want ErrSandboxRefused, got %v", err)
	}
	for _, want := range []string{"darwin", "macbox"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
}
