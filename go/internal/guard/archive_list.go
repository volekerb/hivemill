package guard

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/big"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// T-0805/T-1001: the archive member list of rule E3, a port of Analyzer.archive_names in worker-guard.py. Python reads
// the list with tarfile.open(path, "r|*", ignore_zeros=True) and zipfile.ZipFile; this file follows CPython 3.13's
// code paths step by step (stream offsets, header errors, pax records, the zip central directory) so a crafted archive
// gets the same member list. Where Go cannot follow (xz, a gzip FEXTRA field, a truncated or multi-stream compressed
// stream (a second bzip2 stream or gzip member), very deep header chains, huge header blocks) it refuses: it never lists fewer members than Python would.

// archiveSizeMax is ARCHIVE_SIZE_MAX: the largest archive file the hook lists before refusing.
const archiveSizeMax = 256 << 20

const (
	tarDepthMax  = 100       // nested pax/GNU long headers (Python hits RecursionError near 300)
	tarHeaderMax = 256 << 20 // one pax or GNU long-name data block, read into memory
	tarStreamMax = 8 << 30   // decompressed bytes read from one archive (a bomb would hang the hook)
	tarSparseMax = 1 << 24   // numbers in one GNU sparse 1.0 map
)

// archMember is one member: its name (raw bytes for tar, as Python's surrogateescape str) and its link target ("" when
// the member is not a symlink or hard link).
type archMember struct{ name, link string }

// pyErr is a Python exception: typ is type(e).__name__ (the hook prints it), msg its text.
type pyErr struct{ typ, msg string }

func (e *pyErr) Error() string { return e.typ + ": " + e.msg }

func pyErrType(err error) string {
	var pe *pyErr
	if errors.As(err, &pe) {
		return pe.typ
	}
	return "OSError"
}

func isHeaderErr(err error) bool {
	switch pyErrType(err) {
	case "EmptyHeaderError", "TruncatedHeaderError", "EOFHeaderError", "InvalidHeaderError", "SubsequentHeaderError":
		return true
	}
	return false
}

// osPyErr maps an os error to the OSError subclass Python raises for its errno.
func osPyErr(err error) error {
	var pe *pyErr
	if errors.As(err, &pe) {
		return pe
	}
	var no syscall.Errno
	typ := "OSError"
	if errors.As(err, &no) {
		switch no {
		case syscall.ENOENT:
			typ = "FileNotFoundError"
		case syscall.EACCES, syscall.EPERM:
			typ = "PermissionError"
		case syscall.EISDIR:
			typ = "IsADirectoryError"
		case syscall.ENOTDIR:
			typ = "NotADirectoryError"
		}
	}
	return &pyErr{typ, err.Error()}
}

// archiveNames is archive_names: (member name, link target) for each member of the archive at path, read and never
// extracted. The error is a *pyErr whose typ is the exception Python raises.
func archiveNames(path string, zipped bool) ([]archMember, error) {
	if strings.Contains(path, "\x00") {
		return nil, &pyErr{"ValueError", "embedded null byte"}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, osPyErr(err)
	}
	defer f.Close()
	var out []archMember
	if zipped {
		out, err = zipNames(f)
		if err == nil && len(out) > archiveWalkMax {
			err = &pyErr{"RuntimeError", "too many members"}
		}
		return out, err
	}
	s, err := openTarStream(f)
	if err != nil {
		return nil, err
	}
	w := &tarWalk{s: s, global: &paxDict{vals: map[string]string{}}}
	for {
		ti, err := w.next()
		if err != nil {
			return nil, err
		}
		if ti == nil {
			break
		}
		link := ""
		if ti.typ == '2' || ti.typ == '1' {
			link = ti.link
		}
		out = append(out, archMember{ti.name, link})
		if len(out) > archiveWalkMax {
			return nil, &pyErr{"RuntimeError", "too many members"}
		}
	}
	if len(out) == 0 {
		return nil, &pyErr{"ValueError", "no readable member"}
	}
	return out, nil
}

// --- the tarfile _Stream ("r|*") ---

type fileErrReader struct{ r io.Reader }

func (r fileErrReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		err = osPyErr(err)
	}
	return n, err
}

// decReader turns a decompressor's errors into tarfile.ReadError ("invalid compressed data"), a truncated stream
// included (zlib and bz2 return a short result there; Go cannot tell how much of the last block they would return).
type decReader struct {
	r     io.Reader
	total int64
}

