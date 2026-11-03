package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// T-0887 differential test: the Go scanner and push checker give the same hits as ci/public_text_guard.py (the
// Python hook's checker) on a corpus of clean and term-hit commit messages, PR bodies and diffs, with case and
// Unicode variants. Private-looking values are built at run time so this file holds none.

const pyChecker = `
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("ptg", sys.argv[1] + "/ci/public_text_guard.py")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
req = json.load(sys.stdin)
terms = mod.load_terms_or_fail({"` + termsVar + `": req["terms"]})
out = {"texts": [[h[0] for h in mod.check_text(t, terms)] for t in (req.get("texts") or [])], "pushes": []}
for repo, base, head in (req.get("pushes") or []):
    try:
        out["pushes"].append([h[0] for h in mod.check_push(repo, base, head, terms)])
    except mod.UsageError as e:
        out["pushes"].append("usage: " + str(e))
json.dump(out, sys.stdout)
`

type pyResult struct {
	Texts  [][]string        `json:"texts"`
	Pushes []json.RawMessage `json:"pushes"`
}

func runPyChecker(t *testing.T, terms string, texts []string, pushes [][3]string) pyResult {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	req, err := json.Marshal(map[string]any{"terms": terms, "texts": texts, "pushes": pushes})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-I", "-c", pyChecker, root)
	cmd.Stdin = bytes.NewReader(req)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python checker: %v\n%s", err, stderr.String())
	}
	var res pyResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("python checker output: %v\n%s", err, out)
	}
	return res
}

func dot(parts ...string) string { return strings.Join(parts, ".") }

