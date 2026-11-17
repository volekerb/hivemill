package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRefusesOffMacOS(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb, "linux", 0); code != 2 || strings.TrimSpace(errb.String()) != "ci-freshbox-darwin: macOS only" {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestRefusesNonRoot(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb, "darwin", 501); code != 2 || !strings.Contains(errb.String(), "must run as root") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}
