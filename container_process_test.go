package consolestream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewContainerProcess(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
		opts []ContainerProcessOption
	}{
		{
			name: "basic container process",
			cmd:  "echo",
			args: []string{"test"},
			opts: []ContainerProcessOption{
				WithContainerImage("alpine:latest"),
			},
		},
		{
			name: "container with mount",
			cmd:  "ls",
			args: []string{"-la", "/workspace"},
			opts: []ContainerProcessOption{
				WithContainerImage("alpine:latest"),
				WithContainerMount("/tmp", "/workspace", true),
			},
		},
		{
			name: "container with env vars",
			cmd:  "env",
			args: []string{},
			opts: []ContainerProcessOption{
				WithContainerImage("alpine:latest"),
				WithContainerEnvMap(map[string]string{
					"TEST_VAR": "test_value",
				}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			process := NewContainerProcess(tt.cmd, tt.args, tt.opts...)
			require.NotNil(t, process)
			require.Equal(t, tt.cmd, process.cmd)
			require.Equal(t, tt.args, process.args)
		})
	}
}

func TestContainerProcessExecuteAndStream(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode (requires Docker)")
	}

	t.Parallel()

	process := NewContainerProcess("echo", []string{"hello from container"},
		WithContainerImage("alpine:latest"),
		WithContainerFlushInterval(500*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var events []Event
	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		events = append(events, event)

		if event.EventType() == ProcessEndEvent {
			break
		}
	}

	t.Logf("Received %d events", len(events))

	require.GreaterOrEqual(t, len(events), 3, "Should have at least ContainerCreate, ProcessStart, ProcessEnd")

	// Verify event sequence
	require.Equal(t, ContainerCreateEvent, events[0].EventType())

	// Find ProcessStart
	var foundStart bool
	for _, e := range events {
		if e.EventType() == ProcessStartEvent {
			foundStart = true
			break
		}
	}
	require.True(t, foundStart, "Should have ProcessStart event")

	// Last event should be ProcessEnd
	require.Equal(t, ProcessEndEvent, events[len(events)-1].EventType())

	// Verify output contains expected text
	var output string
	for _, e := range events {
		if e.EventType() == OutputEvent {
			data := e.Event.(*OutputData)
			output += string(data.Data)
		}
	}
	require.Contains(t, output, "hello from container")
}

func TestContainerProcessWithMount(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	// t.Parallel()

	// Create temp file to mount
	tmpFile, err := os.CreateTemp("", "test-container-mount-*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	testContent := "test content from host"
	_, err = tmpFile.WriteString(testContent)
	require.NoError(t, err)
	tmpFile.Close()

	// Mount the temp directory into the container
	tmpDir := os.TempDir()
	tmpFileName := filepath.Base(tmpFile.Name()) // Get just the filename (safe method)

	process := NewContainerProcess("cat", []string{"/workspace/" + tmpFileName},
		WithContainerImage("alpine:latest"),
		WithContainerMount(tmpDir, "/workspace", true))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var output string
	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if event.EventType() == OutputEvent {
			data := event.Event.(*OutputData)
			output += string(data.Data)
		}

		if event.EventType() == ProcessEndEvent {
			break
		}
	}

	require.Contains(t, output, testContent)
}

func TestContainerProcessExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	process := NewContainerProcess("sh", []string{"-c", "exit 42"},
		WithContainerImage("alpine:latest"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var exitCode int
	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if event.EventType() == ProcessEndEvent {
			processEnd := event.Event.(*ProcessEnd)
			exitCode = processEnd.ExitCode
			break
		}
	}

	require.Equal(t, 42, exitCode)
}

func TestContainerProcessCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	process := NewContainerProcess("sleep", []string{"30"},
		WithContainerImage("alpine:latest"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	for event, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			break
		}
		_ = event
	}

	duration := time.Since(start)
	require.Less(t, duration, 5*time.Second, "Container should be cancelled quickly")
}

func TestContainerProcessMissingImage(t *testing.T) {
	t.Parallel()

	process := NewContainerProcess("echo", []string{"test"})
	// No image specified

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var gotError bool
	for event, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			gotError = true
			break
		}
		if event.EventType() == ProcessErrorEvent {
			gotError = true
			break
		}
	}

	require.True(t, gotError, "Should error when image not specified")
}

