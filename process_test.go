package consolestream

import (
	"context"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

func TestNewProcess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  string
		args []string
		opts []ProcessOption
	}{
		{
			name: "basic pipe process",
			cmd:  "echo",
			args: []string{"test"},
		},
		{
			name: "pipe process with options",
			cmd:  "ls",
			args: []string{"-la"},
			opts: []ProcessOption{
				WithPipeMode(),
				WithCancellor(NewLocalCancellor(5 * time.Second)),
			},
		},
		{
			name: "pty process with env",
			cmd:  "env",
			args: []string{},
			opts: []ProcessOption{
				WithPTYMode(),
				WithEnvVar("TEST_VAR", "test_value"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			process := NewProcess(tt.cmd, tt.args, tt.opts...)
			require.NotNil(t, process)
			require.Equal(t, tt.cmd, process.cmd)
			require.Equal(t, tt.args, process.args)
		})
	}
}

func TestProcessExecuteAndStreamPipeMode(t *testing.T) {
	t.Parallel()

	process := NewProcess("bash", []string{"-c", "for i in {1..10}; do echo $i; done; sleep 1;"}, WithPipeMode(), WithFlushInterval(500*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []Event
	for part, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		events = append(events, part)

		// Break when we see a process end to avoid waiting
		if part.EventType() == ProcessEndEvent {
			break
		}
	}

	t.Logf("Received %d events", len(events))

	require.GreaterOrEqual(t, len(events), 2) // At least ProcessStart and ProcessEnd

	// Check that we have the expected event types
	require.Equal(t, ProcessStartEvent, events[0].EventType())
	require.Equal(t, ProcessEndEvent, events[len(events)-1].EventType())

	// Check for output events
	hasOutput := false
	for _, event := range events {
		if event.EventType() == OutputEvent {
			hasOutput = true
			outputData := event.Event.(*OutputData)
			require.Contains(t, string(outputData.Data), "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
		}
	}
	require.True(t, hasOutput, "Should have output events")
}

func TestProcessExecuteAndStreamPTYMode(t *testing.T) {
	t.Parallel()

	process := NewProcess("bash", []string{"-c", "for i in {1..10}; do echo $i; done; sleep 1;"}, WithPTYMode(), WithFlushInterval(500*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []Event
	for part, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		events = append(events, part)

		// Break when we see a process end to avoid waiting
		if part.EventType() == ProcessEndEvent {
			break
		}
	}

	t.Logf("Received %d events", len(events))

	require.GreaterOrEqual(t, len(events), 2) // At least ProcessStart and ProcessEnd

	// Check that we have the expected event types
	require.Equal(t, ProcessStartEvent, events[0].EventType())
	require.Equal(t, ProcessEndEvent, events[len(events)-1].EventType())

	// Check for output events
	hasOutput := false
	for _, event := range events {
		if event.EventType() == OutputEvent {
			hasOutput = true
			outputData := event.Event.(*OutputData)
			require.Contains(t, string(outputData.Data), "1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n7\r\n8\r\n9\r\n10\r\n")
		}
	}
	require.True(t, hasOutput, "Should have output events")
}

func TestProcessPTYSizeApplied(t *testing.T) {
	t.Parallel()

	process := NewProcess("stty", []string{"size"},
		WithPTYMode(),
		WithPTYSize(&pty.Winsize{Rows: 40, Cols: 132}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var outputData []byte
	for part, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if part.EventType() == OutputEvent {
			outputData = append(outputData, part.Event.(*OutputData).Data...)
		}
	}

	require.Contains(t, string(outputData), "40 132")
}

func TestProcessExecuteAndStreamExitCode(t *testing.T) {
	t.Parallel()

	process := NewProcess("sh", []string{"-c", "exit 42"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var exitCode int
	var success bool
	for part, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if part.EventType() == ProcessEndEvent {
			processEnd := part.Event.(*ProcessEnd)
			exitCode = processEnd.ExitCode
			success = processEnd.Success
			break
		}
	}

	require.Equal(t, 42, exitCode)
	require.False(t, success)
}

func TestProcessExecuteAndStreamSignalExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sig  string
		opts []ProcessOption
		want int
	}{
		{"pipe kill", "KILL", nil, 137},
		{"pty kill", "KILL", []ProcessOption{WithPTYMode()}, 137},
		{"pipe term", "TERM", nil, 143},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			process := NewProcess("bash", []string{"-c", "kill -" + tt.sig + " $$"}, tt.opts...)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var end *ProcessEnd
			for part, err := range process.ExecuteAndStream(ctx) {
				require.NoError(t, err)
				if e, ok := part.Event.(*ProcessEnd); ok {
					end = e
				}
			}

			require.NotNil(t, end)
			require.Equal(t, tt.want, end.ExitCode)
			require.False(t, end.Success)
		})
	}
}

func TestProcessCancellation(t *testing.T) {
	t.Parallel()

	process := NewProcess("sleep", []string{"10"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	for part, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			// Context cancellation is expected
			break
		}
		_ = part
		// Should exit due to context timeout
		if time.Since(start) > 2*time.Second {
			t.Fatal("Process should have been cancelled")
		}
	}

	require.Less(t, time.Since(start), 2*time.Second, "Process should be cancelled quickly")
}

func TestProcessBufferFlushing(t *testing.T) {
	t.Parallel()

	// Create a process with custom flush settings
	process := NewProcess("printf", []string{"data1\ndata2\ndata3\n"},
		WithFlushInterval(100*time.Millisecond),
		WithMaxBufferSize(1024))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	outputEvents := 0
	for part, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if part.EventType() == OutputEvent {
			outputEvents++
		}

		// Break when we see a process end
		if part.EventType() == ProcessEndEvent {
			break
		}
	}

	require.GreaterOrEqual(t, outputEvents, 1, "Should have at least one output event")
}