func (r *decReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.total += int64(n)
	if r.total > tarStreamMax {
		return n, &pyErr{"MemoryError", "decompressed stream too large"}
	}
	if err != nil && err != io.EOF {
		var pe *pyErr
		if !errors.As(err, &pe) {
			err = &pyErr{"ReadError", "invalid compressed data"}
		}
	}
	return n, err
}

type tarStream struct {
	src io.Reader
	pos int64
}

// read is _Stream.read: size bytes, short only at the end of the data.
func (s *tarStream) read(size int64) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(s.src, buf)
	s.pos += int64(n)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
}

// readMax is read for a header data block; a block larger than tarHeaderMax that the data actually holds is refused.
func (s *tarStream) readMax(size int64) ([]byte, error) {
	if size <= tarHeaderMax {
		return s.read(size)
	}
	b, err := s.read(tarHeaderMax + 1)
	if err == nil && len(b) > tarHeaderMax {
		err = &pyErr{"MemoryError", "header block too large"}
	}
	return b, err
}

// seek is _Stream.seek: forward only, stopping at the end of the data.
func (s *tarStream) seek(p int64) error {
	if p < s.pos {
		return &pyErr{"StreamError", "seeking backwards is not allowed"}
	}
	n, err := io.CopyN(io.Discard, s.src, p-s.pos)
	s.pos += n
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	return nil
}

var (
	bz2BlockMagic = []byte{0x31, 0x41, 0x59, 0x26, 0x53, 0x59}
	bz2EOSMagic   = []byte{0x17, 0x72, 0x45, 0x38, 0x50, 0x90}
)

// bz2SecondStream reports a byte offset p>0 that starts what looks like a bzip2 stream: Python's BZ2Decompressor reads
// only the first stream (a later one raises EOFError or is skipped), Go's reader reads them all.
func bz2SecondStream(f *os.File) (bool, error) {
	const chunk = 1 << 20
	buf := make([]byte, chunk+9)
	for off := int64(0); ; off += chunk {
		n, err := f.ReadAt(buf, off)
		if err != nil && err != io.EOF {
			return false, osPyErr(err)
		}
		b := buf[:n]
		for i := 0; i+10 <= len(b) && i < chunk; i++ {
			if (off > 0 || i > 0) && b[i] == 'B' && b[i+1] == 'Z' && b[i+2] == 'h' && b[i+3] >= '1' && b[i+3] <= '9' &&
				(bytes.Equal(b[i+4:i+10], bz2BlockMagic) || bytes.Equal(b[i+4:i+10], bz2EOSMagic)) {
				return true, nil
			}
		}
		if n < len(buf) {
			return false, nil
		}
	}
}

// gzMemberEnd reads the first gzip member's deflate data. At its end it skips the 8-byte CRC/ISIZE trailer and
// refuses when any non-zero byte follows: Python reads only the first member, but gzip -d (and so tar -xzf) also
// decodes later members (1f 8b, and the old 1f 9e/9d/1e/a0 formats), so listing the first alone could miss members.
type gzMemberEnd struct {
	r   io.Reader
	raw *bufio.Reader
	end error // the answer once the member has ended
}

func (g *gzMemberEnd) Read(p []byte) (int, error) {
	if g.end != nil {
		return 0, g.end
	}
	n, err := g.r.Read(p)
	if err != io.EOF {
		return n, err
	}
	g.end = err
	if _, derr := g.raw.Discard(8); derr != nil {
		g.end = &pyErr{"ReadError", "truncated gzip trailer"}
		return n, g.end
	}
	for {
		c, rerr := g.raw.ReadByte()
		if rerr != nil {
			return n, g.end // only zero padding, which gzip -d ignores
		}
		if c != 0 {
			g.end = &pyErr{"ReadError", "data after the first gzip member"}
			return n, g.end
		}
	}
}

