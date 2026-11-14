package data

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// opt is an optional float (a Python float or None).
type opt struct {
	v  float64
	ok bool
}

func optMin(xs []float64) opt {
	if len(xs) == 0 {
		return opt{}
	}
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return opt{m, true}
}

func optMax(xs []float64) opt {
	if len(xs) == 0 {
		return opt{}
	}
	m := xs[0]
	for _, x := range xs[1:] {
		if x > m {
			m = x
		}
	}
	return opt{m, true}
}

func secsP(a, b opt) *int64 {
	if a.ok && b.ok && b.v >= a.v {
		v := pyRound(b.v - a.v)
		return &v
	}
	return nil
}

type span2 struct{ s, e float64 }

// unionLen is queries._union_len: total length of the union of the spans.
func unionLen(spans []span2) float64 {
	sorted := append([]span2(nil), spans...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].s != sorted[j].s {
			return sorted[i].s < sorted[j].s
		}
		return sorted[i].e < sorted[j].e
	})
	total, curE := 0.0, 0.0
	var curS float64
	have := false
	for _, sp := range sorted {
		if !have || sp.s > curE {
			if have {
				total += curE - curS
			}
			curS, curE, have = sp.s, sp.e, true
		} else if sp.e > curE {
			curE = sp.e
		}
	}
	if have {
		total += curE - curS
	}
	return total
}

type updEv struct {
	t      float64
	detail string
}

type vdRow struct {
	role, verdict string
	created       float64
	createdOK     bool
}

// Stages is the stage durations of one merged task in Throughput (None = unknown).
type Stages struct {
	TotalS  *int64  `json:"total_s"`
	CodingS *int64  `json:"coding_s"`
	ReviewS *int64  `json:"review_s"`
	JudgeS  *int64  `json:"judge_s"`
	ReworkS *int64  `json:"rework_s"`
	TaskID  string  `json:"task_id"`
	GoalID  *string `json:"goal_id"`
	Merged  string  `json:"merged"`
}

func stages(mergedE float64, ups []updEv, vds []vdRow, spawns []struct {
	role string
	t    float64
}) Stages {
	var all, coder []float64
	for _, s := range spawns {
		all = append(all, s.t)
		if s.role == "coder" {
			coder = append(coder, s.t)
		}
	}
	firstSpawn, coderSpawn := optMin(all), optMin(coder)
	var prs, reviews []float64
	for _, u := range ups {
		if strings.Contains(u.detail, "pr=") || strings.Contains(u.detail, "status=in_review") {
			prs = append(prs, u.t)
		}
		if strings.Contains(u.detail, "status=in_review") {
			reviews = append(reviews, u.t)
		}
	}
	sort.Float64s(reviews)
	prSet := optMin(prs)
	var passes []float64
	for _, v := range vds {
		if (v.role == "reviewer" || v.role == "qa") && v.verdict == "pass" && v.createdOK {
			passes = append(passes, v.created)
		}
	}
	lastPass := optMax(passes)
	var inRev []float64
	for _, t := range reviews {
		if lastPass.ok && t <= lastPass.v {
			inRev = append(inRev, t)
		}
	}
	inReview := optMax(inRev)
	var spans []span2
	reversed := false
	for _, v := range vds {
		if (v.verdict == "fail" || v.verdict == "reject") && v.createdOK {
			nxt := mergedE
			var later []float64
			for _, t := range reviews {
				if t > v.created {
					later = append(later, t)
				}
			}
			if m := optMin(later); m.ok {
				nxt = m.v
			}
			if nxt < v.created {
				reversed = true
			}
			spans = append(spans, span2{v.created, nxt})
		}
	}
	var rework *int64
	if !reversed {
		x := pyRound(unionLen(spans))
		rework = &x
	}
	m := opt{mergedE, true}
	return Stages{TotalS: secsP(firstSpawn, m), CodingS: secsP(coderSpawn, prSet), ReviewS: secsP(inReview, lastPass),
		JudgeS: secsP(lastPass, m), ReworkS: rework}
}