func TestProcessConcurrentExecution(t *testing.T) {
	t.Parallel()

	const numProcesses = 5
	done := make(chan bool, numProcesses)

	for i := range numProcesses {
		go func(id int) {
			defer func() { done <- true }()

			process := NewProcess("echo", []string{"concurrent test"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			eventCount := 0
			for part, err := range process.ExecuteAndStream(ctx) {
				require.NoError(t, err)
				eventCount++

				if part.EventType() == ProcessEndEvent {
					break
				}
			}

			require.GreaterOrEqual(t, eventCount, 2, "Should have at least ProcessStart and ProcessEnd")
		}(i)
	}

	// Wait for all goroutines to complete
	for range numProcesses {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Concurrent execution timed out")
		}
	}
}

func TestProcessWithMetrics(t *testing.T) {
	t.Parallel()

	// This test verifies that metrics don't break the process when meter is nil
	process := NewProcess("echo", []string{"test"}, WithMeter(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCount := 0
	for part, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		eventCount++

		if part.EventType() == ProcessEndEvent {
			break
		}
	}

	require.GreaterOrEqual(t, eventCount, 2)

	// Verify stats are empty when no meter provided
	stats := process.GetStats()
	require.Equal(t, ProcessStatsSnapshot{}, stats)
}

func TestProcessModeSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []ProcessOption
		mode ProcessMode
	}{
		{
			name: "default mode is pipe",
			opts: []ProcessOption{},
			mode: PipeMode,
		},
		{
			name: "explicit pipe mode",
			opts: []ProcessOption{WithPipeMode()},
			mode: PipeMode,
		},
		{
			name: "pty mode",
			opts: []ProcessOption{WithPTYMode()},
			mode: PTYMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			process := NewProcess("echo", []string{"test"}, tt.opts...)
			require.Equal(t, tt.mode, process.mode)
		})
	}
}

func TestProcessPTYOutputSegmentTimes(t *testing.T) {
	t.Parallel()

	process := NewProcess("bash", []string{"-c", "printf a; sleep 0.3; printf b"}, WithPTYMode(), WithFlushInterval(100*time.Millisecond))

	var times []time.Time
	for event, err := range process.ExecuteAndStream(context.Background()) {
		require.NoError(t, err)
		if e, ok := event.Event.(*OutputData); ok {
			for _, s := range e.Segments {
				times = append(times, s.Time)
			}
		}
	}

	require.GreaterOrEqual(t, len(times), 2)
	require.GreaterOrEqual(t, times[len(times)-1].Sub(times[0]), 200*time.Millisecond)
}

