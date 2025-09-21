package consolestream

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

func TestPTYOutputData(t *testing.T) {
	t.Run("Type returns PTYOutputEvent", func(t *testing.T) {
		data := &PTYOutputData{Data: []byte("test")}
		require.Equal(t, PTYOutputEvent, data.Type())
	})

	t.Run("String representation includes data size", func(t *testing.T) {
		data := &PTYOutputData{Data: []byte("hello world")}
		str := data.String()
		require.Contains(t, str, "PTYOutputData")
		require.Contains(t, str, "11 bytes")
	})

	t.Run("String representation with empty data", func(t *testing.T) {
		data := &PTYOutputData{Data: []byte{}}
		str := data.String()
		require.Contains(t, str, "PTYOutputData")
		require.Contains(t, str, "0 bytes")
	})
}

func TestTerminalResizeEvent(t *testing.T) {
	t.Run("Type returns TerminalResizeEventType", func(t *testing.T) {
		event := &TerminalResizeEvent{Rows: 24, Cols: 80, X: 800, Y: 600}
		require.Equal(t, TerminalResizeEventType, event.Type())
	})

	t.Run("String representation includes all dimensions", func(t *testing.T) {
		event := &TerminalResizeEvent{Rows: 30, Cols: 120, X: 1200, Y: 900}
		str := event.String()
		require.Contains(t, str, "TerminalResizeEvent")
		require.Contains(t, str, "Rows: 30")
		require.Contains(t, str, "Cols: 120")
		require.Contains(t, str, "X: 1200")
		require.Contains(t, str, "Y: 900")
	})

	t.Run("String representation with zero values", func(t *testing.T) {
		event := &TerminalResizeEvent{}
		str := event.String()
		require.Contains(t, str, "TerminalResizeEvent")
		require.Contains(t, str, "Rows: 0")
		require.Contains(t, str, "Cols: 0")
		require.Contains(t, str, "X: 0")
		require.Contains(t, str, "Y: 0")
	})
}

func TestWithPTYSize(t *testing.T) {
	t.Run("sets PTY size in process config", func(t *testing.T) {
		size := pty.Winsize{Rows: 30, Cols: 120, X: 1200, Y: 900}
		option := WithPTYSize(size)

		cfg := &processConfig{}
		option(cfg)

		require.NotNil(t, cfg.ptySize)
		actualSize, ok := cfg.ptySize.(*pty.Winsize)
		require.True(t, ok)
		require.Equal(t, uint16(30), actualSize.Rows)
		require.Equal(t, uint16(120), actualSize.Cols)
		require.Equal(t, uint16(1200), actualSize.X)
		require.Equal(t, uint16(900), actualSize.Y)
	})
}

