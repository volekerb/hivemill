package guard

import "testing"

// T-0983 attempt 3: MarkPunctWords turns a quoted or escaped punctuation-only word into a sentinel carrying its value.
func TestMarkPunctWords(t *testing.T) {
	cases := []struct {
		in    string
		vals  []string
		plain bool // the text comes back unchanged
	}{
		{`find x -exec ls {} \; -delete`, []string{";"}, false},
		{`find x -exec ls {} ';' -delete`, []string{";"}, false},
		{`find x -exec ls {} ";" -delete`, []string{";"}, false},
		{`find x -exec ls {} \;"" -delete`, []string{";"}, false},
		{`find x -exec ls {} ""\; -delete`, []string{";"}, false},
		{`find x \( -name a \) -delete`, []string{"(", ")"}, false},
		{`find x -name '&' -name '|' -name '>'`, []string{"&", "|", ">"}, false},
		{`find x -exec ls {} ; -delete`, nil, true}, // an unquoted `;` stays a separator
		{`echo a;b`, nil, true},                     // an unquoted punctuation word is not marked
		{`echo ';'>x`, nil, true},                   // an unquoted `>` in the same word: left to shlex
		{`echo $';'`, nil, true},                    // value is `$;`, not only punctuation
		{`echo ''`, nil, true},                      // empty value
		{`echo "a;"`, nil, true},                    // other characters in the value
	}
	for _, c := range cases {
		out, vals := MarkPunctWords(c.in)
		if c.plain && (out != c.in || len(vals) != 0) {
			t.Errorf("%q: want unchanged, got %q %v", c.in, out, vals)
		}
		if len(vals) != len(c.vals) {
			t.Errorf("%q: want %v, got %v", c.in, c.vals, vals)
			continue
		}
		i := 0
		for _, v := range orderedVals(out, vals) {
			if v != c.vals[i] {
				t.Errorf("%q: value %d is %q, want %q", c.in, i, v, c.vals[i])
			}
			i++
		}
	}
}

// orderedVals lists the values in the order their sentinels appear in out.
func orderedVals(out string, vals map[string]string) []string {
	var res []string
	for n := 0; n < len(vals); n++ {
		for k, v := range vals {
			if len(k) > 2 && k[len(k)-3] == byte('0'+n) && k[len(k)-2:] == "__" {
				res = append(res, v)
			}
		}
	}
	return res
}

// A word that already holds the default sentinel text gets a longer one, so no user word can collide with it.
func TestMarkPunctWordsSentinelNeverCollides(t *testing.T) {
	out, vals := MarkPunctWords(`echo __PUNCT_WORD__0__ ';'`)
	if len(vals) != 1 {
		t.Fatalf("want 1 marked word, got %v", vals)
	}
	for k := range vals {
		if k == "__PUNCT_WORD__0__" || out == `echo __PUNCT_WORD__0__ ';'` {
			t.Errorf("sentinel %q collides with the text %q", k, out)
		}
	}
}

// An unresolvable name is the same for Python and Go: tildeExpand leaves it alone, tildeUnres says so.
func TestTildeUnres(t *testing.T) {
	for _, w := range []string{"~nosuchuser123", "~nosuchuser123/.local/bin/git", "~-", "~-/x"} {
		if !tildeUnres(w, true) || tildeExpand(w, "/work", true) != w {
			t.Errorf("%q: want unresolved and unchanged, got unres=%v expand=%q", w, tildeUnres(w, true), tildeExpand(w, "/work", true))
		}
	}
	if !tildeUnres("~+/x", false) {
		t.Error("~+ with an unknown cwd must be unresolved")
	}
	for _, w := range []string{"~", "~/x", "$HOME/x", "~+/x", "/tmp/x", "x~y", "~root"} {
		if tildeUnres(w, true) {
			t.Errorf("%q: want resolvable", w)
		}
	}
}
