package prflow

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RedEvent is one `red-main` event. Day is the date of the ts in the ts's own UTC offset.
type RedEvent struct {
	At  time.Time
	Day string
}

// RedState is what the event log says about the main branch of one repo: every `red-main` event and the red windows
// they open. The same windows feed the stage map (overlayRed), so red_main stage hours and red_hours agree.
type RedState struct {
	Events  []RedEvent
	Windows []Interval // an open window has a zero To
	open    bool
}

// RedDay is the number of `red-main` events on one day.
type RedDay struct {
	Day    string `json:"day"`
	Events int    `json:"events"`
}

// RedMain is the red-main section of one repo for the report window.
type RedMain struct {
	Repo         string   `json:"repo,omitempty"`
	Events       int      `json:"events"`
	EventsPerDay float64  `json:"events_per_day"`
	Windows      int      `json:"windows"`
	RedHours     float64  `json:"red_hours"`
	PerDay       []RedDay `json:"per_day"`
}

// redMainOf measures one repo in [since, now]: events inside the window, windows that overlap it, and the hours of
// those windows clipped to it. An open window lasts until now, as in the stage map.
func redMainOf(st *RedState, since, now time.Time) RedMain {
	rm := RedMain{PerDay: []RedDay{}}
	if st == nil {
		return rm
	}
	perDay := map[string]int{}
	for _, e := range st.Events {
		if e.At.Before(since) || e.At.After(now) {
			continue
		}
		rm.Events++
		perDay[e.Day]++
	}
	var hours float64
	for _, w := range st.Windows {
		to := w.To
		if to.IsZero() {
			to = now
		}
		lo, hi := maxTime(w.From, since), minTime(to, now)
		if !lo.Before(hi) {
			continue
		}
		rm.Windows++
		hours += hi.Sub(lo).Hours()
	}
	rm.RedHours = round2(hours)
	days := make([]string, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		rm.PerDay = append(rm.PerDay, RedDay{Day: d, Events: perDay[d]})
	}
	return rm
}

func (r *RedMain) setRate(window time.Duration) {
	r.EventsPerDay = round2(float64(r.Events) / (window.Hours() / 24))
}

func (r RedMain) text(b *strings.Builder, indent string) {
	fmt.Fprintf(b, "%sevents %d, events/day %.2f, windows %d, red hours %.1f\n",
		indent, r.Events, r.EventsPerDay, r.Windows, r.RedHours)
	for _, d := range r.PerDay {
		fmt.Fprintf(b, "%s  %-18s %d\n", indent, d.Day, d.Events)
	}
}
