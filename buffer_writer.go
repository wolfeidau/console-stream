package consolestream

import "sync"

// BufferWriter wraps buffer operations and implements io.WriteCloser
// This can be used by both PipeProcess and PTYProcess for consistent buffer management
type BufferWriter struct {
	mu            sync.Mutex
	buffer        []byte
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

// FlushAndClear returns the current buffer contents and clears it
func (w *BufferWriter) FlushAndClear() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.buffer) == 0 {
		return nil
	}

	data := make([]byte, len(w.buffer))
	copy(data, w.buffer)
	w.buffer = w.buffer[:0]
	return data
}

// Len returns the current buffer length (thread-safe)
func (w *BufferWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buffer)
}
