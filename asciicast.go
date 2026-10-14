package consolestream

import (
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// AscicastV3Line represents either a header or event line in asciicast v3 format
type AscicastV3Line interface {
	MarshalJSON() ([]byte, error)
}

// TermInfo represents terminal dimensions for asciicast header
type TermInfo struct {
	Cols    int        `json:"cols"`
	Rows    int        `json:"rows"`
	Type    string     `json:"type,omitempty"`
	Version string     `json:"version,omitempty"`
	Theme   *TermTheme `json:"theme,omitempty"`
}

// TermTheme describes terminal colours. Palette is a colon-separated list of
// hex colours, as asciinema writes it.
type TermTheme struct {
	Fg      string `json:"fg"`
	Bg      string `json:"bg"`
	Palette string `json:"palette"`
}

// AscicastV3Metadata contains the metadata for the asciicast header
type AscicastV3Metadata struct {
	Term      TermInfo          `json:"term"`
	Timestamp *int64            `json:"timestamp,omitempty"`
	Command   string            `json:"command,omitempty"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
}

// AscicastV3Header represents the first line of an asciicast v3 file
type AscicastV3Header struct {
	Version int `json:"version"`
	AscicastV3Metadata
}

// MarshalJSON implements AscicastV3Line interface for headers
func (h AscicastV3Header) MarshalJSON() ([]byte, error) {
	// The alias drops MarshalJSON to avoid recursion; struct field order is preserved.
	type alias AscicastV3Header
	return json.Marshal(alias(h))
}

// AscicastV3Event represents an event line in asciicast v3 format: [interval, code, data]
type AscicastV3Event [3]any

// MarshalJSON implements AscicastV3Line interface for events
func (e AscicastV3Event) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any(e))
}

// roundInterval rounds seconds to microsecond precision.
func roundInterval(s float64) float64 { return math.Round(s*1e6) / 1e6 }

// NewOutputEvent creates an asciicast output event
func NewOutputEvent(interval float64, data string) AscicastV3Event {
	return AscicastV3Event{roundInterval(interval), "o", data}
}

// NewResizeEvent creates an asciicast resize event
func NewResizeEvent(interval float64, cols, rows uint16) AscicastV3Event {
	return AscicastV3Event{roundInterval(interval), "r", fmt.Sprintf("%d %d", cols, rows)}
}

// NewExitEvent creates an asciicast exit event
func NewExitEvent(interval float64, exitCode int) AscicastV3Event {
	return AscicastV3Event{roundInterval(interval), "x", strconv.Itoa(exitCode)}
}

// NewMarkerEvent creates an asciicast marker event
func NewMarkerEvent(interval float64, label string) AscicastV3Event {
	return AscicastV3Event{roundInterval(interval), "m", label}
}

// ToAscicastV3 transforms a stream of console-stream events into asciicast v3 format
func ToAscicastV3(events iter.Seq2[Event, error], metadata AscicastV3Metadata) iter.Seq2[AscicastV3Line, error] {
	return func(yield func(AscicastV3Line, error) bool) {
		// Default the timestamp to the recording start time; copy so each
		// iteration of the sequence gets its own.
		md := metadata
		if md.Timestamp == nil {
			now := time.Now().Unix()
			md.Timestamp = &now
		}

		// Emit header first
		header := AscicastV3Header{
			Version:            3,
			AscicastV3Metadata: md,
		}
		if !yield(header, nil) {
			return
		}

		var lastEventTime time.Time
		lastEventTimeSet := false

		// carry holds a trailing incomplete UTF-8 sequence until the next output event.
		var carry []byte

		// marked holds the last marked (state, kind) per status ID.
		marked := map[string][2]string{}

		for event, err := range events {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}

			// Intervals are relative to the previous emitted line; the first
			// event (any type) anchors the cursor so filtered events only
			// initialise it and never advance it.
			if !lastEventTimeSet {
				lastEventTime = event.Timestamp
				lastEventTimeSet = true
			}
			interval := event.Timestamp.Sub(lastEventTime).Seconds()

			// Transform based on event type
			switch e := event.Event.(type) {
			case *OutputData:
				if len(e.Segments) == 0 {
					var data []byte
					data, carry = splitIncompleteUTF8(append(carry, e.Data...))
					if len(data) == 0 {
						continue
					}
					lastEventTime = event.Timestamp
					if !yield(NewOutputEvent(interval, string(data)), nil) {
						return
					}
					continue
				}
				// One line per read segment, timed by when it was read.
				for i, seg := range e.Segments {
					end := len(e.Data)
					if i+1 < len(e.Segments) {
						end = min(e.Segments[i+1].Offset, len(e.Data))
					}
					if seg.Offset < 0 || seg.Offset > end {
						continue // malformed caller-supplied segment
					}
					var data []byte
					data, carry = splitIncompleteUTF8(append(carry, e.Data[seg.Offset:end]...))
					if len(data) == 0 {
						continue
					}
					segInterval := max(seg.Time.Sub(lastEventTime).Seconds(), 0)
					if seg.Time.After(lastEventTime) {
						lastEventTime = seg.Time
					}
					if !yield(NewOutputEvent(segInterval, string(data)), nil) {
						return
					}
				}

			case *TerminalResizeEvent:
				lastEventTime = event.Timestamp
				if !yield(NewResizeEvent(interval, e.Cols, e.Rows), nil) {
					return
				}

			case *ProgramStatus:
				key := [2]string{e.State, e.Kind}
				if last, ok := marked[e.ID]; ok && last == key {
					continue
				}
				marked[e.ID] = key
				// Status timestamps are read times and can precede the flush time
				// of the previous OutputData; never step the cursor backwards.
				interval = max(interval, 0)
				if event.Timestamp.After(lastEventTime) {
					lastEventTime = event.Timestamp
				}
				if !yield(NewMarkerEvent(interval, programStatusLabel(e)), nil) {
					return
				}

			case *ProcessEnd:
				if len(carry) > 0 {
					if !yield(NewOutputEvent(interval, string(carry)), nil) {
						return
					}
					interval = 0
				}
				if !yield(NewExitEvent(interval, e.ExitCode), nil) {
					return
				}
				return // Exit event ends the session

			case *ProcessError:
				if len(carry) > 0 {
					if !yield(NewOutputEvent(interval, string(carry)), nil) {
						return
					}
					interval = 0
				}
				label := "error: " + e.Message
				if e.Error != nil {
					label += ": " + e.Error.Error()
				}
				if !yield(NewMarkerEvent(interval, label), nil) {
					return
				}
				return // Marker ends the session; no exit code is available

			// Filter out console-stream internal events
			case *ProcessStart, *HeartbeatEvent:
				// Skip these events as they don't map to asciicast format
				continue

			default:
				// Unknown event type - skip it
				continue
			}
		}

		// Input ended without an exit event: don't drop held-back bytes.
		if len(carry) > 0 {
			yield(NewOutputEvent(0, string(carry)), nil)
		}
	}
}

// programStatusLabel formats a marker label:
// status: <state>[/<kind>][ <id>][ <progress>%][: <msg>]
func programStatusLabel(e *ProgramStatus) string {
	label := "status: " + e.State
	if e.Kind != "" {
		label += "/" + e.Kind
	}
	if e.ID != "" {
		label += " " + e.ID
	}
	if e.Progress >= 0 {
		label += " " + strconv.Itoa(e.Progress) + "%"
	}
	if e.Msg != "" {
		label += ": " + e.Msg
	}
	return label
}

// splitIncompleteUTF8 splits b into a complete part and a trailing incomplete
// (but potentially valid) UTF-8 sequence of at most utf8.UTFMax-1 bytes.
// Bytes that can never form a valid rune are left in the complete part.
func splitIncompleteUTF8(b []byte) (complete, rest []byte) {
	for i := len(b) - 1; i >= 0 && i >= len(b)-(utf8.UTFMax-1); i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if !utf8.FullRune(b[i:]) {
			return b[:i], append([]byte(nil), b[i:]...)
		}
		break
	}
	return b, nil
}

// WriteAscicastV3 is a convenience function to write asciicast v3 to any writer
func WriteAscicastV3(events iter.Seq2[Event, error], metadata AscicastV3Metadata, writeFunc func([]byte) error) error {
	for line, err := range ToAscicastV3(events, metadata) {
		if err != nil {
			return err
		}

		data, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("failed to marshal asciicast line: %w", err)
		}

		// Add newline for JSON Lines format
		data = append(data, '\n')

		if err := writeFunc(data); err != nil {
			return fmt.Errorf("failed to write asciicast line: %w", err)
		}
	}

	return nil
}