// openTarStream is _Stream(name, "r", "*"): _StreamProxy reads the first block to pick the compression.
func openTarStream(f *os.File) (*tarStream, error) {
	fr := fileErrReader{f}
	first := make([]byte, 512)
	n, err := io.ReadFull(fr, first)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	first = first[:n]
	raw := bufio.NewReader(io.MultiReader(bytes.NewReader(first), fr))
	switch {
	case bytes.HasPrefix(first, []byte("\x1f\x8b\x08")):
		hdr := func(k int) ([]byte, error) {
			b := make([]byte, k)
			m, err := io.ReadFull(raw, b)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return nil, err
			}
			return b[:m], nil
		}
		if _, err := hdr(3); err != nil {
			return nil, err
		}
		fl, err := hdr(1)
		if err != nil {
			return nil, err
		}
		if len(fl) == 0 { // ord(b"") in _init_read_gz
			return nil, &pyErr{"TypeError", "ord() expected a character, but string of length 0 found"}
		}
		flag := fl[0]
		if _, err := hdr(6); err != nil {
			return nil, err
		}
		if flag&4 != 0 { // FEXTRA: Python skips xlen bytes of the decompressed stream; Go does not follow that
			return nil, &pyErr{"ReadError", "gzip FEXTRA field"}
		}
		for _, bit := range []byte{8, 16} {
			if flag&bit == 0 {
				continue
			}
			for {
				c, err := hdr(1)
				if err != nil {
					return nil, err
				}
				if len(c) == 0 || c[0] == 0 {
					break
				}
			}
		}
		if flag&2 != 0 {
			if _, err := hdr(2); err != nil {
				return nil, err
			}
		}
		return &tarStream{src: &decReader{r: &gzMemberEnd{r: flate.NewReader(raw), raw: raw}}}, nil
	case len(first) >= 10 && string(first[0:3]) == "BZh" && string(first[4:10]) == "1AY&SY":
		multi, err := bz2SecondStream(f)
		if err != nil {
			return nil, err
		}
		if multi {
			return nil, &pyErr{"EOFError", "End of stream already reached"}
		}
		return &tarStream{src: &decReader{r: bzip2.NewReader(raw)}}, nil
	case bytes.HasPrefix(first, []byte("\x5d\x00\x00\x80")) || bytes.HasPrefix(first, []byte("\xfd7zXZ")):
		return nil, &pyErr{"CompressionError", "xz is not read by the Go guard"}
	}
	return &tarStream{src: raw}, nil
}

// --- TarInfo and TarFile.next ---

type tarInfo struct {
	name, link               string
	typ                      byte
	size, offset, offsetData int64
	sparseExt                bool
	origsize                 int64
}

func (t *tarInfo) isreg() bool { return t.typ == '0' || t.typ == 0 || t.typ == '7' || t.typ == 'S' }

// supported is "type in SUPPORTED_TYPES".
func (t *tarInfo) supported() bool { return strings.IndexByte("0\x001256734LKS", t.typ) >= 0 }

// paxDict is the ordered dict of pax headers: setting a key again keeps its place.
type paxDict struct {
	keys []string
	vals map[string]string
}

func (d *paxDict) set(k, v string) {
	if _, ok := d.vals[k]; !ok {
		d.keys = append(d.keys, k)
	}
	d.vals[k] = v
}

func (d *paxDict) has(k string) bool { _, ok := d.vals[k]; return ok }

func (d *paxDict) copy() *paxDict {
	c := &paxDict{keys: append([]string(nil), d.keys...), vals: make(map[string]string, len(d.vals))}
	for k, v := range d.vals {
		c.vals[k] = v
	}
	return c
}

type tarWalk struct {
	s      *tarStream
	offset int64
	global *paxDict
}

func headerErr(typ, msg string) error { return &pyErr{typ, msg} }

// next is TarFile.next with ignore_zeros=True.
func (w *tarWalk) next() (*tarInfo, error) {
	if w.offset != w.s.pos {
		if w.offset == 0 {
			return nil, nil
		}
		if err := w.s.seek(w.offset - 1); err != nil {
			return nil, err
		}
		b, err := w.s.read(1)
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			return nil, &pyErr{"ReadError", "unexpected end of data"}
		}
	}
	for {
		ti, err := w.fromtarfile(true, 0)
		if err == nil {
			return ti, nil
		}
		switch pyErrType(err) {
		case "EOFHeaderError", "InvalidHeaderError":
			w.offset += 512
			continue
		case "EmptyHeaderError", "TruncatedHeaderError":
			if w.offset == 0 {
				return nil, &pyErr{"ReadError", "empty file"}
			}
			return nil, nil
		case "SubsequentHeaderError":
			return nil, &pyErr{"ReadError", err.Error()}
		}
		return nil, err
	}
}

func (w *tarWalk) fromtarfile(dircheck bool, depth int) (*tarInfo, error) {
	if depth > tarDepthMax {
		return nil, &pyErr{"RecursionError", "maximum recursion depth exceeded"}
	}
	buf, err := w.s.read(512)
	if err != nil {
		return nil, err
	}
	ti, err := tarFrombuf(buf, dircheck)
	if err != nil {
		return nil, err
	}
	ti.offset = w.s.pos - 512
	switch ti.typ {
	case 'L', 'K':
		return w.procGnulong(ti, depth)
	case 'S':
		return w.procSparse(ti)
	case 'x', 'g', 'X':
		return w.procPax(ti, depth)
	}
	return w.procBuiltin(ti)
}

