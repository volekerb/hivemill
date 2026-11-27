package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHubWithoutRunnerNeedsBothFlags(t *testing.T) {
	for args, want := range map[string]bool{
		"--hub --peers http://192.0.2.10:7461": true,
		"--peers=http://192.0.2.10:7461 --hub": true,
		"--hub":                                false,
		"--peers http://192.0.2.10:7461":       false,
		"--port 7461":                          false,
	} {
		if got := hubWithoutRunner(strings.Fields(args)); got != want {
			t.Errorf("%q: %v, want %v", args, got, want)
		}
	}
}

func TestObsHubRefusesBadPeersAndPorts(t *testing.T) {
	for _, args := range [][]string{
		{"--hub"}, // no peers anywhere
		{"--hub", "--peers", "ftp://192.0.2.10"},
		{"--hub", "--peers", "http://192.0.2.10:7461/x"},
		{"--hub", "--peers", "http://192.0.2.10:7461", "--port", "70000"},
		{"--hub", "--peers", "http://192.0.2.10:7461", "--listen", "example.com"},
		{"--hub", "--peers", "http://192.0.2.10:7461", "extra"},
	} {
		var out, errb bytes.Buffer
		if rc := cmdObsHub(args, nil, &out, &errb); rc != 2 || errb.Len() == 0 || out.Len() != 0 {
			t.Errorf("%v: rc=%d stderr=%q", args, rc, errb.String())
		}
	}
	// the peers of this machine's factory are used when none is on the command line
	var out, errb bytes.Buffer
	cfg := func(k string) string { return map[string]string{"obs_peers": "not a url", "obs_port": "7461"}[k] }
	if rc := cmdObsHub([]string{"--hub"}, cfg, &out, &errb); rc != 2 || !strings.Contains(errb.String(), "obs peer") {
		t.Errorf("config peers: rc=%d %q", rc, errb.String())
	}
}
