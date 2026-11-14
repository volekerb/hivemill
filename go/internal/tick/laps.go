package tick

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Lap is one entry of the tick-phases line.
type Lap struct {
	Name string
	Sec  float64
}

// LapsTotal sums lap seconds (the tick-phases total on non-live homes matches this plus float counters).
func LapsTotal(laps []Lap) float64 {
	var s float64
	for _, l := range laps {
		s += l.Sec
	}
	return s
}

// Counters is an insertion-ordered counter map; values are JSON numbers kept as text so ints stay ints.
type Counters struct {
	keys []string
	vals map[string]string
}

// Add sums v into key; a key keeps its first-seen position. Two integers sum as an integer, otherwise as float.
func (c *Counters) Add(key, v string) {
	if c.vals == nil {
		c.vals = map[string]string{}
	}
	old, ok := c.vals[key]
	if !ok {
		c.keys = append(c.keys, key)
		c.vals[key] = v
		return
	}
	a, ea := strconv.ParseInt(old, 10, 64)
	b, eb := strconv.ParseInt(v, 10, 64)
	if ea == nil && eb == nil {
		c.vals[key] = strconv.FormatInt(a+b, 10)
		return
	}
	fa, _ := strconv.ParseFloat(old, 64)
	fb, _ := strconv.ParseFloat(v, 64)
	s := strconv.FormatFloat(fa+fb, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	c.vals[key] = s
}

// Merge adds every counter of o in o's order.
func (c *Counters) Merge(o Counters) {
	for _, k := range o.keys {
		c.Add(k, o.vals[k])
	}
}

// Keys returns the keys in order; Get the text of one value.
func (c Counters) Keys() []string      { return append([]string(nil), c.keys...) }
func (c Counters) Get(k string) string { return c.vals[k] }

func (c Counters) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range c.keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		sb.Write(kb)
		sb.WriteByte(':')
		sb.WriteString(c.vals[k])
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

func (c *Counters) UnmarshalJSON(b []byte) error {
	*c = Counters{}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return errors.New("counters is not an object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		var n json.Number
		if err := dec.Decode(&n); err != nil {
			return err
		}
		c.Add(kt.(string), n.String())
	}
	return nil
}

// Timer is the lap bookkeeping of the driver (seconds on one monotonic clock). Go units lap like _pt_lap; a Python
// child's laps are merged by the H8 formulas.
type Timer struct {
	Last float64
	Laps []Lap
	Ctr  Counters
}

// Lap closes the phase that just ran in Go: its time is the time since the previous lap.
func (t *Timer) Lap(name string, now float64) {
	t.Laps = append(t.Laps, Lap{name, now - t.Last})
	t.Last = now
}

// Child merges a Python child that ran from s to e and reported laps and tail. k>0: the first lap also takes the
// time since the previous lap (the gap before the child started) and any unaccounted time inside it; k=0: the child's
// whole time flows into the next lap (Last stays).
func (t *Timer) Child(s, e float64, laps []Lap, tail float64) {
	if len(laps) == 0 {
		return
	}
	sum := 0.0
	for _, l := range laps {
		sum += l.Sec
	}
	d := append([]Lap(nil), laps...)
	d[0].Sec += (s - t.Last) + ((e - s) - sum - tail)
	t.Laps = append(t.Laps, d...)
	t.Last = e - tail
}
