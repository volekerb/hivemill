package hm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OpenTelemetry export (docs/hivemill-observability.md): OTLP/HTTP with the JSON encoding, built by hand so the
// binary needs no SDK. A task is a trace (one root span), each session a child span, and every other event a span
// event on its session's span (gate, review, judge) or on the root (CI, refresh, merge, failure).

// otelBatch is the number of traces per POST.
const otelBatch = 50

func hexID(n int, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:n])
}

// TraceID is the task's trace id: deterministic, so exporting the same task again reuses its ids.
func TraceID(taskKey string) string { return hexID(16, "hivemill-trace", taskKey) }

func spanID(parts ...string) string { return hexID(8, append([]string{"hivemill-span"}, parts...)...) }

func nanos(ts string) string {
	t := parseTS(ts)
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

func kv(k string, v any) map[string]any {
	var val map[string]any
	switch x := v.(type) {
	case string:
		val = map[string]any{"stringValue": x}
	case bool:
		val = map[string]any{"boolValue": x}
	case int:
		val = map[string]any{"intValue": strconv.Itoa(x)}
	case int64:
		val = map[string]any{"intValue": strconv.FormatInt(x, 10)}
	case float64:
		val = map[string]any{"doubleValue": x}
	default:
		b, _ := json.Marshal(x)
		val = map[string]any{"stringValue": truncate(string(b), 1000)}
	}
	return map[string]any{"key": k, "value": val}
}

// attrs drops empty strings, so a span never carries "project": "".
func attrs(pairs ...any) []map[string]any {
	out := []map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if s, ok := pairs[i+1].(string); ok && s == "" {
			continue
		}
		out = append(out, kv(pairs[i].(string), pairs[i+1]))
	}
	return out
}

func eventAttrs(e Event) []map[string]any {
	out := attrs("hivemill.role", e.Role, "hivemill.session_id", e.Session)
	ks := make([]string, 0, len(e.Data))
	for k := range e.Data {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		v := e.Data[k]
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			v = int64(f)
		}
		out = append(out, kv("hivemill."+k, v))
	}
	return out
}

func usageAttrs(u SessUsage, known bool) []map[string]any {
	if !known {
		return attrs("hivemill.cost_known", false)
	}
	return attrs("hivemill.usd", u.USD, "hivemill.tokens", u.Tokens(), "gen_ai.usage.input_tokens", u.Input,
		"gen_ai.usage.output_tokens", u.Output, "hivemill.tokens.cache_read", u.CacheRead, "hivemill.tokens.cache_write",
		u.CacheWrite)
}

func outcomeStatus(outcome string) map[string]any {
	switch {
	case outcome == "merged":
		return map[string]any{"code": 1}
	case strings.HasPrefix(outcome, "FAIL"):
		return map[string]any{"code": 2, "message": outcome}
	}
	return map[string]any{"code": 0}
}

