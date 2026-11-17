package views

import (
	"html"
	"regexp"
	"sort"
	"strings"
)

// TimestampMask is THE documented timestamp regex of the web parity (G-048 plan section 2 P3): the Go parity tool
// (P3e) and the view goldens both apply it to normalised DOM text before comparing, so a page rendered a minute later
// still compares equal. It matches the two clock renderings of the UI: the short local time of Ts ("11-14 12:00") and
// the "Updated 12:00" of the header. Relative ages ("5 m") are not masked: they are the same on both sides when both
// render against the same now. ASCII digits only: both renderers format with ASCII digits.
var TimestampMask = regexp.MustCompile(`[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}|Updated [0-9]{2}:[0-9]{2}`)

// TimestampPlaceholder replaces every TimestampMask match.
const TimestampPlaceholder = "<TS>"

// NormalizeDOM reduces a page to what the parity compares: the text of the document (tags dropped, entities decoded,
// whitespace collapsed to single spaces, timestamps masked) and the sorted set of link targets ("a href" values) and
// anchors ("#id" of every element). Byte equality of the markup is not required. script and style bodies are not text.
func NormalizeDOM(doc string) (text string, links []string) {
	var sb strings.Builder
	set := map[string]bool{}
	skip := ""
	i := 0
	for i < len(doc) {
		if doc[i] != '<' {
			j := strings.IndexByte(doc[i:], '<')
			if j < 0 {
				j = len(doc) - i
			}
			if skip == "" {
				sb.WriteByte(' ') // text chunks are separated, so adjacent elements never glue into one long token
				sb.WriteString(html.UnescapeString(doc[i : i+j]))
			}
			i += j
			continue
		}
		if strings.HasPrefix(doc[i:], "<!--") {
			end := strings.Index(doc[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		end := tagEnd(doc, i)
		if end < 0 {
			sb.WriteByte(' ')
			sb.WriteString(html.UnescapeString(doc[i:]))
			break
		}
		tag := doc[i+1 : end]
		i = end + 1
		if strings.HasPrefix(tag, "!") || strings.HasPrefix(tag, "?") {
			continue
		}
		closing := strings.HasPrefix(tag, "/")
		name, attrs := splitTag(strings.TrimPrefix(tag, "/"))
		name = strings.ToLower(name)
		if closing {
			if name == skip {
				skip = ""
			}
			continue
		}
		if name == "script" || name == "style" {
			skip = name
		}
		for _, a := range attrs {
			switch {
			case a[0] == "href" && name == "a":
				set["a:"+a[1]] = true
			case a[0] == "id":
				set["#"+a[1]] = true
			}
		}
	}
	text = strings.Join(strings.FieldsFunc(sb.String(), pyIsSpace), " ") // str.split(): also U+001C..U+001F, unlike strings.Fields
	text = TimestampMask.ReplaceAllString(text, TimestampPlaceholder)
	for l := range set {
		links = append(links, l)
	}
	sort.Strings(links)
	return text, links
}

// tagEnd is the index of the '>' that closes the tag opened at doc[start], skipping quoted attribute values; -1 if none.
func tagEnd(doc string, start int) int {
	var q byte
	for k := start + 1; k < len(doc); k++ {
		c := doc[k]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '>':
			return k
		}
	}
	return -1
}

var attrSplit = regexp.MustCompile(`([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?`)

// splitTag returns the tag name and its (name, decoded value) attribute pairs.
func splitTag(tag string) (string, [][2]string) {
	tag = strings.TrimSuffix(tag, "/")
	k := strings.IndexAny(tag, " \t\r\n\f")
	if k < 0 {
		return tag, nil
	}
	name, rest := tag[:k], tag[k:]
	var attrs [][2]string
	for _, m := range attrSplit.FindAllStringSubmatch(rest, -1) {
		v := m[2] + m[3] + m[4]
		attrs = append(attrs, [2]string{strings.ToLower(m[1]), html.UnescapeString(v)})
	}
	return name, attrs
}
