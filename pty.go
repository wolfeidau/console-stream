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
)

// PTYOutputData represents terminal output from a PTY process with ANSI sequences preserved
type PTYOutputData struct {
	Data []byte
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

	// Buffer and its protection
	bufferMu  sync.Mutex
	ptyBuffer []byte

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

	return &PTYProcess{
		cmd:           cmd,
		args:          args,
		cancellor:     cfg.cancellor,
		env:           cfg.env,
		ptySize:       ptySize,
		flushInterval: cfg.flushInterval,
		maxBufferSize: cfg.maxBufferSize,
	}
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
				p.bufferMu.Lock()
				eventsEmitted := false

				if len(p.ptyBuffer) > 0 {
					if !yield(newEvent(&PTYOutputData{
						Data: append([]byte(nil), p.ptyBuffer...),
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
					p.ptyBuffer = p.ptyBuffer[:0]
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
				if len(p.ptyBuffer) >= p.maxBufferSize {
					if !yield(newEvent(&PTYOutputData{
						Data: append([]byte(nil), p.ptyBuffer...),
					}), nil) {
						p.bufferMu.Unlock()
						return
					}
					p.ptyBuffer = p.ptyBuffer[:0]
					hasEmittedEventsThisSecond = true
				}
				p.bufferMu.Unlock()

			case err := <-doneChan:
				// Process finished - flush remaining buffers and handle exit
				endTime := time.Now()
				duration := endTime.Sub(processStart)

				p.bufferMu.Lock()
				if len(p.ptyBuffer) > 0 {
					endPart := Event{
						Timestamp: endTime,
						Event: &PTYOutputData{
							Data: append([]byte(nil), p.ptyBuffer...),
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
								Message: "PTY process terminated unexpectedly",
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

// ptyBufferWriter wraps buffer operations and implements io.WriteCloser
type ptyBufferWriter struct {
	bufferMu      *sync.Mutex
	buffer        *[]byte
	flushChan     chan<- struct{}
	maxBufferSize int
}

func newPTYBufferWriter(bufferMu *sync.Mutex, buffer *[]byte, flushChan chan<- struct{}, maxBufferSize int) *ptyBufferWriter {
	return &ptyBufferWriter{
		bufferMu:      bufferMu,
		buffer:        buffer,
		flushChan:     flushChan,
		maxBufferSize: maxBufferSize,
	}
}

func (w *ptyBufferWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	w.bufferMu.Lock()
	*w.buffer = append(*w.buffer, data...)
	if len(*w.buffer) >= w.maxBufferSize {
		// Trigger immediate flush
		select {
		case w.flushChan <- struct{}{}:
		default:
		}
	}
	w.bufferMu.Unlock()

	return len(data), nil
}

func (w *ptyBufferWriter) Close() error {
	return nil
}

// readPTYStream reads from a PTY and accumulates data in the ptyBuffer field
func (p *PTYProcess) readPTYStream(ctx context.Context, ptmx *os.File, flushChan chan<- struct{}) {
	defer p.waiter.Done()
	writer := newPTYBufferWriter(&p.bufferMu, &p.ptyBuffer, flushChan, p.maxBufferSize)
	defer writer.Close()
	_, _ = io.Copy(writer, ptmx)
}
