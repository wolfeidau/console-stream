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
		timestamp := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).Unix()
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
		require.Equal(t, float64(1704110400), result["timestamp"]) // Unix timestamp for 2024-01-01T12:00:00Z

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
		event := NewOutputEvent(1.234567, "Hello World!\n")

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

	t.Run("NewMarkerEvent", func(t *testing.T) {
		data, err := NewMarkerEvent(2.5, "error: boom").MarshalJSON()
		require.NoError(t, err)

		var result [3]any
		require.NoError(t, json.Unmarshal(data, &result))

		require.Equal(t, 2.5, result[0])
		require.Equal(t, "m", result[1])
		require.Equal(t, "error: boom", result[2])
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
				Event:     &OutputData{Data: []byte("Hello\n")},
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
				Event:     &OutputData{Data: []byte("pipe output")},
			}, nil) {
				return
			}

			// This should be included
			if !yield(Event{
				Timestamp: sessionStart.Add(200 * time.Millisecond),
				Event:     &OutputData{Data: []byte("pty output")},
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
		require.Len(t, lines, 4) // header + pipe output + pty output + exit event

		// Verify both output events are present
		event1Data, err := lines[1].MarshalJSON()
		require.NoError(t, err)
		require.Contains(t, string(event1Data), `"o"`)
		require.Contains(t, string(event1Data), `"pipe output"`)

		event2Data, err := lines[2].MarshalJSON()
		require.NoError(t, err)
		require.Contains(t, string(event2Data), `"o"`)
		require.Contains(t, string(event2Data), `"pty output"`)

		exitData, err := lines[3].MarshalJSON()
		require.NoError(t, err)
		require.Contains(t, string(exitData), `"x"`)
	})

	t.Run("intervals are relative to last emitted event", func(t *testing.T) {
		t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

		events := func(yield func(Event, error) bool) {
			for _, ev := range []Event{
				{Timestamp: t0, Event: &ProcessStart{PID: 1, Command: "x"}},
				{Timestamp: t0.Add(1 * time.Second), Event: &OutputData{Data: []byte("a")}},
				{Timestamp: t0.Add(2 * time.Second), Event: &HeartbeatEvent{ProcessAlive: true}},
				{Timestamp: t0.Add(3 * time.Second), Event: &HeartbeatEvent{ProcessAlive: true}},
				{Timestamp: t0.Add(5 * time.Second), Event: &OutputData{Data: []byte("b")}},
				{Timestamp: t0.Add(5 * time.Second), Event: &ProcessEnd{ExitCode: 0}},
			} {
				if !yield(ev, nil) {
					return
				}
			}
		}

		lines := collectAscicastLines(ToAscicastV3(events, AscicastV3Metadata{Term: TermInfo{Cols: 80, Rows: 24}}))
		require.Len(t, lines, 4)

		want := []float64{1.0, 4.0, 0.0}
		for i, w := range want {
			data, err := lines[i+1].MarshalJSON()
			require.NoError(t, err)
			var ev [3]any
			require.NoError(t, json.Unmarshal(data, &ev))
			require.InDelta(t, w, ev[0], 1e-9)
		}
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
				Event:     &OutputData{Data: []byte("before exit")},
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
				Event:     &OutputData{Data: []byte("after exit")},
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
				Event:     &OutputData{Data: []byte("test output")},
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
				Event:     &OutputData{Data: []byte("test")},
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

func TestSplitIncompleteUTF8(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       string
		complete string
		rest     string
	}{
		{"empty", "", "", ""},
		{"ascii", "hello", "hello", ""},
		{"complete 2-byte", "caf\xc3\xa9", "caf\xc3\xa9", ""},
		{"complete 4-byte", "\U0001F600", "\U0001F600", ""},
		{"2-byte prefix", "ab\xc3", "ab", "\xc3"},
		{"3-byte prefix 1", "ab\xe2", "ab", "\xe2"},
		{"3-byte prefix 2", "ab\xe2\x82", "ab", "\xe2\x82"},
		{"4-byte prefix 1", "\xf0", "", "\xf0"},
		{"4-byte prefix 2", "a\xf0\x9f", "a", "\xf0\x9f"},
		{"4-byte prefix 3", "a\xf0\x9f\x98", "a", "\xf0\x9f\x98"},
		{"invalid trailing byte", "ab\xff", "ab\xff", ""},
		{"lone continuation", "ab\xa9", "ab\xa9", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			complete, rest := splitIncompleteUTF8([]byte(tt.in))
			require.Equal(t, tt.complete, string(complete))
			require.Equal(t, tt.rest, string(rest))
		})
	}
}

