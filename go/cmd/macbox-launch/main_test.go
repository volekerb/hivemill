package main

import (
	"testing"

	"factory/go/internal/macbox"
)

func TestRefuseUid(t *testing.T) {
	old := macbox.LauncherUID
	defer func() { macbox.LauncherUID = old }()
	macbox.LauncherUID = func() int { return 0 }
	if code := run([]string{"macbox-launch", "--", "/bin/true"}, []string{"MACBOX_OWNER_UID=501"}); code != 3 {
		t.Fatalf("root uid exit %d want 3", code)
	}
	macbox.LauncherUID = func() int { return 501 }
	if code := run([]string{"macbox-launch", "--", "/bin/true"}, []string{"MACBOX_OWNER_UID=501"}); code != 3 {
		t.Fatalf("owner uid exit %d want 3", code)
	}
	macbox.LauncherUID = func() int { return 502 }
	if code := run([]string{"macbox-launch"}, []string{"MACBOX_OWNER_UID=501"}); code != 2 {
		t.Fatalf("missing -- exit %d want 2", code)
	}
	if code := run([]string{"macbox-launch", "--", "/bin/true"}, nil); code != 3 {
		t.Fatalf("missing owner uid exit %d want 3", code)
	}
}