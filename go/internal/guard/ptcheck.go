package guard

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Port of the git-facing half of ci/public_text_guard.py (check_push and the image payload readers) and of the
// allowlist of ci/scrub_scan.py (T-0887). Like scrub.go it returns only the pattern id of each hit.

func ptGitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
}

// ptGit is public_text_guard._git: git output, or a usageError that names the failure.
func ptGit(repo string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo, "-c", "core.quotepath=off", "-c", "i18n.logOutputEncoding=UTF-8", "-c", "log.showSignature=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = ptGitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil, &usageError{"git " + args[0] + " failed in " + pyRepr(repo) + ": " + strings.TrimFunc(pyDecodeReplace(stderr.Bytes()), pyIsSpace) +
			". Pass --repo DIR inside a git repository and BASE/HEAD that exist there."}
	}
	return nil, &usageError{"git not found on PATH"}
}

// ───────────────────────────── allowlist ─────────────────────────────

type allowEntry struct {
	path string
	pid  string // "" for the legacy path:regex form
	re   *regexp.Regexp
}

type scrubAllowlist []allowEntry

var (
	allowPatternIDs = set("home-path", "rfc1918", "cgnat", "email", "github-token", "github-pat", "anthropic-key",
		"aws-access-key-id", "private-key", "railway-token", "oauth-secret", "generic-secret", "high-entropy-id",
		idOwnerTerm, "env-file")
	legacyOK = set("home-path", "rfc1918", "email")
)

// loadAllowlist is scrub_scan.Allowlist.load.
func loadAllowlist(path string) (scrubAllowlist, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &usageError{"cannot read allowlist " + pyRepr(path) + ": " + pyStrerror(err)}
	}
	if msg := pyUTF8Error(raw); msg != "" {
		// read_text(encoding="utf-8") raises UnicodeDecodeError, which is no UsageError: the hook fails closed
		return nil, errors.New("UnicodeDecodeError: " + msg)
	}
	var entries scrubAllowlist
	for n, line := range pyLines(string(raw)) {
		n++
		if strings.TrimFunc(line, pyIsSpace) == "" || strings.HasPrefix(strings.TrimLeftFunc(line, pyIsSpace), "#") {
			continue
		}
		at := strings.LastIndex(line, " # ")
		if at < 0 || strings.TrimFunc(line[at+3:], pyIsSpace) == "" {
			return nil, &usageError{path + ":" + strconv.Itoa(n) + ": allowlist line needs a '  # reason' comment. See docs/scrub-scan.md."}
		}
		body := line[:at]
		apath, rest, found := strings.Cut(body, ":")
		if !found || strings.TrimFunc(apath, pyIsSpace) == "" || strings.TrimFunc(rest, pyIsSpace) == "" {
			return nil, &usageError{path + ":" + strconv.Itoa(n) + ": expected 'path:pattern-id:regex  # reason'. See docs/scrub-scan.md."}
		}
		rest = strings.TrimFunc(rest, pyIsSpace)
		pid, rxs, colon := strings.Cut(rest, ":")
		if colon && in(allowPatternIDs, pid) {
			if pid == idOwnerTerm {
				return nil, &usageError{path + ":" + strconv.Itoa(n) + ": owner-term hits can never be allowlisted. Remove this line."}
			}
			if strings.TrimFunc(rxs, pyIsSpace) == "" {
				return nil, &usageError{path + ":" + strconv.Itoa(n) + ": expected 'path:pattern-id:regex  # reason'. See docs/scrub-scan.md."}
			}
		} else {
			pid, rxs = "", rest
		}
		src, err := pyRegexToRE2(strings.TrimFunc(rxs, pyIsSpace))
		var re *regexp.Regexp
		if err == nil {
			re, err = regexp.Compile(src)
		}
		if err != nil {
			return nil, &usageError{path + ":" + strconv.Itoa(n) + ": bad regex: " + err.Error()}
		}
		entries = append(entries, allowEntry{strings.TrimFunc(apath, pyIsSpace), pid, re})
	}
	return entries, nil
}

