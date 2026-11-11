package cli

import (
	"io"
	"strings"
	"unicode/utf8"
)

const maxColWidth = 60

// Table is table(rows, cols) of bin/factory_cli.py: upper-case headers, columns two spaces apart, widths measured
// in code points and capped at 60, cells cut at 60 and padded. Rows are already stringified (None is "").
func Table(w io.Writer, cols []string, rows [][]string) {
	if len(rows) == 0 {
		io.WriteString(w, "(none)\n")
		return
	}
	width := make([]int, len(cols))
	for i, c := range cols {
		m := utf8.RuneCountInString(c)
		for _, r := range rows {
			if n := utf8.RuneCountInString(r[i]); n > m {
				m = n
			}
		}
		if m > maxColWidth {
			m = maxColWidth
		}
		width[i] = m
	}
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = ljust(strings.ToUpper(c), width[i])
	}
	io.WriteString(w, strings.Join(head, "  ")+"\n")
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i := range cols {
			cells[i] = ljust(cut(r[i], maxColWidth), width[i])
		}
		io.WriteString(w, strings.Join(cells, "  ")+"\n")
	}
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func ljust(s string, n int) string {
	if k := n - utf8.RuneCountInString(s); k > 0 {
		return s + strings.Repeat(" ", k)
	}
	return s
}
