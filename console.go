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
	"syscall"
	"time"
)

type Process struct {
	cmd       string
	args      []string
	pid       int
	cancellor Cancellor
	mu        sync.Mutex
}

func NewProcess(cmd string, cancellor Cancellor, args ...string) *Process {
	return &Process{
		cmd:       cmd,
		args:      args,
		cancellor: cancellor,
	}
}

// ExecuteAndStream starts a process and returns an iterator over Event objects
func (p *Process) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		// Create the command
		// #nosec G204 - Command and args are controlled by the caller
		cmd := exec.CommandContext(ctx, p.cmd, p.args...)

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

		// Buffer management
		const maxBufferSize = 10 * 1024 * 1024 // 10MB
		var stdoutBuffer, stderrBuffer []byte

		// Channels for coordination
		flushChan := make(chan struct{}, 1)
		doneChan := make(chan error, 1)

		// Start ticker for 1-second intervals
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		// Heartbeat tracking
		var hasEmittedEventsThisSecond bool

		// Read from stdout and stderr
		go p.readStream(ctx, stdoutPipe, &stdoutBuffer, flushChan, maxBufferSize)
		go p.readStream(ctx, stderrPipe, &stderrBuffer, flushChan, maxBufferSize)

		// Wait for process completion
		go func() {
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
				p.mu.Lock()
				eventsEmitted := false

				if len(stdoutBuffer) > 0 {
					if !yield(newEvent(&OutputData{
						Data:   append([]byte(nil), stdoutBuffer...),
						Stream: Stdout,
					}), nil) {
						p.mu.Unlock()
						return
					}
					stdoutBuffer = stdoutBuffer[:0]
					eventsEmitted = true
				}
				if len(stderrBuffer) > 0 {
					if !yield(newEvent(&OutputData{
						Data:   append([]byte(nil), stderrBuffer...),
						Stream: Stderr,
					}), nil) {
						p.mu.Unlock()
						return
					}
					stderrBuffer = stderrBuffer[:0]
					eventsEmitted = true
				}

				// Emit heartbeat if no events were emitted this second
				if !hasEmittedEventsThisSecond && !eventsEmitted {
					if !yield(newEvent(&HeartbeatEvent{
						ProcessAlive: true,
						ElapsedTime:  time.Since(processStart),
					}), nil) {
						p.mu.Unlock()
						return
					}
				}

				// Reset heartbeat tracking
				hasEmittedEventsThisSecond = eventsEmitted
				p.mu.Unlock()

			case <-flushChan:
				// Immediate flush due to buffer size limit
				p.mu.Lock()
				if len(stdoutBuffer) >= maxBufferSize {
					if !yield(newEvent(&OutputData{
						Data:   append([]byte(nil), stdoutBuffer...),
						Stream: Stdout,
					}), nil) {
						p.mu.Unlock()
						return
					}
					stdoutBuffer = stdoutBuffer[:0]
					hasEmittedEventsThisSecond = true
				}
				if len(stderrBuffer) >= maxBufferSize {
					if !yield(newEvent(&OutputData{
						Data:   append([]byte(nil), stderrBuffer...),
						Stream: Stderr,
					}), nil) {
						p.mu.Unlock()
						return
					}
					stderrBuffer = stderrBuffer[:0]
					hasEmittedEventsThisSecond = true
				}
				p.mu.Unlock()

			case err := <-doneChan:
				// Process finished - flush remaining buffers and handle exit
				endTime := time.Now()
				duration := endTime.Sub(processStart)

				p.mu.Lock()
				if len(stdoutBuffer) > 0 {
					endPart := Event{
						Timestamp: endTime,
						Event: &OutputData{
							Data:   append([]byte(nil), stdoutBuffer...),
							Stream: Stdout,
						},
					}
					if !yield(endPart, nil) {
						p.mu.Unlock()
						return
					}
				}
				if len(stderrBuffer) > 0 {
					endPart := Event{
						Timestamp: endTime,
						Event: &OutputData{
							Data:   append([]byte(nil), stderrBuffer...),
							Stream: Stderr,
						},
					}
					if !yield(endPart, nil) {
						p.mu.Unlock()
						return
					}
				}
				p.mu.Unlock()

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
					} else {
						yield(Event{
							Timestamp: endTime,
							Event: &ProcessError{
								Error:   err,
								Message: "Process terminated unexpectedly",
							},
						}, nil)
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
				}
				return
			}
		}
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
	OutputEvent EventType = iota
	ProcessStartEvent
	ProcessEndEvent
	ProcessErrorEvent
	HeartbeatEventType
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

// OutputData represents stdout/stderr data from the process
type OutputData struct {
	Data   []byte
	Stream StreamType
}

func (o *OutputData) Type() EventType {
	return OutputEvent
}

func (o *OutputData) String() string {
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

// Helper functions for event type checking
func IsOutputEvent(event StreamEvent) bool {
	return event.Type() == OutputEvent
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

type ProcessFailedError struct {
	Cmd      string
	ExitCode int
}

func (e ProcessFailedError) Error() string {
	return fmt.Sprintf("process %s exited with code %d", e.Cmd, e.ExitCode)
}

type ProcessKilledError struct {
	Cmd    string
	Signal string
}

func (e ProcessKilledError) Error() string {
	return fmt.Sprintf("process %s was killed by signal %s", e.Cmd, e.Signal)
}

// readStream reads from a pipe and accumulates data in the provided buffer
func (p *Process) readStream(ctx context.Context, pipe io.ReadCloser, buffer *[]byte, flushChan chan<- struct{}, maxBufferSize int) {
	defer pipe.Close()
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
					p.mu.Lock()
					*buffer = append(*buffer, data...)
					if len(*buffer) >= maxBufferSize {
						select {
						case flushChan <- struct{}{}:
						default:
						}
					}
					p.mu.Unlock()
				}
			}
			return
		}

		p.mu.Lock()
		*buffer = append(*buffer, data...)
		if len(*buffer) >= maxBufferSize {
			// Trigger immediate flush
			select {
			case flushChan <- struct{}{}:
			default:
			}
		}
		p.mu.Unlock()
	}
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
		// Process might already be dead, try SIGKILL anyway
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
