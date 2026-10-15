package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	consolestream "github.com/wolfeidau/console-stream"
	"github.com/wolfeidau/osc7501"
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
	timer  consolestream.StatusTimer
}

// timeSummary renders non-zero time per state as working=12.3s blocked=4.1s
// idle=200ms, rounded to 100ms; "" if none.
func (t *statusTracker) timeSummary() string {
	tot := t.timer.Totals()
	var parts []string
	for _, e := range []struct {
		name string
		d    time.Duration
	}{{"working", tot.Working}, {"blocked", tot.Blocked}, {"idle", tot.Idle}} {
		if r := e.d.Round(100 * time.Millisecond); r > 0 {
			parts = append(parts, e.name+"="+r.String())
		}
	}
	return strings.Join(parts, " ")
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

// relay writes OSC 7501 records to a terminal, grouping them under runID.
// A nil relay or nil w disables relaying.
type relay struct {
	w     io.Writer
	runID string
	title string
}

func (r *relay) active() bool { return r != nil && r.w != nil }

func (r *relay) write(rep osc7501.Report) {
	_, _ = r.w.Write(rep.Encode())
}

// child relays a child report with its id prefixed by the run id.
func (r *relay) child(s *consolestream.ProgramStatus) {
	rep := toReport(s)
	if rep.ID == "" {
		rep.ID = r.runID
	} else {
		rep.ID = r.runID + "/" + rep.ID
	}
	if !osc7501.ValidID(rep.ID) {
		return
	}
	r.write(rep)
}

// run relays the run's own record.
func (r *relay) run(state, msg, title string) {
	r.write(osc7501.Report{State: state, ID: r.runID, Msg: msg, Title: title, Progress: -1})
}

// defaultStatusID derives a run id from the command: a sanitised base name
// (at most 27 bytes) plus a random 4 hex digit suffix.
func defaultStatusID(command string) string {
	b := []byte(filepath.Base(command))
	for i, c := range b {
		if !osc7501.ValidSegment(string(c)) {
			b[i] = '-'
		}
	}
	if len(b) > 27 {
		b = b[:27]
	}
	// filepath.Base returns "." or "/" for empty or root paths; fall back when
	// nothing identifying is left.
	if !strings.ContainsFunc(string(b), func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		b = []byte("run")
	}
	var r [2]byte
	_, _ = rand.Read(r[:])
	return string(b) + "-" + hex.EncodeToString(r[:])
}

// wrapProgramStatus tracks statuses, relays them and the run's own record to
// rl (when set) and, if failOnBlocked is set, stops iteration at the first
// blocked status. The returned func reports the blocked error, if any, once
// iteration has ended.
func wrapProgramStatus(events iter.Seq2[consolestream.Event, error], failOnBlocked bool, tracker *statusTracker, rl *relay) (iter.Seq2[consolestream.Event, error], func() error) {
	var blockedErr error
	wrapped := func(yield func(consolestream.Event, error) bool) {
		for event, err := range events {
			if err != nil {
				if !yield(event, err) {
					return
				}
				continue
			}
			relaying := rl.active()
			tracker.timer.Observe(event)
			switch e := event.Event.(type) {
			case *consolestream.ProgramStatus:
				tracker.apply(e)
				if relaying {
					rl.child(e)
				}
				blocking := failOnBlocked && e.State == "blocked"
				if blocking {
					blockedErr = blockedError(e)
					if relaying {
						rl.run("error", blockedErr.Error(), "")
					}
				}
				if !yield(event, nil) || blocking {
					return
				}
				continue
			case *consolestream.ProcessStart:
				if relaying {
					rl.run("working", "", rl.title)
				}
			case *consolestream.ProcessEnd:
				if relaying {
					if e.Success {
						rl.run("done", "", "")
					} else {
						rl.run("error", fmt.Sprintf("exit code %d", e.ExitCode), "")
					}
				}
			case *consolestream.ProcessError:
				if relaying {
					rl.run("error", e.Message, "")
				}
			}
			if !yield(event, nil) {
				return
			}
		}
	}
	return wrapped, func() error { return blockedErr }
}
