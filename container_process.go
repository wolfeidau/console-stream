package consolestream

import (
	"context"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"
)

// ContainerProcess represents a process running inside a container
type ContainerProcess struct {
	// Command
	cmd  string
	args []string

	// Container config
	image      string
	runtime    string
	mounts     []ContainerMount
	env        []string
	workingDir string

	// Shared config
	cancellor     Cancellor
	flushInterval time.Duration
	maxBufferSize int

	// State
	pid         int
	containerID string
	stats       *ProcessStats
}

// NewContainerProcess creates a new container process with functional options
func NewContainerProcess(cmd string, args []string, opts ...ContainerProcessOption) *ContainerProcess {
	cfg := &containerProcessConfig{}
	cfg.applyDefaults()

	for _, opt := range opts {
		opt(cfg)
	}

	process := &ContainerProcess{
		cmd:           cmd,
		args:          args,
		image:         cfg.image,
		runtime:       cfg.runtime,
		mounts:        cfg.mounts,
		env:           cfg.env,
		workingDir:    cfg.workingDir,
		cancellor:     cfg.cancellor,
		flushInterval: cfg.flushInterval,
		maxBufferSize: cfg.maxBufferSize,
	}

	// Initialize stats if meter provided (graceful degradation)
	if cfg.meter != nil {
		if meter, ok := cfg.meter.(metric.Meter); ok {
			processInfo := ProcessInfo{
				Command: cmd,
				Args:    args,
				// PID will be set later when container starts
			}

			stats, err := NewProcessStats(meter, cfg.metricsInterval, processInfo)
			if err == nil {
				process.stats = stats
			}
			// If err != nil, we simply continue without stats (graceful degradation)
		}
	}

	return process
}

// ExecuteAndStream starts a container and returns an iterator over Event objects
func (cp *ContainerProcess) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		// Validation
		if cp.image == "" {
			yield(newEvent(&ProcessError{
				Error:   fmt.Errorf("container image is required"),
				Message: "Container image not specified",
			}), ProcessStartError{Cmd: cp.cmd, Err: fmt.Errorf("image required")})
			return
		}

		// 1. Initialize Docker client
		dockerClient, err := initDockerClient(cp.runtime)
		if err != nil {
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Failed to initialize container runtime client",
			}), ProcessStartError{Cmd: cp.cmd, Err: err})
			return
		}
		defer dockerClient.Close()

		// 2. Create container
		containerID, err := cp.createContainer(ctx, dockerClient)
		if err != nil {
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Failed to create container",
			}), ProcessStartError{Cmd: cp.cmd, Err: err})
			return
		}
		cp.containerID = containerID

		// Ensure cleanup (silent failures per Drone pattern)
		defer cp.removeContainer(context.Background(), dockerClient, containerID)

		// 3. Emit ContainerCreate event
		if !yield(newEvent(&ContainerCreate{
			ContainerID: containerID,
			Image:       cp.image,
		}), nil) {
			return
		}

		// 4. Start container
		processStart := time.Now()
		if err := dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Failed to start container",
			}), ProcessStartError{Cmd: cp.cmd, Err: err})
			return
		}

		// 5. Inspect container to get PID (for metrics)
		inspect, err := dockerClient.ContainerInspect(ctx, containerID)
		if err == nil && inspect.State != nil {
			cp.pid = inspect.State.Pid
		}

		// Initialize stats if metrics enabled
		if cp.stats != nil {
			cp.stats.processInfo.PID = cp.pid
			cp.stats.Start(ctx)
		}

		// 6. Emit ProcessStart event (reuse existing event type)
		if !yield(Event{
			Timestamp: processStart,
			Event: &ProcessStart{
				PID:     cp.pid,
				Command: cp.cmd,
				Args:    cp.args,
			},
		}, nil) {
			return
		}

		if cp.stats != nil {
			cp.stats.RecordEvent(ctx, "process_start")
		}

		// 7. Stream logs in background
		doneChan := make(chan error, 1)
		go cp.streamContainerLogs(ctx, dockerClient, containerID, yield, processStart, doneChan)

		// 8. Wait for container completion
		waitChan, errChan := dockerClient.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

		var exitCode int64
		select {
		case result := <-waitChan:
			exitCode = result.StatusCode
		case err := <-errChan:
			yield(newEvent(&ProcessError{
				Error:   err,
				Message: "Container wait failed",
			}), nil)
			if cp.stats != nil {
				cp.stats.RecordError(ctx, "container_wait_error")
			}
			return
		}

		// Wait for log streaming to complete
		// This always happens after container finishes (or context cancelled)
		<-doneChan

		// 9. Update metrics
		endTime := time.Now()
		duration := endTime.Sub(processStart)

		if cp.stats != nil {
			cp.stats.Stop(ctx)
			cp.stats.SetExitCode(int(exitCode))
		}

		// 10. Emit ProcessEnd event
		if !yield(Event{
			Timestamp: endTime,
			Event: &ProcessEnd{
				ExitCode: int(exitCode),
				Duration: duration,
				Success:  exitCode == 0,
			},
		}, nil) {
			return
		}

		if cp.stats != nil {
			cp.stats.RecordEvent(ctx, "process_end")
		}

		// 11. Emit ContainerRemove event (cleanup completed)
		yield(newEvent(&ContainerRemove{
			ContainerID: containerID,
		}), nil)
	}
}

