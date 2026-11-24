package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/identity"
)

const spendSpikeJSON = `{"total":{"key":"all","requests":9,"cost_usd":30},"sessions":1,"top_sessions":[{"key":"s1","project":"p","requests":20,"cost_usd":30,"avg_context_tokens":300000}],"findings":[]}`

func runSpendAlert(e *ft.Env, who string, args ...string) (code int, out, errs string) {
	m := map[string]string{"FACTORY_HOME": e.Home, "HOME": e.UserHome, "FACTORY_PROC_DIR": e.ProcDir, "FACTORY_AGENT": who, "TZ": "UTC"}
	var o, eb bytes.Buffer
	code = Run(Deps{
		Args: append([]string{"spend-alert"}, args...), Argv0: "bin/factory", Environ: []string{"FACTORY_CLI_IMPL=go"},
		Env: identity.Env{
			Getenv:    func(k string) string { return m[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := m[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return e.UserHome, nil },
			Now:       time.Now,
		},
		Stdout: &o, Stderr: &eb, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { return nil },
	})
	return code, o.String(), eb.String()
}

func TestSpendAlertCmdOwnerSendsOneEscalation(t *testing.T) {
	orig := spendAlertRun
	spendAlertRun = func(context.Context, int) ([]byte, error) { return []byte(spendSpikeJSON), nil }
	defer func() { spendAlertRun = orig }()
	e := ft.New(t)
	code, out, errs := runSpendAlert(e, "owner")
	if code != 0 || !strings.Contains(out, "spend-alert: sent to claude-boss") {
		t.Fatalf("exit %d, out %q err %q", code, out, errs)
	}
	if n := e.Count("SELECT COUNT(*) FROM messages WHERE kind='escalation' AND recipient='claude-boss'"); n != 1 {
		t.Fatalf("%d escalations", n)
	}
	// a second run inside 15 minutes sends nothing
	runSpendAlert(e, "owner")
	if n := e.Count("SELECT COUNT(*) FROM messages WHERE kind='escalation'"); n != 1 {
		t.Fatalf("%d escalations after the second run", n)
	}
}

func TestSpendAlertCmdRefusesNonOwnerAndArguments(t *testing.T) {
	e := ft.New(t)
	if code, _, errs := runSpendAlert(e, "zz-coder"); code != 2 || !strings.Contains(errs, "owner only") {
		t.Fatalf("non-owner: exit %d err %q", code, errs)
	}
	if code, _, errs := runSpendAlert(e, "owner", "now"); code != 2 || !strings.Contains(errs, "usage: factory-go spend-alert") {
		t.Fatalf("argument: exit %d err %q", code, errs)
	}
}
