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

	process := NewProcess("echo", []string{"hello from pipe"})
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
			require.Contains(t, string(outputData.Data), "hello from pipe")
		}
	}
	require.True(t, hasOutput, "Should have output events")
}

func TestProcessExecuteAndStreamPTYMode(t *testing.T) {
	t.Parallel()

	process := NewProcess("echo", []string{"hello from pty"}, WithPTYMode())
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
			require.Contains(t, string(outputData.Data), "hello from pty")
		}
	}
	require.True(t, hasOutput, "Should have output events")
}

func TestProcessWithPTYSize(t *testing.T) {
	t.Parallel()

	size := pty.Winsize{Rows: 24, Cols: 80}
	process := NewProcess("bash", []string{"-c", "tput cols && tput lines"},
		WithPTYMode(),
		WithPTYSize(size))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var outputData []byte
	for part, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if part.EventType() == OutputEvent {
			output := part.Event.(*OutputData)
			outputData = append(outputData, output.Data...)
		}

		// Break when we see a process end
		if part.EventType() == ProcessEndEvent {
			break
		}
	}

	outputStr := string(outputData)
	require.Contains(t, outputStr, "80") // cols
	require.Contains(t, outputStr, "24") // rows
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
	require.Equal(t, PTYStatsSnapshot{}, stats)
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
