package consolestream

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// NewPTYStats creates a new PTYStats instance with OpenTelemetry metrics
func NewPTYStats(meter metric.Meter, interval time.Duration, processInfo ProcessInfo) (*PTYStats, error) {
	if meter == nil {
		return nil, fmt.Errorf("meter cannot be nil")
	}

	stats := &PTYStats{
		metricsInterval: interval,
		processInfo:     processInfo,
	}

	var err error

	// Create histogram for output bytes per flush interval
	stats.outputBytesHistogram, err = meter.Int64Histogram(
		"pty.output.bytes",
		metric.WithDescription("Bytes output from PTY process per flush interval"),
		metric.WithUnit("bytes"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create output bytes histogram: %w", err)
	}

	// Create histogram for input bytes per flush interval (for future use)
	stats.inputBytesHistogram, err = meter.Int64Histogram(
		"pty.input.bytes",
		metric.WithDescription("Bytes input to PTY process per flush interval"),
		metric.WithUnit("bytes"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create input bytes histogram: %w", err)
	}

	// Create counter for total events
	stats.eventCounter, err = meter.Int64Counter(
		"pty.events.total",
		metric.WithDescription("Total number of PTY events processed"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create event counter: %w", err)
	}

	// Create gauge for process status
	stats.processStatusGauge, err = meter.Int64Gauge(
		"pty.process.status",
		metric.WithDescription("PTY process status (0=stopped, 1=running)"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create process status gauge: %w", err)
	}

	return stats, nil
}

// Start initializes the stats and records process start
func (s *PTYStats) Start(ctx context.Context) {
	s.mutex.Lock()
	s.startTime = time.Now()
	s.mutex.Unlock()

	// Record process status as running
	if s.processStatusGauge != nil {
		s.processStatusGauge.Record(ctx, 1,
			metric.WithAttributes(
				attribute.String("command", s.processInfo.Command),
				attribute.Int("pid", s.processInfo.PID),
			),
		)
	}
}

// Stop finalizes the stats and records process stop
func (s *PTYStats) Stop(ctx context.Context) {
	s.mutex.Lock()
	s.duration = time.Since(s.startTime)
	s.mutex.Unlock()

	// Record process status as stopped
	if s.processStatusGauge != nil {
		s.processStatusGauge.Record(ctx, 0,
			metric.WithAttributes(
				attribute.String("command", s.processInfo.Command),
				attribute.Int("pid", s.processInfo.PID),
			),
		)
	}
}

// SetExitCode sets the process exit code
func (s *PTYStats) SetExitCode(code int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.exitCode = &code
}

// RecordError increments the error count
func (s *PTYStats) RecordError(ctx context.Context, eventType string) {
	s.mutex.Lock()
	s.errorCount++
	s.eventCount++
	s.mutex.Unlock()

	// Record event counter
	if s.eventCounter != nil {
		s.eventCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("event_type", eventType),
				attribute.String("command", s.processInfo.Command),
				attribute.Int("pid", s.processInfo.PID),
			),
		)
	}
}

// RecordEvent records a general event
func (s *PTYStats) RecordEvent(ctx context.Context, eventType string) {
	s.mutex.Lock()
	s.eventCount++
	s.mutex.Unlock()

	// Record event counter
	if s.eventCounter != nil {
		s.eventCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("event_type", eventType),
				attribute.String("command", s.processInfo.Command),
				attribute.Int("pid", s.processInfo.PID),
			),
		)
	}
}

// RecordOutput records output data and metrics
func (s *PTYStats) RecordOutput(ctx context.Context, dataSize int64) {
	s.mutex.Lock()
	s.eventCount++
	s.mutex.Unlock()

	// Record event counter
	if s.eventCounter != nil {
		s.eventCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("event_type", "pty_output"),
				attribute.String("command", s.processInfo.Command),
				attribute.Int("pid", s.processInfo.PID),
			),
		)
	}

	// Record output bytes histogram
	if dataSize > 0 && s.outputBytesHistogram != nil {
		s.outputBytesHistogram.Record(ctx, dataSize,
			metric.WithAttributes(
				attribute.String("command", s.processInfo.Command),
				attribute.Int("pid", s.processInfo.PID),
			),
		)
	}
}

// GetSnapshot returns a thread-safe snapshot of the current statistics
func (s *PTYStats) GetSnapshot() PTYStatsSnapshot {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Create a deep copy to avoid race conditions
	snapshot := PTYStatsSnapshot{
		StartTime:       s.startTime,
		EventCount:      s.eventCount,
		ErrorCount:      s.errorCount,
		ExitCode:        s.exitCode,
		Duration:        s.duration,
		metricsInterval: s.metricsInterval,
	}

	return snapshot
}