// TraceSpans is the spans of one task: the root, then one span per session.
func (o *Obs) TraceSpans(t *ObsTask) []map[string]any {
	trace := TraceID(t.Key)
	root := spanID(t.Key, "task")
	end := or(t.Terminal, t.Last)
	sessSpan := map[string]map[string]any{}
	var spans []map[string]any
	rootSpan := map[string]any{"traceId": trace, "spanId": root, "name": "task " + t.Task, "kind": 1,
		"startTimeUnixNano": nanos(t.Start), "endTimeUnixNano": nanos(end), "status": outcomeStatus(t.Row.Outcome),
		"attributes": append(attrs("hivemill.task_id", t.Task, "hivemill.goal_id", t.Goal, "hivemill.project", t.Project,
			"hivemill.machine", t.Machine, "hivemill.harness", t.Harness, "hivemill.outcome", t.Row.Outcome,
			"hivemill.rounds", t.Rounds, "hivemill.gate_blocks", t.Row.GateBlocks, "hivemill.ci_rounds", t.Row.CIRounds,
			"hivemill.pr", t.PR, "hivemill.eval", t.Row.Eval), usageAttrs(o.taskUsage(t), true)...)}
	spans = append(spans, rootSpan)
	for _, s := range o.TaskSessions(t) {
		send := s.End
		if send == "" || parseTS(send).Before(parseTS(s.Start)) {
			send = end
			if n := len(s.Events); n > 0 && parseTS(s.Events[n-1].TS).After(parseTS(s.Start)) {
				send = s.Events[n-1].TS
			}
		}
		if parseTS(send).Before(parseTS(s.Start)) {
			send = s.Start
		}
		sp := map[string]any{"traceId": trace, "spanId": spanID(t.Key, "session", s.ID), "parentSpanId": root,
			"name": "session " + or(s.Role, "agent"), "kind": 1, "startTimeUnixNano": nanos(s.Start), "endTimeUnixNano": nanos(send),
			"attributes": append(attrs("hivemill.session_id", s.ID, "hivemill.role", s.Role, "hivemill.model", s.Model,
				"gen_ai.request.model", s.Model, "hivemill.effort", s.Effort, "hivemill.harness", s.Harness, "hivemill.machine",
				s.Machine, "hivemill.task_id", t.Task, "hivemill.goal_id", t.Goal, "hivemill.project", t.Project),
				usageAttrs(s.Usage, s.CostKnown)...),
			"events": []map[string]any{}}
		sessSpan[s.ID] = sp
		spans = append(spans, sp)
	}
	for _, e := range t.Events {
		if e.Event == "session.started" || e.Event == "session.ended" {
			continue
		}
		target := rootSpan
		if sp := sessSpan[e.Session]; sp != nil {
			target = sp
		}
		evs, _ := target["events"].([]map[string]any)
		target["events"] = append(evs, map[string]any{"timeUnixNano": nanos(e.TS), "name": e.Event, "attributes": eventAttrs(e)})
	}
	if _, ok := rootSpan["events"]; !ok {
		rootSpan["events"] = []map[string]any{}
	}
	return spans
}

func (o *Obs) taskUsage(t *ObsTask) SessUsage {
	var u SessUsage
	for _, s := range o.TaskSessions(t) {
		u.USD += s.Usage.USD
		u.Input += s.Usage.Input
		u.Output += s.Usage.Output
		u.CacheRead += s.Usage.CacheRead
		u.CacheWrite += s.Usage.CacheWrite
	}
	u.USD = round2(u.USD)
	return u
}

// OTLPRequest is one ExportTraceServiceRequest (JSON encoding) for the given tasks, one resource per machine.
func (o *Obs) OTLPRequest(tasks []*ObsTask) map[string]any {
	by := map[string][]map[string]any{}
	for _, t := range tasks {
		by[t.Machine] = append(by[t.Machine], o.TraceSpans(t)...)
	}
	var res []map[string]any
	for _, m := range setKeys(func() map[string]bool {
		x := map[string]bool{}
		for k := range by {
			x[k] = true
		}
		return x
	}()) {
		res = append(res, map[string]any{
			"resource":   map[string]any{"attributes": attrs("service.name", "hivemill", "host.name", m, "hivemill.machine", m)},
			"scopeSpans": []map[string]any{{"scope": map[string]any{"name": "hivemill", "version": "1"}, "spans": by[m]}},
		})
	}
	return map[string]any{"resourceSpans": res}
}

// TracesURL is the OTLP/HTTP traces URL of a base endpoint (…/v1/traces is added unless it is already there).
func TracesURL(endpoint string) string {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasSuffix(e, "/v1/traces") {
		return e
	}
	return e + "/v1/traces"
}

