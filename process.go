package consolestream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ProcessInfo contains process information for metrics attributes
type ProcessInfo struct {
	Command string
	Args    []string
	PID     int
}

// ProcessStatsSnapshot represents a point-in-time view of process statistics
type ProcessStatsSnapshot struct {
	// Basic execution stats
	StartTime  time.Time
	EventCount int
	ErrorCount int
	ExitCode   *int
	Duration   time.Duration

	// Metrics collection interval
	metricsInterval time.Duration
}

// ProcessStats manages comprehensive metrics during process execution
type ProcessStats struct {
	// Basic execution stats
	startTime  time.Time
	eventCount int
	errorCount int
	exitCode   *int
	duration   time.Duration

	// OpenTelemetry metrics instruments
	outputBytesHistogram metric.Int64Histogram
	inputBytesHistogram  metric.Int64Histogram
	eventCounter         metric.Int64Counter
	processStatusGauge   metric.Int64Gauge

	// Configuration
	metricsInterval time.Duration
	processInfo     ProcessInfo

	// Thread safety
	mutex sync.RWMutex
}

// TerminalResizeEvent represents a terminal window size change
type TerminalResizeEvent struct {
	Rows uint16
	Cols uint16
	X    uint16 // Width in pixels
	Y    uint16 // Height in pixels
}

func (t *TerminalResizeEvent) Type() EventType {
	return TerminalResizeEventType
}

func (t *TerminalResizeEvent) String() string {
	return fmt.Sprintf("TerminalResizeEvent{Rows: %d, Cols: %d, X: %d, Y: %d}", t.Rows, t.Cols, t.X, t.Y)
}

// WithPTYSize sets a custom terminal size for PTY processes
func WithPTYSize(size any) ProcessOption {
	return func(cfg *processConfig) {
		cfg.ptySize = size
	}
}

// Process represents a unified process that can run in either PTY or pipe mode
type Process struct {
	cmd           string
	args          []string
	pid           int
	mode          ProcessMode
	cancellor     Cancellor
	env           []string
	workingDir    string
	ptySize       *pty.Winsize
	flushInterval time.Duration
	maxBufferSize int

	// Buffer management
	outputWriter *BufferWriter

	// Pipe mode: separate buffers for stdout/stderr (combined into single stream)
	bufferMu     sync.Mutex
	stdoutBuffer []byte
	stderrBuffer []byte

	// Metrics and observability (optional)
	stats *ProcessStats

	waiter sync.WaitGroup
}

// NewProcess creates a new unified process with functional options
func NewProcess(cmd string, args []string, opts ...ProcessOption) *Process {
	cfg := &processConfig{}
	cfg.applyDefaults()

	for _, opt := range opts {
		opt(cfg)
	}

	var ptySize *pty.Winsize
	if cfg.ptySize != nil {
		if size, ok := cfg.ptySize.(*pty.Winsize); ok {
			ptySize = size
		}
	}

	process := &Process{
		cmd:           cmd,
		args:          args,
		mode:          cfg.mode,
		cancellor:     cfg.cancellor,
		env:           cfg.env,
		workingDir:    cfg.workingDir,
		ptySize:       ptySize,
		flushInterval: cfg.flushInterval,
		maxBufferSize: cfg.maxBufferSize,
	}

	// Initialize stats if meter is provided
	if cfg.meter != nil {
		if meter, ok := cfg.meter.(metric.Meter); ok {
			processInfo := ProcessInfo{
				Command: cmd,
				Args:    args,
				// PID will be set later when process starts
			}

			stats, err := NewProcessStats(meter, cfg.metricsInterval, processInfo)
			if err == nil {
				process.stats = stats
			}
			// If err != nil, we simply continue without stats (graceful degradation)
		}
	}

	return process
}

// ExecuteAndStream starts a process and returns an iterator over Event objects
func (p *Process) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
	if p.mode == PTYMode {
		return p.executePTYMode(ctx)
	}
	return p.executePipeMode(ctx)
}

