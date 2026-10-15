package consolestream

import (
	"strings"
	"time"
)

// StatusDurations is wall-clock time per program status state.
type StatusDurations struct{ Working, Blocked, Idle time.Duration }

// StatusTimer accumulates StatusDurations from an event stream: at each
// instant the run counts once under its most urgent live state
// (blocked > working > idle). The zero value is ready to use; it is not safe
// for concurrent use.
type StatusTimer struct {
	records map[string]string // id -> live state (working, blocked, idle)
	cur     string            // aggregate state, "" when no live record
	since   time.Time
	totals  StatusDurations
	closed  bool

	onAccrue func(state string, d time.Duration)
}

// Observe applies a ProgramStatus at its event timestamp. ProcessEnd and
// ProcessError close the timer; other events are ignored.
func (t *StatusTimer) Observe(ev Event) {
	switch e := ev.Event.(type) {
	case *ProgramStatus:
		t.apply(ev.Timestamp, e)
	case *ProcessEnd, *ProcessError:
		t.Close(ev.Timestamp)
	}
}

// Close stops all records counting at the given time; later reports are
// ignored. It is idempotent.
func (t *StatusTimer) Close(at time.Time) {
	if t.closed {
		return
	}
	t.accrue(at)
	t.cur = ""
	t.records = nil
	t.closed = true
}

// Totals returns the time accrued so far.
func (t *StatusTimer) Totals() StatusDurations { return t.totals }

func (t *StatusTimer) apply(at time.Time, s *ProgramStatus) {
	if t.closed {
		return
	}
	t.accrue(at)
	switch s.State {
	case "working", "blocked", "idle":
		if t.records == nil {
			t.records = map[string]string{}
		}
		t.records[s.ID] = s.State
	case "done", "error":
		delete(t.records, s.ID)
	case "clear":
		if s.ID == "" {
			clear(t.records)
		} else {
			for id := range t.records {
				if id == s.ID || strings.HasPrefix(id, s.ID+"/") {
					delete(t.records, id)
				}
			}
		}
	}
	prev := t.cur
	t.cur = t.aggregate()
	if prev == "" {
		t.since = at
	}
}

func (t *StatusTimer) aggregate() string {
	agg := ""
	for _, st := range t.records {
		switch {
		case st == "blocked":
			return st
		case st == "working":
			agg = st
		case agg == "":
			agg = st
		}
	}
	return agg
}

// accrue adds the time since the interval start to the current state and
// restarts the interval. A timestamp before the start accrues nothing and
// keeps the later start.
func (t *StatusTimer) accrue(at time.Time) {
	if t.cur == "" || !at.After(t.since) {
		return
	}
	d := at.Sub(t.since)
	t.since = at
	switch t.cur {
	case "working":
		t.totals.Working += d
	case "blocked":
		t.totals.Blocked += d
	case "idle":
		t.totals.Idle += d
	}
	if t.onAccrue != nil {
		t.onAccrue(t.cur, d)
	}
}
