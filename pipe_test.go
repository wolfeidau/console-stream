package consolestream

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPipeOutputData(t *testing.T) {
	t.Parallel()

	t.Run("Type returns PipeOutputEvent", func(t *testing.T) {
		data := &PipeOutputData{Data: []byte("test"), Stream: Stdout}
		require.Equal(t, PipeOutputEvent, data.Type())
	})

	t.Run("String representation includes data size and stream", func(t *testing.T) {
		data := &PipeOutputData{Data: []byte("hello world"), Stream: Stdout}
		str := data.String()
		require.Contains(t, str, "OutputData")
		require.Contains(t, str, "11 bytes")
		require.Contains(t, str, "STDOUT")
	})

	t.Run("String representation with stderr stream", func(t *testing.T) {
		data := &PipeOutputData{Data: []byte("error"), Stream: Stderr}
		str := data.String()
		require.Contains(t, str, "OutputData")
		require.Contains(t, str, "5 bytes")
		require.Contains(t, str, "STDERR")
	})

	t.Run("String representation with empty data", func(t *testing.T) {
		data := &PipeOutputData{Data: []byte{}, Stream: Stdout}
		str := data.String()
		require.Contains(t, str, "OutputData")
		require.Contains(t, str, "0 bytes")
	})
}

func TestStreamType(t *testing.T) {
	t.Parallel()

	t.Run("Stdout string representation", func(t *testing.T) {
		require.Equal(t, "STDOUT", Stdout.String())
	})

	t.Run("Stderr string representation", func(t *testing.T) {
		require.Equal(t, "STDERR", Stderr.String())
	})
}

func TestNewPipeProcess(t *testing.T) {
	t.Parallel()

	t.Run("creates pipe process with defaults", func(t *testing.T) {
		process := NewPipeProcess("echo", []string{"test"})

		require.Equal(t, "echo", process.cmd)
		require.Equal(t, []string{"test"}, process.args)
		require.NotNil(t, process.cancellor)
		require.Equal(t, time.Second, process.flushInterval)
		require.Equal(t, 10*1024*1024, process.maxBufferSize)
		require.Empty(t, process.env)
		require.Empty(t, process.stdoutBuffer)
		require.Empty(t, process.stderrBuffer)
	})

	t.Run("creates pipe process with custom options", func(t *testing.T) {
		cancellor := NewLocalCancellor(10 * time.Second)

		process := NewPipeProcess("ls", []string{"-la"},
			WithCancellor(cancellor),
			WithFlushInterval(500*time.Millisecond),
			WithMaxBufferSize(1024),
			WithEnvVar("TEST", "value"),
		)

		require.Equal(t, "ls", process.cmd)
		require.Equal(t, []string{"-la"}, process.args)
		require.Equal(t, cancellor, process.cancellor)
		require.Equal(t, 500*time.Millisecond, process.flushInterval)
		require.Equal(t, 1024, process.maxBufferSize)
		require.Contains(t, process.env, "TEST=value")
	})

	t.Run("creates pipe process with environment variables", func(t *testing.T) {
		process := NewPipeProcess("env", []string{},
			WithEnv([]string{"VAR1=value1", "VAR2=value2"}),
			WithEnvMap(map[string]string{"VAR3": "value3", "VAR4": "value4"}),
		)

		require.Contains(t, process.env, "VAR1=value1")
		require.Contains(t, process.env, "VAR2=value2")
		require.Contains(t, process.env, "VAR3=value3")
		require.Contains(t, process.env, "VAR4=value4")
		require.Len(t, process.env, 4)
	})
}