// allows is Allowlist.allows(names, line, pid) for the one name of an added line.
func (a scrubAllowlist) allows(name, line, pid string) bool {
	if pid == idOwnerTerm {
		return false
	}
	hit := false
	for _, e := range a {
		if e.path == name && e.re.MatchString(line) && (e.pid == pid || (e.pid == "" && in(legacyOK, pid))) {
			hit = true
		}
	}
	return hit
}

// ───────────────────────────── diff header paths ─────────────────────────────

var gitEscapes = map[byte]byte{'a': 7, 'b': 8, 'f': 12, 'n': 10, 'r': 13, 't': 9, 'v': 11, '\\': 92, '"': 34}

const errQuotedPath = "cannot parse a quoted path in the git diff header. Report this to the scanner owner."

// cUnquote is scrub_scan.c_unquote.
func cUnquote(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return nil, &usageError{errQuotedPath}
	}
	inner := data[1 : len(data)-1]
	var out []byte
	for i := 0; i < len(inner); {
		c := inner[i]
		i++
		switch {
		case c != '\\':
			out = append(out, c)
		case i < len(inner) && inner[i] >= '0' && inner[i] <= '7':
			j := i
			for j < len(inner) && j < i+3 && inner[j] >= '0' && inner[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseInt(string(inner[i:j]), 8, 32)
			out = append(out, byte(v&0xff))
			i = j
		default:
			e, ok := byte(0), false
			if i < len(inner) {
				e, ok = gitEscapes[inner[i]]
			}
			if !ok {
				return nil, &usageError{errQuotedPath}
			}
			out = append(out, e)
			i++
		}
	}
	return out, nil
}

// diffHeaderPath is scrub_scan.diff_header_path.
func diffHeaderPath(line []byte) ([]byte, error) {
	rest := line[len("diff --git "):]
	if len(rest) > 0 && rest[0] == '"' {
		i := 1
		for i < len(rest) && rest[i] != '"' {
			if rest[i] == '\\' {
				i += 2
			} else {
				i++
			}
		}
		end := i + 1
		if end > len(rest) {
			end = len(rest)
		}
		first, err := cUnquote(rest[:end])
		if err != nil {
			return nil, err
		}
		second := bytes.TrimLeft(rest[min(end, len(rest)):], " ")
		if bytes.HasPrefix(first, []byte("a/")) && bytes.HasPrefix(second, []byte{'"'}) {
			u, err := cUnquote(second)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(u, append([]byte("b/"), first[2:]...)) {
				return first[2:], nil
			}
		}
	} else if bytes.HasPrefix(rest, []byte("a/")) {
		rest = rest[2:]
		n := 0
		if len(rest) > 3 {
			n = (len(rest) - 3) / 2
		}
		if n > 0 && string(rest[n:n+3]) == " b/" && bytes.Equal(rest[n+3:], rest[:n]) {
			return rest[:n], nil
		}
	}
	return nil, &usageError{"cannot parse a path from a git diff header. Report this to the scanner owner."}
}

// ───────────────────────────── image payloads ─────────────────────────────

type payload struct {
	part string
	data []byte
}

var (
	pngMagic = []byte("\x89PNG\r\n\x1a\n")
	pngPlain = set("IHDR", "PLTE", "IDAT", "IEND", "tRNS", "gAMA", "cHRM", "sRGB", "sBIT", "bKGD", "hIST", "pHYs", "tIME",
		"cICP", "mDCV", "cLLI", "acTL", "fcTL", "fdAT", "oFFs")
	pngText    = set("tEXt", "iTXt", "zTXt", "eXIf", "iCCP", "sPLT")
	webpPlain  = set("VP8 ", "VP8L", "VP8X", "ALPH", "ANIM", "ANMF")
	jpegStands = []byte{0x01, 0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7}
)

const inflateMax = 1 << 24

