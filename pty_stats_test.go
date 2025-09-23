package consolestream

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestNewPTYStats(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	interval := time.Second * 5
	meter := noop.NewMeterProvider().Meter("test")

	t.Run("creates PTYStats with valid meter", func(t *testing.T) {
		stats, err := NewPTYStats(meter, interval, processInfo)
		require.NoError(t, err)
		require.NotNil(t, stats)
		require.Equal(t, interval, stats.metricsInterval)
		require.Equal(t, processInfo, stats.processInfo)
	})

	t.Run("returns error with nil meter", func(t *testing.T) {
		stats, err := NewPTYStats(nil, interval, processInfo)
		require.Error(t, err)
		require.Nil(t, stats)
		require.Contains(t, err.Error(), "meter cannot be nil")
	})
}

func TestPTYStatsStart(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	ctx := context.Background()
	beforeStart := time.Now()

	stats.Start(ctx)

	afterStart := time.Now()

	// Verify start time was set
	snapshot := stats.GetSnapshot()
	require.True(t, snapshot.StartTime.After(beforeStart) || snapshot.StartTime.Equal(beforeStart))
	require.True(t, snapshot.StartTime.Before(afterStart) || snapshot.StartTime.Equal(afterStart))
}

func TestPTYStatsStop(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	ctx := context.Background()
	stats.Start(ctx)

	// Wait a bit to ensure duration is measurable
	time.Sleep(10 * time.Millisecond)

	stats.Stop(ctx)

	snapshot := stats.GetSnapshot()
	require.True(t, snapshot.Duration > 0)
	require.True(t, snapshot.Duration < time.Second) // Should be much less than a second
}

func TestPTYStatsSetExitCode(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	t.Run("sets exit code", func(t *testing.T) {
		exitCode := 42
		stats.SetExitCode(exitCode)

		snapshot := stats.GetSnapshot()
		require.NotNil(t, snapshot.ExitCode)
		require.Equal(t, exitCode, *snapshot.ExitCode)
	})

	t.Run("sets zero exit code", func(t *testing.T) {
		exitCode := 0
		stats.SetExitCode(exitCode)

		snapshot := stats.GetSnapshot()
		require.NotNil(t, snapshot.ExitCode)
		require.Equal(t, exitCode, *snapshot.ExitCode)
	})
}

func TestPTYStatsRecordError(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("records single error", func(t *testing.T) {
		initialSnapshot := stats.GetSnapshot()
		initialErrorCount := initialSnapshot.ErrorCount
		initialEventCount := initialSnapshot.EventCount

		stats.RecordError(ctx, "test_error")

		snapshot := stats.GetSnapshot()
		require.Equal(t, initialErrorCount+1, snapshot.ErrorCount)
		require.Equal(t, initialEventCount+1, snapshot.EventCount)
	})

	t.Run("records multiple errors", func(t *testing.T) {
		initialSnapshot := stats.GetSnapshot()
		initialErrorCount := initialSnapshot.ErrorCount
		initialEventCount := initialSnapshot.EventCount

		stats.RecordError(ctx, "error1")
		stats.RecordError(ctx, "error2")
		stats.RecordError(ctx, "error3")

		snapshot := stats.GetSnapshot()
		require.Equal(t, initialErrorCount+3, snapshot.ErrorCount)
		require.Equal(t, initialEventCount+3, snapshot.EventCount)
	})
}

func TestPTYStatsRecordEvent(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("records single event", func(t *testing.T) {
		initialSnapshot := stats.GetSnapshot()
		initialEventCount := initialSnapshot.EventCount

		stats.RecordEvent(ctx, "test_event")

		snapshot := stats.GetSnapshot()
		require.Equal(t, initialEventCount+1, snapshot.EventCount)
		// Error count should not change for regular events
		require.Equal(t, initialSnapshot.ErrorCount, snapshot.ErrorCount)
	})

	t.Run("records multiple events", func(t *testing.T) {
		initialSnapshot := stats.GetSnapshot()
		initialEventCount := initialSnapshot.EventCount

		stats.RecordEvent(ctx, "event1")
		stats.RecordEvent(ctx, "event2")

		snapshot := stats.GetSnapshot()
		require.Equal(t, initialEventCount+2, snapshot.EventCount)
		require.Equal(t, initialSnapshot.ErrorCount, snapshot.ErrorCount)
	})
}

func TestPTYStatsRecordOutput(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("records output with positive data size", func(t *testing.T) {
		initialSnapshot := stats.GetSnapshot()
		initialEventCount := initialSnapshot.EventCount

		stats.RecordOutput(ctx, 100)

		snapshot := stats.GetSnapshot()
		require.Equal(t, initialEventCount+1, snapshot.EventCount)
		require.Equal(t, initialSnapshot.ErrorCount, snapshot.ErrorCount)
	})

	t.Run("records output with zero data size", func(t *testing.T) {
		initialSnapshot := stats.GetSnapshot()
		initialEventCount := initialSnapshot.EventCount

		stats.RecordOutput(ctx, 0)

		snapshot := stats.GetSnapshot()
		require.Equal(t, initialEventCount+1, snapshot.EventCount)
		require.Equal(t, initialSnapshot.ErrorCount, snapshot.ErrorCount)
	})

	t.Run("records multiple outputs", func(t *testing.T) {
		initialSnapshot := stats.GetSnapshot()
		initialEventCount := initialSnapshot.EventCount

		stats.RecordOutput(ctx, 50)
		stats.RecordOutput(ctx, 200)
		stats.RecordOutput(ctx, 0)

		snapshot := stats.GetSnapshot()
		require.Equal(t, initialEventCount+3, snapshot.EventCount)
		require.Equal(t, initialSnapshot.ErrorCount, snapshot.ErrorCount)
	})
}