func TestToAscicastV3SplitUTF8(t *testing.T) {
	t.Parallel()

	start := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	stream := func(evs ...StreamEvent) iter.Seq2[Event, error] {
		return func(yield func(Event, error) bool) {
			for i, e := range evs {
				if !yield(Event{Timestamp: start.Add(time.Duration(i) * time.Second), Event: e}, nil) {
					return
				}
			}
		}
	}
	// outputs returns the concatenated "o" data and the event type codes.
	outputs := func(t *testing.T, lines []AscicastV3Line) (string, []string) {
		var sb strings.Builder
		var kinds []string
		for _, l := range lines[1:] {
			b, err := l.MarshalJSON()
			require.NoError(t, err)
			var ev [3]any
			require.NoError(t, json.Unmarshal(b, &ev))
			kinds = append(kinds, ev[1].(string))
			if ev[1] == "o" {
				sb.WriteString(ev[2].(string))
			}
		}
		return sb.String(), kinds
	}

	t.Run("rune split across events is preserved", func(t *testing.T) {
		lines := collectAscicastLines(ToAscicastV3(stream(
			&OutputData{Data: []byte("\xc3")},
			&OutputData{Data: []byte("\xa9 done")},
			&ProcessEnd{ExitCode: 0},
		), AscicastV3Metadata{}))

		text, kinds := outputs(t, lines)
		require.Equal(t, "é done", text)
		require.NotContains(t, text, "�")
		require.Equal(t, []string{"o", "x"}, kinds)
	})

	t.Run("trailing incomplete prefix is flushed before exit", func(t *testing.T) {
		lines := collectAscicastLines(ToAscicastV3(stream(
			&OutputData{Data: []byte("ok\xc3")},
			&ProcessEnd{ExitCode: 0},
		), AscicastV3Metadata{}))

		text, kinds := outputs(t, lines)
		require.Equal(t, "ok�", text)
		require.Equal(t, []string{"o", "o", "x"}, kinds)
	})

	t.Run("trailing incomplete prefix is flushed when input ends", func(t *testing.T) {
		lines := collectAscicastLines(ToAscicastV3(stream(
			&OutputData{Data: []byte("ok\xc3")},
		), AscicastV3Metadata{}))

		text, _ := outputs(t, lines)
		require.Equal(t, "ok�", text)
	})
}

func TestAscicastV3HeaderFormat(t *testing.T) {
	t.Parallel()

	header := AscicastV3Header{
		Version: 3,
		AscicastV3Metadata: AscicastV3Metadata{
			Term: TermInfo{
				Cols: 80, Rows: 24, Type: "xterm-256color", Version: "1.0",
				Theme: &TermTheme{Fg: "#ffffff", Bg: "#000000", Palette: "#000000:#ff0000"},
			},
			Command: "ls",
		},
	}

	data, err := json.Marshal(header)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), `{"version":3,"term":`))

	var got AscicastV3Header
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, header, got)
}

func TestToAscicastV3HeaderTimestamp(t *testing.T) {
	t.Parallel()

	firstHeader := func(t *testing.T, md AscicastV3Metadata) AscicastV3Header {
		t.Helper()
		empty := func(yield func(Event, error) bool) {}
		lines := collectAscicastLines(ToAscicastV3(empty, md))
		require.NotEmpty(t, lines)
		h, ok := lines[0].(AscicastV3Header)
		require.True(t, ok)
		return h
	}

	t.Run("defaults to now", func(t *testing.T) {
		h := firstHeader(t, AscicastV3Metadata{Term: TermInfo{Cols: 80, Rows: 24}})
		require.NotNil(t, h.Timestamp)
		require.InDelta(t, time.Now().Unix(), *h.Timestamp, 5)
	})

	t.Run("keeps caller timestamp", func(t *testing.T) {
		ts := int64(1704110400)
		h := firstHeader(t, AscicastV3Metadata{Term: TermInfo{Cols: 80, Rows: 24}, Timestamp: &ts})
		require.Equal(t, ts, *h.Timestamp)
	})
}