// tarBlock is TarInfo._block.
func tarBlock(count int64) (int64, error) {
	if count < 0 {
		return 0, headerErr("InvalidHeaderError", "invalid offset")
	}
	return (count + 511) / 512 * 512, nil
}

func nts(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

const pySat = int64(1) << 60

func saturate(v *big.Int) int64 {
	lim := big.NewInt(pySat)
	if v.Cmp(lim) > 0 {
		return pySat
	}
	if v.Cmp(new(big.Int).Neg(lim)) < 0 {
		return -pySat
	}
	return v.Int64()
}

// nti is tarfile.nti; an empty field (a short sparse block) is Python's IndexError.
func nti(s []byte) (int64, error) {
	if len(s) == 0 {
		return 0, &pyErr{"IndexError", "index out of range"}
	}
	if s[0] == 0x80 || s[0] == 0xff {
		n := new(big.Int)
		for _, c := range s[1:] {
			n.Lsh(n, 8)
			n.Add(n, big.NewInt(int64(c)))
		}
		if s[0] == 0xff {
			m := new(big.Int).Lsh(big.NewInt(1), uint(8*(len(s)-1)))
			n.Sub(n, m)
		}
		return saturate(n), nil
	}
	str := nts(s)
	for i := 0; i < len(str); i++ {
		if str[i] > 0x7f {
			return 0, headerErr("InvalidHeaderError", "invalid header")
		}
	}
	str = strings.Trim(str, " \t\n\v\f\r\x1c\x1d\x1e\x1f")
	if str == "" {
		str = "0"
	}
	v, ok := pyInt(str, 8, true)
	if !ok {
		return 0, headerErr("InvalidHeaderError", "invalid header")
	}
	return v, nil
}

func clampSlice(b []byte, i, j int) []byte {
	if i > len(b) {
		i = len(b)
	}
	if j > len(b) {
		j = len(b)
	}
	if i > j {
		i = j
	}
	return b[i:j]
}

// tarFrombuf is TarInfo._frombuf.
func tarFrombuf(buf []byte, dircheck bool) (*tarInfo, error) {
	if len(buf) == 0 {
		return nil, headerErr("EmptyHeaderError", "empty header")
	}
	if len(buf) != 512 {
		return nil, headerErr("TruncatedHeaderError", "truncated header")
	}
	if bytes.Count(buf, []byte{0}) == 512 {
		return nil, headerErr("EOFHeaderError", "end of file header")
	}
	chk, err := nti(buf[148:156])
	if err != nil {
		return nil, err
	}
	var us, ss int64 = 256, 256
	for i, c := range buf {
		if i >= 148 && i < 156 {
			continue
		}
		us += int64(c)
		ss += int64(int8(c))
	}
	if chk != us && chk != ss {
		return nil, headerErr("InvalidHeaderError", "bad checksum")
	}
	ti := &tarInfo{name: nts(buf[0:100])}
	for _, r := range [][2]int{{100, 108}, {108, 116}, {116, 124}} {
		if _, err := nti(buf[r[0]:r[1]]); err != nil {
			return nil, err
		}
	}
	if ti.size, err = nti(buf[124:136]); err != nil {
		return nil, err
	}
	if _, err := nti(buf[136:148]); err != nil {
		return nil, err
	}
	ti.typ = buf[156]
	ti.link = nts(buf[157:257])
	for _, r := range [][2]int{{329, 337}, {337, 345}} {
		if _, err := nti(buf[r[0]:r[1]]); err != nil {
			return nil, err
		}
	}
	prefix := nts(buf[345:500])
	if dircheck && ti.typ == 0 && strings.HasSuffix(ti.name, "/") {
		ti.typ = '5'
	}
	if ti.typ == 'S' {
		for p := 386; p < 386+4*24; p += 24 {
			if _, err := nti(buf[p : p+12]); err != nil {
				return nil, err
			}
			if _, err := nti(buf[p+12 : p+24]); err != nil {
				return nil, err
			}
		}
		ti.sparseExt = buf[482] != 0
		if ti.origsize, err = nti(buf[483:495]); err != nil {
			return nil, err
		}
	}
	if ti.typ == '5' {
		ti.name = strings.TrimRight(ti.name, "/")
	}
	if prefix != "" && ti.typ != 'L' && ti.typ != 'K' && ti.typ != 'S' {
		ti.name = prefix + "/" + ti.name
	}
	return ti, nil
}

func (w *tarWalk) procBuiltin(ti *tarInfo) (*tarInfo, error) {
	ti.offsetData = w.s.pos
	off := ti.offsetData
	if ti.isreg() || !ti.supported() {
		b, err := tarBlock(ti.size)
		if err != nil {
			return nil, err
		}
		off += b
	}
	w.offset = off
	if err := applyPax(ti, w.global); err != nil {
		return nil, err
	}
	if ti.typ == '5' {
		ti.name = strings.TrimRight(ti.name, "/")
	}
	return ti, nil
}

func (w *tarWalk) procGnulong(ti *tarInfo, depth int) (*tarInfo, error) {
	b, err := tarBlock(ti.size)
	if err != nil {
		return nil, err
	}
	buf, err := w.s.readMax(b)
	if err != nil {
		return nil, err
	}
	next, err := w.fromtarfile(false, depth+1)
	if err != nil {
		if isHeaderErr(err) {
			return nil, &pyErr{"SubsequentHeaderError", err.Error()}
		}
		return nil, err
	}
	next.offset = ti.offset
	if ti.typ == 'L' {
		next.name = nts(buf)
	} else {
		next.link = nts(buf)
	}
	if next.typ == '5' {
		next.name = strings.TrimSuffix(next.name, "/")
	}
	return next, nil
}

func (w *tarWalk) procSparse(ti *tarInfo) (*tarInfo, error) {
	for ti.sparseExt {
		buf, err := w.s.read(512)
		if err != nil {
			return nil, err
		}
		for p := 0; p < 21*24; p += 24 {
			if _, err := nti(clampSlice(buf, p, p+12)); err != nil {
				return nil, err
			}
			if _, err := nti(clampSlice(buf, p+12, p+24)); err != nil {
				return nil, err
			}
		}
		if len(buf) <= 504 {
			return nil, &pyErr{"IndexError", "index out of range"}
		}
		ti.sparseExt = buf[504] != 0
	}
	ti.offsetData = w.s.pos
	b, err := tarBlock(ti.size)
	if err != nil {
		return nil, err
	}
	w.offset = ti.offsetData + b
	ti.size = ti.origsize
	return ti, nil
}

type paxRecord struct{ kw, val string }

func (w *tarWalk) procPax(ti *tarInfo, depth int) (*tarInfo, error) {
	b, err := tarBlock(ti.size)
	if err != nil {
		return nil, err
	}
	buf, err := w.s.readMax(b)
	if err != nil {
		return nil, err
	}
	d := w.global
	if ti.typ != 'g' {
		d = w.global.copy()
	}
	invalid := headerErr("InvalidHeaderError", "invalid header")
	var raw []paxRecord
	for pos := 0; len(buf) > pos && buf[pos] != 0; {
		nd := 0
		for pos+nd < len(buf) && buf[pos+nd] >= '0' && buf[pos+nd] <= '9' {
			nd++
		}
		if nd == 0 || nd > 20 || pos+nd >= len(buf) || buf[pos+nd] != ' ' {
			return nil, invalid
		}
		length := int64(0)
		for _, c := range buf[pos : pos+nd] {
			if length > int64(len(buf)) {
				break
			}
			length = length*10 + int64(c-'0')
		}
		if length < 5 || int64(pos)+length > int64(len(buf)) {
			return nil, invalid
		}
		end := pos + int(length) - 1
		kv := clampSlice(buf, pos+nd+1, end)
		k, v, eq := bytes.Cut(kv, []byte("="))
		if len(k) == 0 || !eq || buf[end] != '\n' {
			return nil, invalid
		}
		raw = append(raw, paxRecord{string(k), string(v)})
		pos += int(length)
	}
	for _, r := range raw {
		d.set(r.kw, r.val)
	}
	next, err := w.fromtarfile(false, depth+1)
	if err != nil {
		if isHeaderErr(err) {
			return nil, &pyErr{"SubsequentHeaderError", err.Error()}
		}
		return nil, err
	}
	switch {
	case d.has("GNU.sparse.map"):
		for _, x := range strings.Split(d.vals["GNU.sparse.map"], ",") {
			if _, ok := pyInt(x, 10, false); !ok {
				return nil, &pyErr{"ValueError", "invalid literal for int()"}
			}
		}
	case d.has("GNU.sparse.size"):
		for _, r := range raw {
			if r.kw == "GNU.sparse.offset" || r.kw == "GNU.sparse.numbytes" {
				if _, ok := pyInt(r.val, 10, false); !ok {
					return nil, invalid
				}
			}
		}
	case d.has("GNU.sparse.major") && d.vals["GNU.sparse.major"] == "1" && d.has("GNU.sparse.minor") && d.vals["GNU.sparse.minor"] == "0":
		if err := w.sparse10(next); err != nil {
			return nil, err
		}
	}
	if ti.typ == 'x' || ti.typ == 'X' {
		if err := applyPax(next, d); err != nil {
			return nil, err
		}
		next.offset = ti.offset
		if d.has("size") {
			off := next.offsetData
			if next.isreg() || !next.supported() {
				b, err := tarBlock(next.size)
				if err != nil {
					return nil, err
				}
				off += b
			}
			w.offset = off
		}
	}
	return next, nil
}

// sparse10 is _proc_gnusparse_10: the map is read (and checked) only for the stream position it leaves.
func (w *tarWalk) sparse10(next *tarInfo) error {
	unpack := &pyErr{"ValueError", "not enough values to unpack"}
	buf, err := w.s.read(512)
	if err != nil {
		return err
	}
	i := bytes.IndexByte(buf, '\n')
	if i < 0 {
		return unpack
	}
	fields, ok := pyInt(string(buf[:i]), 10, true)
	if !ok {
		return &pyErr{"ValueError", "invalid literal for int()"}
	}
	buf = buf[i+1:]
	for n := int64(0); n < fields*2; n++ {
		if n > tarSparseMax {
			return &pyErr{"MemoryError", "sparse map too large"}
		}
		if bytes.IndexByte(buf, '\n') < 0 {
			more, err := w.s.read(512)
			if err != nil {
				return err
			}
			buf = append(buf, more...)
		}
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return unpack
		}
		if _, ok := pyInt(string(buf[:i]), 10, true); !ok {
			return &pyErr{"ValueError", "invalid literal for int()"}
		}
		buf = buf[i+1:]
	}
	next.offsetData = w.s.pos
	return nil
}

