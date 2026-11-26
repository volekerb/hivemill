package hm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// machinesFixture is the coordinator box-a with two session machines, box-b up and box-c down. It returns the handler
// and the machines a probe ran on.
func machinesFixture(t *testing.T) (http.Handler, func() []string) {
	t.Helper()
	r, s := testRunner(t, "")
	r.Log.Machine = "box-a"
	for k, v := range map[string]string{"session_machines": "local,box-b,box-c,core-a", "max_concurrent": "3"} {
		if err := s.SetCfg(k, v); err != nil {
			t.Fatal(err)
		}
	}
	old := MemAvailableMB
	MemAvailableMB = func() int { return 2048 }
	t.Cleanup(func() { MemAvailableMB = old })
	r.DiskFree = func(string) (uint64, uint64, error) { return 100, 58, nil }

	var mu sync.Mutex
	var probed []string
	r.Tools.HC = func(args ...string) (int, string, string) {
		switch strings.Join(args[:min(2, len(args))], " ") {
		case "server status":
			return 0, `{"core":{"name":"core-a"}}`, ""
		case "ls --json":
			return 0, `{"terminals":[],"machines":[{"name":"box-b","state":"connected"},{"name":"box-c","state":"disconnected"}]}`, ""
		case "exec --on":
			mu.Lock()
			defer mu.Unlock()
			probed = append(probed, args[2])
			return 0, "disk=/dev/x 1000 870 130 87% /\n---mem\nMemAvailable: 4096000 kB\n", ""
		}
		return 1, "", "no"
	}
	for task, machine := range map[string]string{"T-0001": "box-b", "T-0002": "box-b", "T-0003": "", "T-0004": "core-a"} {
		run := &Run{Task: task, Project: "www", State: Working, Machine: machine, Round: 1}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Now().UTC().Format("2006-01-02")
	e := ev(day+"T00:00:00Z", "task.dispatched", "T-0001", "G-001", "www", "", "", map[string]any{"machine": "box-b"})
	e.Machine = "box-a"
	writeEvents(t, r.Log.Path, e)
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), probed...)
	}
}

func serveMachines(h http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "http://localhost:7461"+path, nil))
	return w
}

func TestObsMachinesAPI(t *testing.T) {
	h, probed := machinesFixture(t)
	w := serveMachines(h, http.MethodGet, "/api/obs/machines")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got MachinesPage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Cap != 3 || len(got.Cards) != 3 {
		t.Fatalf("cap %d, cards %+v", got.Cap, got.Cards)
	}
	for i, want := range []struct {
		name        string
		coord, down bool
		tasks       string
		free, mem   int // mem -1: unknown
		seen        bool
	}{
		{"box-a", true, false, "T-0003,T-0004", 1, 2048, true},
		{"box-b", false, false, "T-0001,T-0002", 1, 4000, true},
		{"box-c", false, true, "", 3, -1, false},
	} {
		c := got.Cards[i]
		var tasks []string
		for _, x := range c.Running {
			tasks = append(tasks, x.Task)
		}
		mem := -1
		if c.MemMB != nil {
			mem = *c.MemMB
		}
		if c.Name != want.name || c.Coordinator != want.coord || c.Down != want.down || strings.Join(tasks, ",") != want.tasks ||
			c.FreeSlots != want.free || mem != want.mem || (c.LastSeen != "") != want.seen {
			t.Errorf("card %d: %+v (tasks %v, mem %d), want %+v", i, c, tasks, mem, want)
		}
	}
	// disk use: the factory home here, the probe's df line elsewhere, null on the down machine
	for i, want := range []int{42, 87, -1} {
		disk := -1
		if p := got.Cards[i].DiskPct; p != nil {
			disk = *p
		}
		if disk != want {
			t.Errorf("card %d: disk %d%%, want %d%%", i, disk, want)
		}
	}
	// the down machine is not probed, and a reload within the TTL probes nothing again
	serveMachines(h, http.MethodGet, "/api/obs/machines")
	if got := probed(); !reflect.DeepEqual(got, []string{"box-b"}) {
		t.Errorf("probed %v", got)
	}
}

func TestObsMachinesPage(t *testing.T) {
	h, _ := machinesFixture(t)
	w := serveMachines(h, http.MethodGet, "/machines")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, body)
	}
	for _, want := range []string{"box-a", "box-b", "box-c", ">coordinator<", ">down<", "unknown", "T-0001", "4000 MB", "<dt>disk used</dt><dd>87%</dd>",
		`href="/machines" class="on" aria-current="page"`, `href="/task?k=box-a%2fT-0001"`} {
		if !strings.Contains(strings.ReplaceAll(body, "%2F", "%2f"), want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "<form") {
		t.Error("the page has the event filters, which do not apply to it")
	}
	for _, path := range []string{"/machines", "/api/obs/machines"} {
		if w := serveMachines(h, http.MethodPost, path); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d", path, w.Code)
		}
	}
}