// OTLPHeaders parses OTEL_EXPORTER_OTLP_HEADERS ("k=v,k2=v2", values URL-encoded). Credentials live in the
// environment, never in the config table.
func OTLPHeaders(spec string) map[string]string {
	out := map[string]string{}
	for _, p := range strings.Split(spec, ",") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		if d, err := url.QueryUnescape(strings.TrimSpace(v)); err == nil {
			v = d
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// OTLPPost sends one request body.
func OTLPPost(c *http.Client, endpoint string, headers map[string]string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, TracesURL(endpoint), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("otlp: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// OtelOpts is one export.
type OtelOpts struct {
	Endpoint string
	Headers  map[string]string
	Since    time.Time // tasks with an event at or after this time (zero = all)
	Pulled   bool      // also the copies pulled from other machines
	DryRun   io.Writer // print the request bodies instead of sending them
	Client   *http.Client
}

// OtelExport sends every selected task as a trace and returns how many it sent.
func (r *Runner) OtelExport(o OtelOpts) (int, error) {
	obs, err := r.LoadObs(ObsFilter{}, o.Pulled, time.Now())
	if err != nil {
		return 0, err
	}
	var tasks []*ObsTask
	for _, t := range obs.TaskList("", "") {
		if o.Since.IsZero() || !parseTS(t.Last).Before(o.Since) {
			tasks = append(tasks, t)
		}
	}
	return len(tasks), sendTraces(obs, tasks, o)
}

func sendTraces(obs *Obs, tasks []*ObsTask, o OtelOpts) error {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Second}
	}
	for i := 0; i < len(tasks); i += otelBatch {
		j := min(i+otelBatch, len(tasks))
		b, err := json.Marshal(obs.OTLPRequest(tasks[i:j]))
		if err != nil {
			return err
		}
		if o.DryRun != nil {
			fmt.Fprintf(o.DryRun, "%s\n", b)
			continue
		}
		if err := OTLPPost(o.Client, o.Endpoint, o.Headers, b); err != nil {
			return err
		}
	}
	return nil
}

// otelState is the daemon tail's record: task key -> the terminal event already exported.
func (r *Runner) otelStatePath() string { return filepath.Join(r.Env.StateDir(), "otel-exported.json") }

// OtelTail is the daemon's export: with otel_endpoint set, every task of this machine that reached a terminal event
// since its last export goes out as a trace. A failure is printed and retried on the next pass; it never fails the pass.
func (r *Runner) OtelTail(now time.Time) {
	ep := strings.TrimSpace(r.Store.Cfg("otel_endpoint"))
	if ep == "" || ep == "off" {
		return
	}
	if err := r.otelTail(ep, now, &http.Client{Timeout: 5 * time.Second}); err != nil {
		r.printf("otel: %v\n", err)
	}
}

func (r *Runner) otelTail(endpoint string, now time.Time, c *http.Client) error {
	done := map[string]string{}
	if b, err := os.ReadFile(r.otelStatePath()); err == nil {
		_ = json.Unmarshal(b, &done)
	}
	// events only first: most passes have nothing to send and read no transcript
	evs, err := LoadObsEvents(r.ObsSources(false))
	if err != nil {
		return err
	}
	me := r.Machine()
	obs := BuildObs(evs, ObsFilter{}, me, nil, now)
	var ids []string
	due := 0
	for _, t := range obs.TaskList("", "") {
		if t.Terminal != "" && done[t.Key] != t.Terminal {
			ids, due = append(ids, t.Sessions...), due+1
		}
	}
	if due == 0 {
		return nil
	}
	obs = BuildObs(evs, ObsFilter{}, me, SessionUsages(r.Env.UserHome, ids), now)
	var todo []*ObsTask
	for _, t := range obs.TaskList("", "") {
		if t.Terminal != "" && done[t.Key] != t.Terminal {
			todo = append(todo, t)
		}
	}
	hdr := OTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	for i := 0; i < len(todo); i += otelBatch {
		j := min(i+otelBatch, len(todo))
		if err := sendTraces(obs, todo[i:j], OtelOpts{Endpoint: endpoint, Headers: hdr, Client: c}); err != nil {
			return err
		}
		for _, t := range todo[i:j] {
			done[t.Key] = t.Terminal
		}
		b, _ := json.Marshal(done)
		if err := writeFile(r.otelStatePath(), string(b), 0o600); err != nil {
			return err
		}
	}
	return nil
}