// applyPax is TarInfo._apply_pax_info for the fields the member list uses (name, link target, size).
func applyPax(ti *tarInfo, d *paxDict) error {
	for _, k := range d.keys {
		v := d.vals[k]
		switch k {
		case "GNU.sparse.name":
			ti.name = v
		case "GNU.sparse.size", "GNU.sparse.realsize":
			n, ok := pyInt(v, 10, false)
			if !ok {
				return &pyErr{"ValueError", "invalid literal for int()"}
			}
			ti.size = n
		case "path":
			ti.name = strings.TrimRight(v, "/")
		case "linkpath":
			ti.link = v
		case "size":
			n, ok := pyInt(v, 10, false)
			if !ok {
				n = 0
			}
			ti.size = n
		}
	}
	return nil
}

// --- Python int() ---

// ndValue is the decimal value of a Unicode Nd digit (Nd ranges hold runs of 0..9).
func ndValue(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	if r < 0x80 || !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) && (r-rune(rg.Lo))%rune(rg.Stride) == 0 {
			return int((r-rune(rg.Lo))/rune(rg.Stride)) % 10, true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) && (r-rune(rg.Lo))%rune(rg.Stride) == 0 {
			return int((r-rune(rg.Lo))/rune(rg.Stride)) % 10, true
		}
	}
	return 0, false
}