// inflate is public_text_guard._inflate: ok false for a stream that is truncated, corrupt or too large.
func inflate(b []byte) ([]byte, bool) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	out, err := io.ReadAll(io.LimitReader(r, inflateMax+1))
	if err != nil || len(out) > inflateMax {
		return nil, false
	}
	return out, true
}

// pngPayloads is _png_payloads: ok false where Python returns None.
func pngPayloads(blob []byte) ([]payload, bool) {
	pos, first := len(pngMagic), true
	var out []payload
	for pos+12 <= len(blob) {
		n := int(binary.BigEndian.Uint32(blob[pos : pos+4]))
		nameB := blob[pos+4 : pos+8]
		end := pos + 12 + n
		if end > len(blob) || crc32.ChecksumIEEE(blob[pos+4:pos+8+n]) != binary.BigEndian.Uint32(blob[end-4:end]) {
			return nil, false
		}
		data := blob[pos+8 : pos+8+n]
		name := string(nameB)
		if first != (name == "IHDR") {
			return nil, false
		}
		first = false
		switch {
		case in(pngText, name):
			out = append(out, payload{name, data})
			switch name {
			case "zTXt", "iCCP":
				_, after, ok := bytes.Cut(data, []byte{0})
				if !ok || len(after) < 1 {
					return nil, false
				}
				p, ok := inflate(after[1:])
				if !ok {
					return nil, false
				}
				out = append(out, payload{name, p})
			case "iTXt":
				_, rest, ok := bytes.Cut(data, []byte{0})
				if !ok || len(rest) == 0 {
					return nil, false
				}
				if rest[0] != 0 {
					if len(rest) < 2 {
						return nil, false
					}
					parts := bytes.SplitN(rest[2:], []byte{0}, 3)
					if len(parts) < 3 {
						return nil, false
					}
					p, ok := inflate(parts[2])
					if !ok {
						return nil, false
					}
					out = append(out, payload{name, p})
				}
			}
		case !in(pngPlain, name):
			return nil, false
		}
		pos = end
		if name == "IEND" {
			return out, pos == len(blob)
		}
	}
	return nil, false
}

// webpPayloads is _webp_payloads.
func webpPayloads(blob []byte) ([]payload, bool) {
	if len(blob) < 12 || string(blob[8:12]) != "WEBP" || int(binary.LittleEndian.Uint32(blob[4:8]))+8 != len(blob) {
		return nil, false
	}
	pos := 12
	var out []payload
	for pos+8 <= len(blob) {
		name := string(blob[pos : pos+4])
		n := int(binary.LittleEndian.Uint32(blob[pos+4 : pos+8]))
		if pos+8+n > len(blob) {
			return nil, false
		}
		if !in(webpPlain, name) {
			out = append(out, payload{name, blob[pos+8 : pos+8+n]})
		}
		pos += 8 + n + (n & 1)
	}
	return out, pos >= len(blob)
}

// gifSubBlocks is _gif_sub_blocks: the position after the terminator, -1 for none.
func gifSubBlocks(blob []byte, p int) (int, []byte) {
	var data []byte
	for p < len(blob) {
		n := int(blob[p])
		if n == 0 {
			return p + 1, data
		}
		end := min(p+1+n, len(blob))
		data = append(data, blob[min(p+1, len(blob)):end]...)
		p += 1 + n
	}
	return -1, data
}

// gifPayloads is _gif_payloads.
func gifPayloads(blob []byte) ([]payload, bool) {
	if len(blob) < 13 || (string(blob[:6]) != "GIF87a" && string(blob[:6]) != "GIF89a") {
		return nil, false
	}
	pos := 13
	var out []payload
	if blob[10]&0x80 != 0 {
		pos += 3 * (2 << (blob[10] & 7))
	}
	for pos < len(blob) {
		switch kind := blob[pos]; kind {
		case 0x3b:
			return out, pos+1 == len(blob)
		case 0x21:
			if pos+1 >= len(blob) {
				return nil, false
			}
			end, data := gifSubBlocks(blob, pos+2)
			if end < 0 {
				return nil, false
			}
			if !(blob[pos+1] == 0xf9 && len(data) == 4) { // all but a 4-byte graphic control is scanned
				out = append(out, payload{"extension", data})
			}
			pos = end
		case 0x2c:
			if pos+10 > len(blob) {
				return nil, false
			}
			flags := blob[pos+9]
			pos += 10
			if flags&0x80 != 0 {
				pos += 3 * (2 << (flags & 7))
			}
			end, _ := gifSubBlocks(blob, pos+1)
			if end < 0 {
				return nil, false
			}
			pos = end
		default:
			return nil, false
		}
	}
	return nil, false
}

