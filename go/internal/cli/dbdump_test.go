package cli

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// DB-diff golden (S2 rule W3): a canonical dump of every table, one line per row, and the +/- line diff of two dumps.

type dumpRow struct {
	table string
	rid   string // rowid, or the primary key values joined by "," for a WITHOUT ROWID table
	num   int64
	isNum bool
	line  string
}

func dumpLess(a, b dumpRow) bool {
	if a.table != b.table {
		return a.table < b.table
	}
	if a.isNum && b.isNum {
		return a.num < b.num
	}
	return a.rid < b.rid
}

// dumpValue is one typed value: NULL, int, real (Python repr), text (Go %q), blob x'hex'.
func dumpValue(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return pyFloatRepr(x), nil
	case string:
		return fmt.Sprintf("%q", x), nil
	case []byte:
		return fmt.Sprintf("x'%x'", x), nil
	}
	return "", fmt.Errorf("db dump: unsupported value type %T", v)
}

func dumpTable(d *sql.DB, name string, noRowid bool) ([]dumpRow, error) {
	q := `SELECT name, pk FROM pragma_table_info('` + strings.ReplaceAll(name, "'", "''") + `') ORDER BY cid`
	cr, err := d.Query(q)
	if err != nil {
		return nil, err
	}
	var cols []string
	pks := map[int]string{}
	for cr.Next() {
		var n string
		var pk int
		if err := cr.Scan(&n, &pk); err != nil {
			cr.Close()
			return nil, err
		}
		cols = append(cols, n)
		if pk > 0 {
			pks[pk] = n
		}
	}
	cr.Close()
	sel, order := make([]string, 0, len(cols)+1), "rowid"
	if noRowid {
		var pkc []string
		for i := 1; i <= len(pks); i++ {
			pkc = append(pkc, `"`+pks[i]+`"`)
		}
		sel, order = append(sel, "NULL"), strings.Join(pkc, ",")
	} else {
		sel = append(sel, "rowid")
	}
	for _, c := range cols {
		sel = append(sel, `"`+c+`"`)
	}
	rs, err := d.Query(fmt.Sprintf(`SELECT %s FROM "%s" ORDER BY %s`, strings.Join(sel, ","), name, order))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []dumpRow
	for rs.Next() {
		vals := make([]any, len(cols)+1)
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := dumpRow{table: name}
		var b strings.Builder
		for i, c := range cols {
			s, err := dumpValue(vals[i+1])
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", name, c, err)
			}
			b.WriteString(" " + c + "=" + s)
			if noRowid && pks[1] != "" {
				for k := 1; k <= len(pks); k++ {
					if pks[k] == c {
						if r.rid != "" {
							r.rid += ","
						}
						r.rid += s
					}
				}
			}
		}
		if !noRowid {
			n, ok := vals[0].(int64)
			if !ok {
				return nil, fmt.Errorf("%s: rowid is %T", name, vals[0])
			}
			r.rid, r.num, r.isNum = strconv.FormatInt(n, 10), n, true
		}
		r.line = name + " " + r.rid + b.String()
		out = append(out, r)
	}
	return out, rs.Err()
}

// dumpDB is the canonical dump of every table (sqlite_sequence included), ordered by table name then rowid.
func dumpDB(path string) ([]dumpRow, error) {
	d, err := openRaw(path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rs, err := d.Query(`SELECT name, sql FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	type tbl struct {
		name    string
		noRowid bool
	}
	var tbls []tbl
	for rs.Next() {
		var n string
		var s sql.NullString
		if err := rs.Scan(&n, &s); err != nil {
			rs.Close()
			return nil, err
		}
		tbls = append(tbls, tbl{n, strings.Contains(strings.ToUpper(s.String), "WITHOUT ROWID")})
	}
	rs.Close()
	var all []dumpRow
	for _, t := range tbls {
		rows, err := dumpTable(d, t.name, t.noRowid)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	sort.SliceStable(all, func(i, j int) bool { return dumpLess(all[i], all[j]) })
	return all, nil
}

// diffDump is db.diff: "- <line>" for a row only in before (or changed), "+ <line>" for a row only in after (or
// changed), ordered like the dump; a changed row prints its "-" before its "+".
func diffDump(before, after []dumpRow) string {
	type k struct{ table, rid string }
	idx := func(rows []dumpRow) map[k]dumpRow {
		m := map[k]dumpRow{}
		for _, r := range rows {
			m[k{r.table, r.rid}] = r
		}
		return m
	}
	b, a := idx(before), idx(after)
	type ent struct {
		row  dumpRow
		sign string
	}
	var ents []ent
	for key, r := range b {
		if n, ok := a[key]; !ok || n.line != r.line {
			ents = append(ents, ent{r, "-"})
		}
	}
	for key, r := range a {
		if o, ok := b[key]; !ok || o.line != r.line {
			ents = append(ents, ent{r, "+"})
		}
	}
	sort.Slice(ents, func(i, j int) bool {
		if dumpLess(ents[i].row, ents[j].row) {
			return true
		}
		if dumpLess(ents[j].row, ents[i].row) {
			return false
		}
		return ents[i].sign == "-" && ents[j].sign == "+"
	})
	var s strings.Builder
	for _, e := range ents {
		s.WriteString(e.sign + " " + e.row.line + "\n")
	}
	return s.String()
}

// diffTables names the tables on which two db.diff texts differ, for the error message.
func diffTables(got, want string) []string {
	count := map[string]int{}
	for sign, text := range map[int]string{1: got, -1: want} {
		for _, l := range strings.Split(text, "\n") {
			if f := strings.Fields(l); len(f) >= 2 {
				count[f[1]+"\x00"+l] += sign
			}
		}
	}
	set := map[string]bool{}
	for k, v := range count {
		if v != 0 {
			set[strings.SplitN(k, "\x00", 2)[0]] = true
		}
	}
	var out []string
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