// pyInt is int(s, base) for base 8 or 10 on a str (ascii=false: Unicode spaces and digits, s must be valid UTF-8) or
// on bytes (ascii=true). The value saturates at +-2**60; ok is false where Python raises ValueError.
func pyInt(s string, base int, ascii bool) (int64, bool) {
	var rs []rune
	if ascii {
		for i := 0; i < len(s); i++ {
			if s[i] > 0x7f {
				return 0, false
			}
		}
		s = strings.Trim(s, " \t\n\v\f\r")
		rs = []rune(s)
	} else {
		if !utf8.ValidString(s) {
			return 0, false
		}
		rs = []rune(strings.TrimFunc(s, unicode.IsSpace))
	}
	neg := false
	if len(rs) > 0 && (rs[0] == '+' || rs[0] == '-') {
		neg = rs[0] == '-'
		rs = rs[1:]
	}
	if base == 8 && len(rs) >= 2 && rs[0] == '0' && (rs[1] == 'o' || rs[1] == 'O') {
		rs = rs[2:]
		if len(rs) > 0 && rs[0] == '_' {
			rs = rs[1:]
		}
	}
	if len(rs) == 0 {
		return 0, false
	}
	var v int64
	digits := 0
	for i, r := range rs {
		if r == '_' {
			if i == 0 || i == len(rs)-1 || rs[i-1] == '_' {
				return 0, false
			}
			continue
		}
		d, ok := ndValue(r)
		if ascii && r > 0x7f {
			ok = false
		}
		if !ok || d >= base {
			return 0, false
		}
		digits++
		if v < pySat {
			v = v*int64(base) + int64(d)
		}
	}
	if base == 10 && digits > 4300 {
		return 0, false
	}
	if v > pySat {
		v = pySat
	}
	if neg {
		v = -v
	}
	return v, true
}

