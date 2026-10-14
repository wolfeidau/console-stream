package consolestream

import (
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/wolfeidau/console-stream/internal/osc7501"
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
	eventCounter         metric.Int64Counter
	processStatusGauge   metric.Int64Gauge

	// Configuration
	processInfo ProcessInfo

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
func WithPTYSize(size *pty.Winsize) ProcessOption {
	return func(cfg *processConfig) {
		cfg.ptySize = size
	}
}

// Process represents a unified process that can run in either PTY or pipe mode
type Process struct {
	cmd               string
	args              []string
	mode              ProcessMode
	cancellor         Cancellor
	env               []string
	workingDir        string
	ptySize           *pty.Winsize
	flushInterval     time.Duration
	heartbeatInterval time.Duration
	maxBufferSize     int
	programStatus     bool

	// Metrics and observability (optional)
	stats *ProcessStats
}

// NewProcess creates a new unified process with functional options.
// Children run in their own process group (pipe: Setpgid, PTY: Setsid) and so
// do not receive terminal-generated signals; cancel the ExecuteAndStream
// context on SIGINT/SIGTERM (e.g. signal.NotifyContext) to stop them.
func NewProcess(cmd string, args []string, opts ...ProcessOption) *Process {
	cfg := &processConfig{}
	cfg.applyDefaults()

	for _, opt := range opts {
		opt(cfg)
	}

	process := &Process{
		cmd:               cmd,
		args:              args,
		mode:              cfg.mode,
		cancellor:         cfg.cancellor,
		env:               cfg.env,
		workingDir:        cfg.workingDir,
		ptySize:           cfg.ptySize,
		flushInterval:     cfg.flushInterval,
		heartbeatInterval: cfg.heartbeatInterval,
		maxBufferSize:     cfg.maxBufferSize,
		programStatus:     cfg.programStatus,
	}

	// Initialize stats if meter is provided
	if cfg.meter != nil {
		processInfo := ProcessInfo{
			Command: cmd,
			Args:    args,
			// PID will be set later when process starts
		}

		stats, err := NewProcessStats(cfg.meter, processInfo)
		if err == nil {
			process.stats = stats
		}
		// If err != nil, we simply continue without stats (graceful degradation)
	}

	return process
}

// ExecuteAndStream starts a process and returns an iterator over Event objects.
//
// The child runs in its own process group (pipe mode: Setpgid; PTY mode:
// Setsid), so it does not receive terminal-generated signals such as Ctrl-C.
// Callers should cancel ctx on SIGINT/SIGTERM (e.g. signal.NotifyContext) so
// the Cancellor can stop the child and its descendants.
//
// Breaking out of the loop or cancelling ctx stops the child via the Cancellor
// and waits for all goroutines to finish before the iterator returns.
func (p *Process) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		p.run(ctx, yield)
	}
}

