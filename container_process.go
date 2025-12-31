package consolestream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
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

		// 2. Create container (with automatic image pull if needed)
		containerID, err := cp.createContainer(ctx, dockerClient)
		if err != nil {
			// Check if error is due to missing image
			if cerrdefs.IsNotFound(err) {
				// Pull the image with progress tracking
				if pullErr := cp.pullImageWithProgress(ctx, dockerClient, yield); pullErr != nil {
					yield(newEvent(&ProcessError{
						Error:   pullErr,
						Message: "Failed to pull container image",
					}), ProcessStartError{Cmd: cp.cmd, Err: pullErr})
					return
				}

				// Retry container creation after successful pull
				containerID, err = cp.createContainer(ctx, dockerClient)
				if err != nil {
					yield(newEvent(&ProcessError{
						Error:   err,
						Message: "Failed to create container after image pull",
					}), ProcessStartError{Cmd: cp.cmd, Err: err})
					return
				}
			} else {
				yield(newEvent(&ProcessError{
					Error:   err,
					Message: "Failed to create container",
				}), ProcessStartError{Cmd: cp.cmd, Err: err})
				return
			}
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

// pullProgress represents a single line of JSON progress from Docker's ImagePull
type pullProgress struct {
	Status         string `json:"status"`
	ID             string `json:"id,omitempty"`
	Progress       string `json:"progress,omitempty"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail,omitempty"`
	Error string `json:"error,omitempty"`
}

// pullImageWithProgress pulls a Docker image and emits progress events
func (cp *ContainerProcess) pullImageWithProgress(
	ctx context.Context,
	dockerClient *client.Client,
	yield func(Event, error) bool,
) error {
	// Emit pull start event
	if !yield(newEvent(&ImagePullStart{
		Image: cp.image,
	}), nil) {
		return fmt.Errorf("cancelled during image pull start")
	}

	// Pull the image
	pullReader, err := dockerClient.ImagePull(ctx, cp.image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", cp.image, err)
	}
	defer pullReader.Close()

	// Track overall progress across all layers
	layerProgress := make(map[string]*pullProgress)
	var lastProgressEmit time.Time
	var digest string

	scanner := bufio.NewScanner(pullReader)
	for scanner.Scan() {
		var progress pullProgress
		if err := json.Unmarshal(scanner.Bytes(), &progress); err != nil {
			continue // Skip malformed lines
		}

		// Check for errors
		if progress.Error != "" {
			return fmt.Errorf("image pull failed: %s", progress.Error)
		}

		// Track layer progress
		if progress.ID != "" && progress.ProgressDetail.Total > 0 {
			layerProgress[progress.ID] = &progress
		}

		// Extract digest from final status message
		if strings.HasPrefix(progress.Status, "Digest: sha256:") {
			digest = strings.TrimPrefix(progress.Status, "Digest: ")
		}

		// Emit progress events periodically (every 2 seconds or 10% progress change)
		now := time.Now()
		if now.Sub(lastProgressEmit) >= 2*time.Second {
			totalBytes := int64(0)
			currentBytes := int64(0)

			for _, layer := range layerProgress {
				totalBytes += layer.ProgressDetail.Total
				currentBytes += layer.ProgressDetail.Current
			}

			percentComplete := 0
			if totalBytes > 0 {
				percentComplete = int((currentBytes * 100) / totalBytes)
			}

			if !yield(newEvent(&ImagePullProgress{
				Image:           cp.image,
				Status:          progress.Status,
				PercentComplete: percentComplete,
				BytesDownloaded: currentBytes,
				BytesTotal:      totalBytes,
			}), nil) {
				return fmt.Errorf("cancelled during image pull")
			}

			lastProgressEmit = now
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading pull progress: %w", err)
	}

	// Emit completion event
	yield(newEvent(&ImagePullComplete{
		Image:  cp.image,
		Digest: digest,
	}), nil)

	return nil
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
