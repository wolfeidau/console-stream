package consolestream

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

// ProcessOption represents a functional option for configuring processes
type ProcessOption func(*processConfig)

// processConfig holds configuration options for processes
type processConfig struct {
	cancellor       Cancellor
	env             []string
	ptySize         any           // Uses any to avoid dependency on pty package
	flushInterval   time.Duration // How often to flush buffers
	maxBufferSize   int           // Maximum buffer size before forced flush
	meter           any           // OpenTelemetry meter (uses any to avoid dependency)
	metricsInterval time.Duration // How often to aggregate metrics buckets
}

// applyDefaults sets sensible defaults for the process configuration
func (cfg *processConfig) applyDefaults() {
	if cfg.cancellor == nil {
		cfg.cancellor = NewLocalCancellor(5 * time.Second)
	}
	if cfg.flushInterval == 0 {
		cfg.flushInterval = time.Second // Default 1-second flush interval
	}
	if cfg.maxBufferSize == 0 {
		cfg.maxBufferSize = 10 * 1024 * 1024 // Default 10MB buffer limit
	}
	if cfg.metricsInterval == 0 {
		cfg.metricsInterval = time.Second // Default 1-second metrics aggregation
	}
}

// WithCancellor sets a custom cancellor for the process
func WithCancellor(cancellor Cancellor) ProcessOption {
	return func(cfg *processConfig) {
		cfg.cancellor = cancellor
	}
}

// WithEnv sets environment variables as a slice of "KEY=value" strings
func WithEnv(env []string) ProcessOption {
	return func(cfg *processConfig) {
		cfg.env = append(cfg.env, env...)
	}
}

// WithEnvVar sets a single environment variable
func WithEnvVar(key, value string) ProcessOption {
	return func(cfg *processConfig) {
		cfg.env = append(cfg.env, key+"="+value)
	}
}

// WithEnvMap sets environment variables from a map for convenience
func WithEnvMap(envMap map[string]string) ProcessOption {
	return func(cfg *processConfig) {
		for key, value := range envMap {
			cfg.env = append(cfg.env, key+"="+value)
		}
	}
}

// WithFlushInterval sets how often to flush output buffers
func WithFlushInterval(interval time.Duration) ProcessOption {
	return func(cfg *processConfig) {
		cfg.flushInterval = interval
	}
}

// WithMaxBufferSize sets the maximum buffer size before forcing a flush
func WithMaxBufferSize(size int) ProcessOption {
	return func(cfg *processConfig) {
		cfg.maxBufferSize = size
	}
}

// WithMeter sets an OpenTelemetry meter for metrics collection
func WithMeter(meter any) ProcessOption {
	return func(cfg *processConfig) {
		cfg.meter = meter
	}
}

// WithMetricsInterval sets how often to aggregate metrics into buckets
func WithMetricsInterval(interval time.Duration) ProcessOption {
	return func(cfg *processConfig) {
		cfg.metricsInterval = interval
	}
}

type StreamType int

const (
	Stdout StreamType = iota
	Stderr
)

func (s StreamType) String() string {
	switch s {
	case Stdout:
		return "STDOUT"
	case Stderr:
		return "STDERR"
	default:
		return "UNKNOWN"
	}
}

// StreamEvent represents different types of events that can occur during process execution
type StreamEvent interface {
	Type() EventType
	String() string
}

// EventType defines the different kinds of events
type EventType int

const (
	ProcessStartEvent EventType = iota
	ProcessEndEvent
	ProcessErrorEvent
	HeartbeatEventType
	PipeOutputEvent
	PTYOutputEvent
	TerminalResizeEventType
)

func (e EventType) String() string {
	switch e {
	case PipeOutputEvent:
		return "PIPE_OUTPUT"
	case ProcessStartEvent:
		return "PROCESS_START"
	case ProcessEndEvent:
		return "PROCESS_END"
	case ProcessErrorEvent:
		return "PROCESS_ERROR"
	case HeartbeatEventType:
		return "HEARTBEAT"
	case PTYOutputEvent:
		return "PTY_OUTPUT"
	case TerminalResizeEventType:
		return "TERMINAL_RESIZE"
	default:
		return "UNKNOWN"
	}
}

type Event struct {
	Timestamp time.Time
	Event     StreamEvent
}

// EventType returns the type of the event contained in this Event
func (e Event) EventType() EventType {
	return e.Event.Type()
}

// String returns a human-readable representation of the Event
func (e Event) String() string {
	return fmt.Sprintf("[%s] %s: %s", e.EventType().String(), e.Timestamp.Format("15:04:05.000"), e.Event.String())
}

// PipeOutputData represents stdout/stderr data from the process
type PipeOutputData struct {
	Data   []byte
	Stream StreamType
}