func TestProcessHeartbeatInterval(t *testing.T) {
	t.Parallel()

	countHeartbeats := func(t *testing.T, p *Process) int {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n := 0
		for event, err := range p.ExecuteAndStream(ctx) {
			require.NoError(t, err)
			if IsHeartbeatEvent(event.Event) {
				n++
			}
		}
		return n
	}

	t.Run("independent of batch interval", func(t *testing.T) {
		t.Parallel()
		// Ticks at ~1s and ~2s; a 50ms batch interval must not add heartbeats.
		p := NewProcess("sleep", []string{"2.5"},
			WithFlushInterval(50*time.Millisecond),
			WithHeartbeatInterval(time.Second))
		n := countHeartbeats(t, p)
		require.GreaterOrEqual(t, n, 1)
		require.LessOrEqual(t, n, 3)
	})

	t.Run("none while output is delivered", func(t *testing.T) {
		t.Parallel()
		p := NewProcess("bash", []string{"-c", "for i in $(seq 15); do echo $i; sleep 0.1; done"},
			WithFlushInterval(50*time.Millisecond),
			WithHeartbeatInterval(500*time.Millisecond))
		require.Equal(t, 0, countHeartbeats(t, p))
	})

	t.Run("buffered output counts as activity", func(t *testing.T) {
		t.Parallel()
		// Output stays buffered under the 5s batch interval; no heartbeat should fire.
		p := NewProcess("bash", []string{"-c", "for i in $(seq 15); do echo $i; sleep 0.1; done"},
			WithFlushInterval(5*time.Second),
			WithHeartbeatInterval(500*time.Millisecond))
		require.Equal(t, 0, countHeartbeats(t, p))
	})
}

const statusScript = `printf 'a\033]7501;state=working:progress=60:msg=aGk=\033\\b'`

const statusStream = "a\x1b]7501;state=working:progress=60:msg=aGk=\x1b\\b"

func collectEvents(t *testing.T, p *Process) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var events []Event
	for event, err := range p.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		events = append(events, event)
	}
	return events
}

func TestProcessProgramStatusOrdering(t *testing.T) {
	t.Parallel()

	for name, mode := range map[string]ProcessOption{"pipe": WithPipeMode(), "pty": WithPTYMode()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := NewProcess("bash", []string{"-c", statusScript}, mode, WithProgramStatus())
			events := collectEvents(t, p)

			var all, before, after []byte
			statuses := 0
			for _, e := range events {
				switch ev := e.Event.(type) {
				case *OutputData:
					all = append(all, ev.Data...)
					if statuses == 0 {
						before = append(before, ev.Data...)
					} else {
						after = append(after, ev.Data...)
					}
				case *ProgramStatus:
					statuses++
					require.Equal(t, "working", ev.State)
					require.Equal(t, 60, ev.Progress)
					require.Equal(t, "hi", ev.Msg)
				}
			}
			require.Equal(t, 1, statuses)
			require.Equal(t, statusStream, string(all))
			require.Equal(t, "a\x1b]7501;state=working:progress=60:msg=aGk=\x1b\\", string(before))
			require.Equal(t, "b", string(after))
		})
	}
}

func TestProcessProgramStatusPrompt(t *testing.T) {
	t.Parallel()

	for name, mode := range map[string]ProcessOption{"pipe": WithPipeMode(), "pty": WithPTYMode()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			script := `printf '\033]7501;state=blocked:kind=permission\033\\'; sleep 3`
			p := NewProcess("bash", []string{"-c", script}, mode, WithProgramStatus())

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			var startedAt time.Time
			for event, err := range p.ExecuteAndStream(ctx) {
				require.NoError(t, err)
				switch ev := event.Event.(type) {
				case *ProcessStart:
					startedAt = time.Now()
				case *ProgramStatus:
					require.Equal(t, "blocked", ev.State)
					require.Equal(t, "permission", ev.Kind)
					require.Equal(t, -1, ev.Progress)
					require.Less(t, time.Since(startedAt), 500*time.Millisecond)
					return
				}
			}
			require.Fail(t, "no ProgramStatus received")
		})
	}
}

func TestProcessProgramStatusOffByDefault(t *testing.T) {
	t.Parallel()

	p := NewProcess("bash", []string{"-c", statusScript})
	var all []byte
	for _, e := range collectEvents(t, p) {
		require.False(t, IsProgramStatusEvent(e.Event))
		if o, ok := e.Event.(*OutputData); ok {
			all = append(all, o.Data...)
		}
	}
	require.Equal(t, statusStream, string(all))
}