func corpusTerms(t *testing.T) (file string, terms []string) {
	t.Helper()
	terms = []string{"zebracorp", "Acme", "straße", "Istanbul", "kayak", "ΣΑΣ", "ab", "mixed Case Name"}
	file = filepath.Join(t.TempDir(), "terms.txt")
	if err := os.WriteFile(file, []byte(strings.Join(terms, "\n")+"\n \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file, terms
}

// textCorpus is the clean and hit texts: owner-term variants, every pattern id, boundaries, Unicode.
func textCorpus() []string {
	user, ghp := "a"+"lice", "gh"+"p_"+strings.Repeat("A1b2", 9)
	tk, rw, uu := "to"+"ken", "rail"+"way", "0123abcd"+"-0123-4abc-8abc-0123456789ab"
	UU, v22 := strings.ToUpper(uu), strings.Repeat("ab", 11)
	hex := strings.Repeat("a1b2c3d4", 6)
	home := "/ho" + "me/"
	var c []string
	add := func(s ...string) { c = append(c, s...) }
	add("", "clean text about T-0528 and PR #3", "Fixes the thing.\n\nSecond line.", "line one\r\nline two\rline three\x0bline four\x1cfive six")
	// owner terms: case, whole-word terms, substring terms, Unicode folding (U+0130, U+0131, U+017F, U+212A)
	add("zebracorp", "ZEBRACORP", "ZebraCorp.", "xzebracorpx", "no zebra corp here", "ZEBRACORP zebracorp", "zebracorpzebracorp",
		"acme", "ACME corp", "acmeinc", "an Acme.", "_acme_", "acme1", "Acme-Inc", "straße", "STRASSE", "STRAßE", "strasse", "istanbul", "ISTANBUL",
		"İstanbul", "ıstanbul", "IstanbuI", "kayak", "KAYAK", "kaſak", "KAYAK", "ΣΑΣ", "σας", "σασ", "ςας",
		"ab", "ab cd", "cab", "a b", "AB.", "(ab)", "abé", "mixed case name", "MIXED   CASE NAME", "Mixed Case Name", "mixed\tCase Name")
	// the generic shapes
	add("mail "+user+"@"+"realmail.test please", "from "+user+"@"+"example.com", "x@"+"example.org y", "noreply@"+"github.com", "NOREPLY@"+"anthropic.com",
		"12345+bob@"+"users.noreply.github.com", "bob@"+"users.noreply.github.com", "systemd-user@"+"1000.service", "a@"+"b", "a@"+"b.c", "u@"+"host.name.with.many.labels.in.a.row.x.y.z.w",
		"first.last+tag@"+"sub.domain.example.net.", "x"+"@"+"éxample.com")
	add(home+user+"/notes", home+"user/notes", home+"Runner/x", home+"<name>/x", home+"example/", "see "+home+user, home+"a b/c", home+"é/x/")
	add(dot("192", "168", "1", "4"), "host "+dot("10", "0", "0", "5")+" up", dot("172", "16", "0", "1"), dot("172", "31", "255", "255"), dot("172", "32", "0", "1"),
		dot("172", "015", "0", "1"), dot("100", "64", "1", "1"), dot("100", "127", "1", "1"), dot("100", "128", "1", "1"), dot("11", "0", "0", "1"), "v"+dot("1", "2", "3", "4", "5"),
		dot("1", "2", "3", "4"), dot("8", "8", "8", "8"), dot("127", "0", "0", "1"), dot("0", "0", "0", "0"), dot("255", "255", "255", "255"), dot("192", "0", "2", "7"),
		dot("198", "51", "100", "9"), dot("203", "0", "113", "200"), dot("010", "1", "1", "1"), dot("1", "02", "3", "4"), dot("256", "1", "1", "1"), dot("10", "0", "0", "5")+".",
		dot("10", "0", "0", "5")+".7", "1"+dot("10", "0", "0", "5"), dot("10", "0", "0", "5")+"1", "a"+dot("10", "0", "0", "5"), "("+dot("192", "168", "0", "1")+")",
		"１０."+dot("０", "０", "５"), dot("١٠", "٠", "٠", "٥"), dot("1", "2", "3", "4")+"/24", dot("2", "3", "4"),
		dot("192", "168", "1", "1")+":8080", "ip="+dot("172", "20", "1", "9")+", next")
	lo, lan, in, ts, arpa := ".lo"+"cal", ".l"+"an", ".in"+"ternal", ".ts"+".net", ".home"+".arpa"
	add("srv"+lo, "SRV"+strings.ToUpper(lan), "db"+in, "x.y.z"+ts, "router"+arpa, "a.b.c.d.e.f.g.h.i"+lo, "a.b.c.d.e.f.g.h.i.j"+lo, "foo"+lo+".x", "foo"+lo+".", "foo"+lo+".bar",
		"foo"+lo+"-x", "foo"+lo+"_", "-foo"+lo, "foo.\u0130nternal", "foo.\u0131nternal", "foo"+lan+"\u212a", "foo"+lo+"\u00e9", "my.host"+lo+", ok", lo, "lo"+"cal", "a"+lo+lan)
	add(ghp, "x"+ghp, "é"+ghp, ghp+"zz", "gh"+"s_"+strings.Repeat("Z9", 20), "github"+"_pat_"+strings.Repeat("a1", 15), "sk-"+"ant-api03-"+strings.Repeat("Ab1_", 8),
		"AK"+"IA"+"ABCDEFGHIJKLMNOP", "AK"+"IA"+"short", "-----BEGIN "+"RSA PRIVATE KEY-----", "-----BEGIN "+"PRIVATE KEY-----", "-----BEGIN "+" PRIVATE KEY-----")
	add("RAILWAY_"+"TOKEN="+strings.Repeat("v1", 10), "RAILWAY_"+"API_TOKEN: \""+strings.Repeat("x9", 10)+"\"", "RAILWAY_"+"TOKEN = <your token here ok>", "RAILWAY_"+"TOKEN=$VAR_NAME_THAT_IS_LONG",
		"RAILWAY_"+"TOKEN=changemechangemechangeme", "RAILWAY_"+"TOKENé="+v22, "xRAILWAY_"+"TOKEN="+v22,
		rw+" id "+uu, "id "+UU+" RAILWAY", "id "+uu, "RA\u0130LWAY "+uu,
		"x"+uu+" "+rw, uu+"x "+rw)
	add("client_"+"secret="+strings.Repeat("Ab3", 8), "REFRESH_"+"TOKEN: '"+strings.Repeat("a1", 12)+"'", "access_"+"token\" : \""+strings.Repeat("q", 20)+"\"", "CLİENT_"+"SECRET="+strings.Repeat("q9", 12),
		"client_"+"secret="+strings.Repeat("a", 19), "xclient_"+"secret="+strings.Repeat("a", 22), "client_"+"secrets="+strings.Repeat("a", 22), "clıent_"+"secret="+strings.Repeat("a", 22))
	add("pass"+"word="+strings.Repeat("Zx9", 6), "to"+"ken: "+strings.Repeat("a1b2", 5), "api_"+"key = \""+strings.Repeat("k7", 10)+"\"", "API-"+"KEY="+strings.Repeat("k7", 10), "private-"+"key: "+strings.Repeat("e", 20),
		"sec"+"ret=changemechangeme", "sec"+"ret="+strings.Repeat("a", 10), "sec"+"ret = api.session_tokens_value_long", "sec"+"ret="+"<redacted-secret-value>", "sec"+"ret=$ENV_VAR_VALUE_LONG_X",
		"to"+"ken=testtesttesttesttest", "to"+"ken=exa"+"ſ"+"mple_value_long_enough", "pass"+"wd=abc.def.ghi.jkl.mno.pqr", "PASSW"+"ORD:"+strings.Repeat("z", 16), "sec"+"ret=é"+strings.Repeat("z", 16),
		"apİ_"+"key="+strings.Repeat("k7", 10), "ſecret="+strings.Repeat("k7", 10), "prıvate_"+"key="+strings.Repeat("k7", 10), "Key token="+strings.Repeat("a", 18))
	add("secret: "+hex+"9", tk+"="+strings.Repeat("Ab1", 15), "see "+tk+" "+strings.Repeat("Ab1", 15), "x"+strings.Repeat("Ab1", 15), "the key in /path/"+strings.Repeat("Ab1", 15)+" is",
		"token=path/"+strings.Repeat("Ab1", 12), tk+" = /"+strings.Repeat("Ab1", 15), strings.Repeat("Ab1", 15), tk+"="+strings.Repeat("Ab", 20), tk+"="+strings.Repeat("Ab1", 15)+"==",
		tk+"="+strings.Repeat("Ab1", 15)+"===x", tk+"="+strings.Repeat("Ab1", 15)+"=a", tk+"="+strings.Repeat("a", 64), tk+"="+strings.Repeat("a1", 32), "sha256 "+strings.Repeat("0f", 32),
		tk+"="+strings.Repeat("test1", 9), "a "+strings.Repeat("x", 99)+" "+tk+" "+strings.Repeat("Ab1", 15), strings.Repeat("é", 120)+tk+"="+strings.Repeat("Ab1", 15))
	// long lines and mixtures
	add(strings.Repeat("A clean line about T-0528 and PR #3.\n", 40), strings.Repeat("a", 5000), strings.Repeat("a@", 300), strings.Repeat("1.", 400),
		"first\nzebracorp\nthird "+dot("10", "1", "1", "1")+"\n"+ghp, "\u0000nul\u0000", "\xef\xbb\xbfzebracorp", "zebra​corp", "é acme")
	return c
}

func TestScannerMatchesPythonChecker(t *testing.T) {
	file, terms := corpusTerms(t)
	t.Setenv(termsVar, file)
	corpus := textCorpus()
	res := runPyChecker(t, file, corpus, nil)
	if len(res.Texts) != len(corpus) {
		t.Fatalf("python returned %d results for %d texts", len(res.Texts), len(corpus))
	}
	loaded, err := loadTermsOrFail("")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(terms) {
		t.Fatalf("loaded %d terms, want %d", len(loaded), len(terms))
	}
	sc := newScrubScanner(loaded)
	hits := 0
	for i, text := range corpus {
		got, want := sc.scanLines(text), res.Texts[i]
		if len(want) > 0 {
			hits++
		}
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("text %d %q: go hits %v, python hits %v", i, shorten(text), got, want)
		}
	}
	if hits < 80 || hits == len(corpus) {
		t.Errorf("the corpus must hold both hits and clean texts: %d of %d texts hit", hits, len(corpus))
	}
}

func shorten(s string) string {
	if len(s) > 80 {
		return fmt.Sprintf("%s...(%d bytes)", s[:80], len(s))
	}
	return s
}

// The same comparison on fixed-seed mixes of the corpus: pieces joined by separators that sit on pattern boundaries,
// with single characters inserted where a lookaround or a Unicode rule decides.
func TestScannerMatchesPythonOnMixes(t *testing.T) {
	file, _ := corpusTerms(t)
	t.Setenv(termsVar, file)
	base := textCorpus()
	rnd := rand.New(rand.NewSource(20261103))
	seps := []string{" ", "\n", "=", ":", ".", "\"", "-", "/", "é", "1", "_", "@", "", "İ", "١", "K", "ſ", "'", "\t", " "}
	var mixes []string
	for i := 0; i < 2500; i++ {
		var b strings.Builder
		for n := 1 + rnd.Intn(4); n > 0; n-- {
			b.WriteString(base[rnd.Intn(len(base))])
			b.WriteString(seps[rnd.Intn(len(seps))])
		}
		s := []rune(b.String())
		if len(s) > 0 && rnd.Intn(2) == 0 {
			at := rnd.Intn(len(s))
			s = append(s[:at], append([]rune(seps[rnd.Intn(len(seps))]), s[at:]...)...)
		}
		mixes = append(mixes, string(s))
	}
	res := runPyChecker(t, file, mixes, nil)
	loaded, err := loadTermsOrFail("")
	if err != nil {
		t.Fatal(err)
	}
	sc := newScrubScanner(loaded)
	bad := 0
	for i, text := range mixes {
		got, want := sc.scanLines(text), res.Texts[i]
		if (len(got) != 0 || len(want) != 0) && !reflect.DeepEqual(got, want) {
			if bad++; bad <= 10 {
				t.Errorf("mix %d %q: go hits %v, python hits %v", i, shorten(text), got, want)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d mixes differ", bad, len(mixes))
	}
}
