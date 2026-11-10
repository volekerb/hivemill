package guard

import "testing"

func TestNormalizePushURL(t *testing.T) {
	bare := "https://github.com/acme/widget.git"
	other := "https://github.com/other/repo.git"
	for _, tc := range []struct {
		in, want string
	}{
		{bare, bare},
		{bare + "/", bare},
		{bare + "//", bare},
		{bare + "///", bare},
		{other, other},
	} {
		if got := normalizePushURL(tc.in); got != tc.want {
			t.Errorf("normalizePushURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if normalizePushURL(other) == normalizePushURL(bare) {
		t.Errorf("normalizePushURL(%q) must not equal normalizePushURL(%q)", other, bare)
	}
}

func TestReLiveRemoteCfgUnicodeRemoteName(t *testing.T) {
	for _, s := range []string{
		"remote.ünicode.url=",
		"remote.café.url=",
		"REMOTE.Ünicode.URL=",
	} {
		if !reLiveRemoteCfg.MatchString(s) {
			t.Errorf("reLiveRemoteCfg.MatchString(%q) = false, want true", s)
		}
	}
}

func TestRePushURLSchemeUnicode(t *testing.T) {
	for _, s := range []string{"ünicode://push", "café://x"} {
		if !rePushURLScheme.MatchString(s) {
			t.Errorf("rePushURLScheme.MatchString(%q) = false, want true", s)
		}
	}
}

func TestFactoryURLShapeOK(t *testing.T) {
	accept := []string{
		"https://github.com/.owner/repo.git",
		"https://github.com/owner/.github.git",
		"https://github.com/owner/.git.git",
		"https://github.com/.../repo.git",
	}
	reject := []string{
		"https://github.com/./repo.git",
		"https://github.com/../repo.git",
		"https://github.com/owner/..git.git",
		"https://github.com/owner/...git.git",
	}
	for _, u := range accept {
		if !factoryURLShapeOK(u) {
			t.Errorf("factoryURLShapeOK(%q) = false, want true", u)
		}
	}
	for _, u := range reject {
		if factoryURLShapeOK(u) {
			t.Errorf("factoryURLShapeOK(%q) = true, want false", u)
		}
	}
}
