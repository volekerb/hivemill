package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestBrowserPin: the pinned browser constants equal the ones in ci/screenshots.py (read as text).
func TestBrowserPin(t *testing.T) {
	data, err := os.ReadFile("../../../ci/screenshots.py")
	if err != nil {
		t.Fatalf("cannot read ci/screenshots.py: %v", err)
	}
	grab := func(name string) string {
		m := regexp.MustCompile(`(?m)^` + name + ` = "?([0-9a-f.]+)"?$`).FindSubmatch(data)
		if m == nil {
			t.Fatalf("no %s constant in ci/screenshots.py", name)
		}
		return string(m[1])
	}
	if v := grab("VERSION"); v != browserVersion {
		t.Errorf("VERSION: screenshots.py %s, film-render %s", v, browserVersion)
	}
	if s := grab("SIZE"); s != strconv.Itoa(browserSize) {
		t.Errorf("SIZE: screenshots.py %s, film-render %d", s, browserSize)
	}
	if h := grab("SHA256"); h != browserSHA256 {
		t.Errorf("SHA256: screenshots.py %s, film-render %s", h, browserSHA256)
	}
}

func TestVerifyZipRefusesBadArchive(t *testing.T) {
	p := t.TempDir() + "/x.zip"
	if err := os.WriteFile(p, []byte("not the pinned archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyZip(p)
	ee, ok := err.(*exitError)
	if !ok || ee.code != 3 {
		t.Fatalf("a wrong-size zip must be refused with exit 3, got %v", err)
	}
}
