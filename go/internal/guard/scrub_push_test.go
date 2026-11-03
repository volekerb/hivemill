package guard

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// T-0887: checkPush (the pushed range: messages, identities, path names, added lines, image metadata) against
// ci/public_text_guard.py on a repo that holds every case of the checker.

func pngChunk(name string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(name)
	b.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(name), data...)))
	return b.Bytes()
}

func zlibBytes(s string) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func pngWith(chunks ...[]byte) []byte {
	out := append([]byte{}, pngMagic...)
	out = append(out, pngChunk("IHDR", make([]byte, 13))...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return append(out, pngChunk("IEND", nil)...)
}

func putFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPushCheckMatchesPythonChecker(t *testing.T) {
	file, _ := corpusTerms(t)
	t.Setenv(termsVar, file)
	ptHermeticGit(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := func(args ...string) string { return runGit(t, dir, args...) }
	commit := func(msg string, extra ...string) {
		g(append(append([]string{}, extra...), "add", "-A")...)
		g(append(append([]string{}, extra...), "commit", "-q", "--allow-empty", "-m", msg)...)
	}
	home := "/ho" + "me/"
	ghp := "gh" + "p_" + strings.Repeat("A1b2", 9)
	g("init", "-q", "-b", "main")
	putFile(t, filepath.Join(dir, "README.md"), []byte("# project\n"))
	commit("first")
	g("tag", "base")
	// clean content and message
	putFile(t, filepath.Join(dir, "src", "a.go"), []byte("package a\n\nfunc A() int { return 1 }\n"))
	commit("add a.go")
	// owner term, address and token in added lines; CRLF; a removed line is not scanned
	putFile(t, filepath.Join(dir, "notes.txt"), []byte("line\r\nabout zebracorp here\r\nhost "+dot("10", "1", "2", "3")+"\nkey "+ghp+"\n"))
	commit("add notes")
	putFile(t, filepath.Join(dir, "notes.txt"), []byte("line\n"))
	commit("trim notes")
	// the allowlisted shape of the real allowlist file (path + pattern id), and the same line at another path
	putFile(t, filepath.Join(dir, "docs", "demo-data.md"), []byte("<dir>"+home+".claude/x\n"))
	putFile(t, filepath.Join(dir, "docs", "other.md"), []byte("<dir>"+home+".claude/x\n"))
	commit("allowlisted and not")
	// path names: a term, spaces, quotes and non-ASCII (git quotes some)
	putFile(t, filepath.Join(dir, "zebracorp-plan.md"), []byte("fine\n"))
	putFile(t, filepath.Join(dir, "a b", "c d.txt"), []byte("fine\n"))
	putFile(t, filepath.Join(dir, "q\"uote.txt"), []byte("fine\n"))
	putFile(t, filepath.Join(dir, "tab\there.txt"), []byte("fine\n"))
	putFile(t, filepath.Join(dir, "é中.txt"), []byte("fine\n"))
	commit("paths")
	// images: metadata is scanned, pixel data is not; a corrupt image is scanned as text
	putFile(t, filepath.Join(dir, "img", "text.png"), pngWith(pngChunk("tEXt", []byte("Comment\x00about zebracorp"))))
	putFile(t, filepath.Join(dir, "img", "ztxt.png"), pngWith(pngChunk("zTXt", append([]byte("Comment\x00\x00"), zlibBytes("by zebracorp")...))))
	putFile(t, filepath.Join(dir, "img", "itxt.png"), pngWith(pngChunk("iTXt", append([]byte("Comment\x00\x01\x00\x00\x00"), zlibBytes("by zebracorp")...))))
	putFile(t, filepath.Join(dir, "img", "clean.png"), pngWith(pngChunk("IDAT", zlibBytes("zebracorp in pixels")), pngChunk("tEXt", []byte("Comment\x00fine"))))
	putFile(t, filepath.Join(dir, "img", "bad.png"), append(pngWith(pngChunk("tEXt", []byte("Comment\x00fine"))), []byte("trailing zebracorp")...))
	putFile(t, filepath.Join(dir, "img", "unknown.png"), pngWith(pngChunk("prVt", []byte("zebracorp"))))
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xfe}, 0, 15)
	jpeg = append(append(jpeg, []byte("by zebracorp")...), 0xff, 0xd9)
	putFile(t, filepath.Join(dir, "img", "c.jpg"), jpeg)
	gif := append([]byte("GIF89a"), 1, 0, 1, 0, 0, 0, 0)
	gif = append(gif, 0x21, 0xfe, 12)
	gif = append(append(gif, []byte("by zebracorp")...), 0, 0x3b)
	putFile(t, filepath.Join(dir, "img", "c.gif"), gif)
	exif := []byte("by zebracorp")
	webp := append([]byte("WEBP"), []byte("EXIF")...)
	webp = binary.LittleEndian.AppendUint32(webp, uint32(len(exif)))
	webp = append(webp, exif...)
	riff := binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(webp)))
	putFile(t, filepath.Join(dir, "img", "c.webp"), append(riff, webp...))
	putFile(t, filepath.Join(dir, "bin", "blob.bin"), []byte("\x00\x01 zebracorp \x02"))
	commit("images")
	// identity and message of a commit
	putFile(t, filepath.Join(dir, "x.txt"), []byte("x\n"))
	g("add", "-A")
	g("-c", "user.name=Alice Real", "-c", "user.email=alice@"+"realmail.test", "commit", "-q", "-m", "msg with "+dot("192", "168", "0", "9")+" and Acme.")
	// a merge: what the side branch brings is reported once, what main brings in is not added by the merge
	g("checkout", "-q", "-b", "side", "base")
	putFile(t, filepath.Join(dir, "side.txt"), []byte("side zebracorp\n"))
	commit("side work")
	g("checkout", "-q", "main")
	g("merge", "-q", "--no-ff", "-m", "merge side", "side")
	// a deleted file and a mode change only
	g("rm", "-q", "notes.txt")
	commit("remove notes")
	head := strings.TrimSpace(g("rev-parse", "HEAD"))
	base := strings.TrimSpace(g("rev-parse", "base"))
	cases := [][3]string{{dir, base, head}, {dir, base, "HEAD~3"}, {dir, head, head}, {dir, base, "side"}, {dir, "-x", head}, {dir, "nosuchrev", head}, {filepath.Join(dir, "missing"), base, head}}
	res := runPyChecker(t, file, nil, cases)
	loaded, err := loadTermsOrFail("")
	if err != nil {
		t.Fatal(err)
	}
	sc := newScrubScanner(loaded)
	allow := "../../../ci/scrub_allowlist.txt"
	hitCases := 0
	for i, c := range cases {
		ids, gerr := checkPush(c[0], c[1], c[2], sc, allow)
		var want any
		if err := json.Unmarshal(res.Pushes[i], &want); err != nil {
			t.Fatal(err)
		}
		var got any = stringSlice(ids)
		if gerr != nil {
			got = "usage: " + gerr.Error()
		}
		if gs, ok := got.([]any); ok && len(gs) > 0 {
			hitCases++
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("push %v: go %v, python %v", c[1:], got, want)
		}
	}
	if hitCases < 3 {
		t.Errorf("only %d push ranges had hits: the repo does not exercise the checker", hitCases)
	}
}

func stringSlice(ids []string) []any {
	out := make([]any, len(ids))
	for i, s := range ids {
		out[i] = s
	}
	return out
}
