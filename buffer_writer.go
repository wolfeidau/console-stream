package consolestream

import (
	"sync"
	"time"

	"github.com/wolfeidau/osc7501"
)

// pendingStatus is a program status report and the buffer offset it follows.
type pendingStatus struct {
	Offset int
	Time   time.Time
	Report osc7501.Report
}

// BufferWriter wraps buffer operations and implements io.WriteCloser
// This can be used by both PipeProcess and PTYProcess for consistent buffer management
type BufferWriter struct {
	mu            sync.Mutex
	buffer        []byte
	segments      []OutputSegment
	statuses      []pendingStatus
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

// addStatus records a program status report at the current end of the buffer
// and signals the stream loop to deliver it promptly.
func (w *BufferWriter) addStatus(r osc7501.Report) {
	w.mu.Lock()
	w.statuses = append(w.statuses, pendingStatus{Offset: len(w.buffer), Time: time.Now(), Report: r})
	select {
	case w.flushChan <- struct{}{}:
	default:
	}
	w.mu.Unlock()
}

// hasStatus reports whether any program status is pending.
func (w *BufferWriter) hasStatus() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.statuses) > 0
}

// FlushSegments returns copies of the buffer contents and their read segments,
// clearing both (and any pending statuses). It returns nil, nil when the buffer is empty.
func (w *BufferWriter) FlushSegments() ([]byte, []OutputSegment) {
	data, segs, _ := w.flushBatch()
	return data, segs
}

// flushBatch returns copies of the buffer, its segments and pending statuses,
// clearing all three. It returns nils only when both buffer and statuses are
// empty; a status can be pending with zero bytes.
func (w *BufferWriter) flushBatch() ([]byte, []OutputSegment, []pendingStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.buffer) == 0 && len(w.statuses) == 0 {
		return nil, nil, nil
	}
	var statuses []pendingStatus
	if len(w.statuses) > 0 {
		statuses = make([]pendingStatus, len(w.statuses))
		copy(statuses, w.statuses)
		w.statuses = w.statuses[:0]
	}
	if len(w.buffer) == 0 {
		return nil, nil, statuses
	}

	data := make([]byte, len(w.buffer))
	copy(data, w.buffer)
	segs := make([]OutputSegment, len(w.segments))
	copy(segs, w.segments)
	w.buffer = w.buffer[:0]
	w.segments = w.segments[:0]
	return data, segs, statuses
}

// Len returns the current buffer length (thread-safe)
func (w *BufferWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buffer)
}
