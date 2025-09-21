package consolestream

import (
	"bufio"
	"context"
	"io"
	"iter"
	"os/exec"
	"sync"
	"time"
)

type PipeProcess struct {
	cmd       string
	args      []string
	pid       int
	cancellor Cancellor
	mu        sync.Mutex
}

func NewPipeProcess(cmd string, cancellor Cancellor, args ...string) *PipeProcess {
	return &PipeProcess{
		cmd:       cmd,
		args:      args,
		cancellor: cancellor,
	}
}

// ExecuteAndStream starts a process and returns an iterator over Event objects
func (p *PipeProcess) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
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
					if !yield(newEvent(&PipeOutputData{
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
					if !yield(newEvent(&PipeOutputData{
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
					if !yield(newEvent(&PipeOutputData{
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
					if !yield(newEvent(&PipeOutputData{
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
				// PipeProcess finished - flush remaining buffers and handle exit
				endTime := time.Now()
				duration := endTime.Sub(processStart)

				p.mu.Lock()
				if len(stdoutBuffer) > 0 {
					endPart := Event{
						Timestamp: endTime,
						Event: &PipeOutputData{
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
						Event: &PipeOutputData{
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
								Message: "PipeProcess terminated unexpectedly",
							},
						}, nil)
					}
				} else {
					// PipeProcess completed successfully
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

// readStream reads from a pipe and accumulates data in the provided buffer
func (p *PipeProcess) readStream(ctx context.Context, pipe io.ReadCloser, buffer *[]byte, flushChan chan<- struct{}, maxBufferSize int) {
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