func (o *PipeOutputData) Type() EventType {
	return PipeOutputEvent
}

func (o *PipeOutputData) String() string {
	return fmt.Sprintf("OutputData{Stream: %s, Size: %d bytes}", o.Stream.String(), len(o.Data))
}

// ProcessStart represents the beginning of process execution
type ProcessStart struct {
	PID     int
	Command string
	Args    []string
}

func (p *ProcessStart) Type() EventType {
	return ProcessStartEvent
}

func (p *ProcessStart) String() string {
	return fmt.Sprintf("ProcessStart{PID: %d, Command: %s}", p.PID, p.Command)
}

// ProcessEnd represents the completion of process execution
type ProcessEnd struct {
	ExitCode int
	Duration time.Duration
	Success  bool
}

func (p *ProcessEnd) Type() EventType {
	return ProcessEndEvent
}

func (p *ProcessEnd) String() string {
	status := "success"
	if !p.Success {
		status = "failed"
	}
	return fmt.Sprintf("ProcessEnd{ExitCode: %d, Duration: %v, Status: %s}", p.ExitCode, p.Duration, status)
}

// ProcessError represents an error during process execution
type ProcessError struct {
	Error   error
	Message string
}

func (p *ProcessError) Type() EventType {
	return ProcessErrorEvent
}

func (p *ProcessError) String() string {
	return fmt.Sprintf("ProcessError{Message: %s, Error: %v}", p.Message, p.Error)
}

// HeartbeatEvent represents a keep-alive signal when no output is generated
type HeartbeatEvent struct {
	ProcessAlive bool
	ElapsedTime  time.Duration
}

func (h *HeartbeatEvent) Type() EventType {
	return HeartbeatEventType
}

func (h *HeartbeatEvent) String() string {
	return fmt.Sprintf("HeartbeatEvent{Alive: %t, Elapsed: %v}", h.ProcessAlive, h.ElapsedTime)
}

func IsProcessStartEvent(event StreamEvent) bool {
	return event.Type() == ProcessStartEvent
}

func IsProcessEndEvent(event StreamEvent) bool {
	return event.Type() == ProcessEndEvent
}

func IsProcessErrorEvent(event StreamEvent) bool {
	return event.Type() == ProcessErrorEvent
}

func IsHeartbeatEvent(event StreamEvent) bool {
	return event.Type() == HeartbeatEventType
}

func IsProcessLifecycleEvent(event StreamEvent) bool {
	eventType := event.Type()
	return eventType == ProcessStartEvent || eventType == ProcessEndEvent || eventType == ProcessErrorEvent
}

func IsPipeOutputEvent(event StreamEvent) bool {
	return event.Type() == PipeOutputEvent
}

func IsPTYOutputEvent(event StreamEvent) bool {
	return event.Type() == PTYOutputEvent
}

func IsTerminalResizeEvent(event StreamEvent) bool {
	return event.Type() == TerminalResizeEventType
}

// Helper function to create Event with event and timestamp
func newEvent(event StreamEvent) Event {
	return Event{
		Timestamp: time.Now(),
		Event:     event,
	}
}

// Cancellor defines the interface for cancelling processes
type Cancellor interface {
	Cancel(ctx context.Context, pid int) error
}

// Error types for different process failure scenarios
type ProcessStartError struct {
	Cmd string
	Err error
}

func (e ProcessStartError) Error() string {
	return fmt.Sprintf("failed to start process %s: %v", e.Cmd, e.Err)
}

type PipeProcessFailedError struct {
	Cmd      string
	ExitCode int
}

func (e PipeProcessFailedError) Error() string {
	return fmt.Sprintf("process %s exited with code %d", e.Cmd, e.ExitCode)
}

type PipeProcessKilledError struct {
	Cmd    string
	Signal string
}

func (e PipeProcessKilledError) Error() string {
	return fmt.Sprintf("process %s was killed by signal %s", e.Cmd, e.Signal)
}

// LocalCancellor implements process cancellation for local processes
type LocalCancellor struct {
	terminateTimeout time.Duration
}

func NewLocalCancellor(terminateTimeout time.Duration) *LocalCancellor {
	return &LocalCancellor{
		terminateTimeout: terminateTimeout,
	}
}

func (lc *LocalCancellor) Cancel(ctx context.Context, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid: %d", pid)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", pid, err)
	}

	// Send SIGTERM first
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// PipeProcess might already be dead, try SIGKILL anyway
		return process.Signal(syscall.SIGKILL)
	}

	// Wait for graceful termination or timeout
	termCtx, cancel := context.WithTimeout(ctx, lc.terminateTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := process.Wait()
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-termCtx.Done():
		// Timeout reached, force kill
		return process.Signal(syscall.SIGKILL)
	}
}
