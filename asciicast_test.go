package consolestream

import (
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAscicastV3HeaderMarshalJSON(t *testing.T) {
	t.Parallel()

	t.Run("header with all metadata fields", func(t *testing.T) {
		timestamp := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
		metadata := AscicastV3Metadata{
			Term:      TermInfo{Cols: 80, Rows: 24},
			Timestamp: &timestamp,
			Command:   "echo test",
			Title:     "Test Session",
			Env: map[string]string{
				"TERM":  "xterm-256color",
				"SHELL": "/bin/bash",
			},
			Tags: []string{"demo", "test"},
		}

		header := AscicastV3Header{
			Version:            3,
			AscicastV3Metadata: metadata,
		}

		data, err := header.MarshalJSON()
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		require.Equal(t, float64(3), result["version"])
		require.Equal(t, "echo test", result["command"])
		require.Equal(t, "Test Session", result["title"])
		require.Equal(t, "2024-01-01T12:00:00Z", result["timestamp"])

		term := result["term"].(map[string]any)
		require.Equal(t, float64(80), term["cols"])
		require.Equal(t, float64(24), term["rows"])

		env := result["env"].(map[string]any)
		require.Equal(t, "xterm-256color", env["TERM"])
		require.Equal(t, "/bin/bash", env["SHELL"])

		tags := result["tags"].([]any)
		require.Len(t, tags, 2)
		require.Contains(t, tags, "demo")
		require.Contains(t, tags, "test")
	})

	t.Run("header with minimal metadata", func(t *testing.T) {
		metadata := AscicastV3Metadata{
			Term: TermInfo{Cols: 80, Rows: 24},
		}

		header := AscicastV3Header{
			Version:            3,
			AscicastV3Metadata: metadata,
		}

		data, err := header.MarshalJSON()
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		require.Equal(t, float64(3), result["version"])
		term := result["term"].(map[string]any)
		require.Equal(t, float64(80), term["cols"])
		require.Equal(t, float64(24), term["rows"])

		// Optional fields should not be present when empty
		_, hasCommand := result["command"]
		require.False(t, hasCommand)
		_, hasTitle := result["title"]
		require.False(t, hasTitle)
		_, hasTimestamp := result["timestamp"]
		require.False(t, hasTimestamp)
	})
}

func TestAscicastV3EventMarshalJSON(t *testing.T) {
	t.Parallel()

	t.Run("output event", func(t *testing.T) {
		event := NewOutputEvent(1.234567, []byte("Hello World!\n"))

		data, err := event.MarshalJSON()
		require.NoError(t, err)

		var result [3]any
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		require.Equal(t, 1.234567, result[0])
		require.Equal(t, "o", result[1])
		require.Equal(t, "Hello World!\n", result[2])
	})

	t.Run("resize event", func(t *testing.T) {
		event := NewResizeEvent(2.5, 120, 30)

		data, err := event.MarshalJSON()
		require.NoError(t, err)

		var result [3]any
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		require.Equal(t, 2.5, result[0])
		require.Equal(t, "r", result[1])
		require.Equal(t, "120 30", result[2])
	})

	t.Run("exit event", func(t *testing.T) {
		event := NewExitEvent(10.123, 0)

		data, err := event.MarshalJSON()
		require.NoError(t, err)

		var result [3]any
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		require.Equal(t, 10.123, result[0])
		require.Equal(t, "x", result[1])
		require.Equal(t, "0", result[2])
	})

	t.Run("exit event with non-zero code", func(t *testing.T) {
		event := NewExitEvent(5.0, 1)

		data, err := event.MarshalJSON()
		require.NoError(t, err)

		var result [3]any
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		require.Equal(t, 5.0, result[0])
		require.Equal(t, "x", result[1])
		require.Equal(t, "1", result[2])
	})
}

func TestToAscicastV3(t *testing.T) {
	t.Parallel()

	t.Run("transforms PTY events correctly", func(t *testing.T) {
		sessionStart := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

		events := func(yield func(Event, error) bool) {
			// First PTY output event - this sets session start
			if !yield(Event{
				Timestamp: sessionStart,
				Event:     &PTYOutputData{Data: []byte("Hello\n")},
			}, nil) {
				return
			}

			// Terminal resize event
			if !yield(Event{
				Timestamp: sessionStart.Add(100 * time.Millisecond),
				Event:     &TerminalResizeEvent{Cols: 120, Rows: 30},
			}, nil) {
				return
			}

			// Process end event
			if !yield(Event{
				Timestamp: sessionStart.Add(200 * time.Millisecond),
				Event:     &ProcessEnd{ExitCode: 0},
			}, nil) {
				return
			}
		}

		metadata := AscicastV3Metadata{
			Term:    TermInfo{Cols: 80, Rows: 24},
			Command: "echo Hello",
		}

		lines := collectAscicastLines(ToAscicastV3(events, metadata))
		require.Len(t, lines, 4) // header + 3 events

		// Check header
		headerData, err := lines[0].MarshalJSON()
		require.NoError(t, err)
		require.Contains(t, string(headerData), `"version":3`)
		require.Contains(t, string(headerData), `"command":"echo Hello"`)

		// Check output event (first event should have interval 0)
		eventData, err := lines[1].MarshalJSON()
		require.NoError(t, err)
		var outputEvent [3]any
		err = json.Unmarshal(eventData, &outputEvent)
		require.NoError(t, err)
		require.Equal(t, float64(0), outputEvent[0]) // First event should be at time 0
		require.Equal(t, "o", outputEvent[1])
		require.Equal(t, "Hello\n", outputEvent[2])

		// Check resize event
		eventData, err = lines[2].MarshalJSON()
		require.NoError(t, err)
		var resizeEvent [3]any
		err = json.Unmarshal(eventData, &resizeEvent)
		require.NoError(t, err)
		require.Equal(t, 0.1, resizeEvent[0]) // 100ms = 0.1s
		require.Equal(t, "r", resizeEvent[1])
		require.Equal(t, "120 30", resizeEvent[2])

		// Check exit event
		eventData, err = lines[3].MarshalJSON()
		require.NoError(t, err)
		var exitEvent [3]any
		err = json.Unmarshal(eventData, &exitEvent)
		require.NoError(t, err)
		require.Equal(t, 0.1, exitEvent[0]) // 100ms delta from resize event = 0.1s
		require.Equal(t, "x", exitEvent[1])
		require.Equal(t, "0", exitEvent[2])
	})

	t.Run("filters unsupported events", func(t *testing.T) {
		sessionStart := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

		events := func(yield func(Event, error) bool) {
			// These should be filtered out
			if !yield(Event{
				Timestamp: sessionStart,
				Event:     &ProcessStart{PID: 123, Command: "echo", Args: []string{"hello"}},
			}, nil) {
				return
			}

			if !yield(Event{
				Timestamp: sessionStart.Add(50 * time.Millisecond),
				Event:     &HeartbeatEvent{ProcessAlive: true},
			}, nil) {
				return
			}

			if !yield(Event{
				Timestamp: sessionStart.Add(100 * time.Millisecond),
				Event:     &PipeOutputData{Data: []byte("pipe output"), Stream: Stdout},
			}, nil) {
				return
			}

			// This should be included
			if !yield(Event{
				Timestamp: sessionStart.Add(200 * time.Millisecond),
				Event:     &PTYOutputData{Data: []byte("pty output")},
			}, nil) {
				return
			}

			// This should be included and end the session
			if !yield(Event{
				Timestamp: sessionStart.Add(300 * time.Millisecond),
				Event:     &ProcessEnd{ExitCode: 0},
			}, nil) {
				return
			}
		}

		metadata := AscicastV3Metadata{
			Term: TermInfo{Cols: 80, Rows: 24},
		}

		lines := collectAscicastLines(ToAscicastV3(events, metadata))
		require.Len(t, lines, 3) // header + PTY output + exit event

		// Verify only PTY output and exit events are present
		eventData, err := lines[1].MarshalJSON()
		require.NoError(t, err)
		require.Contains(t, string(eventData), `"o"`)
		require.Contains(t, string(eventData), `"pty output"`)

		eventData, err = lines[2].MarshalJSON()
		require.NoError(t, err)
		require.Contains(t, string(eventData), `"x"`)
	})

	t.Run("handles error propagation", func(t *testing.T) {
		testErr := errors.New("test error")

		events := func(yield func(Event, error) bool) {
			if !yield(Event{}, testErr) {
				return
			}
		}

		metadata := AscicastV3Metadata{
			Term: TermInfo{Cols: 80, Rows: 24},
		}

		var receivedError error
		for line, err := range ToAscicastV3(events, metadata) {
			if err != nil {
				receivedError = err
				break
			}
			// Should still receive header first
			require.NotNil(t, line)
		}

		require.Error(t, receivedError)
		require.Equal(t, "test error", receivedError.Error())
	})

	t.Run("stops after exit event", func(t *testing.T) {
		sessionStart := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

		events := func(yield func(Event, error) bool) {
			if !yield(Event{
				Timestamp: sessionStart,
				Event:     &PTYOutputData{Data: []byte("before exit")},
			}, nil) {
				return
			}

			if !yield(Event{
				Timestamp: sessionStart.Add(100 * time.Millisecond),
				Event:     &ProcessEnd{ExitCode: 0},
			}, nil) {
				return
			}

			// This should not be processed
			if !yield(Event{
				Timestamp: sessionStart.Add(200 * time.Millisecond),
				Event:     &PTYOutputData{Data: []byte("after exit")},
			}, nil) {
				return
			}
		}

		metadata := AscicastV3Metadata{
			Term: TermInfo{Cols: 80, Rows: 24},
		}

		lines := collectAscicastLines(ToAscicastV3(events, metadata))
		require.Len(t, lines, 3) // header + output + exit (no events after exit)
	})
}

func TestWriteAscicastV3(t *testing.T) {
	t.Parallel()

	t.Run("writes JSON lines format correctly", func(t *testing.T) {
		sessionStart := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

		events := func(yield func(Event, error) bool) {
			if !yield(Event{
				Timestamp: sessionStart,
				Event:     &PTYOutputData{Data: []byte("test output")},
			}, nil) {
				return
			}

			if !yield(Event{
				Timestamp: sessionStart.Add(100 * time.Millisecond),
				Event:     &ProcessEnd{ExitCode: 0},
			}, nil) {
				return
			}
		}

		metadata := AscicastV3Metadata{
			Term:    TermInfo{Cols: 80, Rows: 24},
			Command: "test command",
		}

		var output strings.Builder
		err := WriteAscicastV3(events, metadata, func(data []byte) error {
			_, err := output.Write(data)
			return err
		})

		require.NoError(t, err)

		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		require.Len(t, lines, 3) // header + output + exit

		// Each line should be valid JSON
		for i, line := range lines {
			var obj any
			unmarshalErr := json.Unmarshal([]byte(line), &obj)
			require.NoError(t, unmarshalErr, "line %d should be valid JSON: %s", i, line)
		}

		// First line should be header
		var header map[string]any
		err = json.Unmarshal([]byte(lines[0]), &header)
		require.NoError(t, err)
		require.Equal(t, float64(3), header["version"])
		require.Equal(t, "test command", header["command"])

		// Second line should be output event
		var outputEvent [3]any
		err = json.Unmarshal([]byte(lines[1]), &outputEvent)
		require.NoError(t, err)
		require.Equal(t, "o", outputEvent[1])
		require.Equal(t, "test output", outputEvent[2])

		// Third line should be exit event
		var exitEvent [3]any
		err = json.Unmarshal([]byte(lines[2]), &exitEvent)
		require.NoError(t, err)
		require.Equal(t, "x", exitEvent[1])
		require.Equal(t, "0", exitEvent[2])
	})

	t.Run("handles write errors", func(t *testing.T) {
		events := func(yield func(Event, error) bool) {
			if !yield(Event{
				Timestamp: time.Now(),
				Event:     &PTYOutputData{Data: []byte("test")},
			}, nil) {
				return
			}
		}

		metadata := AscicastV3Metadata{
			Term: TermInfo{Cols: 80, Rows: 24},
		}

		writeErr := errors.New("write error")
		err := WriteAscicastV3(events, metadata, func(data []byte) error {
			return writeErr
		})

		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to write asciicast line")
	})
}

// Helper function to collect asciicast lines from iterator
func collectAscicastLines(seq iter.Seq2[AscicastV3Line, error]) []AscicastV3Line {
	var lines []AscicastV3Line
	for line, err := range seq {
		if err != nil {
			break
		}
		lines = append(lines, line)
	}
	return lines
}
