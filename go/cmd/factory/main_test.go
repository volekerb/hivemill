package main

import (
	"path/filepath"
	"testing"
	"time"

	"factory/go/internal/identity"
)

func clockEnv(t *testing.T, vars map[string]string) identity.Env {
	t.Helper()
	pw := t.TempDir()
	return identity.Env{
		Getenv:    func(k string) string { return vars[k] },
		LookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		PwDir:     func(int) (string, error) { return pw, nil },
	}
}

func TestTestClock(t *testing.T) {
	const set = "2026-11-12T01:02:03+00:00"
	want := time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC)
	t.Run("set on a temp home: fixed time", func(t *testing.T) {
		env := clockEnv(t, map[string]string{"FACTORY_TEST_NOW": set, "FACTORY_HOME": t.TempDir()})
		now, err := testClock(env)
		if err != nil || !now().Equal(want) {
			t.Fatalf("got %v, %v; want %v", now(), err, want)
		}
	})
	t.Run("offset is kept", func(t *testing.T) {
		env := clockEnv(t, map[string]string{"FACTORY_TEST_NOW": "2026-11-12T02:02:03+01:00", "FACTORY_HOME": t.TempDir()})
		now, err := testClock(env)
		if err != nil || !now().Equal(want) {
			t.Fatalf("got %v, %v", now(), err)
		}
		if _, off := now().Zone(); off != 3600 {
			t.Fatalf("offset %d, want 3600", off)
		}
	})
	t.Run("set on the live home (passwd home/factory): real clock", func(t *testing.T) {
		env := clockEnv(t, map[string]string{"FACTORY_TEST_NOW": set})
		pw, _ := env.PwDir(0)
		env.Getenv = func(k string) string {
			return map[string]string{"FACTORY_TEST_NOW": set, "FACTORY_HOME": filepath.Join(pw, "factory")}[k]
		}
		env.LookupEnv = nil
		now, err := testClock(env)
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(now()); d < 0 || d > time.Minute {
			t.Fatalf("live home got a fixed clock: %v", now())
		}
	})
	t.Run("unset: real clock", func(t *testing.T) {
		now, err := testClock(clockEnv(t, map[string]string{"FACTORY_HOME": t.TempDir()}))
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(now()); d < 0 || d > time.Minute {
			t.Fatalf("unset got %v", now())
		}
	})
	t.Run("unparsable on a temp home: error", func(t *testing.T) {
		env := clockEnv(t, map[string]string{"FACTORY_TEST_NOW": "yesterday", "FACTORY_HOME": t.TempDir()})
		if _, err := testClock(env); err == nil || err.Error() != "FACTORY_TEST_NOW must be an ISO timestamp" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no offset on a temp home: error", func(t *testing.T) {
		for _, raw := range []string{"2026-11-12T01:02:03", "2026-11-12T01:02", "2026-11-12"} {
			env := clockEnv(t, map[string]string{"FACTORY_TEST_NOW": raw, "FACTORY_HOME": t.TempDir()})
			if _, err := testClock(env); err == nil || err.Error() != "FACTORY_TEST_NOW must be an ISO timestamp" {
				t.Fatalf("%q: err = %v", raw, err)
			}
		}
	})
	t.Run("no offset on the live home: ignored", func(t *testing.T) {
		env := clockEnv(t, map[string]string{})
		pw, _ := env.PwDir(0)
		env.Getenv = func(k string) string {
			return map[string]string{"FACTORY_TEST_NOW": "2026-11-12T01:02:03", "FACTORY_HOME": filepath.Join(pw, "factory")}[k]
		}
		env.LookupEnv = nil
		now, err := testClock(env)
		if err != nil || time.Since(now()) > time.Minute {
			t.Fatalf("live home must use the real clock: %v, %v", now(), err)
		}
	})
	t.Run("unparsable on the live home: ignored", func(t *testing.T) {
		env := clockEnv(t, map[string]string{"FACTORY_TEST_NOW": "yesterday"})
		pw, _ := env.PwDir(0)
		env.Getenv = func(k string) string {
			return map[string]string{"FACTORY_TEST_NOW": "yesterday", "FACTORY_HOME": filepath.Join(pw, "factory")}[k]
		}
		env.LookupEnv = nil
		if _, err := testClock(env); err != nil {
			t.Fatalf("live home must ignore the variable, got %v", err)
		}
	})
}