// --- zipfile.ZipFile ---

const cp437High = "ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ "

var cp437Table = []rune(cp437High)

func cp437(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c < 0x80 {
			sb.WriteByte(c)
		} else {
			sb.WriteRune(cp437Table[c-0x80])
		}
	}
	return sb.String()
}

func cutNUL(s string) string {
	if i := strings.IndexByte(s, 0); i >= 0 {
		return s[:i]
	}
	return s
}

// zipNames is ZipFile(path).infolist() names: _EndRecData, _EndRecData64 and _RealGetContents.
func zipNames(f *os.File) ([]archMember, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, osPyErr(err)
	}
	size := st.Size()
	at := func(off int64, n int64) ([]byte, error) {
		if off < 0 {
			return nil, &pyErr{"OSError", "Invalid argument"}
		}
		if off >= size || n <= 0 {
			return nil, nil
		}
		if n > size-off {
			n = size - off
		}
		b := make([]byte, n)
		k, err := f.ReadAt(b, off)
		if err != nil && err != io.EOF {
			return nil, osPyErr(err)
		}
		return b[:k], nil
	}
	bad := func(m string) error { return &pyErr{"BadZipFile", m} }
	location, sizeCD, offsetCD, ok := zipEndRec(at, size)
	if !ok {
		return nil, bad("File is not a zip file")
	}
	_ = offsetCD // Python computes concat from it; names do not depend on it
	startDir := location - int64(sizeCD)
	if startDir < 0 {
		return nil, bad("Bad offset for central directory")
	}
	data, err := at(startDir, int64(sizeCD))
	if err != nil {
		return nil, err
	}
	p := 0
	rd := func(n int) []byte {
		e := p + n
		if e > len(data) {
			e = len(data)
		}
		b := data[p:e]
		p = e
		return b
	}
	var out []archMember
	for total := uint64(0); total < sizeCD; {
		cd := rd(46)
		if len(cd) != 46 {
			return nil, bad("Truncated central directory")
		}
		if string(cd[0:4]) != "PK\x01\x02" {
			return nil, bad("Bad magic number for central directory")
		}
		le16 := func(i int) int { return int(binary.LittleEndian.Uint16(cd[i:])) }
		fnlen, extralen, commentlen := le16(28), le16(30), le16(32)
		rawName := rd(fnlen)
		crc := crc32.ChecksumIEEE(rawName)
		var name string
		if le16(8)&0x800 != 0 {
			if !utf8.Valid(rawName) {
				return nil, &pyErr{"UnicodeDecodeError", "'utf-8' codec can't decode the file name"}
			}
			name = string(rawName)
		} else {
			name = cp437(rawName)
		}
		name = cutNUL(name)
		extra := rd(extralen)
		rd(commentlen)
		if cd[6] > 63 {
			return nil, &pyErr{"NotImplementedError", fmt.Sprintf("zip file version %.1f", float64(cd[6])/10)}
		}
		fileSize := uint64(binary.LittleEndian.Uint32(cd[24:]))
		compSize := uint64(binary.LittleEndian.Uint32(cd[20:]))
		hdrOff := uint64(binary.LittleEndian.Uint32(cd[42:]))
		for len(extra) >= 4 {
			tp, ln := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
			if ln+4 > len(extra) {
				return nil, bad(fmt.Sprintf("Corrupt extra field %04x (size=%d)", tp, ln))
			}
			fd := extra[4 : ln+4]
			switch tp {
			case 0x0001:
				take := func() (uint64, bool) {
					if len(fd) < 8 {
						return 0, false
					}
					v := binary.LittleEndian.Uint64(fd)
					fd = fd[8:]
					return v, true
				}
				ok := true
				if fileSize == 0xFFFFFFFFFFFFFFFF || fileSize == 0xFFFFFFFF {
					fileSize, ok = take()
				}
				if ok && compSize == 0xFFFFFFFF {
					compSize, ok = take()
				}
				if ok && hdrOff == 0xFFFFFFFF {
					hdrOff, ok = take()
				}
				if !ok {
					return nil, bad("Corrupt zip64 extra field.")
				}
			case 0x7075:
				if len(fd) < 5 {
					return nil, bad("Corrupt unicode path extra field (0x7075)")
				}
				if fd[0] == 1 && binary.LittleEndian.Uint32(fd[1:5]) == crc {
					u := fd[5:]
					if !utf8.Valid(u) {
						return nil, bad("Corrupt unicode path extra field (0x7075): invalid utf-8 bytes")
					}
					if len(u) > 0 {
						name = cutNUL(string(u))
					}
				}
			}
			extra = extra[ln+4:]
		}
		out = append(out, archMember{name: name})
		total += uint64(46 + fnlen + extralen + commentlen)
	}
	return out, nil
}

