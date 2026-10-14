package main

import (
	"errors"
	"io"
	"iter"
	"os"
	"sort"
	"strconv"
	"strings"

	consolestream "github.com/wolfeidau/console-stream"
	"github.com/wolfeidau/console-stream/internal/osc7501"
)

// statusLine renders a status as state[/kind][ id][ n%][: msg].
func statusLine(s *consolestream.ProgramStatus) string {
	var b strings.Builder
	b.WriteString(s.State)
	if s.Kind != "" {
		b.WriteString("/" + s.Kind)
	}
	if s.ID != "" {
		b.WriteString(" " + s.ID)
	}
	if s.Progress >= 0 {
		b.WriteString(" " + strconv.Itoa(s.Progress) + "%")
	}
	if s.Msg != "" {
		b.WriteString(": " + s.Msg)
	}
	return b.String()
}

// statusTracker keeps the last state[/kind] reported per ID.
type statusTracker struct {
	states map[string]string
}

func newStatusTracker() *statusTracker {
	return &statusTracker{states: map[string]string{}}
}

func (t *statusTracker) apply(s *consolestream.ProgramStatus) {
	if s.State == "clear" {
		if s.ID == "" {
			clear(t.states)
			return
		}
		for id := range t.states {
			if id == s.ID || strings.HasPrefix(id, s.ID+"/") {
				delete(t.states, id)
			}
		}
		return
	}
	v := s.State
	if s.Kind != "" {
		v += "/" + s.Kind
	}
	t.states[s.ID] = v
}

// summary returns id=state[/kind] entries sorted by id; the root id shows as "-".
func (t *statusTracker) summary() string {
	ids := make([]string, 0, len(t.states))
	for id := range t.states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		if name == "" {
			name = "-"
		}
		parts = append(parts, name+"="+t.states[id])
	}
	return strings.Join(parts, ", ")
}

// reemitWriter returns stderr when it is a terminal, else nil.
func reemitWriter() io.Writer {
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return os.Stderr
	}
	return nil
}

func toReport(s *consolestream.ProgramStatus) osc7501.Report {
	return osc7501.Report{
		State: s.State, ID: s.ID, Kind: s.Kind, App: s.App,
		Title: s.Title, Msg: s.Msg, Progress: s.Progress,
	}
}

func blockedError(s *consolestream.ProgramStatus) error {
	msg := "program blocked"
	if s.Kind != "" {
		msg += " (" + s.Kind + ")"
	}
	if s.ID != "" {
		msg += " " + s.ID
	}
	if s.Msg != "" {
		msg += ": " + s.Msg
	}
	return errors.New(msg)
}

// wrapProgramStatus tracks statuses, re-emits them to reemit (when non-nil) and,
// if failOnBlocked is set, stops iteration at the first blocked status. The
// returned func reports the blocked error, if any, once iteration has ended.
func wrapProgramStatus(events iter.Seq2[consolestream.Event, error], failOnBlocked bool, tracker *statusTracker, reemit io.Writer) (iter.Seq2[consolestream.Event, error], func() error) {
	var blockedErr error
	wrapped := func(yield func(consolestream.Event, error) bool) {
		for event, err := range events {
			s, ok := event.Event.(*consolestream.ProgramStatus)
			if err != nil || !ok {
				if !yield(event, err) {
					return
				}
				continue
			}
			tracker.apply(s)
			if reemit != nil {
				_, _ = reemit.Write(toReport(s).Encode())
			}
			if !yield(event, nil) {
				return
			}
			if failOnBlocked && s.State == "blocked" {
				blockedErr = blockedError(s)
				return
			}
		}
	}
	return wrapped, func() error { return blockedErr }
}