// executePTYMode handles PTY process execution
func (p *Process) executePTYMode(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		// Create the command
		// #nosec G204 - Command and args are controlled by the caller
		cmd := exec.CommandContext(ctx, p.cmd, p.args...)
		if len(p.env) > 0 {
			cmd.Env = p.env
		}
		if p.workingDir != "" {
			cmd.Dir = p.workingDir
		}

		// Start the command with PTY
		var ptmx *os.File
		var err error
		if p.ptySize != nil {
			ptmx, err = pty.StartWithSize(cmd, p.ptySize)
		} else {
			ptmx, err = pty.Start(cmd)
		}
		if err != nil {
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Failed to start PTY process",
			}), ProcessStartError{Cmd: p.cmd, Err: err})
			return
		}
		defer ptmx.Close()

		// Store the PID and track start time
		p.pid = cmd.Process.Pid
		processStart := time.Now()

		// Initialize stats if metrics are enabled
		if p.stats != nil {
			// Update process info with PID now that it's available
			p.stats.processInfo.PID = p.pid
			p.stats.Start(ctx)
		}

		// Emit ProcessStart event
		processStartEvent := Event{
			Timestamp: processStart,
			Event: &ProcessStart{
				PID:     p.pid,
				Command: p.cmd,
				Args:    p.args,
			},
		}
		if !yield(processStartEvent, nil) {
			return
		}

		// Record event in metrics
		if p.stats != nil {
			p.stats.RecordEvent(ctx, "process_start")
		}

		// Channels for coordination
		flushChan := make(chan struct{}, 1)
		doneChan := make(chan error, 1)

		// Initialize buffer writer
		p.outputWriter = NewBufferWriter(flushChan, p.maxBufferSize)

		// Start ticker for configurable intervals
		ticker := time.NewTicker(p.flushInterval)
		defer ticker.Stop()

		// Heartbeat tracking
		var hasEmittedEventsThisSecond bool

		p.waiter.Add(1)

		// Read from PTY
		go p.readPTYStream(ctx, ptmx, flushChan)

		// Wait for process completion
		go func() {
			p.waiter.Wait()
			doneChan <- cmd.Wait()
		}()

		// Main streaming loop
		for {
			select {
			case <-ctx.Done():
				// Context cancelled, use cancellor
				if p.cancellor != nil {
					_ = p.cancellor.Cancel(ctx, p.pid) // Best effort cancellation
				}
				return

			case <-ticker.C:
				// configured interval - flush buffers if they have data
				eventsEmitted := false

				if data := p.outputWriter.FlushAndClear(); data != nil {
					if !yield(newEvent(&OutputData{
						Data: data,
					}), nil) {
						return
					}
					eventsEmitted = true
					// Record metrics for output data
					if p.stats != nil {
						p.stats.RecordOutput(ctx, int64(len(data)))
					}
				}

				// Emit heartbeat if no events were emitted this second
				if !hasEmittedEventsThisSecond && !eventsEmitted {
					if !yield(newEvent(&HeartbeatEvent{
						ProcessAlive: true,
						ElapsedTime:  time.Since(processStart),
					}), nil) {
						return
					}
				}

				// Reset heartbeat tracking
				hasEmittedEventsThisSecond = eventsEmitted

			case <-flushChan:
				// Immediate flush due to buffer size limit
				if p.outputWriter.Len() >= p.maxBufferSize {
					if data := p.outputWriter.FlushAndClear(); data != nil {
						if !yield(newEvent(&OutputData{
							Data: data,
						}), nil) {
							return
						}
						hasEmittedEventsThisSecond = true
						// Record metrics for output data
						if p.stats != nil {
							p.stats.RecordOutput(ctx, int64(len(data)))
						}
					}
				}

			case err := <-doneChan:
				// Process finished - flush remaining buffers and handle exit
				endTime := time.Now()
				duration := endTime.Sub(processStart)

				if data := p.outputWriter.FlushAndClear(); data != nil {
					endPart := Event{
						Timestamp: endTime,
						Event: &OutputData{
							Data: data,
						},
					}
					if !yield(endPart, nil) {
						return
					}
					// Record metrics for final output data
					if p.stats != nil {
						p.stats.RecordOutput(ctx, int64(len(data)))
					}
				}

				// Update final stats
				if p.stats != nil {
					p.stats.Stop(ctx)
					if err != nil {
						if exitError, ok := err.(*exec.ExitError); ok {
							p.stats.SetExitCode(exitError.ExitCode())
						}
					} else {
						p.stats.SetExitCode(0)
					}
				}

				// Emit ProcessEnd or ProcessError event
				if err != nil {
					if exitError, ok := err.(*exec.ExitError); ok {
						yield(Event{
							Timestamp: endTime,
							Event: &ProcessEnd{
								ExitCode: exitError.ExitCode(),
								Duration: duration,
								Success:  false,
							},
						}, nil)
						if p.stats != nil {
							p.stats.RecordEvent(ctx, "process_end")
						}
					} else {
						yield(Event{
							Timestamp: endTime,
							Event: &ProcessError{
								Error:   err,
								Message: "PTY process terminated unexpectedly",
							},
						}, nil)
						if p.stats != nil {
							p.stats.RecordError(ctx, "process_error")
						}
					}
				} else {
					// Process completed successfully
					yield(Event{
						Timestamp: endTime,
						Event: &ProcessEnd{
							ExitCode: 0,
							Duration: duration,
							Success:  true,
						},
					}, nil)
					if p.stats != nil {
						p.stats.RecordEvent(ctx, "process_end")
					}
				}
				return
			}
		}
	}
}