// zipEndRec is _EndRecData (with _EndRecData64): the end-record location, the central directory size and offset; ok
// is false where Python raises BadZipFile("File is not a zip file") (any OSError in there becomes that too).
func zipEndRec(at func(int64, int64) ([]byte, error), size int64) (location int64, sizeCD, offsetCD uint64, ok bool) {
	const sig = "PK\x05\x06"
	if size < 22 {
		return 0, 0, 0, false
	}
	data, err := at(size-22, 22)
	if err != nil {
		return 0, 0, 0, false
	}
	var rec []byte
	if len(data) == 22 && string(data[0:4]) == sig && data[20] == 0 && data[21] == 0 {
		rec, location = data, size-22
	} else {
		maxCS := size - 65535 - 22
		if maxCS < 0 {
			maxCS = 0
		}
		data, err = at(maxCS, 65535+22)
		if err != nil {
			return 0, 0, 0, false
		}
		start := bytes.LastIndex(data, []byte(sig))
		if start < 0 || start+22 > len(data) {
			return 0, 0, 0, false
		}
		rec, location = data[start:start+22], maxCS+int64(start)
	}
	sizeCD = uint64(binary.LittleEndian.Uint32(rec[12:]))
	offsetCD = uint64(binary.LittleEndian.Uint32(rec[16:]))
	// _EndRecData64
	off := location - 20
	if off < 0 {
		return location, sizeCD, offsetCD, true
	}
	loc, err := at(off, 20)
	if err != nil || len(loc) != 20 {
		return 0, 0, 0, false
	}
	if string(loc[0:4]) != "PK\x06\x07" {
		return location, sizeCD, offsetCD, true
	}
	diskno, reloff, disks := binary.LittleEndian.Uint32(loc[4:]), binary.LittleEndian.Uint64(loc[8:]), binary.LittleEndian.Uint32(loc[16:])
	if diskno != 0 || disks > 1 {
		return 0, 0, 0, false
	}
	off -= 56
	if off < 0 || reloff > uint64(off) {
		return 0, 0, 0, false
	}
	extrasz := off - int64(reloff)
	r64, err := at(int64(reloff), 56)
	if err != nil || len(r64) != 56 {
		return 0, 0, 0, false
	}
	if !bytes.HasPrefix(r64, []byte("PK\x06\x06")) && int64(reloff) != off {
		extrasz = 0
		if r64, err = at(off, 56); err != nil || len(r64) != 56 {
			return 0, 0, 0, false
		}
	}
	if !bytes.HasPrefix(r64, []byte("PK\x06\x06")) {
		return 0, 0, 0, false
	}
	sz, dirsize, diroffset := binary.LittleEndian.Uint64(r64[4:]), binary.LittleEndian.Uint64(r64[40:]), binary.LittleEndian.Uint64(r64[48:])
	if dirsize > reloff || diroffset != reloff-dirsize || sz != uint64(44+extrasz) {
		return 0, 0, 0, false
	}
	return off - extrasz, dirsize, diroffset, true
}

// pyReprSE is pyRepr of a str decoded with surrogateescape: an invalid UTF-8 byte prints as \udcXX (a lone
// surrogate is not printable). The rest is pyRepr.
func pyReprSE(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\udc%02x`, s[i])
		case r == rune(q) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
		i += size
	}
	b.WriteByte(q)
	return b.String()
}
