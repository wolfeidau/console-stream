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
// ProcessMode defines whether to use PTY or pipe mode for process execution
type ProcessMode int

const (
	PipeMode ProcessMode = iota
	PTYMode
)

func (m ProcessMode) String() string {
	switch m {
	case PipeMode:
		return "PIPE"
	case PTYMode:
		return "PTY"
	default:
		return "UNKNOWN"
	}
}

type processConfig struct {
	cancellor       Cancellor
	env             []string
	workingDir      string
	mode            ProcessMode   // PTY or Pipe mode
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
	// Default to pipe mode for backward compatibility
	if cfg.mode == 0 {
		cfg.mode = PipeMode
	}
	if cfg.flushInterval == 0 {
		cfg.flushInterval = time.Second // Default 1-second flush interval
	}
	if cfg.maxBufferSize == 0 {
		cfg.maxBufferSize = 10 * 1024 * 1024 // Default 10MB buffer limit (matches PipeProcess)
	}
	if cfg.metricsInterval == 0 {
		cfg.metricsInterval = time.Second // Default 1-second metrics aggregation
	}
}

// ContainerProcessOption represents a functional option for configuring container processes
type ContainerProcessOption func(*containerProcessConfig)

// containerProcessConfig holds configuration options for container processes
type containerProcessConfig struct {
	// Shared options
	cancellor       Cancellor
	env             []string
	workingDir      string
	flushInterval   time.Duration
	maxBufferSize   int
	meter           any
	metricsInterval time.Duration

	// Container-specific options
	image   string
	runtime string
	mounts  []ContainerMount
}

// applyDefaults sets sensible defaults for the container process configuration
func (cfg *containerProcessConfig) applyDefaults() {
	if cfg.cancellor == nil {
		cfg.cancellor = NewLocalCancellor(5 * time.Second)
	}
	if cfg.runtime == "" {
		cfg.runtime = "docker" // Default to Docker
	}
	if cfg.flushInterval == 0 {
		cfg.flushInterval = time.Second // Default 1-second flush interval
	}
	if cfg.maxBufferSize == 0 {
		cfg.maxBufferSize = 10 * 1024 * 1024 // Default 10MB buffer limit (matches PipeProcess)
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

// WithWorkingDir sets the working directory for the process
func WithWorkingDir(dir string) ProcessOption {
	return func(cfg *processConfig) {
		cfg.workingDir = dir
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

// WithPipeMode sets the process to use standard pipes (stdout/stderr)
func WithPipeMode() ProcessOption {
	return func(cfg *processConfig) {
		cfg.mode = PipeMode
	}
}

// WithPTYMode sets the process to use a pseudo-terminal
func WithPTYMode() ProcessOption {
	return func(cfg *processConfig) {
		cfg.mode = PTYMode
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
	OutputEvent
	TerminalResizeEventType
	ContainerCreateEvent
	ContainerRemoveEvent
)

func (e EventType) String() string {
	switch e {
	case OutputEvent:
		return "OUTPUT"
	case ProcessStartEvent:
		return "PROCESS_START"
	case ProcessEndEvent:
		return "PROCESS_END"
	case ProcessErrorEvent:
		return "PROCESS_ERROR"
	case HeartbeatEventType:
		return "HEARTBEAT"
	case TerminalResizeEventType:
		return "TERMINAL_RESIZE"
	case ContainerCreateEvent:
		return "CONTAINER_CREATE"
	case ContainerRemoveEvent:
		return "CONTAINER_REMOVE"
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

// OutputData represents output data from the process (combined stdout/stderr)
type OutputData struct {
	Data []byte
}

func (o *OutputData) Type() EventType {
	return OutputEvent
}

func (o *OutputData) String() string {
	return fmt.Sprintf("OutputData{Size: %d bytes}", len(o.Data))
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

// ContainerCreate represents container creation
type ContainerCreate struct {
	ContainerID string
	Image       string
}

func (c *ContainerCreate) Type() EventType {
	return ContainerCreateEvent
}

func (c *ContainerCreate) String() string {
	return fmt.Sprintf("ContainerCreate{ID: %s, Image: %s}", c.ContainerID, c.Image)
}

// ContainerRemove represents container cleanup
type ContainerRemove struct {
	ContainerID string
}

func (c *ContainerRemove) Type() EventType {
	return ContainerRemoveEvent
}

func (c *ContainerRemove) String() string {
	return fmt.Sprintf("ContainerRemove{ID: %s}", c.ContainerID)
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

func IsOutputEvent(event StreamEvent) bool {
	return event.Type() == OutputEvent
}

func IsTerminalResizeEvent(event StreamEvent) bool {
	return event.Type() == TerminalResizeEventType
}

func IsContainerCreateEvent(event StreamEvent) bool {
	return event.Type() == ContainerCreateEvent
}

func IsContainerRemoveEvent(event StreamEvent) bool {
	return event.Type() == ContainerRemoveEvent
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

// ContainerMount represents a volume mount for container processes
type ContainerMount struct {
	Source   string
	Target   string
	ReadOnly bool
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

// Container-specific options

// WithContainerImage sets the container image to use (REQUIRED for container processes)
func WithContainerImage(image string) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.image = image
	}
}

// WithContainerRuntime sets the container runtime ("docker" or "podman")
func WithContainerRuntime(runtime string) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.runtime = runtime
	}
}

// WithContainerMount adds a volume mount to the container
func WithContainerMount(source, target string, readOnly bool) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.mounts = append(cfg.mounts, ContainerMount{
			Source:   source,
			Target:   target,
			ReadOnly: readOnly,
		})
	}
}

// Shared option adapters for container processes

// WithContainerEnv sets environment variables as a slice of "KEY=value" strings
func WithContainerEnv(env []string) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.env = append(cfg.env, env...)
	}
}

// WithContainerEnvMap sets environment variables from a map for convenience
func WithContainerEnvMap(envMap map[string]string) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		for key, value := range envMap {
			cfg.env = append(cfg.env, key+"="+value)
		}
	}
}

// WithContainerWorkingDir sets the working directory for the container process
func WithContainerWorkingDir(dir string) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.workingDir = dir
	}
}

// WithContainerCancellor sets a custom cancellor for the container process
func WithContainerCancellor(cancellor Cancellor) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.cancellor = cancellor
	}
}

// WithContainerFlushInterval sets how often to flush output buffers for container processes
func WithContainerFlushInterval(interval time.Duration) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.flushInterval = interval
	}
}

// WithContainerMaxBufferSize sets the maximum buffer size before forcing a flush for container processes
func WithContainerMaxBufferSize(size int) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.maxBufferSize = size
	}
}

// WithContainerMeter sets an OpenTelemetry meter for metrics collection for container processes
func WithContainerMeter(meter any) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.meter = meter
	}
}

// WithContainerMetricsInterval sets how often to aggregate metrics into buckets for container processes
func WithContainerMetricsInterval(interval time.Duration) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.metricsInterval = interval
	}
}
