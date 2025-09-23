package consolestream

import (
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"go.opentelemetry.io/otel/metric"
)

// PTYOutputData represents terminal output from a PTY process with ANSI sequences preserved
type PTYOutputData struct {
	Data string
}

func (p *PTYOutputData) Type() EventType {
	return PTYOutputEvent
}

func (p *PTYOutputData) String() string {
	return fmt.Sprintf("PTYOutputData{Size: %d bytes}", len(p.Data))
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

// ProcessInfo contains process information for metrics attributes
type ProcessInfo struct {
	Command string
	Args    []string
	PID     int
}

// PTYStatsSnapshot represents a point-in-time view of PTY process statistics
type PTYStatsSnapshot struct {
	// Basic execution stats
	StartTime  time.Time
	EventCount int
	ErrorCount int
	ExitCode   *int
	Duration   time.Duration

	// Metrics collection interval
	metricsInterval time.Duration
}

// PTYStats manages comprehensive metrics during PTY process execution
type PTYStats struct {
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

// PTYProcess represents a process running in a pseudo-terminal
type PTYProcess struct {
	cmd           string
	args          []string
	pid           int
	cancellor     Cancellor
	env           []string
	ptySize       *pty.Winsize
	flushInterval time.Duration
	maxBufferSize int

	// Buffer writer for PTY output
	ptyWriter *BufferWriter

	// Metrics and observability (optional)
	stats *PTYStats

	waiter sync.WaitGroup
}

// WithPTYSize sets a custom terminal size for the PTY process
func WithPTYSize(size pty.Winsize) ProcessOption {
	return func(cfg *processConfig) {
		// Store as interface{} to avoid dependency coupling in console.go
		cfg.ptySize = &size
	}
}

// NewPTYProcess creates a new PTY process with functional options
func NewPTYProcess(cmd string, args []string, opts ...ProcessOption) *PTYProcess {
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

	process := &PTYProcess{
		cmd:           cmd,
		args:          args,
		cancellor:     cfg.cancellor,
		env:           cfg.env,
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

			stats, err := NewPTYStats(meter, cfg.metricsInterval, processInfo)
			if err == nil {
				process.stats = stats
			}
			// If err != nil, we simply continue without stats (graceful degradation)
		}
	}

	return process
}

// ExecuteAndStream starts a PTY process and returns an iterator over Event objects
func (p *PTYProcess) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		// Create the command
		// #nosec G204 - Command and args are controlled by the caller
		cmd := exec.CommandContext(ctx, p.cmd, p.args...)
		if len(p.env) > 0 {
			cmd.Env = p.env
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
		p.ptyWriter = NewBufferWriter(flushChan, p.maxBufferSize)

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

				if data := p.ptyWriter.FlushAndClear(); data != nil {
					if !yield(newEvent(&PTYOutputData{
						Data: string(data),
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
				if p.ptyWriter.Len() >= p.maxBufferSize {
					if data := p.ptyWriter.FlushAndClear(); data != nil {
						if !yield(newEvent(&PTYOutputData{
							Data: string(data),
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

				if data := p.ptyWriter.FlushAndClear(); data != nil {
					endPart := Event{
						Timestamp: endTime,
						Event: &PTYOutputData{
							Data: string(data),
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

// readPTYStream reads from a PTY and accumulates data using the BufferWriter
func (p *PTYProcess) readPTYStream(_ context.Context, ptmx *os.File, _ chan<- struct{}) {
	defer p.waiter.Done()
	defer p.ptyWriter.Close()
	_, _ = io.Copy(p.ptyWriter, ptmx)
}

// GetStats returns a copy of the current PTY process statistics
func (p *PTYProcess) GetStats() PTYStatsSnapshot {
	if p.stats == nil {
		return PTYStatsSnapshot{}
	}

	return p.stats.GetSnapshot()
}