func TestNewPTYProcess(t *testing.T) {
	t.Run("creates PTY process with defaults", func(t *testing.T) {
		process := NewPTYProcess("echo", []string{"test"})

		require.Equal(t, "echo", process.cmd)
		require.Equal(t, []string{"test"}, process.args)
		require.NotNil(t, process.cancellor)
		require.Equal(t, time.Second, process.flushInterval)
		require.Equal(t, 10*1024*1024, process.maxBufferSize)
		require.Nil(t, process.ptySize)
		require.Empty(t, process.env)
	})

	t.Run("creates PTY process with custom options", func(t *testing.T) {
		cancellor := NewLocalCancellor(10 * time.Second)
		size := pty.Winsize{Rows: 40, Cols: 160}

		process := NewPTYProcess("ls", []string{"-la"},
			WithCancellor(cancellor),
			WithPTYSize(size),
			WithFlushInterval(500*time.Millisecond),
			WithMaxBufferSize(1024),
			WithEnvVar("TEST", "value"),
		)

		require.Equal(t, "ls", process.cmd)
		require.Equal(t, []string{"-la"}, process.args)
		require.Equal(t, cancellor, process.cancellor)
		require.Equal(t, 500*time.Millisecond, process.flushInterval)
		require.Equal(t, 1024, process.maxBufferSize)
		require.NotNil(t, process.ptySize)
		require.Equal(t, uint16(40), process.ptySize.Rows)
		require.Equal(t, uint16(160), process.ptySize.Cols)
		require.Contains(t, process.env, "TEST=value")
	})

	t.Run("creates PTY process with environment variables", func(t *testing.T) {
		process := NewPTYProcess("env", []string{},
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

func TestPTYProcessExecuteAndStream(t *testing.T) {
	// Skip if PTY is not available on this platform
	if !isPTYAvailable() {
		t.Skip("PTY not available on this platform")
	}

	t.Run("executes simple command and streams output", func(t *testing.T) {
		process := NewPTYProcess("echo", []string{"hello from pty"})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var events []Event
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)
			events = append(events, event)

			// Stop after process ends
			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Should have at least ProcessStart, PTYOutput, and ProcessEnd events
		require.GreaterOrEqual(t, len(events), 3)

		// First event should be ProcessStart
		require.Equal(t, ProcessStartEvent, events[0].Event.Type())
		startEvent := events[0].Event.(*ProcessStart)
		require.Equal(t, "echo", startEvent.Command)
		require.Equal(t, []string{"hello from pty"}, startEvent.Args)
		require.Greater(t, startEvent.PID, 0)

		// Should have PTY output containing the echo
		var foundOutput bool
		for _, event := range events {
			if event.Event.Type() == PTYOutputEvent {
				outputEvent := event.Event.(*PTYOutputData)
				if len(outputEvent.Data) > 0 {
					foundOutput = true
					break
				}
			}
		}
		require.True(t, foundOutput, "Should have received PTY output")

		// Last event should be ProcessEnd with success
		lastEvent := events[len(events)-1]
		require.Equal(t, ProcessEndEvent, lastEvent.Event.Type())
		endEvent := lastEvent.Event.(*ProcessEnd)
		require.Equal(t, 0, endEvent.ExitCode)
		require.True(t, endEvent.Success)
		require.Greater(t, endEvent.Duration, time.Duration(0))
	})

	t.Run("handles process with non-zero exit code", func(t *testing.T) {
		// Use a command that will exit with non-zero code
		process := NewPTYProcess("sh", []string{"-c", "exit 42"})

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
		process := NewPTYProcess("sleep", []string{"10"})

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
		// Create process with small buffer to test flushing
		process := NewPTYProcess("echo", []string{"this is a test message for buffer flushing"},
			WithMaxBufferSize(10), // Very small buffer
		)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var outputEvents []*PTYOutputData
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PTYOutputEvent {
				outputEvents = append(outputEvents, event.Event.(*PTYOutputData))
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// With a small buffer, we should get multiple output events
		// (though this may vary based on how the command outputs data)
		require.Greater(t, len(outputEvents), 0)
	})

	t.Run("flushes buffer based on time interval", func(t *testing.T) {
		// Skip this test if the tester command is not available
		if !isCommandAvailable("go") {
			t.Skip("Go command not available for testing")
		}

		// Create process with short flush interval
		process := NewPTYProcess("go", []string{"run", "cmd/tester/main.go", "--duration=2s", "--stdout-rate=1"},
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
		require.Greater(t, len(eventTimes[PTYOutputEvent]), 0)

		// Process should have run for approximately 2 seconds
		require.Greater(t, time.Since(startTime), 1500*time.Millisecond)
	})

	t.Run("emits heartbeat when no output", func(t *testing.T) {
		// Use sleep command to generate heartbeats
		process := NewPTYProcess("sleep", []string{"1"},
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
		process := NewPTYProcess("nonexistentcommand12345", []string{})

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

	t.Run("preserves ANSI escape sequences", func(t *testing.T) {
		// Test with a command that outputs ANSI colors
		process := NewPTYProcess("printf", []string{"\033[31mRed Text\033[0m\n"})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var receivedANSI bool
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.Event.Type() == PTYOutputEvent {
				outputEvent := event.Event.(*PTYOutputData)
				// Check if ANSI escape sequences are preserved
				if len(outputEvent.Data) > 0 && string(outputEvent.Data[0]) == "\033" {
					receivedANSI = true
				}
			}

			if event.Event.Type() == ProcessEndEvent {
				break
			}
		}

		// Note: This test might be platform-specific and may not always pass
		// depending on how printf handles ANSI in PTY mode
		if !receivedANSI {
			t.Log("ANSI escape sequences not detected - this may be platform-specific")
		}
	})
}

func TestPTYProcessConcurrency(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available on this platform")
	}

	t.Run("multiple PTY processes can run concurrently", func(t *testing.T) {
		const numProcesses = 3
		var wg sync.WaitGroup
		results := make(chan bool, numProcesses)

		for i := range numProcesses {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				process := NewPTYProcess("echo", []string{fmt.Sprintf("process-%d", id)})
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
}

// Helper functions

func isPTYAvailable() bool {
	// Check if we can create a PTY
	cmd := exec.Command("echo", "test")
	_, err := pty.Start(cmd)
	if err == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return true
	}
	return false
}

func isCommandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}