// Span is a start/end/seconds triple (queries.task_lifecycle span()).
type Span struct {
	Start   *string `json:"start"`
	End     *string `json:"end"`
	Seconds *int64  `json:"seconds"`
}

func mkSpan(a, b *string) Span {
	ea, aok := epochP(a)
	eb, bok := epochP(b)
	return Span{a, b, secsP(opt{ea, aok}, opt{eb, bok})}
}

// Stage is one working stage of a task lifecycle; review stages carry head.
type Stage struct {
	Stage   string
	Span    Span
	HasHead bool
	Head    *string
}

// MarshalJSON writes stage, start, end, seconds[, head].
func (s Stage) MarshalJSON() ([]byte, error) {
	p := []kv{{"stage", s.Stage}, {"start", s.Span.Start}, {"end", s.Span.End}, {"seconds", s.Span.Seconds}}
	if s.HasHead {
		p = append(p, kv{"head", s.Head})
	}
	return ordered(p...)
}

// Wait is a wait inside the stages: ci, or blocked (with who it waits on).
type Wait struct {
	Kind  string
	Waits string
	Span  Span
}

// MarshalJSON writes kind, [waits,] start, end, seconds.
func (w Wait) MarshalJSON() ([]byte, error) {
	p := []kv{{"kind", w.Kind}}
	if w.Kind == "blocked" {
		p = append(p, kv{"waits", w.Waits})
	}
	return ordered(append(p, kv{"start", w.Span.Start}, kv{"end", w.Span.End}, kv{"seconds", w.Span.Seconds})...)
}

// WaitsTotal sums the waits.
type WaitsTotal struct {
	CIS             int64 `json:"ci_s"`
	OwnerBlockedS   int64 `json:"owner_blocked_s"`
	InternalBlocked int64 `json:"internal_blocked_s"`
}

// Total is the whole span of a task lifecycle.
type Total struct {
	Span
	Open bool
}

// MarshalJSON writes start, end, seconds, open.
func (t Total) MarshalJSON() ([]byte, error) {
	return ordered(kv{"start", t.Start}, kv{"end", t.End}, kv{"seconds", t.Seconds}, kv{"open", t.Open})
}

// Lifecycle is queries.task_lifecycle's result.
type Lifecycle struct {
	TaskID     string     `json:"task_id"`
	Stages     []Stage    `json:"stages"`
	Waits      []Wait     `json:"waits"`
	WaitsTotal WaitsTotal `json:"waits_total"`
	Total      Total      `json:"total"`
	GapsS      *int64     `json:"gaps_s"`
}

var (
	blockedRe = regexp.MustCompile(`\bstatus=blocked\b`)
	waitsRe   = regexp.MustCompile(`waits=(\w+)`)
	anyStatus = regexp.MustCompile(`\bstatus=\w+`)
)

type tsEv struct{ ts, actor, action, detail string }

