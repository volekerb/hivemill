package views

import (
	"fmt"
	"html/template"
	"math"
	"regexp"
	"strconv"
	"strings"

	"factory/go/internal/web/data"
)

// Safe is markup that is already escaped.
type Safe string

// Attr is one attribute of El. A nil or false Val drops the attribute, true writes it bare.
type Attr struct {
	Name string
	Val  any
}

// At builds an Attr.
func At(name string, val any) Attr { return Attr{name, val} }

// Cls is At("class", v).
func Cls(v any) Attr { return Attr{"class", v} }

var (
	attrRe = regexp.MustCompile(`^[a-z-]+$`)
	tagRe  = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	// relOK is html.py _REL_RE, ^/(?!/)[\w./?=&%#:+~-]*$, with Python's Unicode \w ([\p{L}\p{N}_]); Python's $ also matches before one trailing newline, hence \n?\z.
	relOK = regexp.MustCompile(`^/[\p{L}\p{N}_./?=&%#:+~-]*\n?\z`)
	prRe  = regexp.MustCompile(`^https://github\.com/[\p{L}\p{N}_.-]+/[\p{L}\p{N}_.-]+/pull/\p{Nd}+\n?\z`)
	void  = map[string]bool{"meta": true, "link": true, "br": true, "hr": true, "img": true}
)

// H escapes one value; nil is "".
func H(x any) string {
	if x == nil {
		return ""
	}
	return template.HTMLEscapeString(str(x))
}

// str is Python str() for the value kinds views pass.
func str(x any) string {
	switch v := x.(type) {
	case nil:
		return ""
	case string:
		return v
	case Safe:
		return string(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case *string:
		if v == nil {
			return ""
		}
		return *v
	case fmt.Stringer:
		return v.String()
	}
	return fmt.Sprint(x)
}

// render is html._render: nil/false -> "", Safe kept, lists joined, everything else escaped.
func render(x any) string {
	switch v := x.(type) {
	case nil:
		return ""
	case bool:
		if !v {
			return ""
		}
		return H(v)
	case Safe:
		return string(v)
	case []any:
		var b strings.Builder
		for _, i := range v {
			b.WriteString(render(i))
		}
		return b.String()
	case []Safe:
		var b strings.Builder
		for _, i := range v {
			b.WriteString(string(i))
		}
		return b.String()
	case *string:
		if v == nil {
			return ""
		}
		return H(*v)
	}
	return H(x)
}

// El is <tag attrs>children</tag>. Attr arguments become attributes (in order), everything else a child: strings
// are escaped, Safe values are not. Void tags (meta, link, br, hr, img) take no children.
func El(tag string, args ...any) Safe {
	if !tagRe.MatchString(tag) {
		panic(fmt.Sprintf("views: bad tag %q", tag))
	}
	var attrs, kids strings.Builder
	for _, a := range args {
		at, ok := a.(Attr)
		if !ok {
			kids.WriteString(render(a))
			continue
		}
		if !attrRe.MatchString(at.Name) {
			panic(fmt.Sprintf("views: bad attribute name %q", at.Name))
		}
		switch v := at.Val.(type) {
		case nil:
			continue
		case bool:
			if v {
				attrs.WriteString(" " + at.Name)
			}
			continue
		case *string:
			if v == nil {
				continue
			}
		}
		attrs.WriteString(" " + at.Name + `="` + H(at.Val) + `"`)
	}
	if void[tag] {
		return Safe("<" + tag + attrs.String() + ">")
	}
	return Safe("<" + tag + attrs.String() + ">" + kids.String() + "</" + tag + ">")
}

// Td is a table cell with a class (html.table's (value, "num") tuple).
type Td struct {
	V     any
	Class string
}

// Table is html.table: cols are header labels, rows cells (Td for a classed cell).
func Table(cols []string, rows [][]any, cls string) Safe {
	var th []any
	for _, c := range cols {
		th = append(th, El("th", c))
	}
	var body []any
	for _, r := range rows {
		var cells []any
		for _, c := range r {
			if t, ok := c.(Td); ok {
				cells = append(cells, El("td", At("class", t.Class), t.V))
			} else {
				cells = append(cells, El("td", c))
			}
		}
		body = append(body, El("tr", cells))
	}
	return El("table", El("thead", El("tr", th)), El("tbody", body), Cls(cls))
}

// Badge is html.badge.
func Badge(text any, kind string) Safe {
	return El("span", text, Cls("badge badge--"+kind))
}

// Link is html.link: an anchor for a relative path or a github PR URL, anything else plain text.
func Link(href string, text any) Safe {
	if (relOK.MatchString(href) && !strings.HasPrefix(href, "//")) || prRe.MatchString(href) {
		return El("a", At("href", href), text)
	}
	return Safe(H(text))
}

// Ts is html.ts: short local time with the full ISO string as a tooltip.
func Ts(iso string) Safe {
	d, ok := data.ParseTS(iso)
	if !ok {
		return Safe(H(iso))
	}
	return El("span", At("title", iso), d.In(data.Loc).Format("01-02 15:04"))
}

// pyRound is Python's round() to int (half to even).
func pyRound(x float64) int64 { return int64(math.RoundToEven(x)) }

// Dur is html.dur; ok=false is None.
func Dur(seconds float64, ok bool) string {
	if !ok {
		return "-"
	}
	s := pyRound(seconds)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd %dh", s/86400, s%86400/3600)
}

// DurP is Dur for an optional integer (nil = "-").
func DurP(s *int64) string {
	if s == nil {
		return "-"
	}
	return Dur(float64(*s), true)
}

// Card is html.card.
func Card(title string, body ...any) Safe {
	args := append([]any{El("h2", title, Cls("card-title"))}, body...)
	return El("section", append(args, Cls("card"))...)
}

// Empty is html.empty.
func Empty(text string) Safe { return El("p", text, Cls("empty")) }

// commas formats n like Python's thousands-separator format.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	r := strings.Join(out, ",")
	if neg {
		r = "-" + r
	}
	return r
}

// money formats a dollar amount with thousands separators and two decimals (Python's comma + .2f).
func money(v float64) string {
	s := strconv.FormatFloat(math.Abs(v), 'f', 2, 64)
	i := strings.IndexByte(s, '.')
	n, _ := strconv.ParseInt(s[:i], 10, 64)
	r := commas(n) + s[i:]
	if v < 0 {
		r = "-" + r
	}
	return "$" + r
}

// fmtG is Python's g format (six significant digits).
func fmtG(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }

// pct0 is Python's .0% format.
func pct0(v float64) string { return strconv.FormatFloat(v*100, 'f', 0, 64) + "%" }

// f1 is Python's .1f format.
func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
