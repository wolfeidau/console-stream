package consolestream

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/console-stream/internal/osc7501"
)

func TestBufferWriter_Write(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 1)
	writer := NewBufferWriter(flushChan, 100)

	// Test writing data
	n, err := writer.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, 5, writer.Len())

	// Test writing empty data
	n, err = writer.Write([]byte(""))
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Equal(t, 5, writer.Len())

	// Test writing nil data
	n, err = writer.Write(nil)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Equal(t, 5, writer.Len())
}

func TestBufferWriter_FlushAndClear(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 1)
	writer := NewBufferWriter(flushChan, 100)

	// Test flush on empty buffer
	data := writer.FlushAndClear()
	require.Nil(t, data)
	require.Equal(t, 0, writer.Len())

	// Write some data
	_, err := writer.Write([]byte("hello world"))
	require.NoError(t, err)
	require.Equal(t, 11, writer.Len())

	// Flush and verify
	data = writer.FlushAndClear()
	require.Equal(t, []byte("hello world"), data)
	require.Equal(t, 0, writer.Len())

	// Verify buffer is cleared
	data = writer.FlushAndClear()
	require.Nil(t, data)
}

func TestBufferWriter_MaxBufferSizeFlush(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 10)
	maxSize := 10
	writer := NewBufferWriter(flushChan, maxSize)

	// Write data less than max size
	_, err := writer.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 0, len(flushChan)) // No flush signal

	// Write data that exceeds max size
	_, err = writer.Write([]byte(" world!"))
	require.NoError(t, err)
	require.Equal(t, 1, len(flushChan)) // Flush signal sent

	// Drain the flush channel
	<-flushChan

	// Write exactly max size
	writer.FlushAndClear() // Clear first
	_, err = writer.Write(make([]byte, maxSize))
	require.NoError(t, err)
	require.Equal(t, 1, len(flushChan)) // Flush signal sent
}

func TestBufferWriter_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 100)
	writer := NewBufferWriter(flushChan, 1000)

	// Test concurrent writes
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < 100; j++ {
				var b byte
				if id >= 0 && id <= 255 {
					b = byte(id)
				}
				_, err := writer.Write([]byte{b})
				require.NoError(t, err)
			}
		}(i)
	}

	// Test concurrent reads
	go func() {
		defer func() { done <- true }()
		for i := 0; i < 50; i++ {
			writer.Len()
			writer.FlushAndClear()
		}
	}()

	// Wait for all goroutines
	for i := 0; i < 11; i++ {
		<-done
	}

	// Final state should be consistent
	finalLen := writer.Len()
	require.True(t, finalLen >= 0)
}

func TestBufferWriter_FlushChannelBlocking(t *testing.T) {
	t.Parallel()

	// Create a channel with no buffer to test non-blocking behavior
	flushChan := make(chan struct{})
	writer := NewBufferWriter(flushChan, 5)

	// Write data that exceeds max size - should not block even with full channel
	_, err := writer.Write([]byte("hello world"))
	require.NoError(t, err)

	// Verify that the operation completed (didn't block)
	require.Equal(t, 11, writer.Len())
}

func TestBufferWriter_Close(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 1)
	writer := NewBufferWriter(flushChan, 100)

	// Close should not return error
	err := writer.Close()
	require.NoError(t, err)

	// Should still be able to use writer after close
	_, err = writer.Write([]byte("test"))
	require.NoError(t, err)
	require.Equal(t, 4, writer.Len())
}

func TestBufferWriter_LenThreadSafety(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 1)
	writer := NewBufferWriter(flushChan, 100)

	// Start goroutine that continuously writes
	done := make(chan bool)
	go func() {
		defer func() { done <- true }()
		for i := 0; i < 1000; i++ {
			_, _ = writer.Write([]byte("x"))
		}
	}()

	// Continuously check length from main goroutine
	for i := 0; i < 100; i++ {
		length := writer.Len()
		require.True(t, length >= 0)
		require.True(t, length <= 1000)
	}

	<-done
	finalLen := writer.Len()
	require.Equal(t, 1000, finalLen)
}

func TestBufferWriter_NewBufferWriter(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 1)
	maxSize := 50

	writer := NewBufferWriter(flushChan, maxSize)

	require.NotNil(t, writer)
	require.Equal(t, 0, writer.Len())
	require.Equal(t, maxSize, writer.maxBufferSize)
	require.NotNil(t, writer.flushChan)
}

