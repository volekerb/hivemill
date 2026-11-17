package views

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SVG chart primitives (components C8-C10), ported from web/charts.py. Every function returns a Safe built with El or
// svgEl, so labels are escaped. Geometry lives in SVG attributes (no inline style: the CSP is style-src 'self');
// colours come from the app.css classes. Every chart has a role="img" aria-label summary; sparkline and columns are
// followed by a visually hidden <table class="sr-only"> with the same data.

const (
	sparkW, sparkH, sparkPad = 120, 24, 3
	colW                     = 24
	maxW                     = 336 // widest chart that still fits a phone card at 1:1
	colTop, colBottom        = 14, 16
	labelPad                 = 18.0
)

var chartTones = map[string]bool{"success": true, "warning": true, "danger": true, "neutral": true}

// Point is one (day "YYYY-MM-DD", number) pair, oldest first.
type Point struct {
	Day string
	V   float64
}

// pyFloat is Python's str(float): shortest round-trip repr, with ".0" for integral values.
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// svgEl assembles an <svg> root: html El only takes lower-case attribute names, and SVG needs viewBox.
func svgEl(inner []any, viewBox string, attrs ...Attr) Safe {
	var b strings.Builder
	b.WriteString(`<svg viewBox="` + H(viewBox) + `"`)
	for _, a := range attrs {
		if a.Val == nil {
			continue
		}
		b.WriteString(" " + a.Name + `="` + H(a.Val) + `"`)
	}
	b.WriteString(">")
	for _, i := range inner {
		b.WriteString(render(i))
	}
	b.WriteString("</svg>")
	return Safe(b.String())
}

func lineEl(x string, y1, y2 int, cls string) Safe {
	return Safe(fmt.Sprintf(`<line x1="%s" x2="%s" y1="%d" y2="%d" class="%s"/>`, H(x), H(x), y1, y2, H(cls)))
}

func clampPct(v float64) float64 {
	if math.IsNaN(v) {
		return 100
	}
	return math.Max(0, math.Min(100, v))
}

func num(v float64) string { return f1(v) }

// Compact is a short figure for a count: 1500 -> "1.5k", 48567200 -> "48.6M"; below 1000 the number as it is.
func Compact(n float64) string {
	switch {
	case math.Abs(n) >= 1_000_000:
		return f1(n/1_000_000) + "M"
	case math.Abs(n) >= 1_000:
		k := f1(n / 1_000)
		if kv, _ := strconv.ParseFloat(k, 64); math.Abs(kv) < 1000 {
			return k + "k"
		}
		return f1(n/1_000_000) + "M"
	}
	return fmtG(n)
}

// exact is the exact value for a tooltip or table cell: whole numbers with thousands separators, never an exponent.
func exact(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) {
		return commas(int64(v))
	}
	return Compact(v)
}

func dayOf(iso string) string {
	r := []rune(iso)
	if len(r) >= 10 {
		return string(r[5:10])
	}
	return iso
}

func dataTable(points []Point, label, caption, unit string) Safe {
	var rows []any
	for _, p := range points {
		rows = append(rows, El("tr", El("td", p.Day), El("td", exact(p.V))))
	}
	return El("table", El("caption", caption),
		El("thead", El("tr", El("th", pyCapitalize(unit), At("scope", "col")), El("th", pyCapitalize(label), At("scope", "col")))),
		El("tbody", rows), Cls("sr-only"))
}

// Meter is C8: a 100 x 6 track with a fill whose width is exact (value / total, one decimal). label ("3/9 tasks") sits
// beside the bar; noun is the unit of the aria-label ("3 of 9 tasks done").
func Meter(value, total int64, label, noun string) Safe {
	w := 0.0
	if total != 0 {
		w = clampPct(100 * float64(value) / float64(total))
	}
	bar := svgEl([]any{
		El("rect", Cls("track"), At("x", 0), At("y", 0), At("width", 100), At("height", 6)),
		El("rect", Cls("fill-data1"), At("x", 0), At("y", 0), At("width", num(w)), At("height", 6)),
	}, "0 0 100 6", At("preserveAspectRatio", "none"), At("role", "img"), At("focusable", "false"),
		At("aria-label", fmt.Sprintf("%d of %d %s done", value, total, noun)), At("class", "meter-svg"))
	return El("div", bar, El("span", label, Cls("lbl")), Cls("meter"))
}

// GaugeBar is the bare C9 bar: 100 x 8 track, a fill classed by tone and a 2 px tick at each threshold percentage.
func GaugeBar(pct float64, thresholds []float64, tone, aria string) Safe {
	if !chartTones[tone] {
		tone = "success"
	}
	inner := []any{
		El("rect", Cls("track"), At("x", 0), At("y", 0), At("width", 100), At("height", 8)),
		El("rect", Cls("fill-"+tone), At("x", 0), At("y", 0), At("width", num(clampPct(pct))), At("height", 8)),
	}
	for _, t := range thresholds {
		inner = append(inner, lineEl(num(clampPct(t)), 0, 8, "tick"))
	}
	return svgEl(inner, "0 0 100 8", At("preserveAspectRatio", "none"), At("role", "img"), At("focusable", "false"),
		At("aria-label", aria), At("class", "gauge"))
}

// Zone is a gauge's (tone, word) pair such as ("danger", "Over cap").
type Zone struct{ Tone, Word string }