// createContainer creates a Docker/Podman container with the configured settings
func (cp *ContainerProcess) createContainer(ctx context.Context, dockerClient *client.Client) (string, error) {
	// Build command
	cmd := append([]string{cp.cmd}, cp.args...)

	// Container configuration
	config := &container.Config{
		Image:      cp.image,
		Cmd:        cmd,
		Env:        cp.env,
		WorkingDir: cp.workingDir,
		Tty:        false, // No TTY for MVP (can add WithContainerPTY later)
		OpenStdin:  false,
	}

	// Host configuration
	hostConfig := &container.HostConfig{
		AutoRemove: false, // We handle cleanup explicitly
	}

	// Add mounts
	for _, mount := range cp.mounts {
		bind := fmt.Sprintf("%s:%s", mount.Source, mount.Target)
		if mount.ReadOnly {
			bind += ":ro"
		}
		hostConfig.Binds = append(hostConfig.Binds, bind)
	}

	// Create container
	resp, err := dockerClient.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("container create failed: %w", err)
	}

	return resp.ID, nil
}

// streamContainerLogs streams container logs to the yield function
func (cp *ContainerProcess) streamContainerLogs(
	ctx context.Context,
	dockerClient *client.Client,
	containerID string,
	yield func(Event, error) bool,
	startTime time.Time,
	done chan<- error,
) {
	defer func() { done <- nil }()

	// Get container logs with follow
	logsOptions := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	}

	logsReader, err := dockerClient.ContainerLogs(ctx, containerID, logsOptions)
	if err != nil {
		yield(newEvent(&ProcessError{
			Error:   err,
			Message: "Failed to attach to container logs",
		}), nil)
		return
	}
	defer logsReader.Close()

	// Use BufferWriter for consistent buffering with Process
	flushChan := make(chan struct{}, 1)
	bufferWriter := NewBufferWriter(flushChan, cp.maxBufferSize)

	// Ticker for periodic flushing
	ticker := time.NewTicker(cp.flushInterval)
	defer ticker.Stop()

	// Demultiplex Docker's multiplexed stream
	// Docker uses 8-byte header: [STREAM_TYPE][0][0][0][SIZE1][SIZE2][SIZE3][SIZE4]
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	// Use errgroup to manage all copy goroutines
	g := &errgroup.Group{}

	// Start demuxing in background
	g.Go(func() error {
		defer stdoutWriter.Close()
		defer stderrWriter.Close()
		// Use Docker's stdcopy package for demultiplexing
		_, err := stdcopy.StdCopy(stdoutWriter, stderrWriter, logsReader)
		return err
	})

	// Read from both streams concurrently (not sequentially)
	// Each stream copies to the buffer writer independently
	g.Go(func() error {
		_, err := io.Copy(bufferWriter, stdoutReader)
		return err
	})
	g.Go(func() error {
		_, err := io.Copy(bufferWriter, stderrReader)
		return err
	})

	// Monitor when all io.Copy goroutines finish
	copyDone := make(chan error, 1)
	go func() {
		copyDone <- g.Wait()
	}()

	// Heartbeat tracking
	var hasEmittedEventsThisSecond bool

	// Main flushing loop (same pattern as Process)
	for {
		select {
		case <-ctx.Done():
			// Context cancelled - final flush before returning
			if data := bufferWriter.FlushAndClear(); data != nil {
				yield(newEvent(&OutputData{
					Data: data,
				}), nil)
			}
			return

		case <-copyDone:
			// All log streams finished (container stopped and logs fully read)
			// Do final flush and exit
			if data := bufferWriter.FlushAndClear(); data != nil {
				yield(newEvent(&OutputData{
					Data: data,
				}), nil)
			}
			return

		case <-ticker.C:
			// Time-based flush
			eventsEmitted := false

			if data := bufferWriter.FlushAndClear(); data != nil {
				if !yield(newEvent(&OutputData{
					Data: data,
				}), nil) {
					return
				}
				eventsEmitted = true

				if cp.stats != nil {
					cp.stats.RecordOutput(ctx, int64(len(data)))
				}
			}

			// Heartbeat if no output
			if !hasEmittedEventsThisSecond && !eventsEmitted {
				if !yield(newEvent(&HeartbeatEvent{
					ProcessAlive: true,
					ElapsedTime:  time.Since(startTime),
				}), nil) {
					return
				}
			}

			hasEmittedEventsThisSecond = eventsEmitted

		case <-flushChan:
			// Size-based flush
			if bufferWriter.Len() >= cp.maxBufferSize {
				if data := bufferWriter.FlushAndClear(); data != nil {
					if !yield(newEvent(&OutputData{
						Data: data,
					}), nil) {
						return
					}
					hasEmittedEventsThisSecond = true

					if cp.stats != nil {
						cp.stats.RecordOutput(ctx, int64(len(data)))
					}
				}
			}
		}
	}
}

// removeContainer removes a container with silent failure handling
func (cp *ContainerProcess) removeContainer(ctx context.Context, dockerClient *client.Client, containerID string) {
	// Silent cleanup with timeout (Drone pattern)
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	removeOptions := container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	}

	_ = dockerClient.ContainerRemove(cleanupCtx, containerID, removeOptions)
	// Errors are intentionally ignored - log removal failures are not critical
}