func TestPTYStatsGetSnapshot(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("returns snapshot with initial values", func(t *testing.T) {
		snapshot := stats.GetSnapshot()
		require.True(t, snapshot.StartTime.IsZero())
		require.Equal(t, 0, snapshot.EventCount)
		require.Equal(t, 0, snapshot.ErrorCount)
		require.Nil(t, snapshot.ExitCode)
		require.Equal(t, time.Duration(0), snapshot.Duration)
		require.Equal(t, time.Second, snapshot.metricsInterval)
	})

	t.Run("returns snapshot after operations", func(t *testing.T) {
		stats.Start(ctx)
		stats.RecordEvent(ctx, "test")
		stats.RecordError(ctx, "error")
		stats.SetExitCode(1)
		time.Sleep(10 * time.Millisecond)
		stats.Stop(ctx)

		snapshot := stats.GetSnapshot()
		require.False(t, snapshot.StartTime.IsZero())
		require.Equal(t, 2, snapshot.EventCount) // 1 event + 1 error
		require.Equal(t, 1, snapshot.ErrorCount)
		require.NotNil(t, snapshot.ExitCode)
		require.Equal(t, 1, *snapshot.ExitCode)
		require.True(t, snapshot.Duration > 0)
	})

	t.Run("snapshot is independent of original", func(t *testing.T) {
		snapshot1 := stats.GetSnapshot()
		stats.RecordEvent(ctx, "new_event")
		snapshot2 := stats.GetSnapshot()

		// First snapshot should not change
		require.Equal(t, snapshot1.EventCount, snapshot1.EventCount)
		// Second snapshot should have incremented count
		require.Equal(t, snapshot1.EventCount+1, snapshot2.EventCount)
	})
}

func TestPTYStatsThreadSafety(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "echo",
		Args:    []string{"test"},
		PID:     1234,
	}
	meter := noop.NewMeterProvider().Meter("test")
	stats, err := NewPTYStats(meter, time.Second, processInfo)
	require.NoError(t, err)

	ctx := context.Background()
	stats.Start(ctx)

	// Test concurrent access to ensure thread safety
	t.Run("concurrent operations", func(t *testing.T) {
		const numGoroutines = 10
		const operationsPerGoroutine = 100

		var wg sync.WaitGroup
		wg.Add(numGoroutines * 4) // 4 different operation types

		// Concurrent RecordEvent calls
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()
				for j := 0; j < operationsPerGoroutine; j++ {
					stats.RecordEvent(ctx, "concurrent_event")
				}
			}(i)
		}

		// Concurrent RecordError calls
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()
				for j := 0; j < operationsPerGoroutine; j++ {
					stats.RecordError(ctx, "concurrent_error")
				}
			}(i)
		}

		// Concurrent RecordOutput calls
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()
				for j := 0; j < operationsPerGoroutine; j++ {
					stats.RecordOutput(ctx, int64(j+1))
				}
			}(i)
		}

		// Concurrent GetSnapshot calls
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()
				for j := 0; j < operationsPerGoroutine; j++ {
					snapshot := stats.GetSnapshot()
					require.NotNil(t, snapshot)
				}
			}(i)
		}

		wg.Wait()

		final := stats.GetSnapshot()
		// We should have: numGoroutines * operationsPerGoroutine for each of the 3 recording operations
		expectedTotal := numGoroutines * operationsPerGoroutine * 3
		require.Equal(t, expectedTotal, final.EventCount)
		require.Equal(t, numGoroutines*operationsPerGoroutine, final.ErrorCount)
	})
}

func TestPTYStatsIntegration(t *testing.T) {
	t.Parallel()

	processInfo := ProcessInfo{
		Command: "test-command",
		Args:    []string{"--verbose", "--output"},
		PID:     5678,
	}
	meter := noop.NewMeterProvider().Meter("integration-test")
	interval := 100 * time.Millisecond

	stats, err := NewPTYStats(meter, interval, processInfo)
	require.NoError(t, err)

	ctx := context.Background()

	// Simulate a complete process lifecycle
	t.Run("complete lifecycle", func(t *testing.T) {
		// Process starts
		beforeStart := time.Now()
		stats.Start(ctx)

		// Some processing happens
		stats.RecordOutput(ctx, 256)
		stats.RecordEvent(ctx, "initialization")
		stats.RecordOutput(ctx, 512)
		stats.RecordEvent(ctx, "processing")

		// An error occurs
		stats.RecordError(ctx, "temporary_failure")

		// More processing
		stats.RecordOutput(ctx, 1024)
		stats.RecordEvent(ctx, "recovery")

		// Wait to ensure some duration
		time.Sleep(50 * time.Millisecond)

		// Process exits with code
		stats.SetExitCode(0)
		stats.Stop(ctx)
		afterStop := time.Now()

		// Verify final state
		snapshot := stats.GetSnapshot()
		require.True(t, snapshot.StartTime.After(beforeStart) || snapshot.StartTime.Equal(beforeStart))
		require.True(t, snapshot.StartTime.Before(afterStop))
		require.Equal(t, 7, snapshot.EventCount) // 3 outputs + 3 events + 1 error
		require.Equal(t, 1, snapshot.ErrorCount)
		require.NotNil(t, snapshot.ExitCode)
		require.Equal(t, 0, *snapshot.ExitCode)
		require.True(t, snapshot.Duration > 0)
		require.True(t, snapshot.Duration < time.Second)
	})
}
