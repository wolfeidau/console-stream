package consolestream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
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
	flushInterval time.Duration
	maxBufferSize int

	// State
	stats *ProcessStats
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
		flushInterval: cfg.flushInterval,
		maxBufferSize: cfg.maxBufferSize,
	}

	// Initialize stats if meter provided (graceful degradation)
	if cfg.meter != nil {
		processInfo := ProcessInfo{
			Command: cmd,
			Args:    args,
			// PID will be set later when container starts
		}

		stats, err := NewProcessStats(cfg.meter, processInfo)
		if err == nil {
			process.stats = stats
		}
		// If err != nil, we simply continue without stats (graceful degradation)
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
					if errors.Is(pullErr, errConsumerStopped) {
						return
					}
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

		// 5. Initialize stats if metrics enabled
		// Only inspect container for PID if we need it for metrics
		pid := 0
		if cp.stats != nil {
			inspect, err := dockerClient.ContainerInspect(ctx, containerID)
			if err == nil && inspect.State != nil {
				pid = inspect.State.Pid
				cp.stats.processInfo.PID = pid
			}
			cp.stats.Start(ctx)
		}

		// runCtx bounds the background Docker calls; cancelled on every return path.
		runCtx, cancelRun := context.WithCancel(ctx)
		defer cancelRun()

		flush := make(chan struct{}, 1)
		done := make(chan error, 1)
		exited := make(chan struct{}) // closed once the wait result is sent
		w := NewBufferWriter(flush, cp.maxBufferSize)

		// Log goroutine: demultiplexes straight into the buffer; never yields.
		logsDone := make(chan struct{})
		var logErr error // written before logsDone is closed
		go func() {
			defer close(logsDone)
			reader, err := dockerClient.ContainerLogs(runCtx, containerID, container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
				Follow:     true,
			})
			if err != nil {
				logErr = err
				return
			}
			defer reader.Close()
			if _, err := stdcopy.StdCopy(w, w, reader); err != nil && runCtx.Err() == nil {
				logErr = err
			}
		}()

		// Waiter goroutine: reports the exit once all logs are in the buffer.
		waitCh, errCh := dockerClient.ContainerWait(runCtx, containerID, container.WaitConditionNotRunning)
		go func() {
			defer close(exited)
			var result error
			select {
			case res := <-waitCh:
				<-logsDone
				result = &containerExit{code: res.StatusCode, logErr: logErr}
			case err := <-errCh:
				<-logsDone
				result = err
			}
			done <- result
		}()

		// stop terminates the container and drains the background goroutines.
		stop := func() {
			stopTimeout := 2
			_ = dockerClient.ContainerStop(context.WithoutCancel(ctx), containerID, container.StopOptions{Timeout: &stopTimeout})
			cancelRun()
			<-exited
		}

		// 6. Emit ProcessStart event (reuse existing event type)
		if !yield(Event{
			Timestamp: processStart,
			Event: &ProcessStart{
				PID:     pid,
				Command: cp.cmd,
				Args:    cp.args,
			},
		}, nil) {
			stop()
			return
		}

		if cp.stats != nil {
			cp.stats.RecordEvent(ctx, "process_start")
		}

		// 7. Stream output until the container exits
		endTime, ok, waitErr := streamOutput(ctx, yield, w, flush, done, processStart, cp.flushInterval, cp.maxBufferSize, cp.stats)
		if !ok {
			stop()
			return
		}

		exit, isExit := waitErr.(*containerExit)
		if !isExit {
			if cp.stats != nil {
				cp.stats.RecordError(ctx, "container_wait_error")
			}
			yield(Event{Timestamp: endTime, Event: &ProcessError{
				Error:   waitErr,
				Message: "Container wait failed",
			}}, nil)
			return
		}

		if exit.logErr != nil {
			if !yield(Event{Timestamp: endTime, Event: &ProcessError{
				Error:   exit.logErr,
				Message: "Failed to stream container logs",
			}}, nil) {
				return
			}
		}

		// 8. Update metrics
		if cp.stats != nil {
			cp.stats.Stop(ctx)
			cp.stats.SetExitCode(int(exit.code))
		}

		// 9. Emit ProcessEnd event
		if !yield(Event{
			Timestamp: endTime,
			Event: &ProcessEnd{
				ExitCode: int(exit.code),
				Duration: endTime.Sub(processStart),
				Success:  exit.code == 0,
			},
		}, nil) {
			return
		}

		if cp.stats != nil {
			cp.stats.RecordEvent(ctx, "process_end")
		}

		// 10. Emit ContainerRemove event (cleanup completed)
		yield(newEvent(&ContainerRemove{
			ContainerID: containerID,
		}), nil)
	}
}

// containerExit carries the container exit status over the done channel.
type containerExit struct {
	code   int64
	logErr error // log attach/stream failure, if any
}

func (e *containerExit) Error() string { return fmt.Sprintf("container exited with code %d", e.code) }

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

	// Add mounts with validation
	for _, mount := range cp.mounts {
		// Validate source path exists
		if _, err := os.Stat(mount.Source); err != nil {
			return "", fmt.Errorf("mount source %s does not exist: %w", mount.Source, err)
		}

		// Validate target path is absolute
		if !filepath.IsAbs(mount.Target) {
			return "", fmt.Errorf("mount target must be absolute path, got: %s", mount.Target)
		}

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

// errConsumerStopped signals that the consumer stopped iterating.
var errConsumerStopped = errors.New("consumer stopped iterating")

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
		return errConsumerStopped
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
				return errConsumerStopped
			}

			lastProgressEmit = now
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading pull progress: %w", err)
	}

	// Emit completion event
	if !yield(newEvent(&ImagePullComplete{
		Image:  cp.image,
		Digest: digest,
	}), nil) {
		return errConsumerStopped
	}

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