// jpegPayloads is _jpeg_payloads.
func jpegPayloads(blob []byte) ([]payload, bool) {
	if len(blob) < 3 || blob[0] != 0xff || blob[1] != 0xd8 || blob[2] != 0xff {
		return nil, false
	}
	standalone := func(b byte) bool { return bytes.IndexByte(jpegStands, b) >= 0 }
	pos := 2
	var out []payload
	for pos+2 <= len(blob) {
		if blob[pos] != 0xff {
			return nil, false
		}
		marker := blob[pos+1]
		switch {
		case marker == 0xff:
			pos++
			continue
		case marker == 0xd9: // EOI must be the last thing in the file
			return out, pos+2 == len(blob)
		case standalone(marker):
			pos += 2
			continue
		}
		if pos+4 > len(blob) {
			return nil, false
		}
		n := int(binary.BigEndian.Uint16(blob[pos+2 : pos+4]))
		if n < 2 || pos+2+n > len(blob) {
			return nil, false
		}
		if marker == 0xfe || (marker >= 0xe1 && marker <= 0xef) { // comment and APP1..APP15 (EXIF, XMP, ...) carry text
			out = append(out, payload{"segment", blob[pos+4 : pos+2+n]})
		}
		pos += 2 + n
		if marker == 0xda { // entropy-coded data: skip to the next real marker (not stuffing, RSTn or fill)
			for pos+1 < len(blob) && !(blob[pos] == 0xff && blob[pos+1] != 0x00 && blob[pos+1] != 0xff && !(standalone(blob[pos+1]) && blob[pos+1] != 0x01)) {
				pos++
			}
		}
	}
	return nil, false
}

// imagePayloads is image_payloads: ok false when blob is not a verified image (scan it all).
func imagePayloads(blob []byte) ([]payload, bool) {
	switch {
	case bytes.HasPrefix(blob, pngMagic):
		return pngPayloads(blob)
	case bytes.HasPrefix(blob, []byte("RIFF")):
		return webpPayloads(blob)
	case bytes.HasPrefix(blob, []byte("GIF8")):
		return gifPayloads(blob)
	case bytes.HasPrefix(blob, []byte{0xff, 0xd8}):
		return jpegPayloads(blob)
	}
	return nil, false
}

// ───────────────────────────── check_push ─────────────────────────────

