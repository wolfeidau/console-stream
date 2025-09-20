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

// ExecuteAndStream starts a process and returns an iterator over StreamPart objects
func (p *Process) ExecuteAndStream(ctx context.Context) iter.Seq2[StreamPart, error] {
	return func(yield func(StreamPart, error) bool) {
		// Create the command
		// #nosec G204 - Command and args are controlled by the caller
		cmd := exec.CommandContext(ctx, p.cmd, p.args...)

		// Get stdout and stderr pipes
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			yield(StreamPart{}, ProcessStartError{Cmd: p.cmd, Err: err})
			return
		}

		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			yield(StreamPart{}, ProcessStartError{Cmd: p.cmd, Err: err})
			return
		}

		// Start the command
		if err := cmd.Start(); err != nil {
			yield(StreamPart{}, ProcessStartError{Cmd: p.cmd, Err: err})
			return
		}

		// Store the PID
		p.pid = cmd.Process.Pid

		// Buffer management
		const maxBufferSize = 10 * 1024 * 1024 // 10MB
		var stdoutBuffer, stderrBuffer []byte

		// Channels for coordination
		flushChan := make(chan struct{}, 1)
		doneChan := make(chan error, 1)

		// Start ticker for 1-second intervals
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

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
				if len(stdoutBuffer) > 0 {
					if !yield(StreamPart{
						Timestamp: time.Now(),
						Data:      append([]byte(nil), stdoutBuffer...),
						Stream:    Stdout,
					}, nil) {
						p.mu.Unlock()
						return
					}
					stdoutBuffer = stdoutBuffer[:0]
				}
				if len(stderrBuffer) > 0 {
					if !yield(StreamPart{
						Timestamp: time.Now(),
						Data:      append([]byte(nil), stderrBuffer...),
						Stream:    Stderr,
					}, nil) {
						p.mu.Unlock()
						return
					}
					stderrBuffer = stderrBuffer[:0]
				}
				p.mu.Unlock()

			case <-flushChan:
				// Immediate flush due to buffer size limit
				p.mu.Lock()
				if len(stdoutBuffer) >= maxBufferSize {
					if !yield(StreamPart{
						Timestamp: time.Now(),
						Data:      append([]byte(nil), stdoutBuffer...),
						Stream:    Stdout,
					}, nil) {
						p.mu.Unlock()
						return
					}
					stdoutBuffer = stdoutBuffer[:0]
				}
				if len(stderrBuffer) >= maxBufferSize {
					if !yield(StreamPart{
						Timestamp: time.Now(),
						Data:      append([]byte(nil), stderrBuffer...),
						Stream:    Stderr,
					}, nil) {
						p.mu.Unlock()
						return
					}
					stderrBuffer = stderrBuffer[:0]
				}
				p.mu.Unlock()

			case err := <-doneChan:
				// Process finished - flush remaining buffers and handle exit
				p.mu.Lock()
				if len(stdoutBuffer) > 0 {
					if !yield(StreamPart{
						Timestamp: time.Now(),
						Data:      append([]byte(nil), stdoutBuffer...),
						Stream:    Stdout,
					}, nil) {
						p.mu.Unlock()
						return
					}
				}
				if len(stderrBuffer) > 0 {
					if !yield(StreamPart{
						Timestamp: time.Now(),
						Data:      append([]byte(nil), stderrBuffer...),
						Stream:    Stderr,
					}, nil) {
						p.mu.Unlock()
						return
					}
				}
				p.mu.Unlock()

				if err != nil {
					if exitError, ok := err.(*exec.ExitError); ok {
						yield(StreamPart{}, ProcessFailedError{
							Cmd:      p.cmd,
							ExitCode: exitError.ExitCode(),
						})
					} else {
						yield(StreamPart{}, ProcessKilledError{
							Cmd:    p.cmd,
							Signal: "unknown",
						})
					}
				}
				return
			}
		}
	}
}

type Stream int

const (
	Stdout Stream = iota
	Stderr
)

type StreamPart struct {
	Timestamp time.Time
	Data      []byte
	Stream    Stream
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
