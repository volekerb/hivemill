package hm

import (
	"errors"
	"html/template"
	"net/url"
	"strings"
)

// The /task page as one timeline (buildTrace): a header, one row per step, then the drill template's former task view
// (fields, sessions, raw events) collapsed at the end. No script, only the classes of app.css.

// tracePage is the data of the /task page's timeline.
type tracePage struct {
	Trace       Trace
	GoalTitle   string
	Rows        []traceRow
	CostPending bool // a session's cost is not known yet, so the total is partial
}

// traceRow is one step as shown. Mark is the word a step that stands out carries (colour is never the only signal) and
// Class its colour; SessionID is the id in the step's session link.
type traceRow struct {
	TraceStep
	Mark, Class, SessionID string
}

func newTracePage(tr Trace, goalTitle string) *tracePage {
	p := &tracePage{Trace: tr, GoalTitle: goalTitle}
	for _, s := range tr.Steps {
		row := traceRow{TraceStep: s}
		row.Mark, row.Class = stepMark(s)
		if u, err := url.Parse(s.Links.Session); err == nil {
			row.SessionID = u.Query().Get("s")
		}
		p.Rows = append(p.Rows, row)
	}
	return p
}

// stepMark is the word and CSS class of a failed, blocked or held step ("" for the others).
func stepMark(s TraceStep) (string, string) {
	switch s.Kind {
	case "task.failed", "ci.red":
		return "failed", "bad"
	case "gate.blocked":
		return "blocked", "bad"
	case "judge.verdict":
		// the summary is "judge <verdict>: reasons"
		switch v, _, _ := strings.Cut(s.Summary, ":"); v {
		case "judge MERGE":
		case "judge HOLD":
			return "held", "warn"
		default:
			return "blocked", "bad"
		}
	case "run.held":
		return "held", "warn"
	}
	return "", ""
}

const traceTmpl = `{{define "drill"}}{{with .TracePg}}
{{if $.TaskTitle}}<p>{{$.TaskTitle}}</p>{{end}}
<dl class="kv">
<dt>goal</dt><dd>{{or .Trace.Goal "(no goal)"}}{{if .GoalTitle}}: {{.GoalTitle}}{{end}}</dd>
<dt>status</dt><dd class="{{outcomeClass $.Task.Row.Outcome}}">{{$.Task.Row.Outcome}}</dd>
<dt>total cost</dt><dd>{{usd .Trace.TotalUSD}}{{if .CostPending}} <span class="warn">partial: a session cost is pending</span>{{end}}</dd>
<dt>hours to merge</dt><dd>{{fp $.Task.Row.HoursToMerge}}</dd>
{{if $.Task.PR}}<dt>PR</dt><dd>{{with safeURL $.Task.PR}}<a href="{{.}}" rel="noreferrer">{{$.Task.PR}}</a>{{else}}{{$.Task.PR}}{{end}}</dd>{{end}}
</dl>
<h2>Trace</h2>
<div class="scroll" tabindex="0" role="region" aria-label="task trace"><table>
<tr><th scope="col">time</th><th scope="col">role</th><th scope="col">state</th><th scope="col">step</th><th scope="col" class="num">cost</th><th scope="col">links</th></tr>
{{range .Rows}}<tr data-step="{{.Kind}}"><td>{{when .TS}}</td><td>{{.Role}}</td><td class="{{.Class}}">{{if .Mark}}<strong>{{.Mark}}</strong>{{end}}</td><td class="wrap {{.Class}}">{{.Summary}}</td>
<td class="num">{{with .CostUSD}}{{usd (deref .)}}{{end}}</td><td>{{with safeURL .Links.PR}}<a href="{{.}}" rel="noreferrer">PR</a> {{end}}{{if .SessionID}}<a href="{{$.Q "/session" "s" .SessionID}}">session <code>{{short .SessionID}}</code></a>{{end}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">no step recorded yet</td></tr>{{end}}
</table></div>
<details><summary>Raw task fields, sessions and events</summary>
{{template "drillRaw" $}}
</details>
{{else}}{{template "drillRaw" .}}{{end}}{{end}}`

// addTracePage puts the timeline in front of the drill template: its former body stays as "drillRaw", which every page
// without a TracePg renders as before.
func addTracePage(t *template.Template) error {
	drill := t.Lookup("drill")
	if drill == nil || drill.Tree == nil {
		return errors.New("obs: no drill template")
	}
	if _, err := t.AddParseTree("drillRaw", drill.Tree.Copy()); err != nil {
		return err
	}
	_, err := t.New("trace").Funcs(template.FuncMap{"deref": func(f *float64) float64 { return *f }}).Parse(traceTmpl)
	return err
}