func TestAscicastV3IntervalRounding(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(NewOutputEvent(1.0011960419, "x"))
	require.NoError(t, err)
	require.Equal(t, `[1.001196,"o","x"]`, string(data))
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

func TestToAscicastV3ProcessError(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("ends with marker and no exit event", func(t *testing.T) {
		events := []Event{
			{Timestamp: t0, Event: &ProcessStart{PID: 1}},
			{Timestamp: t0.Add(1 * time.Second), Event: &OutputData{Data: []byte("hi")}},
			{Timestamp: t0.Add(2 * time.Second), Event: &HeartbeatEvent{ProcessAlive: true}},
			{Timestamp: t0.Add(3 * time.Second), Event: &ProcessError{Error: errors.New("boom"), Message: "process terminated unexpectedly"}},
			{Timestamp: t0.Add(4 * time.Second), Event: &OutputData{Data: []byte("late")}},
			{Timestamp: t0.Add(5 * time.Second), Event: &ProcessEnd{ExitCode: 1}},
		}

		lines := collectAscicastLines(ToAscicastV3(eventSeq(events), AscicastV3Metadata{}))
		require.Len(t, lines, 3)

		var o, m [3]any
		d, err := lines[1].MarshalJSON()
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(d, &o))
		d, err = lines[2].MarshalJSON()
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(d, &m))

		require.Equal(t, []any{1.0, "o", "hi"}, o[:])
		require.Equal(t, []any{2.0, "m", "error: process terminated unexpectedly: boom"}, m[:])
	})

	t.Run("flushes held-back UTF-8 bytes before marker", func(t *testing.T) {
		events := []Event{
			{Timestamp: t0, Event: &OutputData{Data: []byte("\xc3")}},
			{Timestamp: t0.Add(time.Second), Event: &ProcessError{Message: "failed"}},
		}

		lines := collectAscicastLines(ToAscicastV3(eventSeq(events), AscicastV3Metadata{}))
		require.Len(t, lines, 3)

		var o, m [3]any
		d, err := lines[1].MarshalJSON()
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(d, &o))
		d, err = lines[2].MarshalJSON()
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(d, &m))

		require.Equal(t, "o", o[1])
		require.Contains(t, o[2], "\xef\xbf\xbd")
		require.Equal(t, []any{0.0, "m", "error: failed"}, m[:])
	})
}

func eventSeq(events []Event) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func TestToAscicastV3Segments(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	run := func(evs ...Event) []AscicastV3Line {
		return collectAscicastLines(ToAscicastV3(eventSeq(evs), AscicastV3Metadata{}))
	}
	decode := func(t *testing.T, l AscicastV3Line) (float64, string, string) {
		b, err := l.MarshalJSON()
		require.NoError(t, err)
		var ev [3]any
		require.NoError(t, json.Unmarshal(b, &ev))
		return ev[0].(float64), ev[1].(string), ev[2].(string)
	}

	t.Run("one line per segment", func(t *testing.T) {
		lines := run(
			Event{Timestamp: t0, Event: &ProcessStart{}},
			Event{Timestamp: t0.Add(2 * time.Second), Event: &OutputData{
				Data: []byte("aabbc"),
				Segments: []OutputSegment{
					{Offset: 0, Time: t0.Add(1000 * time.Millisecond)},
					{Offset: 2, Time: t0.Add(1200 * time.Millisecond)},
					{Offset: 4, Time: t0.Add(1400 * time.Millisecond)},
				},
			}},
		)
		require.Len(t, lines, 4)
		want := []struct {
			iv float64
			s  string
		}{{1.0, "aa"}, {0.2, "bb"}, {0.2, "c"}}
		for i, w := range want {
			iv, kind, s := decode(t, lines[i+1])
			require.Equal(t, "o", kind)
			require.InDelta(t, w.iv, iv, 1e-9)
			require.Equal(t, w.s, s)
		}
	})

	t.Run("rune split across segments is intact", func(t *testing.T) {
		lines := run(
			Event{Timestamp: t0, Event: &ProcessStart{}},
			Event{Timestamp: t0.Add(time.Second), Event: &OutputData{
				Data: []byte("\xc3\xa9"),
				Segments: []OutputSegment{
					{Offset: 0, Time: t0.Add(100 * time.Millisecond)},
					{Offset: 1, Time: t0.Add(200 * time.Millisecond)},
				},
			}},
		)
		require.Len(t, lines, 2)
		iv, _, s := decode(t, lines[1])
		require.Equal(t, "é", s)
		require.InDelta(t, 0.2, iv, 1e-9)
	})
}

