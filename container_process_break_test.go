package consolestream

import (
	"context"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/stretchr/testify/require"
)

// requireContainerGone fails unless the container disappears within timeout.
func requireContainerGone(t *testing.T, containerID string, timeout time.Duration) {
	t.Helper()

	cli, err := initDockerClient("docker")
	require.NoError(t, err)
	defer cli.Close()

	require.Eventually(t, func() bool {
		_, err := cli.ContainerInspect(context.Background(), containerID)
		return cerrdefs.IsNotFound(err)
	}, timeout, 100*time.Millisecond, "container %s should be removed", containerID)
}

func TestContainerProcessEarlyBreakRemovesContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	requireDocker(t)

	t.Parallel()

	process := NewContainerProcess("sleep", []string{"30"},
		WithContainerImage("alpine:latest"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var containerID string
	var started time.Time
	for event, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)

		switch event.EventType() {
		case ContainerCreateEvent:
			containerID = event.Event.(*ContainerCreate).ContainerID
		case ProcessStartEvent:
			started = time.Now()
		}
		if !started.IsZero() {
			break
		}
	}

	require.NotEmpty(t, containerID)
	require.Less(t, time.Since(started), 5*time.Second, "iterator should return promptly after break")
	requireContainerGone(t, containerID, 5*time.Second)
}

func TestContainerProcessBreakOnFirstOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping container test in short mode")
	}

	requireDocker(t)

	t.Parallel()

	process := NewContainerProcess("sh", []string{"-c", "echo hi; sleep 30"},
		WithContainerImage("alpine:latest"),
		WithContainerFlushInterval(100*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var containerID string
	var sawOutput bool
	var broke time.Time
	require.NotPanics(t, func() {
		for event, err := range process.ExecuteAndStream(ctx) {
			require.NoError(t, err)

			if event.EventType() == ContainerCreateEvent {
				containerID = event.Event.(*ContainerCreate).ContainerID
			}
			if event.EventType() == OutputEvent {
				sawOutput = true
				broke = time.Now()
				break
			}
		}
	})

	require.True(t, sawOutput)
	require.Less(t, time.Since(broke), 5*time.Second, "iterator should return promptly after break")
	requireContainerGone(t, containerID, 5*time.Second)
}