var (
	hunkRe   = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)`)
	ccHunkRe = regexp.MustCompile(`^(@@@+)(?: -[0-9]+(?:,[0-9]+)?)+ \+([0-9]+)`)
	logFlags = []string{"--root", "-p", "--text", "--no-textconv", "--no-ext-diff", "--no-renames", "--no-color", "--no-use-mailmap", "--src-prefix=a/", "--dst-prefix=b/"}
)

// ptBlob is public_text_guard._blob: the bytes of path at commit, ok false when they cannot be read.
func ptBlob(repo, commit string, path []byte) ([]byte, bool) {
	b, err := ptGit(repo, "cat-file", "blob", commit+":"+string(path))
	return b, err == nil
}

// checkPush is public_text_guard.check_push: the pattern ids of the hits in what pushing head publishes beyond base.
func checkPush(repo, base, head string, sc *scrubScanner, allowPath string) ([]string, error) {
	for _, rev := range []string{base, head} {
		if strings.HasPrefix(rev, "-") {
			return nil, &usageError{"revision " + pyRepr(rev) + " looks like an option. Pass a commit id or branch name."}
		}
	}
	allow, err := loadAllowlist(allowPath)
	if err != nil {
		return nil, err
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, &usageError{"cannot read random bytes for the git log marker: " + err.Error()}
	}
	mark := "\x01" + hex.EncodeToString(rnd[:]) + "\x01"
	format := "--format=" + mark + "H%H%n" + mark + "A%an <%ae>%n" + mark + "C%cn <%ce>%n" + mark + "M%n%B" + mark + "E"
	args := append([]string{"log", "--cc"}, logFlags...)
	args = append(args, base+".."+head, format, "--")
	out, err := ptGit(repo, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	full, state := "", "msg"
	var path []byte
	newline, width := 0, 1
	inHeader := false
	skip := -1 // -1: not decided for this file yet; 1: verified image, its added lines are not text
	for _, raw := range bytes.Split(out, []byte("\n")) {
		line := pyDecodeReplace(raw)
		switch {
		case strings.HasPrefix(line, mark+"H"):
			full, state, path, inHeader, skip = line[len(mark)+1:], "head", nil, false, -1
			continue
		case state == "head" && (strings.HasPrefix(line, mark+"A") || strings.HasPrefix(line, mark+"C")):
			sc.scanLine(line[len(mark)+1:], &ids)
			continue
		case state == "head" && strings.HasPrefix(line, mark+"M"):
			state = "msg"
			continue
		case state == "msg":
			if end := strings.Index(line, mark+"E"); end < 0 {
				sc.scanLine(line, &ids)
			} else {
				sc.scanLine(line[:end], &ids)
				state = "diff"
			}
			continue
		}
		if bytes.HasPrefix(raw, []byte("diff --git ")) || bytes.HasPrefix(raw, []byte("diff --cc ")) {
			if bytes.HasPrefix(raw, []byte("diff --cc ")) { // combined (merge) diff: one path, no a/ b/ prefix
				rest := raw[len("diff --cc "):]
				if len(rest) > 0 && rest[0] == '"' {
					p, err := cUnquote(rest)
					if err != nil {
						return nil, err
					}
					path = p
				} else {
					path = rest
				}
			} else {
				p, err := diffHeaderPath(raw)
				if err != nil {
					return nil, err
				}
				path = p
			}
			inHeader, skip = true, -1
			sc.scanLine(pyDecodeReplace(path), &ids)
			continue
		}
		var hunkNo []byte
		isHunk := false
		if bytes.HasPrefix(raw, []byte("@@@")) {
			if m := ccHunkRe.FindSubmatch(raw); m != nil {
				isHunk, hunkNo = true, m[2]
				width = len(m[1]) - 1
			}
		}
		if !isHunk && bytes.HasPrefix(raw, []byte("@@")) {
			if m := hunkRe.FindSubmatch(raw); m != nil {
				isHunk, hunkNo = true, m[1]
				width = 1
			}
		}
		if inHeader && !isHunk {
			continue
		}
		if isHunk {
			inHeader = false
			newline, _ = strconv.Atoi(string(hunkNo))
			continue
		}
		w := min(width, len(raw))
		if bytes.Equal(raw[:w], bytes.Repeat([]byte{'+'}, width)) {
			if skip < 0 {
				skip = 0
				if blob, ok := ptBlob(repo, full, path); ok {
					if payloads, ok := imagePayloads(blob); ok {
						skip = 1
						for _, p := range payloads {
							for _, ln := range pyLines(pyDecodeReplace(p.data)) {
								sc.scanLine(ln, &ids)
							}
						}
					}
				}
			}
			if skip == 1 {
				continue
			}
			content := pyDecodeReplace(raw[width:])
			var found []string
			sc.scanLine(content, &found)
			for _, pid := range found {
				if !allow.allows(string(path), content, pid) {
					ids = append(ids, pid)
				}
			}
			newline++
		} else if !bytes.Contains(raw[:w], []byte("-")) {
			newline++
		}
	}
	return ids, nil
}
