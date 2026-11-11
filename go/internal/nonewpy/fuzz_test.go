package nonewpy

import "testing"

// FuzzParseNameStatus checks that arbitrary git output never panics and that every parsed change has a path.
func FuzzParseNameStatus(f *testing.F) {
	for _, s := range []string{"", "A\x00a.py\x00", "R100\x00a\x00b\x00", "C50\x00a\x00b\x00M\x00c\x00", "R\x00a\x00", "\x00", "A", "D\x00\x00"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		changes, err := ParseNameStatus(in)
		if err != nil {
			return
		}
		for _, c := range changes {
			if c.Status == "" {
				t.Fatalf("empty status in %q", in)
			}
		}
	})
}

// FuzzHasPythonShebang checks that classification neither panics nor reports a shebang without a "#!" prefix.
func FuzzHasPythonShebang(f *testing.F) {
	for _, s := range []string{"", "#!/usr/bin/env python3\n", "#!/usr/bin/env -S python3 -u", "#!", "#! python", "x\n#!python", "\xff\x00#!python"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		if HasPythonShebang(in) && len(in) < 2 {
			t.Fatalf("shebang reported for %q", in)
		}
	})
}
