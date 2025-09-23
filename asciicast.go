package consolestream

import (
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"strconv"
	"time"
)

// AscicastV3Line represents either a header or event line in asciicast v3 format
type AscicastV3Line interface {
	MarshalJSON() ([]byte, error)
}

// TermInfo represents terminal dimensions for asciicast header
type TermInfo struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
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
	// Create a map to merge version with metadata
	result := make(map[string]any)
	result["version"] = h.Version

	// Marshal metadata to get its fields
	metadataBytes, err := json.Marshal(h.AscicastV3Metadata)
	if err != nil {
		return nil, err
	}

	// Unmarshal into map to merge with version
	var metadataMap map[string]any
	if err := json.Unmarshal(metadataBytes, &metadataMap); err != nil {
		return nil, err
	}

	// Merge maps
	maps.Copy(result, metadataMap)

	return json.Marshal(result)
}

// AscicastV3Event represents an event line in asciicast v3 format: [interval, code, data]
type AscicastV3Event [3]any

// MarshalJSON implements AscicastV3Line interface for events
func (e AscicastV3Event) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any(e))
}

// NewOutputEvent creates an asciicast output event
func NewOutputEvent(interval float64, data string) AscicastV3Event {
	return AscicastV3Event{interval, "o", data}
}

// NewResizeEvent creates an asciicast resize event
func NewResizeEvent(interval float64, cols, rows uint16) AscicastV3Event {
	return AscicastV3Event{interval, "r", fmt.Sprintf("%d %d", cols, rows)}
}

// NewExitEvent creates an asciicast exit event
func NewExitEvent(interval float64, exitCode int) AscicastV3Event {
	return AscicastV3Event{interval, "x", strconv.Itoa(exitCode)}
}

// ToAscicastV3 transforms a stream of console-stream events into asciicast v3 format
func ToAscicastV3(events iter.Seq2[Event, error], metadata AscicastV3Metadata) iter.Seq2[AscicastV3Line, error] {
	return func(yield func(AscicastV3Line, error) bool) {
		// Emit header first
		header := AscicastV3Header{
			Version:            3,
			AscicastV3Metadata: metadata,
		}
		if !yield(header, nil) {
			return
		}

		var lastEventTime time.Time
		lastEventTimeSet := false

		for event, err := range events {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}

			// Calculate interval from last event (0 for first event)
			var interval float64
			if !lastEventTimeSet {
				interval = 0
				lastEventTime = event.Timestamp
				lastEventTimeSet = true
			} else {
				interval = event.Timestamp.Sub(lastEventTime).Seconds()
				lastEventTime = event.Timestamp
			}

			// Transform based on event type
			switch e := event.Event.(type) {
			case *OutputData:
				if !yield(NewOutputEvent(interval, string(e.Data)), nil) {
					return
				}

			case *TerminalResizeEvent:
				if !yield(NewResizeEvent(interval, e.Cols, e.Rows), nil) {
					return
				}

			case *ProcessEnd:
				if !yield(NewExitEvent(interval, e.ExitCode), nil) {
					return
				}
				return // Exit event ends the session

			// Filter out console-stream internal events
			case *ProcessStart, *ProcessError, *HeartbeatEvent:
				// Skip these events as they don't map to asciicast format
				continue

			default:
				// Unknown event type - skip it
				continue
			}
		}
	}
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