func TestBufferWriter_Segments(t *testing.T) {
	t.Parallel()

	t.Run("separated writes create segments", func(t *testing.T) {
		w := NewBufferWriter(make(chan struct{}, 1), 100)
		_, err := w.Write([]byte("abc"))
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		_, err = w.Write([]byte("de"))
		require.NoError(t, err)

		data, segs := w.FlushSegments()
		require.Equal(t, "abcde", string(data))
		require.Len(t, segs, 2)
		require.Equal(t, 0, segs[0].Offset)
		require.Equal(t, 3, segs[1].Offset)
		require.True(t, segs[1].Time.After(segs[0].Time))

		data, segs = w.FlushSegments()
		require.Nil(t, data)
		require.Nil(t, segs)
		require.Equal(t, 0, w.Len())
	})

	t.Run("back-to-back writes merge", func(t *testing.T) {
		w := NewBufferWriter(make(chan struct{}, 1), 100)
		_, _ = w.Write([]byte("a"))
		_, _ = w.Write([]byte("b"))
		_, segs := w.FlushSegments()
		require.Len(t, segs, 1)
	})

	t.Run("FlushAndClear clears segments", func(t *testing.T) {
		w := NewBufferWriter(make(chan struct{}, 1), 100)
		_, _ = w.Write([]byte("a"))
		require.Equal(t, "a", string(w.FlushAndClear()))
		_, _ = w.Write([]byte("b"))
		data, segs := w.FlushSegments()
		require.Equal(t, "b", string(data))
		require.Len(t, segs, 1)
		require.Equal(t, 0, segs[0].Offset)
	})
}

func TestBufferWriter_AddStatus(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 1)
	writer := NewBufferWriter(flushChan, 100)

	_, err := writer.Write([]byte("abc"))
	require.NoError(t, err)
	require.False(t, writer.hasStatus())

	writer.addStatus(osc7501.Report{State: "working", Progress: -1})
	require.True(t, writer.hasStatus())
	require.Len(t, flushChan, 1)

	_, err = writer.Write([]byte("de"))
	require.NoError(t, err)

	data, segs, statuses := writer.flushBatch()
	require.Equal(t, []byte("abcde"), data)
	require.NotEmpty(t, segs)
	require.Len(t, statuses, 1)
	require.Equal(t, 3, statuses[0].Offset)
	require.Equal(t, "working", statuses[0].Report.State)
	require.False(t, statuses[0].Time.IsZero())

	require.False(t, writer.hasStatus())
	require.Equal(t, 0, writer.Len())
	data, segs, statuses = writer.flushBatch()
	require.Nil(t, data)
	require.Nil(t, segs)
	require.Nil(t, statuses)
}

func TestBufferWriter_StatusOnlyBatch(t *testing.T) {
	t.Parallel()

	flushChan := make(chan struct{}, 1)
	writer := NewBufferWriter(flushChan, 100)

	writer.addStatus(osc7501.Report{State: "blocked", Progress: -1})
	writer.addStatus(osc7501.Report{State: "done", Progress: -1}) // flushChan full: must not block

	data, segs, statuses := writer.flushBatch()
	require.Nil(t, data)
	require.Nil(t, segs)
	require.Len(t, statuses, 2)
	require.Equal(t, 0, statuses[0].Offset)
	require.Equal(t, "done", statuses[1].Report.State)
	require.False(t, writer.hasStatus())
}

func TestBufferWriter_FlushSegmentsClearsStatuses(t *testing.T) {
	t.Parallel()

	writer := NewBufferWriter(make(chan struct{}, 1), 100)
	_, err := writer.Write([]byte("x"))
	require.NoError(t, err)
	writer.addStatus(osc7501.Report{State: "working", Progress: -1})

	data, _ := writer.FlushSegments()
	require.Equal(t, []byte("x"), data)
	require.False(t, writer.hasStatus())
}

func TestSliceSegments(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(100, 0)
	segs := []OutputSegment{{0, t0}, {4, t0.Add(time.Second)}, {8, t0.Add(2 * time.Second)}}

	require.Equal(t, []OutputSegment{{0, t0}, {1, t0.Add(time.Second)}}, sliceSegments(segs, 3, 8))
	require.Equal(t, []OutputSegment{{0, t0.Add(time.Second)}, {4, t0.Add(2 * time.Second)}}, sliceSegments(segs, 4, 12))
	require.Nil(t, sliceSegments(nil, 0, 3))
}
