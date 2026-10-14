package consolestream

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/creack/pty"
	"go.opentelemetry.io/otel/metric"
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

// processConfig holds configuration options for processes
type processConfig struct {
	cancellor     Cancellor
	env           []string
	workingDir    string
	mode          ProcessMode   // PTY or Pipe mode
	ptySize       *pty.Winsize  // Terminal size for PTY mode
	flushInterval time.Duration // Output batch interval
	maxBufferSize int           // Maximum batch size before early delivery
	meter         metric.Meter  // OpenTelemetry meter
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
		cfg.maxBufferSize = 10 * 1024 * 1024 // Default 10MB buffer limit (matches PipeProcess)
	}
}

// ContainerProcessOption represents a functional option for configuring container processes
type ContainerProcessOption func(*containerProcessConfig)

// containerProcessConfig holds configuration options for container processes
type containerProcessConfig struct {
	// Shared options
	env           []string
	workingDir    string
	flushInterval time.Duration
	maxBufferSize int
	meter         metric.Meter

	// Container-specific options
	image   string
	runtime string
	mounts  []ContainerMount
}

// applyDefaults sets sensible defaults for the container process configuration
func (cfg *containerProcessConfig) applyDefaults() {
	if cfg.runtime == "" {
		cfg.runtime = "docker" // Default to Docker
	}
	if cfg.flushInterval == 0 {
		cfg.flushInterval = time.Second // Default 1-second flush interval
	}
	if cfg.maxBufferSize == 0 {
		cfg.maxBufferSize = 10 * 1024 * 1024 // Default 10MB buffer limit (matches PipeProcess)
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

// WithFlushInterval sets the output batch interval (default 1s): how often
// buffered output is delivered as an OutputData event. It bounds live delivery
// latency and event volume. It does not affect timing accuracy: each event's
// Segments record when output was read. A HeartbeatEvent fires after two
// consecutive intervals with no output, so this also sets heartbeat cadence.
func WithFlushInterval(interval time.Duration) ProcessOption {
	return func(cfg *processConfig) {
		cfg.flushInterval = interval
	}
}

// WithMaxBufferSize sets the maximum batch size (default 10MB): output is
// delivered as soon as the buffer reaches it, without waiting for the interval.
func WithMaxBufferSize(size int) ProcessOption {
	return func(cfg *processConfig) {
		cfg.maxBufferSize = size
	}
}

// WithMeter sets an OpenTelemetry meter for metrics collection
func WithMeter(meter metric.Meter) ProcessOption {
	return func(cfg *processConfig) {
		cfg.meter = meter
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
	ImagePullStartEvent
	ImagePullProgressEvent
	ImagePullCompleteEvent
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
	case ImagePullStartEvent:
		return "IMAGE_PULL_START"
	case ImagePullProgressEvent:
		return "IMAGE_PULL_PROGRESS"
	case ImagePullCompleteEvent:
		return "IMAGE_PULL_COMPLETE"
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
	// Segments carries per-read timing for Data; may be nil for events not
	// produced by this library.
	Segments []OutputSegment
}

// OutputSegment marks where a write into the output buffer began and when it was read.
// Segment i covers Data[Offset_i:Offset_{i+1}] (the last runs to len(Data)).
type OutputSegment struct {
	Offset int
	Time   time.Time
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

// ImagePullStart represents the beginning of an image pull operation
type ImagePullStart struct {
	Image string
}

func (i *ImagePullStart) Type() EventType {
	return ImagePullStartEvent
}

func (i *ImagePullStart) String() string {
	return fmt.Sprintf("ImagePullStart{Image: %s}", i.Image)
}

// ImagePullProgress represents progress during an image pull
type ImagePullProgress struct {
	Image           string
	Status          string
	PercentComplete int
	BytesDownloaded int64
	BytesTotal      int64
}

func (i *ImagePullProgress) Type() EventType {
	return ImagePullProgressEvent
}

func (i *ImagePullProgress) String() string {
	return fmt.Sprintf("ImagePullProgress{Image: %s, Status: %s, Progress: %d%%}", i.Image, i.Status, i.PercentComplete)
}

// ImagePullComplete represents successful completion of an image pull
type ImagePullComplete struct {
	Image  string
	Digest string
}

func (i *ImagePullComplete) Type() EventType {
	return ImagePullCompleteEvent
}

func (i *ImagePullComplete) String() string {
	return fmt.Sprintf("ImagePullComplete{Image: %s, Digest: %s}", i.Image, i.Digest)
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

// ContainerMount represents a volume mount for container processes
type ContainerMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// LocalCancellor implements process cancellation for local processes.
//
// Cancel signals the whole process group led by pid (kill(-pid)), so
// grandchildren spawned by the process are terminated with it. Processes
// started by this library are group leaders (Setpgid for pipes, Setsid for
// PTYs). If pid is not a group leader, Cancel falls back to signalling pid
// alone.
//
// Limitation: PTY children that create their own job-control process groups
// (for example interactive shells) have descendants outside the group, which
// the group kill does not reach.
type LocalCancellor struct {
	terminateTimeout time.Duration
}

func NewLocalCancellor(terminateTimeout time.Duration) *LocalCancellor {
	return &LocalCancellor{
		terminateTimeout: terminateTimeout,
	}
}

// Cancel sends SIGTERM to the process group led by pid (or to pid alone if it
// is not a group leader), then SIGKILL if it has not exited by the terminate
// timeout or ctx is done.
func (lc *LocalCancellor) Cancel(ctx context.Context, pid int) error {
	return lc.cancel(ctx, pid, true)
}

// cancelGroup is like Cancel but never signals the bare pid. It is safe to use
// after the leader has been reaped: a pgid cannot be reused while any group
// member lives, and kill(-pgid) returns ESRCH once the group is empty.
func (lc *LocalCancellor) cancelGroup(ctx context.Context, pgid int) error {
	return lc.cancel(ctx, pgid, false)
}

func (lc *LocalCancellor) cancel(ctx context.Context, pid int, allowPidFallback bool) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid: %d", pid)
	}

	target := -pid
	err := syscall.Kill(target, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) && allowPidFallback {
		target = pid // not a group leader
		err = syscall.Kill(target, syscall.SIGTERM)
	}
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone
		}
		return killIgnoreGone(target)
	}

	// Poll for exit (the owner reaps the child) until timeout, then force kill.
	// Not calling Wait here avoids racing the owner's cmd.Wait.
	deadline := time.NewTimer(lc.terminateTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		if err := syscall.Kill(target, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return killIgnoreGone(target)
		case <-deadline.C:
			return killIgnoreGone(target)
		}
	}
}

// killIgnoreGone sends SIGKILL to target (negative for a process group).
func killIgnoreGone(target int) error {
	if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
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

// WithContainerFlushInterval sets the output batch interval (default 1s): how often
// buffered output is delivered as an OutputData event. It bounds live delivery
// latency and event volume. It does not affect timing accuracy: each event's
// Segments record when output was read. A HeartbeatEvent fires after two
// consecutive intervals with no output, so this also sets heartbeat cadence.
func WithContainerFlushInterval(interval time.Duration) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.flushInterval = interval
	}
}

// WithContainerMaxBufferSize sets the maximum batch size (default 10MB): output is
// delivered as soon as the buffer reaches it, without waiting for the interval.
func WithContainerMaxBufferSize(size int) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.maxBufferSize = size
	}
}

// WithContainerMeter sets an OpenTelemetry meter for metrics collection for container processes
func WithContainerMeter(meter metric.Meter) ContainerProcessOption {
	return func(cfg *containerProcessConfig) {
		cfg.meter = meter
	}
}