// TaskLifecycle is queries.task_lifecycle: ordered stages of one task from idea to merge (None = unknown, never a
// guess). It returns nil for an unknown task.
func (c *Conn) TaskLifecycle(tid string, now time.Time) (*Lifecycle, error) {
	var created, tstatus string
	err := c.QueryRow("SELECT created, status FROM tasks WHERE id=?", tid).Scan(&created, &tstatus)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var evs []tsEv
	err = c.each("SELECT ts, actor, action, detail FROM events WHERE entity='task' AND entity_id=? ORDER BY id", []any{tid}, func(r rowScanner) error {
		var e tsEv
		var actor, detail sql.NullString
		if err := r.Scan(&e.ts, &actor, &e.action, &detail); err != nil {
			return err
		}
		e.actor, e.detail = actor.String, detail.String
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	type vd struct {
		role, verdict, created string
		head                   *string
		e                      float64
	}
	var rv []vd
	var allVerdicts []vd
	err = c.each("SELECT role, verdict, head_sha, created FROM verdicts WHERE task_id=? ORDER BY id", []any{tid}, func(r rowScanner) error {
		var v vd
		var head sql.NullString
		if err := r.Scan(&v.role, &v.verdict, &head, &v.created); err != nil {
			return err
		}
		v.head = nsp(head)
		allVerdicts = append(allVerdicts, v)
		if e, ok := epoch(v.created); ok && (v.role == "reviewer" || v.role == "qa") {
			v.e = e
			rv = append(rv, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var firstSpawn *string
	err = c.each("SELECT started FROM agents WHERE task_id=? AND role='coder' AND started IS NOT NULL", []any{tid}, func(r rowScanner) error {
		var s string
		if err := r.Scan(&s); err != nil {
			return err
		}
		if e, ok := epoch(s); ok {
			if firstSpawn == nil {
				firstSpawn = sp(s)
			} else if cur, _ := epoch(*firstSpawn); e < cur {
				firstSpawn = sp(s)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var merged *string
	for _, e := range evs {
		if ep, ok := epoch(e.ts); ok && e.action == "merged" {
			if merged == nil {
				merged = sp(e.ts)
			} else if cur, _ := epoch(*merged); ep > cur {
				merged = sp(e.ts)
			}
		}
	}
	type up struct {
		ts, detail string
		e          float64
	}
	var ups []up
	for _, e := range evs {
		if ep, ok := epoch(e.ts); ok && e.action == "update" {
			ups = append(ups, up{e.ts, e.detail, ep})
		}
	}
	// extreme returns the first element with the smallest (min) or largest (max) epoch, like Python's min/max.
	pickTS := func(cands []up, wantMax bool) *string {
		var best *up
		for i := range cands {
			if best == nil || (wantMax && cands[i].e > best.e) || (!wantMax && cands[i].e < best.e) {
				best = &cands[i]
			}
		}
		if best == nil {
			return nil
		}
		return sp(best.ts)
	}
	var stagesOut []Stage
	var specStart *string
	for _, e := range evs {
		if e.action != "add" {
			continue
		}
		var ptask sql.NullString
		err := c.QueryRow("SELECT task_id FROM agents WHERE name=? AND role='pm' AND task_id IS NOT NULL", e.actor).Scan(&ptask)
		if err == nil && e.actor != "" {
			var pcreated string
			err2 := c.QueryRow("SELECT created FROM tasks WHERE id=?", ptask.String).Scan(&pcreated)
			if err2 == nil {
				specStart = sp(pcreated)
				stagesOut = append(stagesOut, Stage{Stage: "spec", Span: mkSpan(specStart, sp(created))})
			} else if err2 != sql.ErrNoRows {
				return nil, err2
			}
		} else if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		break
	}
	stagesOut = append(stagesOut, Stage{Stage: "queued", Span: mkSpan(sp(created), firstSpawn)})
	var prOpenC, roundC []up
	for _, u := range ups {
		if strings.Contains(u.detail, "pr=") || strings.Contains(u.detail, "status=in_review") {
			prOpenC = append(prOpenC, u)
		}
		if strings.Contains(u.detail, "status=in_review") {
			roundC = append(roundC, u)
		}
	}
	stagesOut = append(stagesOut, Stage{Stage: "coding", Span: mkSpan(firstSpawn, pickTS(prOpenC, false))})
	sort.SliceStable(roundC, func(i, j int) bool { return roundC[i].e < roundC[j].e })
	for i, r0 := range roundC {
		var last *vd
		for k := range rv {
			v := &rv[k]
			if v.e >= r0.e && (i+1 >= len(roundC) || v.e < roundC[i+1].e) {
				if last == nil || v.e > last.e {
					last = v
				}
			}
		}
		var end, head *string
		if last != nil {
			end, head = sp(last.created), first8(last.head)
		}
		stagesOut = append(stagesOut, Stage{Stage: "review", Span: mkSpan(sp(r0.ts), end), HasHead: true, Head: head})
	}
	var lastPass *string
	var lp float64
	for _, v := range rv {
		if v.verdict == "pass" && (lastPass == nil || v.e > lp) {
			lastPass, lp = sp(v.created), v.e
		}
	}
	stagesOut = append(stagesOut, Stage{Stage: "judge", Span: mkSpan(lastPass, merged)})

	waits := []Wait{}
	type tsE struct {
		ts string
		e  float64
	}
	var autos []tsE
	for _, e := range evs {
		if ep, ok := epoch(e.ts); ok && e.action == "review-autospawn" {
			autos = append(autos, tsE{e.ts, ep})
		}
	}
	var prevEnd *string
	havePrev := false
	for _, e := range evs {
		ep, ok := epoch(e.ts)
		if e.action != "review-autospawn-wait" || !ok {
			continue
		}
		var nxt *string
		var best float64
		for _, a := range autos {
			if a.e >= ep && (nxt == nil || a.e < best) {
				nxt, best = sp(a.ts), a.e
			}
		}
		if havePrev && prevEnd != nil && nxt != nil && *nxt == *prevEnd {
			continue
		}
		prevEnd, havePrev = nxt, true
		waits = append(waits, Wait{Kind: "ci", Span: mkSpan(sp(e.ts), nxt)})
	}
	for i, u := range ups {
		if !blockedRe.MatchString(u.detail) {
			continue
		}
		who := "internal"
		if m := waitsRe.FindStringSubmatch(u.detail); m != nil {
			who = m[1]
		}
		var nxt *string
		for _, u2 := range ups[i+1:] {
			if anyStatus.MatchString(u2.detail) {
				nxt = sp(u2.ts)
				break
			}
		}
		if nxt == nil && tstatus == "blocked" {
			nxt = sp(isoSeconds(nowOr(now)))
		}
		waits = append(waits, Wait{Kind: "blocked", Waits: who, Span: mkSpan(sp(u.ts), nxt)})
	}
	var t0 *string
	var t0e float64
	for _, s := range []*string{specStart, sp(created)} {
		if e, ok := epochP(s); ok && (t0 == nil || e < t0e) {
			t0, t0e = s, e
		}
	}
	end := merged
	if end == nil {
		end = sp(isoSeconds(nowOr(now)))
	}
	total := Total{Span: mkSpan(t0, end), Open: merged == nil}
	var gaps *int64
	allSec := total.Seconds != nil
	var spans []span2
	for _, s := range stagesOut {
		if s.Span.Seconds == nil {
			allSec = false
			break
		}
		a, _ := epochP(s.Span.Start)
		b, _ := epochP(s.Span.End)
		spans = append(spans, span2{a, b})
	}
	if allSec {
		v := pyRound(float64(*total.Seconds) - unionLen(spans))
		gaps = &v
	}
	var sums WaitsTotal
	for _, w := range waits {
		if w.Span.Seconds == nil {
			continue
		}
		switch {
		case w.Kind == "ci":
			sums.CIS += *w.Span.Seconds
		case w.Waits == "owner":
			sums.OwnerBlockedS += *w.Span.Seconds
		default:
			sums.InternalBlocked += *w.Span.Seconds
		}
	}
	return &Lifecycle{TaskID: tid, Stages: stagesOut, Waits: waits, WaitsTotal: sums, Total: total, GapsS: gaps}, nil
}

func (c *Conn) taskGoals() (map[string]string, error) {
	out := map[string]string{}
	err := c.each("SELECT id, goal_id FROM tasks", nil, func(r rowScanner) error {
		var id, g string
		if err := r.Scan(&id, &g); err != nil {
			return err
		}
		out[id] = g
		return nil
	})
	return out, err
}

// GoalSet is a set of goal ids (the org filter); nil means no filter.
type GoalSet map[string]bool

func (g GoalSet) has(id string) bool { return g == nil || g[id] }

// DayCount is one day of merges_per_day.
type DayCount struct {
	Day    string `json:"day"`
	Merges int64  `json:"merges"`
}

// GoalDone is a row of Throughput's tasks_per_goal.
type GoalDone struct {
	GoalID string  `json:"goal_id"`
	Title  *string `json:"title"`
	Done   int64   `json:"done"`
}

// ThroughputResult is queries.throughput's result.
type ThroughputResult struct {
	Days         int        `json:"days"`
	MergesPerDay []DayCount `json:"merges_per_day"`
	TasksPerGoal []GoalDone `json:"tasks_per_goal"`
	Tasks        []Stages   `json:"tasks"`
}

func dayList(first time.Time, days int) []string {
	out := make([]string, days)
	for i := 0; i < days; i++ {
		out[i] = time.Date(first.Year(), first.Month(), first.Day()+i, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
	}
	return out
}

func (c *Conn) lastMerged(keep func(string) bool) (map[string]float64, error) {
	last := map[string]float64{}
	err := c.each("SELECT entity_id, ts FROM events WHERE entity='task' AND action='merged' ORDER BY id", nil, func(r rowScanner) error {
		var id string
		var ts string
		if err := r.Scan(&id, &ts); err != nil {
			return err
		}
		if keep != nil && !keep(id) {
			return nil
		}
		if e, ok := epoch(ts); ok {
			last[id] = e
		}
		return nil
	})
	return last, err
}

// Throughput is queries.throughput: merges per local day, done tasks per goal and per merged task stage durations.
func (c *Conn) Throughput(days int, now time.Time, goals GoalSet) (*ThroughputResult, error) {
	nowD := nowOr(now).In(Loc)
	firstDay := time.Date(nowD.Year(), nowD.Month(), nowD.Day()-(days-1), 0, 0, 0, 0, Loc)
	cutoff := tsec(firstDay)
	tgoal, err := c.taskGoals()
	if err != nil {
		return nil, err
	}
	last, err := c.lastMerged(nil)
	if err != nil {
		return nil, err
	}
	dl := dayList(firstDay, days)
	perDay := map[string]int64{}
	win := map[string]float64{}
	for tid, e := range last {
		if goals != nil && !goals[tgoal[tid]] {
			continue
		}
		if e >= cutoff {
			d := localDay(e)
			if contains(dl, d) {
				perDay[d]++
			}
			win[tid] = e
		}
	}
	ids := make([]string, 0, len(win))
	for id := range win {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	evs := map[string][]updEv{}
	vds := map[string][]vdRow{}
	sps := map[string][]struct {
		role string
		t    float64
	}{}
	err = c.inRows("SELECT entity_id, ts, action, detail FROM events WHERE entity='task' AND action='update' AND entity_id IN ({in}) ORDER BY id", ids, func(r rowScanner) error {
		var id, ts string
		var action string
		var d sql.NullString
		if err := r.Scan(&id, &ts, &action, &d); err != nil {
			return err
		}
		if e, ok := epoch(ts); ok {
			evs[id] = append(evs[id], updEv{e, d.String})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = c.inRows("SELECT task_id, role, verdict, created FROM verdicts WHERE task_id IN ({in}) ORDER BY id", ids, func(r rowScanner) error {
		var id, role, verdict, created string
		if err := r.Scan(&id, &role, &verdict, &created); err != nil {
			return err
		}
		e, ok := epoch(created)
		vds[id] = append(vds[id], vdRow{role, verdict, e, ok})
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = c.inRows("SELECT task_id, role, started FROM agents WHERE started IS NOT NULL AND task_id IN ({in})", ids, func(r rowScanner) error {
		var id, role, started string
		if err := r.Scan(&id, &role, &started); err != nil {
			return err
		}
		if e, ok := epoch(started); ok {
			sps[id] = append(sps[id], struct {
				role string
				t    float64
			}{role, e})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	meta := map[string]string{}
	err = c.inRows("SELECT id, goal_id, title FROM tasks WHERE id IN ({in})", ids, func(r rowScanner) error {
		var id, g, t string
		if err := r.Scan(&id, &g, &t); err != nil {
			return err
		}
		meta[id] = g
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := &ThroughputResult{Days: days, MergesPerDay: []DayCount{}, TasksPerGoal: []GoalDone{}, Tasks: []Stages{}}
	for _, tid := range ids {
		st := stages(win[tid], evs[tid], vds[tid], sps[tid])
		st.TaskID = tid
		if g, ok := meta[tid]; ok {
			st.GoalID = sp(g)
		}
		st.Merged = isoSeconds(time.Unix(int64(math.Floor(win[tid])), 0).In(Loc))
		res.Tasks = append(res.Tasks, st)
	}
	err = c.each("SELECT t.goal_id, g.title, COUNT(*) done FROM tasks t LEFT JOIN goals g ON g.id=t.goal_id "+
		"WHERE t.status='done' GROUP BY t.goal_id ORDER BY t.goal_id", nil, func(r rowScanner) error {
		var g GoalDone
		var title sql.NullString
		if err := r.Scan(&g.GoalID, &title, &g.Done); err != nil {
			return err
		}
		g.Title = nsp(title)
		if goals == nil || goals[g.GoalID] {
			res.TasksPerGoal = append(res.TasksPerGoal, g)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, d := range dl {
		res.MergesPerDay = append(res.MergesPerDay, DayCount{d, perDay[d]})
	}
	return res, nil
}

// MergesPerDay is queries.merges_per_day: merges per local day for the last days days, oldest first; a task counts
// once, on the day of its last merge.
func (c *Conn) MergesPerDay(days int, now time.Time, goals GoalSet) ([]DayCount, error) {
	nowD := nowOr(now).In(Loc)
	first := time.Date(nowD.Year(), nowD.Month(), nowD.Day()-(days-1), 0, 0, 0, 0, Loc)
	var keep func(string) bool
	if goals != nil {
		tg, err := c.taskGoals()
		if err != nil {
			return nil, err
		}
		keep = func(id string) bool { g, ok := tg[id]; return ok && goals[g] }
	}
	last, err := c.lastMerged(keep)
	if err != nil {
		return nil, err
	}
	dl := dayList(first, days)
	per := map[string]int64{}
	for _, e := range last {
		per[localDay(e)]++
	}
	out := []DayCount{}
	for _, d := range dl {
		out = append(out, DayCount{d, per[d]})
	}
	return out, nil
}

var taskIDRe = regexp.MustCompile(`T-\d+`)

// AttendedTasks is queries.attended_tasks: the subset of ids that had the owner involved (an update event with
// waits=owner, a judge-blocked event with auto_merge=0, or a boss decision message naming the task).
func (c *Conn) AttendedTasks(ids []string) (map[string]bool, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var uniq []string
	for id := range want {
		uniq = append(uniq, id)
	}
	sort.Strings(uniq)
	out := map[string]bool{}
	err := c.inRows("SELECT entity_id, action, detail FROM events WHERE entity='task' AND entity_id IN ({in}) AND action IN ('update','judge-blocked')", uniq, func(r rowScanner) error {
		var id, action string
		var d sql.NullString
		if err := r.Scan(&id, &action, &d); err != nil {
			return err
		}
		if (action == "update" && strings.Contains(d.String, "waits=owner")) || (action == "judge-blocked" && strings.Contains(d.String, "auto_merge=0")) {
			out[id] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	bosses := bossNames(false)
	err = c.each("SELECT task_id, subject, body FROM messages WHERE kind='decision' AND sender IN ("+placeholders(len(bosses))+")", strArgs(bosses), func(r rowScanner) error {
		var tid, subj, body sql.NullString
		if err := r.Scan(&tid, &subj, &body); err != nil {
			return err
		}
		if tid.Valid {
			out[tid.String] = true
		}
		for _, id := range wholeWordTaskIDs(subj.String + " " + body.String) {
			out[id] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := map[string]bool{}
	for id := range out {
		if want[id] {
			res[id] = true
		}
	}
	return res, nil
}

// wholeWordTaskIDs is re.findall(r"(?<![\w-])T-\d+(?!\w)", s): T-11 does not name T-111, and a T-n glued to a
// word or hyphen on either side is not a task id.
func wholeWordTaskIDs(s string) []string {
	var out []string
	for _, loc := range taskIDRe.FindAllStringIndex(s, -1) {
		before := []rune(s[:loc[0]])
		if n := len(before); n > 0 && (isWord(before[n-1]) || before[n-1] == '-') {
			continue
		}
		if rest := []rune(s[loc[1]:]); len(rest) > 0 && isWord(rest[0]) {
			continue
		}
		out = append(out, s[loc[0]:loc[1]])
	}
	return out
}

var _ = fmt.Sprint