// executePipeMode handles pipe process execution
func (p *Process) executePipeMode(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		// Create the command
		// #nosec G204 - Command and args are controlled by the caller
		cmd := exec.CommandContext(ctx, p.cmd, p.args...)
		if len(p.env) > 0 {
			cmd.Env = p.env
		}
		if p.workingDir != "" {
			cmd.Dir = p.workingDir
		}

		// Get stdout and stderr pipes
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Failed to create stdout pipe",
			}), ProcessStartError{Cmd: p.cmd, Err: err})
			return
		}

		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Failed to create stderr pipe",
			}), ProcessStartError{Cmd: p.cmd, Err: err})
			return
		}

		// Start the command
		if err := cmd.Start(); err != nil {
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Failed to start process",
			}), ProcessStartError{Cmd: p.cmd, Err: err})
			return
		}

		// Store the PID and track start time
		p.pid = cmd.Process.Pid
		processStart := time.Now()

		// Initialize stats if metrics are enabled
		if p.stats != nil {
			p.stats.processInfo.PID = p.pid
			p.stats.Start(ctx)
		}

		// Emit ProcessStart event
		processStartEvent := Event{
			Timestamp: processStart,
			Event: &ProcessStart{
				PID:     p.pid,
				Command: p.cmd,
				Args:    p.args,
			},
		}
		if !yield(processStartEvent, nil) {
			return
		}

		// Record event in metrics
		if p.stats != nil {
			p.stats.RecordEvent(ctx, "process_start")
		}

		// Channels for coordination
		flushChan := make(chan struct{}, 1)
		doneChan := make(chan error, 1)

		// Start ticker for configurable intervals
		ticker := time.NewTicker(p.flushInterval)
		defer ticker.Stop()

		// Heartbeat tracking
		var hasEmittedEventsThisSecond bool

		p.waiter.Add(2)

		// Read from stdout and stderr (they will be combined in order)
		go p.readPipeStream(ctx, stdoutPipe, flushChan)
		go p.readPipeStream(ctx, stderrPipe, flushChan)

		// Wait for process completion
		go func() {
			p.waiter.Wait()
			doneChan <- cmd.Wait()
		}()

		// Main streaming loop
		for {
			select {
			case <-ctx.Done():
				// Context cancelled, use cancellor
				if p.cancellor != nil {
					_ = p.cancellor.Cancel(ctx, p.pid) // Best effort cancellation
				}
				return

			case <-ticker.C:
				// 1-second interval - flush buffers if they have data
				p.bufferMu.Lock()
				eventsEmitted := false

				// Combine stdout and stderr into single output event
				var combinedData []byte
				if len(p.stdoutBuffer) > 0 {
					combinedData = append(combinedData, p.stdoutBuffer...)
					p.stdoutBuffer = p.stdoutBuffer[:0]
				}
				if len(p.stderrBuffer) > 0 {
					combinedData = append(combinedData, p.stderrBuffer...)
					p.stderrBuffer = p.stderrBuffer[:0]
				}

				if len(combinedData) > 0 {
					if !yield(newEvent(&OutputData{
						Data: combinedData,
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
					eventsEmitted = true
					// Record metrics for output data
					if p.stats != nil {
						p.stats.RecordOutput(ctx, int64(len(combinedData)))
					}
				}

				// Emit heartbeat if no events were emitted this second
				if !hasEmittedEventsThisSecond && !eventsEmitted {
					if !yield(newEvent(&HeartbeatEvent{
						ProcessAlive: true,
						ElapsedTime:  time.Since(processStart),
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
				}

				// Reset heartbeat tracking
				hasEmittedEventsThisSecond = eventsEmitted
				p.bufferMu.Unlock()

			case <-flushChan:
				// Immediate flush due to buffer size limit
				p.bufferMu.Lock()
				var combinedData []byte
				if len(p.stdoutBuffer) >= p.maxBufferSize || len(p.stderrBuffer) >= p.maxBufferSize {
					if len(p.stdoutBuffer) > 0 {
						combinedData = append(combinedData, p.stdoutBuffer...)
						p.stdoutBuffer = p.stdoutBuffer[:0]
					}
					if len(p.stderrBuffer) > 0 {
						combinedData = append(combinedData, p.stderrBuffer...)
						p.stderrBuffer = p.stderrBuffer[:0]
					}

					if len(combinedData) > 0 {
						if !yield(newEvent(&OutputData{
							Data: combinedData,
						}), nil) {
							p.bufferMu.Unlock()
							return
						}
						hasEmittedEventsThisSecond = true
						// Record metrics for output data
						if p.stats != nil {
							p.stats.RecordOutput(ctx, int64(len(combinedData)))
						}
					}
				}
				p.bufferMu.Unlock()

			case err := <-doneChan:
				// Process finished - flush remaining buffers and handle exit
				endTime := time.Now()
				duration := endTime.Sub(processStart)

				p.bufferMu.Lock()
				var combinedData []byte
				if len(p.stdoutBuffer) > 0 {
					combinedData = append(combinedData, p.stdoutBuffer...)
				}
				if len(p.stderrBuffer) > 0 {
					combinedData = append(combinedData, p.stderrBuffer...)
				}

				if len(combinedData) > 0 {
					endPart := Event{
						Timestamp: endTime,
						Event: &OutputData{
							Data: combinedData,
						},
					}
					if !yield(endPart, nil) {
						p.bufferMu.Unlock()
						return
					}
					// Record metrics for final output data
					if p.stats != nil {
						p.stats.RecordOutput(ctx, int64(len(combinedData)))
					}
				}
				p.bufferMu.Unlock()

				// Update final stats
				if p.stats != nil {
					p.stats.Stop(ctx)
					if err != nil {
						if exitError, ok := err.(*exec.ExitError); ok {
							p.stats.SetExitCode(exitError.ExitCode())
						}
					} else {
						p.stats.SetExitCode(0)
					}
				}

				// Emit ProcessEnd or ProcessError event
				if err != nil {
					if exitError, ok := err.(*exec.ExitError); ok {
						yield(Event{
							Timestamp: endTime,
							Event: &ProcessEnd{
								ExitCode: exitError.ExitCode(),
								Duration: duration,
								Success:  false,
							},
						}, nil)
						if p.stats != nil {
							p.stats.RecordEvent(ctx, "process_end")
						}
					} else {
						yield(Event{
							Timestamp: endTime,
							Event: &ProcessError{
								Error:   err,
								Message: "Pipe process terminated unexpectedly",
							},
						}, nil)
						if p.stats != nil {
							p.stats.RecordError(ctx, "process_error")
						}
					}
				} else {
					// Process completed successfully
					yield(Event{
						Timestamp: endTime,
						Event: &ProcessEnd{
							ExitCode: 0,
							Duration: duration,
							Success:  true,
						},
					}, nil)
					if p.stats != nil {
						p.stats.RecordEvent(ctx, "process_end")
					}
				}
				return
			}
		}
	}
}

// readPTYStream reads from a PTY and accumulates data using the BufferWriter
func (p *Process) readPTYStream(_ context.Context, ptmx *os.File, _ chan<- struct{}) {
	defer p.waiter.Done()
	defer p.outputWriter.Close()
	_, _ = io.Copy(p.outputWriter, ptmx)
}

// readPipeStream reads from a pipe and accumulates data in the struct buffer fields
func (p *Process) readPipeStream(ctx context.Context, pipe io.ReadCloser, flushChan chan<- struct{}) {
	defer func() {
		pipe.Close()
		p.waiter.Done()
	}()
	reader := bufio.NewReader(pipe)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		data, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// End of stream, add any remaining data
				if len(data) > 0 {
					p.bufferMu.Lock()
					// For simplicity, combine all pipe data into stdout buffer
					// (since we combine them anyway in the output event)
					p.stdoutBuffer = append(p.stdoutBuffer, data...)
					if len(p.stdoutBuffer) >= p.maxBufferSize {
						select {
						case flushChan <- struct{}{}:
						default:
						}
					}
					p.bufferMu.Unlock()
				}
			}
			return
		}

		p.bufferMu.Lock()
		// For simplicity, combine all pipe data into stdout buffer
		// (since we combine them anyway in the output event)
		p.stdoutBuffer = append(p.stdoutBuffer, data...)
		if len(p.stdoutBuffer) >= p.maxBufferSize {
			// Trigger immediate flush
			select {
			case flushChan <- struct{}{}:
			default:
			}
		}
		p.bufferMu.Unlock()
	}
}

// GetStats returns a copy of the current process statistics
func (p *Process) GetStats() ProcessStatsSnapshot {
	if p.stats == nil {
		return ProcessStatsSnapshot{}
	}

	return p.stats.GetSnapshot()
}

// NewProcessStats creates a new ProcessStats instance with OpenTelemetry metrics
func NewProcessStats(meter metric.Meter, interval time.Duration, processInfo ProcessInfo) (*ProcessStats, error) {
	if meter == nil {
		return nil, fmt.Errorf("meter cannot be nil")
	}

	stats := &ProcessStats{
		metricsInterval: interval,
		processInfo:     processInfo,
	}

	var err error

	// Create histogram for output bytes per flush interval
	stats.outputBytesHistogram, err = meter.Int64Histogram(
		"process.output.bytes",
		metric.WithDescription("Bytes output from process per flush interval"),
		metric.WithUnit("bytes"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create output bytes histogram: %w", err)
	}

	// Create histogram for input bytes per flush interval (for future use)
	stats.inputBytesHistogram, err = meter.Int64Histogram(
		"process.input.bytes",
		metric.WithDescription("Bytes input to process per flush interval"),
		metric.WithUnit("bytes"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create input bytes histogram: %w", err)
	}

	// Create counter for total events
	stats.eventCounter, err = meter.Int64Counter(
		"process.events.total",
		metric.WithDescription("Total number of process events processed"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create event counter: %w", err)
	}

	// Create gauge for process status
	stats.processStatusGauge, err = meter.Int64Gauge(
		"process.status",
		metric.WithDescription("Process status (0=stopped, 1=running)"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create process status gauge: %w", err)
	}

	return stats, nil
}

// Start initializes the stats and records process start
func (s *ProcessStats) Start(ctx context.Context) {
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
func (s *ProcessStats) Stop(ctx context.Context) {
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
func (s *ProcessStats) SetExitCode(code int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.exitCode = &code
}

// RecordError increments the error count
func (s *ProcessStats) RecordError(ctx context.Context, eventType string) {
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
func (s *ProcessStats) RecordEvent(ctx context.Context, eventType string) {
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
func (s *ProcessStats) RecordOutput(ctx context.Context, dataSize int64) {
	s.mutex.Lock()
	s.eventCount++
	s.mutex.Unlock()

	// Record event counter
	if s.eventCounter != nil {
		s.eventCounter.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("event_type", "output"),
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
func (s *ProcessStats) GetSnapshot() ProcessStatsSnapshot {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Create a deep copy to avoid race conditions
	snapshot := ProcessStatsSnapshot{
		StartTime:       s.startTime,
		EventCount:      s.eventCount,
		ErrorCount:      s.errorCount,
		ExitCode:        s.exitCode,
		Duration:        s.duration,
		metricsInterval: s.metricsInterval,
	}

	return snapshot
}