// startProcess starts the command in the configured mode. It returns the
// reader (PTY master or pipe read end) that carries the output; the caller
// owns closing it. label names the mode in error messages.
func (p *Process) startProcess(cmd *exec.Cmd) (r *os.File, label string, err error) {
	if p.mode == PTYMode {
		if p.ptySize != nil {
			r, err = pty.StartWithSize(cmd, p.ptySize)
		} else {
			r, err = pty.Start(cmd)
		}
		return r, "PTY", err
	}

	// A single pipe carries stdout and stderr so their relative order is kept.
	r, pw, err := os.Pipe()
	if err != nil {
		return nil, "Pipe", err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	// Make the child a process-group leader so Cancel can signal the group.
	// (PTY mode gets this from pty.Start's Setsid.)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	err = cmd.Start()
	_ = pw.Close() // child holds its own copy
	if err != nil {
		_ = r.Close()
		return nil, "Pipe", err
	}
	return r, "Pipe", nil
}

// run executes the process, streaming events to yield. All run state is local.
func (p *Process) run(ctx context.Context, yield func(Event, error) bool) {
	// The Cancellor is the single cancellation mechanism, so no CommandContext.
	// #nosec G204 - Command and args are controlled by the caller
	cmd := exec.Command(p.cmd, p.args...)
	if len(p.env) > 0 {
		cmd.Env = p.env
	}
	if p.workingDir != "" {
		cmd.Dir = p.workingDir
	}

	out, label, err := p.startProcess(cmd)
	if err != nil {
		yield(newEvent(&ProcessError{
			Error:   err,
			Message: "Failed to start " + label + " process",
		}), ProcessStartError{Cmd: p.cmd, Err: err})
		return
	}
	defer out.Close()

	pid := cmd.Process.Pid
	processStart := time.Now()

	if p.stats != nil {
		p.stats.processInfo.PID = pid
		p.stats.Start(ctx)
	}

	flush := make(chan struct{}, 1)
	done := make(chan error, 1)
	reaped := make(chan struct{}) // closed once cmd.Wait has returned
	exited := make(chan struct{}) // closed once the child is reaped and output drained
	w := NewBufferWriter(flush, p.maxBufferSize)

	// Reader drains output until EOF or out is closed.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var dst io.Writer = w
		if p.programStatus {
			dst = &osc7501.Scanner{W: w, OnReport: w.addStatus}
		}
		_, _ = io.Copy(dst, out)
	}()

	// Reap the child promptly, then wait for the reader so output is complete.
	go func() {
		err := cmd.Wait()
		close(reaped)
		<-readerDone
		done <- err
		close(exited)
	}()

	// stop terminates the child via the Cancellor and drains goroutines.
	stop := func() {
		cancelCtx := context.WithoutCancel(ctx)
		select {
		case <-reaped:
			// The leader is reaped, so its pid may be reused, but its pgid
			// cannot be while any group member lives. Signal only the group
			// to catch descendants still holding the tty/pipe. Custom
			// Cancellors are not called: their semantics are unknown.
			if lc, ok := p.cancellor.(*LocalCancellor); ok {
				_ = lc.cancelGroup(cancelCtx, pid)
			}
		default:
			if p.cancellor != nil {
				_ = p.cancellor.Cancel(cancelCtx, pid) // Best effort cancellation
			} else {
				_ = cmd.Process.Kill()
			}
		}
		_ = out.Close() // unblocks the reader if a descendant holds the tty/pipe
		<-exited
	}

	if !yield(Event{
		Timestamp: processStart,
		Event:     &ProcessStart{PID: pid, Command: p.cmd, Args: p.args},
	}, nil) {
		stop()
		return
	}
	if p.stats != nil {
		p.stats.RecordEvent(ctx, "process_start")
	}

	endTime, ok, waitErr := streamOutput(ctx, yield, w, flush, done, processStart, p.flushInterval, p.heartbeatInterval, p.maxBufferSize, p.stats)
	if !ok {
		stop()
		return
	}

	emitProcessEnd(ctx, yield, p.stats, waitErr, processStart, endTime, label)
}

// GetStats returns a copy of the current process statistics
func (p *Process) GetStats() ProcessStatsSnapshot {
	if p.stats == nil {
		return ProcessStatsSnapshot{}
	}

	return p.stats.GetSnapshot()
}

// NewProcessStats creates a new ProcessStats instance with OpenTelemetry metrics
func NewProcessStats(meter metric.Meter, processInfo ProcessInfo) (*ProcessStats, error) {
	if meter == nil {
		return nil, fmt.Errorf("meter cannot be nil")
	}

	stats := &ProcessStats{
		processInfo: processInfo,
	}

	var err error

	// Create histogram for output bytes per OutputData batch
	stats.outputBytesHistogram, err = meter.Int64Histogram(
		"process.output.bytes",
		metric.WithDescription("Bytes output from process per OutputData batch"),
		metric.WithUnit("bytes"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create output bytes histogram: %w", err)
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
		StartTime:  s.startTime,
		EventCount: s.eventCount,
		ErrorCount: s.errorCount,
		ExitCode:   s.exitCode,
		Duration:   s.duration,
	}

	return snapshot
}
