package guard

import "testing"

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