func TestToAscicastV3ProgramStatusMarkers(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: t0, Event: &ProcessStart{PID: 1}},
		{Timestamp: t0.Add(1 * time.Second), Event: &ProgramStatus{State: "working", ID: "build", Progress: -1}},
		{Timestamp: t0.Add(2 * time.Second), Event: &OutputData{Data: []byte("out")}},
		{Timestamp: t0.Add(3 * time.Second), Event: &ProgramStatus{State: "working", ID: "build", Progress: 50}},
		{Timestamp: t0.Add(4 * time.Second), Event: &ProgramStatus{State: "blocked", ID: "build", Kind: "question", Progress: -1, Msg: "ok?"}},
		{Timestamp: t0.Add(5 * time.Second), Event: &ProgramStatus{State: "done", ID: "build", Progress: 100}},
		{Timestamp: t0.Add(6 * time.Second), Event: &OutputData{Data: []byte("late")}},
		{Timestamp: t0.Add(7 * time.Second), Event: &ProcessEnd{ExitCode: 0, Success: true}},
	}

	lines := collectAscicastLines(ToAscicastV3(eventSeq(events), AscicastV3Metadata{}))
	require.Len(t, lines, 7) // header, marker, o, marker, marker, o, x

	var got [][3]any
	for _, l := range lines[1:] {
		d, err := l.MarshalJSON()
		require.NoError(t, err)
		var ev [3]any
		require.NoError(t, json.Unmarshal(d, &ev))
		got = append(got, ev)
	}

	require.Equal(t, [][3]any{
		{1.0, "m", "status: working build"},
		{1.0, "o", "out"},
		{2.0, "m", "status: blocked/question build: ok?"},
		{1.0, "m", "status: done build 100%"},
		{1.0, "o", "late"},
		{1.0, "x", "0"},
	}, got)
}

func TestToAscicastV3ProgramStatusBeforeFlushTime(t *testing.T) {
	t.Parallel()

	// A status read before the preceding OutputData's flush time must not
	// produce a negative interval or move the cursor backwards.
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: t0, Event: &ProcessStart{PID: 1}},
		{Timestamp: t0.Add(2 * time.Second), Event: &OutputData{Data: []byte("out")}},
		{Timestamp: t0.Add(1500 * time.Millisecond), Event: &ProgramStatus{State: "working", Progress: -1}},
		{Timestamp: t0.Add(3 * time.Second), Event: &OutputData{Data: []byte("more")}},
	}

	lines := collectAscicastLines(ToAscicastV3(eventSeq(events), AscicastV3Metadata{}))
	require.Len(t, lines, 4) // header, o, m, o

	var intervals []any
	for _, l := range lines[1:] {
		d, err := l.MarshalJSON()
		require.NoError(t, err)
		var ev [3]any
		require.NoError(t, json.Unmarshal(d, &ev))
		intervals = append(intervals, ev[0])
	}
	require.Equal(t, []any{2.0, 0.0, 1.0}, intervals)
}
