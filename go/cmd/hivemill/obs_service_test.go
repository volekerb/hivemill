package main

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"factory/go/internal/hm"
)

func TestLaunchdObsPlist(t *testing.T) {
	c, home := standalone(t)
	out := c.ok("launchd", "obs", "--home", home, "--path", "/opt/zz/bin:/usr/bin")
	for _, want := range []string{"<string>hivemill.obs</string>", `hivemill" obs`,
		"<key>FACTORY_HOME</key>\n\t\t<string>" + home + "</string>", "<string>/opt/zz/bin:/usr/bin</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>", "hivemill.obs.log"} {
		if !strings.Contains(strings.ReplaceAll(out, "    ", "\t"), want) {
			t.Errorf("obs plist lacks %q:\n%s", want, out)
		}
	}
}

func TestHealthFlagsObsDown(t *testing.T) {
	c, home := standalone(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	c.ok("config", "obs_port", port)
	c.ok("config", "alert_cmd", "true")
	r, err := hm.NewRunner(hm.Env{Home: home, UserHome: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Store.Close()
	if err := r.Heartbeat(time.Now()); err != nil {
		t.Fatal(err)
	}
	if out := c.ok("health", "--alert"); strings.Contains(out, "obs") {
		t.Errorf("obs answers, health says: %q", out)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, errs := c.run("", "health", "--alert")
	if code != 1 || !strings.Contains(out, "problem: hivemill obs does not answer on 127.0.0.1:"+port) || !strings.Contains(out, "obs alert sent") {
		t.Errorf("obs down: exit %d\n%s%s", code, out, errs)
	}
	if code, out, _ := c.run("", "health", "--alert"); code != 1 || strings.Contains(out, "alert sent") {
		t.Errorf("second check: exit %d, one alert per alert_every_s: %q", code, out)
	}
	if code, out, _ := c.run("", "health"); code != 1 || !strings.Contains(out, "obs does not answer") {
		t.Errorf("plain health: exit %d %q", code, out)
	}
}
