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
	cmd           string
	args          []string
	pid           int
	cancellor     Cancellor
	env           []string
	flushInterval time.Duration
	maxBufferSize int

	// Buffers and their protection
	bufferMu     sync.Mutex
	stdoutBuffer []byte
	stderrBuffer []byte
}

func NewPipeProcess(cmd string, args []string, opts ...ProcessOption) *PipeProcess {
	cfg := &processConfig{}
	cfg.applyDefaults()

	for _, opt := range opts {
		opt(cfg)
	}

	return &PipeProcess{
		cmd:           cmd,
		args:          args,
		cancellor:     cfg.cancellor,
		env:           cfg.env,
		flushInterval: cfg.flushInterval,
		maxBufferSize: cfg.maxBufferSize,
	}
}

// ExecuteAndStream starts a process and returns an iterator over Event objects
func (p *PipeProcess) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		// Create the command
		// #nosec G204 - Command and args are controlled by the caller
		cmd := exec.CommandContext(ctx, p.cmd, p.args...)
		if len(p.env) > 0 {
			cmd.Env = p.env
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

		// Channels for coordination
		flushChan := make(chan struct{}, 1)
		doneChan := make(chan error, 1)

		// Start ticker for configurable intervals
		ticker := time.NewTicker(p.flushInterval)
		defer ticker.Stop()

		// Heartbeat tracking
		var hasEmittedEventsThisSecond bool

		// Read from stdout and stderr
		go p.readStream(ctx, stdoutPipe, Stdout, flushChan)
		go p.readStream(ctx, stderrPipe, Stderr, flushChan)

		// Wait for process completion
		go func() {
			time.Sleep(100 * time.Millisecond) // Small delay to ensure pipes are read
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

				if len(p.stdoutBuffer) > 0 {
					if !yield(newEvent(&PipeOutputData{
						Data:   append([]byte(nil), p.stdoutBuffer...),
						Stream: Stdout,
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
					p.stdoutBuffer = p.stdoutBuffer[:0]
					eventsEmitted = true
				}
				if len(p.stderrBuffer) > 0 {
					if !yield(newEvent(&PipeOutputData{
						Data:   append([]byte(nil), p.stderrBuffer...),
						Stream: Stderr,
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
					p.stderrBuffer = p.stderrBuffer[:0]
					eventsEmitted = true
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
				if len(p.stdoutBuffer) >= p.maxBufferSize {
					if !yield(newEvent(&PipeOutputData{
						Data:   append([]byte(nil), p.stdoutBuffer...),
						Stream: Stdout,
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
					p.stdoutBuffer = p.stdoutBuffer[:0]
					hasEmittedEventsThisSecond = true
				}
				if len(p.stderrBuffer) >= p.maxBufferSize {
					if !yield(newEvent(&PipeOutputData{
						Data:   append([]byte(nil), p.stderrBuffer...),
						Stream: Stderr,
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
					p.stderrBuffer = p.stderrBuffer[:0]
					hasEmittedEventsThisSecond = true
				}
				p.bufferMu.Unlock()

			case err := <-doneChan:
				// PipeProcess finished - flush remaining buffers and handle exit
				endTime := time.Now()
				duration := endTime.Sub(processStart)

				p.bufferMu.Lock()
				if len(p.stdoutBuffer) > 0 {
					endPart := Event{
						Timestamp: endTime,
						Event: &PipeOutputData{
							Data:   append([]byte(nil), p.stdoutBuffer...),
							Stream: Stdout,
						},
					}
					if !yield(endPart, nil) {
						p.bufferMu.Unlock()
						return
					}
				}
				if len(p.stderrBuffer) > 0 {
					endPart := Event{
						Timestamp: endTime,
						Event: &PipeOutputData{
							Data:   append([]byte(nil), p.stderrBuffer...),
							Stream: Stderr,
						},
					}
					if !yield(endPart, nil) {
						p.bufferMu.Unlock()
						return
					}
				}
				p.bufferMu.Unlock()

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

// readStream reads from a pipe and accumulates data in the struct buffer fields
func (p *PipeProcess) readStream(ctx context.Context, pipe io.ReadCloser, streamType StreamType, flushChan chan<- struct{}) {
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
					p.bufferMu.Lock()
					if streamType == Stdout {
						p.stdoutBuffer = append(p.stdoutBuffer, data...)
						if len(p.stdoutBuffer) >= p.maxBufferSize {
							select {
							case flushChan <- struct{}{}:
							default:
							}
						}
					} else {
						p.stderrBuffer = append(p.stderrBuffer, data...)
						if len(p.stderrBuffer) >= p.maxBufferSize {
							select {
							case flushChan <- struct{}{}:
							default:
							}
						}
					}
					p.bufferMu.Unlock()
				}
			}
			return
		}

		p.bufferMu.Lock()
		if streamType == Stdout {
			p.stdoutBuffer = append(p.stdoutBuffer, data...)
			if len(p.stdoutBuffer) >= p.maxBufferSize {
				// Trigger immediate flush
				select {
				case flushChan <- struct{}{}:
				default:
				}
			}
		} else {
			p.stderrBuffer = append(p.stderrBuffer, data...)
			if len(p.stderrBuffer) >= p.maxBufferSize {
				// Trigger immediate flush
				select {
				case flushChan <- struct{}{}:
				default:
				}
			}
		}
		p.bufferMu.Unlock()
	}
}