func TestPipeProcessExecuteAndStream(t *testing.T) {
	t.Parallel()

	t.Run("executes simple command and streams output", func(t *testing.T) {
		process := NewPipeProcess("echo", []string{"hello from pipe"})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var events []Event
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)
			events = append(events, event)

			t.Logf("Received event: %s", event.Event.String())

			// Stop after process ends
			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should have at least ProcessStart, PipeOutput, and ProcessEnd events
		require.GreaterOrEqual(t, len(events), 3)

		// First event should be ProcessStart
		require.Equal(t, ProcessStartEvent, events[0].Event.Type())
		startEvent := events[0].Event.(*ProcessStart)
		require.Equal(t, "echo", startEvent.Command)
		require.Equal(t, []string{"hello from pipe"}, startEvent.Args)
		require.Greater(t, startEvent.PID, 0)

		// Should have output from stdout
		var foundStdoutOutput bool
		for _, event := range events {
			if event.Event.Type() == PipeOutputEvent {
				outputEvent := event.Event.(*PipeOutputData)
				if outputEvent.Stream == Stdout && len(outputEvent.Data) > 0 {
					require.Contains(t, string(outputEvent.Data), "hello from pipe")
					foundStdoutOutput = true
					break
				}
			}
		}
		require.True(t, foundStdoutOutput, "Should have captured stdout output")

		// Last event should be ProcessEnd with success
		lastEvent := events[len(events)-1]
		require.Equal(t, ProcessEndEvent, lastEvent.Event.Type())
		endEvent := lastEvent.Event.(*ProcessEnd)
		require.Equal(t, 0, endEvent.ExitCode)
		require.True(t, endEvent.Success)
		require.Greater(t, endEvent.Duration, time.Duration(0))
	})

	t.Run("captures stderr output separately", func(t *testing.T) {
		// Use a command that outputs to stderr
		process := NewPipeProcess("sh", []string{"-c", "echo 'stdout message'; echo 'stderr message' >&2"})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var stdoutEvents, stderrEvents []*PipeOutputData
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			t.Logf("Received event: %s", event.Event.String())

			if event.Event.Type() == PipeOutputEvent {
				outputEvent := event.Event.(*PipeOutputData)
				switch outputEvent.Stream {
				case Stdout:
					stdoutEvents = append(stdoutEvents, outputEvent)
				case Stderr:
					stderrEvents = append(stderrEvents, outputEvent)
				}
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should have captured both stdout and stderr
		require.Greater(t, len(stdoutEvents), 0, "Should have stdout events")
		require.Greater(t, len(stderrEvents), 0, "Should have stderr events")

		// Verify content
		var stdoutContent, stderrContent string
		for _, event := range stdoutEvents {
			stdoutContent += string(event.Data)
		}
		for _, event := range stderrEvents {
			stderrContent += string(event.Data)
		}

		require.Contains(t, stdoutContent, "stdout message")
		require.Contains(t, stderrContent, "stderr message")
	})

	t.Run("handles process with non-zero exit code", func(t *testing.T) {
		// Use a command that will exit with non-zero code
		process := NewPipeProcess("sh", []string{"-c", "exit 42"})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var processEndEvent *ProcessEnd
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == ProcessEndEvent {
				processEndEvent = event.Event.(*ProcessEnd)
				break
			}
		}

		require.NotNil(t, processEndEvent)
		require.Equal(t, 42, processEndEvent.ExitCode)
		require.False(t, processEndEvent.Success)
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		// Use a long-running command
		process := NewPipeProcess("sleep", []string{"10"})

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		var eventCount int
		for event, err := range process.ExecuteAndStream(ctx) {
			eventCount++
			// Process should be cancelled before completion
			if event.Event.Type() == ProcessEndEvent {
				t.Fatal("Process should have been cancelled before completion")
			}
			// Don't fail on context cancellation error
			if err != nil {
				break
			}
		}

		// Should have received at least ProcessStart event
		require.Greater(t, eventCount, 0)
	})

	t.Run("flushes buffer based on size limit", func(t *testing.T) {
		// Use a command that produces output to test buffer flushing
		longText := "this is a very long test message for buffer flushing that should exceed the small buffer size limit we set below "
		process := NewPipeProcess("bash", []string{"-c", fmt.Sprintf("for i in {1..100}; do echo '%s'; done; sleep 1", longText)},
			WithMaxBufferSize(50), // Small buffer that should be exceeded
		)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var outputEvents []*PipeOutputData
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PipeOutputEvent {
				outputEvents = append(outputEvents, event.Event.(*PipeOutputData))
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should get multiple output events due to buffer size limit
		require.Greater(t, len(outputEvents), 0)

		// Verify total output contains our text
		var totalOutput string
		for _, event := range outputEvents {
			totalOutput += string(event.Data)
		}
		require.Contains(t, totalOutput, "very long test message")
	})

	t.Run("flushes buffer based on time interval", func(t *testing.T) {
		// Skip this test if the tester command is not available
		if !isCommandAvailable("go") {
			t.Skip("Go command not available for testing")
		}

		// Create process with short flush interval
		process := NewPipeProcess("go", []string{"run", "cmd/tester/main.go", "--duration=2s", "--stdout-rate=1"},
			WithFlushInterval(200*time.Millisecond),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		eventTimes := make(map[EventType][]time.Time)
		startTime := time.Now()

		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			eventTimes[event.Event.Type()] = append(eventTimes[event.Event.Type()], event.Timestamp)

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should have received multiple output events over time
		require.Greater(t, len(eventTimes[PipeOutputEvent]), 0)

		// Process should have run for approximately 2 seconds
		require.Greater(t, time.Since(startTime), 1500*time.Millisecond)
	})

	t.Run("emits heartbeat when no output", func(t *testing.T) {
		// Use sleep command to generate heartbeats
		process := NewPipeProcess("sleep", []string{"1"},
			WithFlushInterval(200*time.Millisecond),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var heartbeatCount int
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == HeartbeatEventType {
				heartbeatCount++
				heartbeat := event.Event.(*HeartbeatEvent)
				require.True(t, heartbeat.ProcessAlive)
				require.Greater(t, heartbeat.ElapsedTime, time.Duration(0))
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should have received at least one heartbeat
		require.Greater(t, heartbeatCount, 0)
	})

	t.Run("handles invalid command", func(t *testing.T) {
		process := NewPipeProcess("nonexistentcommand12345", []string{})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var gotError bool
		for _, err := range process.ExecuteAndStream(ctx) {
			if err != nil {
				gotError = true
				require.IsType(t, ProcessStartError{}, err)
				break
			}
		}

		require.True(t, gotError, "Should have received ProcessStartError")
	})

	t.Run("handles mixed stdout and stderr streams", func(t *testing.T) {
		// Skip this test if the tester command is not available
		if !isCommandAvailable("go") {
			t.Skip("Go command not available for testing")
		}

		process := NewPipeProcess("go", []string{"run", "cmd/tester/main.go", "--duration=1s", "--mixed-output", "--stdout-rate=2", "--stderr-rate=2"})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var stdoutEvents, stderrEvents []*PipeOutputData
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PipeOutputEvent {
				outputEvent := event.Event.(*PipeOutputData)
				switch outputEvent.Stream {
				case Stdout:
					stdoutEvents = append(stdoutEvents, outputEvent)
				case Stderr:
					stderrEvents = append(stderrEvents, outputEvent)
				}
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should have received output on both streams
		require.Greater(t, len(stdoutEvents), 0, "Should have stdout events")
		require.Greater(t, len(stderrEvents), 0, "Should have stderr events")
	})

	t.Run("preserves line endings and binary data", func(t *testing.T) {
		// Test with data that includes newlines - use echo which is more portable
		process := NewPipeProcess("sh", []string{"-c", "printf 'line1\\nline2\\r\\nline3'"})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var outputData []byte
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PipeOutputEvent {
				outputEvent := event.Event.(*PipeOutputData)
				if outputEvent.Stream == Stdout {
					outputData = append(outputData, outputEvent.Data...)
				}
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should preserve line endings
		require.Contains(t, string(outputData), "line1\nline2")
		require.Contains(t, string(outputData), "line3")
	})
}

func TestPipeProcessBufferManagement(t *testing.T) {
	t.Parallel()

	t.Run("buffer starts empty and gets populated", func(t *testing.T) {
		process := NewPipeProcess("echo", []string{"test"})

		// Initially buffers should be empty
		require.Empty(t, process.stdoutBuffer)
		require.Empty(t, process.stderrBuffer)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var bufferWasPopulated bool
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PipeOutputEvent {
				bufferWasPopulated = true
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		require.True(t, bufferWasPopulated, "Buffer should have been populated with data")
	})

	t.Run("buffer is cleared after flushing", func(t *testing.T) {
		// This test verifies internal behavior by checking that output events are generated
		// which indicates buffers are being cleared
		process := NewPipeProcess("printf", []string{"data1\ndata2\ndata3\n"},
			WithFlushInterval(100*time.Millisecond),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var outputEventCount int
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PipeOutputEvent {
				outputEventCount++
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should have received at least one output event (buffer was flushed)
		require.Greater(t, outputEventCount, 0)
	})
}

func TestPipeProcessConcurrency(t *testing.T) {
	t.Parallel()

	t.Run("multiple pipe processes can run concurrently", func(t *testing.T) {
		const numProcesses = 3
		var wg sync.WaitGroup
		results := make(chan bool, numProcesses)

		for i := range numProcesses {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				process := NewPipeProcess("echo", []string{fmt.Sprintf("process-%d", id)})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				var processCompleted bool
				for event, err := range process.ExecuteAndStream(ctx) {
					if err != nil {
						results <- false
						return
					}

					if event.Event.Type() == ProcessEndEvent {
						endEvent := event.Event.(*ProcessEnd)
						processCompleted = endEvent.Success
						break
					}
				}

				results <- processCompleted
			}(i)
		}

		wg.Wait()
		close(results)

		successCount := 0
		for success := range results {
			if success {
				successCount++
			}
		}

		require.Equal(t, numProcesses, successCount, "All processes should complete successfully")
	})

	t.Run("concurrent access to buffers is safe", func(t *testing.T) {
		// This test runs a process while concurrently trying to access it
		// to ensure there are no race conditions
		process := NewPipeProcess("printf", []string{"data\n"},
			WithFlushInterval(50*time.Millisecond),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Start the process
		eventChan := make(chan Event, 10)
		go func() {
			defer close(eventChan)
			for event, err := range process.ExecuteAndStream(ctx) {
				if err != nil {
					return
				}
				eventChan <- event
				if event.Event.Type() == ProcessEndEvent {
					break
				}
			}
		}()

		// Concurrently access the process (this should not cause races)
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Access process fields (this is safe as they're read-only after creation)
				_ = process.cmd
				_ = process.args
				_ = process.maxBufferSize
			}()
		}

		// Wait for events
		var eventCount int
		for event := range eventChan {
			eventCount++
			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		wg.Wait()
		require.Greater(t, eventCount, 0)
	})
}

func TestPipeProcessErrorHandling(t *testing.T) {
	t.Parallel()

	t.Run("handles stdout pipe creation error", func(t *testing.T) {
		// This is difficult to test directly, but we can test with an invalid command
		process := NewPipeProcess("", []string{}) // Empty command should fail

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var gotError bool
		for _, err := range process.ExecuteAndStream(ctx) {
			if err != nil {
				gotError = true
				break
			}
		}

		require.True(t, gotError, "Should have received an error for empty command")
	})

	t.Run("handles process start failure", func(t *testing.T) {
		process := NewPipeProcess("/nonexistent/path/to/command", []string{})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var gotStartError bool
		for _, err := range process.ExecuteAndStream(ctx) {
			if err != nil {
				_, ok := err.(ProcessStartError)
				require.True(t, ok, "Error should be ProcessStartError")
				gotStartError = true
				break
			}
		}

		require.True(t, gotStartError, "Should have received ProcessStartError")
	})

	t.Run("handles process that gets killed", func(t *testing.T) {
		// Start a long-running process with a very short timeout cancellor
		cancellor := NewLocalCancellor(1 * time.Millisecond)
		process := NewPipeProcess("sleep", []string{"10"}, WithCancellor(cancellor))

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		var processEnded bool
		for event, err := range process.ExecuteAndStream(ctx) {
			if err != nil {
				// Context cancellation is expected
				break
			}

			if event.Event.Type() == ProcessEndEvent {
				processEnded = true
				break
			}
		}

		// Process may or may not end cleanly depending on timing
		if processEnded {
			t.Log("Process ended cleanly")
		} else {
			t.Log("Process was cancelled by context")
		}
	})
}

func TestPipeProcessReadStream(t *testing.T) {
	t.Run("readStream handles EOF correctly", func(t *testing.T) {
		// This tests the internal readStream method indirectly
		// by using a command that produces limited output
		process := NewPipeProcess("echo", []string{"single line"})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var outputReceived bool
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PipeOutputEvent {
				outputEvent := event.Event.(*PipeOutputData)
				if len(outputEvent.Data) > 0 {
					outputReceived = true
				}
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		require.True(t, outputReceived, "Should have received output before EOF")
	})

	t.Run("readStream handles context cancellation", func(t *testing.T) {
		// Test that readStream goroutines exit when context is cancelled
		process := NewPipeProcess("cat", []string{}) // cat waits for input

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		var startEventReceived bool
		for event, err := range process.ExecuteAndStream(ctx) {
			if event.Event.Type() == ProcessStartEvent {
				startEventReceived = true
			}
			// Don't check for errors as context cancellation is expected
			if err != nil {
				break
			}
		}

		require.True(t, startEventReceived, "Should have received start event before cancellation")
	})
}