// Gauge is C9: a row of label, bar, value. pct is the fill (0-100), thresholds the tick positions; the zone word is
// always shown, so colour is not the only signal. An empty caption is none.
func Gauge(label, valueText string, pct float64, thresholds []float64, zone Zone, caption string) Safe {
	aria := label + " " + valueText + ", " + zone.Word
	var sub any
	if caption != "" {
		aria += ", " + caption
		sub = " · " + caption
	}
	return El("div", El("span", label, Cls("gl")), GaugeBar(pct, thresholds, zone.Tone, aria),
		El("span", valueText, El("span", El("b", zone.Word, Cls("gz fg-"+zone.Tone)), sub), Cls("gv")), Cls("gauge-row"))
}

func summary(points []Point, label, unit string) string {
	total, peak := 0.0, 0.0
	for _, p := range points {
		total += p.V
		if p.V > peak {
			peak = p.V
		}
	}
	text := fmt.Sprintf("%s %s in %d %ss", Compact(total), label, len(points), unit)
	if peak > 0 {
		day := ""
		for _, p := range points {
			if p.V == peak {
				day = p.Day
			}
		}
		text += fmt.Sprintf(", peak %s on %s", Compact(peak), dayOf(day))
	}
	return text
}

// Sparkline is C10: a 120 x 24 polyline with the last point emphasised, the sr-only data table and the visible caption.
func Sparkline(points []Point, label, caption string) Safe {
	n := len(points)
	top := 0.0
	for _, p := range points {
		top = math.Max(top, p.V)
	}
	if top == 0 {
		top = 1
	}
	step := 0.0
	if n > 1 {
		step = float64(sparkW-2*sparkPad) / float64(n-1)
	}
	var shapes []any
	if n > 0 {
		var pts []string
		var lx, ly float64
		for i, p := range points {
			x := float64(sparkPad) + float64(i)*step
			y := float64(sparkH-sparkPad) - (p.V/top)*float64(sparkH-2*sparkPad)
			pts = append(pts, num(x)+","+num(y))
			lx, ly = x, y
		}
		shapes = []any{El("polyline", At("points", strings.Join(pts, " "))),
			El("circle", Cls("spark-last"), At("cx", num(lx)), At("cy", num(ly)), At("r", "2.5"))}
	}
	svg := svgEl(shapes, fmt.Sprintf("0 0 %d %d", sparkW, sparkH), At("width", sparkW), At("height", sparkH), At("role", "img"),
		At("focusable", "false"), At("aria-label", summary(points, label, "day")), At("class", "spark"))
	return El("span", svg, dataTable(points, label, fmt.Sprintf("%s per day, last %d days", pyCapitalize(label), n), "day"),
		El("span", caption, Cls("spark-cap")), Cls("spark-wrap"))
}

// Columns is C10: one <rect> per day, each with a <title> holding its exact value; value labels on non-zero bars when
// there are 14 days or fewer; an x label every 7th day. Empty data gives the calm C14 line instead of an empty axis.
// unit names what one bar is ("day" or "week").
func Columns(points []Point, label string, height int, unit string) Safe {
	any0 := false
	for _, p := range points {
		if p.V != 0 {
			any0 = true
		}
	}
	if len(points) == 0 || !any0 {
		return EmptyState("No "+label+" in this range", nil)
	}
	n := len(points)
	top := 0.0
	for _, p := range points {
		top = math.Max(top, p.V)
	}
	plot := float64(height - colTop - colBottom)
	maxLen := 0
	for _, p := range points {
		if p.V != 0 {
			if l := len([]rune(Compact(p.V))); l > maxLen {
				maxLen = l
			}
		}
	}
	lw := 6*maxLen + 6
	cw := colW
	if lw > colW {
		m := maxW / n
		if m < colW {
			m = colW
		}
		cw = lw
		if m < lw {
			cw = m
		}
	}
	stride := 1
	if lw > cw {
		stride = (lw + cw - 1) / cw
	}
	var bars, texts []any
	for i, p := range points {
		bh := 1.0
		if p.V != 0 {
			bh = math.Max(1.0, p.V/top*plot)
		}
		x := labelPad + float64(i*cw) + 4
		cls := "col"
		if p.V == 0 {
			cls = "col col--zero"
		}
		bars = append(bars, El("rect", El("title", fmt.Sprintf("%s: %s %s", dayOf(p.Day), exact(p.V), label)), Cls(cls),
			At("x", pyFloat(x)), At("y", num(float64(height-colBottom)-bh)), At("width", cw-8), At("height", num(bh))))
		half := float64(cw-8) / 2
		if p.V != 0 && n <= 14 && i%stride == 0 {
			texts = append(texts, El("text", Compact(p.V), Cls("col-val"), At("x", num(x+half)),
				At("y", num(float64(height-colBottom)-bh-3)), At("text-anchor", "middle")))
		}
		if i%7 == 0 {
			ta := "middle"
			if i == 0 {
				ta = "start"
			} else if i == n-1 {
				ta = "end"
			}
			texts = append(texts, El("text", dayOf(p.Day), Cls("col-x"), At("x", num(x+half)), At("y", height-3), At("text-anchor", ta)))
		}
	}
	vbW := float64(n*cw) + 2*labelPad
	svg := svgEl(append(bars, texts...), fmt.Sprintf("0 0 %d %d", int(vbW), height), At("height", height), At("role", "img"),
		At("focusable", "false"), At("preserveAspectRatio", "xMidYMin meet"), At("aria-label", summary(points, label, unit)), At("class", "cols"))
	return El("div", svg, dataTable(points, label, fmt.Sprintf("%s per %s, last %d %ss", pyCapitalize(label), unit, n, unit), unit), Cls("cols-wrap"))
}
