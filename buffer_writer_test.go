package consolestream

import (
	"testing"

	"github.com/stretchr/testify/require"
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
				_, err := writer.Write([]byte{byte(id)})
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