func TestContainerProcessWithEnvVars(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	process := NewContainerProcess("sh", []string{"-c", "echo $TEST_VAR"},
		WithContainerImage("alpine:latest"),
		WithContainerEnvMap(map[string]string{
			"TEST_VAR": "hello_from_env",
		}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var output string
	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if event.EventType() == OutputEvent {
			data := event.Event.(*OutputData)
			output += string(data.Data)
		}

		if event.EventType() == ProcessEndEvent {
			break
		}
	}

	require.Contains(t, output, "hello_from_env")
}

func TestContainerProcessWithWorkingDir(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	process := NewContainerProcess("pwd", []string{},
		WithContainerImage("alpine:latest"),
		WithContainerWorkingDir("/tmp"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var output string
	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		if event.EventType() == OutputEvent {
			data := event.Event.(*OutputData)
			output += string(data.Data)
		}

		if event.EventType() == ProcessEndEvent {
			break
		}
	}

	require.Contains(t, strings.TrimSpace(output), "/tmp")
}

func TestContainerProcessEventOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	process := NewContainerProcess("echo", []string{"test"},
		WithContainerImage("alpine:latest"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var eventTypes []EventType
	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		eventTypes = append(eventTypes, event.EventType())

		if event.EventType() == ProcessEndEvent {
			break
		}
	}

	// Verify event order
	require.GreaterOrEqual(t, len(eventTypes), 3)
	require.Equal(t, ContainerCreateEvent, eventTypes[0], "First event should be ContainerCreate")

	// Find ProcessStart - should come after ContainerCreate
	var processStartIndex int
	for i, et := range eventTypes {
		if et == ProcessStartEvent {
			processStartIndex = i
			break
		}
	}
	require.Greater(t, processStartIndex, 0, "ProcessStart should come after ContainerCreate")

	// Last event should be ProcessEnd
	require.Equal(t, ProcessEndEvent, eventTypes[len(eventTypes)-1], "Last event should be ProcessEnd")
}

func TestContainerProcessConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	const numContainers = 3

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan bool, numContainers)

	for i := 0; i < numContainers; i++ {
		i := i
		go func() {
			process := NewContainerProcess("echo", []string{"container", string(rune('0' + i))},
				WithContainerImage("alpine:latest"))

			for event, err := range process.ExecuteAndStream(ctx) {
				require.NoError(t, err)

				if event.EventType() == ProcessEndEvent {
					processEnd := event.Event.(*ProcessEnd)
					require.Equal(t, 0, processEnd.ExitCode)
					break
				}
			}

			done <- true
		}()
	}

	// Wait for all containers to complete
	for i := 0; i < numContainers; i++ {
		select {
		case <-done:
			// Success
		case <-time.After(60 * time.Second):
			t.Fatal("Timeout waiting for containers to complete")
		}
	}
}

func TestContainerProcessInvalidMount(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	t.Run("nonexistent source path", func(t *testing.T) {
		t.Parallel()

		process := NewContainerProcess("echo", []string{"test"},
			WithContainerImage("alpine:latest"),
			WithContainerMount("/nonexistent/path", "/workspace", false))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var gotError bool
		for event, err := range process.ExecuteAndStream(ctx) {
			if err != nil {
				require.Contains(t, err.Error(), "mount source")
				require.Contains(t, err.Error(), "does not exist")
				gotError = true
				break
			}
			if event.EventType() == ProcessErrorEvent {
				gotError = true
				break
			}
		}

		require.True(t, gotError, "Should error on nonexistent mount source")
	})

	t.Run("relative target path", func(t *testing.T) {
		t.Parallel()

		tmpDir := os.TempDir()
		process := NewContainerProcess("echo", []string{"test"},
			WithContainerImage("alpine:latest"),
			WithContainerMount(tmpDir, "relative/path", false))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var gotError bool
		for event, err := range process.ExecuteAndStream(ctx) {
			if err != nil {
				require.Contains(t, err.Error(), "mount target must be absolute")
				gotError = true
				break
			}
			if event.EventType() == ProcessErrorEvent {
				gotError = true
				break
			}
		}

		require.True(t, gotError, "Should error on relative mount target")
	})
}

func TestContainerProcessImagePull(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	t.Parallel()

	// Use a specific versioned image that's unlikely to be cached locally
	// This tests automatic image pulling
	process := NewContainerProcess("echo", []string{"pulled successfully"},
		WithContainerImage("alpine:3.19.0"))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var events []Event
	var foundPullStart bool
	var foundPullComplete bool
	var output string

	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		events = append(events, event)

		switch event.EventType() {
		case ImagePullStartEvent:
			foundPullStart = true
			pullStart := event.Event.(*ImagePullStart)
			require.Equal(t, "alpine:3.19.0", pullStart.Image)
			t.Logf("Image pull started: %s", pullStart.Image)

		case ImagePullProgressEvent:
			progress := event.Event.(*ImagePullProgress)
			t.Logf("Pull progress: %d%% (%d/%d bytes)",
				progress.PercentComplete, progress.BytesDownloaded, progress.BytesTotal)

		case ImagePullCompleteEvent:
			foundPullComplete = true
			pullComplete := event.Event.(*ImagePullComplete)
			t.Logf("Image pull complete: %s (digest: %s)", pullComplete.Image, pullComplete.Digest)

		case OutputEvent:
			data := event.Event.(*OutputData)
			output += string(data.Data)

		case ProcessEndEvent:
			goto done
		}
	}

done:
	// Note: Pull events only occur if image wasn't already cached
	// So we don't require them, but we log them if they occurred
	if foundPullStart {
		t.Log("Image pull was triggered (image not cached)")
		require.True(t, foundPullComplete, "Should have ImagePullComplete if pull started")
	} else {
		t.Log("Image was already cached (no pull needed)")
	}

	// Always verify the container ran successfully
	require.Contains(t, output, "pulled successfully")
	require.GreaterOrEqual(t, len(events), 3, "Should have at least ContainerCreate, ProcessStart, ProcessEnd")
}
