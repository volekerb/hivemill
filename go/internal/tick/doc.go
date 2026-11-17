// Package tick is the Go driver of `factory tick` (G-041 S3b). It owns the tick lock, the unit order and the laps;
// every unit that is not ported yet runs in a Python child (`factory tick --phases ...`) that shares the lock and
// hands its state back through run/tick-handoff.json. No unit is ported in this package version: the whole tick
// is one child. A later task registers a Go unit with registerGo from its own file's init().
package tick
