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

const obsUsage = "usage: hivemill obs [--listen HOST] [--port N] [--json] [--days N] [--machine M] [--harness H] | " +
	"obs --hub [--peers URL,URL] [--listen HOST] [--port N] | obs pull --from HOST:PATH [--from ...] [--as NAME] | obs sources"

// hubWithoutRunner reports whether the arguments ask for a hub whose peers are all on the command line: that one
// needs no factory on this machine.
func hubWithoutRunner(args []string) bool {
	hub, peers := false, false
	for _, a := range args {
		hub = hub || a == "--hub" || a == "-hub"
		peers = peers || a == "--peers" || a == "-peers" || strings.HasPrefix(a, "--peers=") || strings.HasPrefix(a, "-peers=")
	}
	return hub && peers
}

// cmdObsHub serves only the All factories page over the peers: `hivemill obs --hub`. cfg reads a config key of this
// machine's factory, or is nil when there is none.
func cmdObsHub(args []string, cfg func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("obs --hub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_ = fs.Bool("hub", true, "serve only the All factories page")
	peersFlag := fs.String("peers", "", "the factories' obs URLs, comma-separated (default: config obs_peers)")
	listen := fs.String("listen", "", "bind IP address or localhost (default: config obs_listen, 127.0.0.1)")
	port := fs.Int("port", 0, "port (default: config obs_port, 7461)")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, obsUsage)
		return 2
	}
	get := func(k string) string {
		if cfg == nil {
			return ""
		}
		return cfg(k)
	}
	peers, err := hm.ParsePeers(or(*peersFlag, get("obs_peers")))
	if err != nil || len(peers) == 0 {
		if err == nil {
			err = fmt.Errorf("no factory to show: pass --peers URL,URL or set config obs_peers")
		}
		fmt.Fprintln(stderr, "hivemill: obs --hub:", err)
		return 2
	}
	if *port == 0 {
		*port, _ = strconv.Atoi(strings.TrimSpace(or(get("obs_port"), "7461")))
	}
	if *port <= 0 || *port > 65535 {
		fmt.Fprintln(stderr, "hivemill: obs: bad port (config obs_port)")
		return 2
	}
	host, err := hm.ObsListen(*listen, get("obs_listen"), stderr)
	if err != nil {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 2
	}
	addr := hm.ObsAddr(host, *port)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	fmt.Fprintf(stdout, "hivemill obs hub: http://%s/ over %d factor%s (read-only; Ctrl-C stops)\n", addr, len(peers), map[bool]string{true: "y", false: "ies"}[len(peers) == 1])
	if err := hm.ServeHub(ctx, addr, peers); err != nil {
		fmt.Fprintln(stderr, "hivemill: obs:", err)
		return 1
	}
	return 0
}

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
	for _, a := range args {
		if a == "--hub" || a == "-hub" {
			return cmdObsHub(args, r.Store.Cfg, stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("obs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "", "bind IP address or localhost (default: config obs_listen, 127.0.0.1)")
	port := fs.Int("port", 0, "port (default: config obs_port)")
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
		local, ok := r.OpenAsks()
		asks := o.OpenAsks(r.Machine(), local, ok)
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
	host, err := hm.ObsListen(*listen, r.Store.Cfg("obs_listen"), stderr)
	if err != nil {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 2
	}
	addr := hm.ObsAddr(host, *port)
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
