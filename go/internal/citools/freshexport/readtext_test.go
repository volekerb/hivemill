package freshexport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTextLikePython(t *testing.T) {
	dir := t.TempDir()
	for in, want := range map[string]string{"a\r\nb\rc\n": "a\nb\nc\n", "é": "é", "": ""} {
		p := filepath.Join(dir, "f")
		if err := os.WriteFile(p, []byte(in), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadText(p); err != nil || got != want {
			t.Errorf("ReadText(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	p := filepath.Join(dir, "bad")
	if err := os.WriteFile(p, []byte("a\xffb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadText(p); err == nil {
		t.Error("invalid UTF-8 was accepted")
	}
}
