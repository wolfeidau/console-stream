package consolestream

import (
	"sync"
	"time"
)

// BufferWriter wraps buffer operations and implements io.WriteCloser
// This can be used by both PipeProcess and PTYProcess for consistent buffer management
type BufferWriter struct {
	mu            sync.Mutex
	buffer        []byte
	segments      []OutputSegment
	flushChan     chan<- struct{}
	maxBufferSize int
}

func NewBufferWriter(flushChan chan<- struct{}, maxBufferSize int) *BufferWriter {
	return &BufferWriter{
		flushChan:     flushChan,
		maxBufferSize: maxBufferSize,
	}
}

func (w *BufferWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	w.mu.Lock()
	now := time.Now()
	// Writes within 1ms of the previous segment are merged into it.
	if n := len(w.segments); n == 0 || now.Sub(w.segments[n-1].Time) >= time.Millisecond {
		w.segments = append(w.segments, OutputSegment{Offset: len(w.buffer), Time: now})
	}
	w.buffer = append(w.buffer, data...)
	if len(w.buffer) >= w.maxBufferSize {
		// Trigger immediate flush
		select {
		case w.flushChan <- struct{}{}:
		default:
		}
	}
	w.mu.Unlock()

	return len(data), nil
}

func (w *BufferWriter) Close() error {
	return nil
}

// FlushAndClear returns the current buffer contents and clears it (including segments)
func (w *BufferWriter) FlushAndClear() []byte {
	data, _ := w.FlushSegments()
	return data
}

// FlushSegments returns copies of the buffer contents and their read segments,
// clearing both. It returns nil, nil when the buffer is empty.
func (w *BufferWriter) FlushSegments() ([]byte, []OutputSegment) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.buffer) == 0 {
		return nil, nil
	}

	data := make([]byte, len(w.buffer))
	copy(data, w.buffer)
	segs := make([]OutputSegment, len(w.segments))
	copy(segs, w.segments)
	w.buffer = w.buffer[:0]
	w.segments = w.segments[:0]
	return data, segs
}

// Len returns the current buffer length (thread-safe)
func (w *BufferWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buffer)
}
