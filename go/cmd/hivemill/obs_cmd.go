package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/hm"
)

const obsUsage = "usage: hivemill obs [--port N] [--json] [--days N] [--machine M] [--harness H] | " +
	"obs pull --from HOST:PATH [--from ...] [--as NAME] | obs sources"

const otelUsage = "usage: hivemill otel export [--since 24h|YYYY-MM-DD] [--endpoint URL] [--pulled] [--dry-run]"

// cmdObs: the read-only page, the JSON portfolio, and pulling other machines' event logs.
func cmdObs(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "pull" {
		fs := flag.NewFlagSet("obs pull", flag.ContinueOnError)
		fs.SetOutput(stderr)
		var from multi
		fs.Var(&from, "from", "HOST:PATH (ssh, read-only) or a local file; repeatable")
		as := fs.String("as", "", "machine label (default: the host)")
		if fs.Parse(args[1:]) != nil || len(from) == 0 || fs.NArg() > 0 || (*as != "" && len(from) > 1) {
			fmt.Fprintln(stderr, obsUsage)
			return 2
		}
		code := 0
		for _, f := range from {
			label, n, err := r.ObsPull(f, *as, nil, time.Now())
			if err != nil {
				fmt.Fprintf(stderr, "hivemill: obs pull %s: %v\n", f, err)
				code = 1
				continue
			}
			fmt.Fprintf(stdout, "%s: %d events from %s\n", label, n, f)
		}
		return code
	}
	if len(args) > 0 && args[0] == "sources" {
		for _, s := range r.ObsSources(true) {
			fmt.Fprintf(stdout, "%s\t%s\tpulled=%s\t%s\n", s.Label, s.Path, or(s.Pulled, "-"), or(s.From, "local"))
		}
		return 0
	}
	fs := flag.NewFlagSet("obs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "port on 127.0.0.1 (default: config obs_port)")
	asJSON := fs.Bool("json", false, "print the portfolio as JSON and exit")
	days := fs.Int("days", 30, "outcome window in days")
	machine := fs.String("machine", "", "only this machine")
	harness := fs.String("harness", "", "only this harness")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, obsUsage)
		return 2
	}
	if *asJSON {
		o, err := r.LoadObs(hm.ObsFilter{Machine: *machine, Harness: *harness}, true, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, "hivemill:", err)
			return 1
		}
		asks, _ := r.OpenAsks()
		b, _ := json.MarshalIndent(map[string]any{"filter": o.Filter, "days": *days, "projects": o.Portfolio(*days, asks),
			"machines": o.Machines, "harnesses": o.Harnesses, "last_seen": o.LastSeen}, "", " ")
		fmt.Fprintf(stdout, "%s\n", b)
		return 0
	}
	if *port == 0 {
		*port, _ = strconv.Atoi(strings.TrimSpace(r.Store.Cfg("obs_port")))
	}
	if *port <= 0 || *port > 65535 {
		fmt.Fprintln(stderr, "hivemill: obs: bad port (config obs_port)")
		return 2
	}
	addr := hm.ObsAddr(*port)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	fmt.Fprintf(stdout, "hivemill obs: http://%s/ (read-only; Ctrl-C stops)\n", addr)
	if err := r.ServeObs(ctx, addr); err != nil {
		fmt.Fprintln(stderr, "hivemill: obs:", err)
		return 1
	}
	return 0
}

// cmdOtel: `otel export` sends tasks as OTLP/HTTP JSON traces.
func cmdOtel(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprintln(stderr, otelUsage)
		return 2
	}
	fs := flag.NewFlagSet("otel export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "", "tasks with an event in this window: a duration (24h, 168h) or a date (YYYY-MM-DD)")
	endpoint := fs.String("endpoint", "", "OTLP/HTTP base URL (default: config otel_endpoint, then OTEL_EXPORTER_OTLP_ENDPOINT)")
	pulled := fs.Bool("pulled", false, "also export the logs pulled from other machines")
	dry := fs.Bool("dry-run", false, "print the request bodies, send nothing")
	if fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, otelUsage)
		return 2
	}
	o := hm.OtelOpts{Pulled: *pulled, Headers: hm.OTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))}
	if *since != "" {
		t, err := parseSince(*since, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, "hivemill:", err)
			return 2
		}
		o.Since = t
	}
	o.Endpoint = strings.TrimSpace(*endpoint)
	for _, v := range []string{r.Store.Cfg("otel_endpoint"), os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")} {
		if o.Endpoint == "" && strings.TrimSpace(v) != "" && v != "off" {
			o.Endpoint = strings.TrimSpace(v)
		}
	}
	if *dry {
		o.DryRun = stdout
	} else if o.Endpoint == "" {
		fmt.Fprintln(stderr, "hivemill: otel export: no endpoint (--endpoint, config otel_endpoint or OTEL_EXPORTER_OTLP_ENDPOINT)")
		return 2
	}
	n, err := r.OtelExport(o)
	if err != nil {
		fmt.Fprintln(stderr, "hivemill: otel export:", err)
		return 1
	}
	if !*dry {
		fmt.Fprintf(stdout, "exported %d traces to %s\n", n, hm.TracesURL(o.Endpoint))
	}
	return 0
}

func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: use a duration (24h) or a date (YYYY-MM-DD)", s)
}
