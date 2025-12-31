# EXTRACTABLE SPEC: Container Support for console-stream

> **Note:** This section is a standalone specification that can be extracted and provided to the console-stream project maintainers. It proposes adding native container execution support to console-stream based on proven patterns from Drone CI.

---

## Specification: Container Execution Support for console-stream

**Version:** 1.0
**Date:** 2025-12-31
**Target Project:** [github.com/wolfeidau/console-stream](https://github.com/wolfeidau/console-stream)
**Proposed By:** airunner project

### Executive Summary

This specification proposes adding first-class container execution support to console-stream, enabling users to execute processes inside Docker/Podman containers while maintaining the same streaming event model that console-stream provides for direct process execution.

**Motivation:** Many automation systems (CI/CD, job runners, build systems) need to execute workloads in isolated containers. Currently, console-stream only supports direct process execution. Adding container support would:

1. Enable process isolation and sandboxing
2. Provide reproducible execution environments
3. Support ephemeral, immutable execution contexts
4. Eliminate the need for wrapper solutions (like `docker exec` wrapped in console-stream)

**Design Goal:** Extend console-stream with container execution that feels identical to current process execution from the API perspective.

---

### Research: Production Container Execution Patterns

#### Case Study: Drone CI

[Drone](https://drone.io) is a production-grade CI/CD system that executes millions of pipeline steps in containers. Their implementation provides valuable patterns for reliable container execution.

**Key Findings from [drone-runner-docker](https://github.com/drone-runners/drone-runner-docker):**

1. **Docker SDK over CLI**
   - Uses `github.com/docker/docker/client` (official Docker Go SDK)
   - No CLI shelling - direct API communication via Docker socket
   - Type-safe configuration with structured errors
   - Source: [engine/engine.go](https://github.com/drone-runners/drone-runner-docker/blob/master/engine/engine.go)

2. **Native Log Streaming**
   ```go
   func (e *Docker) tail(ctx context.Context, id string, output io.Writer) error {
       opts := types.ContainerLogsOptions{
           Follow:     true,
           ShowStdout: true,
           ShowStderr: true,
       }

       logs, err := e.client.ContainerLogs(ctx, id, opts)

       go func() {
           stdcopy.StdCopy(output, output, logs)
           logs.Close()
       }()
   }
   ```
   - Uses Docker's native `ContainerLogs` API with `Follow: true`
   - Properly demultiplexes stdout/stderr using `stdcopy.StdCopy`
   - Goroutine-based streaming for non-blocking operation

3. **Error Handling Patterns**
   - Typed error detection: `client.IsErrNotFound(err)`, `errdefs.IsConflict(err)`
   - Automatic image pulling on "image not found" errors
   - Silent cleanup failures (log but don't error on container removal)
   - Graceful degradation for operational resilience

4. **Container Lifecycle**
   - Create → Start → AttachLogs → Wait → Inspect → Remove
   - Explicit cleanup even on failures
   - Support for container health checks and retry logic

**Why This Matters:**

Drone's architecture proves that:
- Docker SDK is reliable at scale (millions of builds)
- Native log streaming is superior to CLI parsing
- Type-safe APIs reduce operational errors
- Container execution can be as reliable as direct process execution

**Additional Research:**

- **Jenkins Docker Plugin:** Uses Docker SDK with similar patterns
- **GitLab Runner:** Also uses Docker SDK for container execution
- **Podman Compatibility:** Docker SDK works with Podman via Docker-compatible socket (`/var/run/podman/podman.sock`)

---

### Proposed API Design

#### Current console-stream API (Direct Execution)

```go
// Current: Direct process execution
process := consolestream.NewProcess("go", []string{"test", "./..."},
    consolestream.WithPTYMode(),
    consolestream.WithEnvMap(env),
)

for event, err := range process.ExecuteAndStream(ctx) {
    switch e := event.Event.(type) {
    case *consolestream.ProcessStart:
        // Handle start
    case *consolestream.OutputData:
        // Handle output
    case *consolestream.ProcessEnd:
        // Handle completion
    }
}
```

#### Proposed: Container Execution (Same API Surface)

```go
// NEW: Container process execution
process := consolestream.NewContainerProcess("go", []string{"test", "./..."},
    // Container-specific options
    consolestream.WithContainerImage("golang:1.25"),
    consolestream.WithContainerRuntime("docker"), // or "podman"

    // Existing options still work
    consolestream.WithEnvMap(env),
    consolestream.WithWorkingDir("/workspace"),

    // Container-specific configurations
    consolestream.WithContainerMount("/host/path", "/container/path", false), // source, target, readonly
    consolestream.WithContainerMemoryLimit(536870912), // bytes
    consolestream.WithContainerCPULimit(500), // millicores
)

// SAME event streaming model
for event, err := range process.ExecuteAndStream(ctx) {
    switch e := event.Event.(type) {
    case *consolestream.ContainerCreate:
        // NEW: Container created
        fmt.Printf("Container ID: %s\n", e.ContainerID)
    case *consolestream.ProcessStart:
        // Same as before
    case *consolestream.OutputData:
        // Same as before - output streaming identical
    case *consolestream.ProcessEnd:
        // Same as before
    case *consolestream.ContainerRemove:
        // NEW: Container cleaned up
    }
}
```

**Key Design Principles:**

1. **API Consistency:** Container execution feels like direct execution
2. **Event Model Unchanged:** Same `ProcessStart`, `OutputData`, `ProcessEnd` events
3. **New Events for Container Lifecycle:** Additive, not breaking
4. **Option Pattern:** Container settings via functional options (consistent with current API)
5. **Runtime Agnostic:** Support Docker and Podman via same interface

---

### Implementation Architecture

#### Package Structure

```
console-stream/
├── process.go              # Existing: Direct process execution
├── container_process.go    # NEW: Container process execution
├── container_client.go     # NEW: Docker SDK client wrapper
├── events.go              # Update: Add container events
├── options.go             # Update: Add container options
└── internal/
    └── demux.go           # NEW: stdout/stderr demultiplexing
```

#### Core Types

```go
// container_process.go

type ContainerProcess struct {
    command      string
    args         []string
    image        string
    runtime      string // "docker" or "podman"

    // Container configuration
    mounts       []ContainerMount
    memoryLimit  int64
    cpuLimit     int64
    envVars      map[string]string
    workingDir   string
    networkMode  string
    privileged   bool

    // Docker SDK client
    client       *client.Client
    containerID  string
}

type ContainerMount struct {
    Source   string
    Target   string
    ReadOnly bool
}

// New container-specific events
type ContainerCreate struct {
    ContainerID string
    Image       string
    CreatedAt   time.Time
}

type ContainerRemove struct {
    ContainerID string
    Duration    time.Duration
}
```

#### Functional Options

```go
// options.go

func WithContainerImage(image string) ProcessOption {
    return func(p *Process) {
        if cp, ok := p.(*ContainerProcess); ok {
            cp.image = image
        }
    }
}

func WithContainerRuntime(runtime string) ProcessOption {
    return func(p *Process) {
        if cp, ok := p.(*ContainerProcess); ok {
            cp.runtime = runtime
        }
    }
}

func WithContainerMount(source, target string, readOnly bool) ProcessOption {
    return func(p *Process) {
        if cp, ok := p.(*ContainerProcess); ok {
            cp.mounts = append(cp.mounts, ContainerMount{source, target, readOnly})
        }
    }
}

func WithContainerMemoryLimit(bytes int64) ProcessOption {
    return func(p *Process) {
        if cp, ok := p.(*ContainerProcess); ok {
            cp.memoryLimit = bytes
        }
    }
}

func WithContainerCPULimit(millicores int64) ProcessOption {
    return func(p *Process) {
        if cp, ok := p.(*ContainerProcess); ok {
            cp.cpuLimit = millicores
        }
    }
}
```

#### Container Execution Flow

```go
// container_process.go

func (cp *ContainerProcess) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error] {
    return func(yield func(Event, error) bool) {
        // 1. Initialize Docker/Podman client
        client, err := cp.initClient()
        if err != nil {
            yield(Event{}, fmt.Errorf("failed to init container runtime: %w", err))
            return
        }
        defer client.Close()

        // 2. Create container
        containerID, err := cp.createContainer(ctx, client)
        if err != nil {
            yield(Event{}, fmt.Errorf("failed to create container: %w", err))
            return
        }

        // Ensure cleanup
        defer cp.removeContainer(ctx, client, containerID)

        // Emit ContainerCreate event
        if !yield(Event{Event: &ContainerCreate{ContainerID: containerID}}, nil) {
            return
        }

        // 3. Start container
        if err := client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
            yield(Event{}, fmt.Errorf("failed to start container: %w", err))
            return
        }

        // 4. Stream logs
        cp.streamLogs(ctx, client, containerID, yield)

        // 5. Wait for container completion
        statusCh, errCh := client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

        var exitCode int64
        select {
        case result := <-statusCh:
            exitCode = result.StatusCode
        case err := <-errCh:
            yield(Event{}, fmt.Errorf("container wait error: %w", err))
            return
        }

        // 6. Emit ProcessEnd event
        yield(Event{Event: &ProcessEnd{ExitCode: int(exitCode)}}, nil)
    }
}

func (cp *ContainerProcess) createContainer(ctx context.Context, client *client.Client) (string, error) {
    // Build container configuration (modeled after Drone)
    config := &container.Config{
        Image:      cp.image,
        Cmd:        append([]string{cp.command}, cp.args...),
        Env:        mapToEnvSlice(cp.envVars),
        WorkingDir: cp.workingDir,
    }

    hostConfig := &container.HostConfig{
        AutoRemove: false, // We control cleanup
    }

    // Resource limits
    if cp.memoryLimit > 0 {
        hostConfig.Resources.Memory = cp.memoryLimit
    }
    if cp.cpuLimit > 0 {
        cpus := float64(cp.cpuLimit) / 1000.0
        hostConfig.Resources.NanoCPUs = int64(cpus * 1e9)
    }

    // Volume mounts
    for _, mount := range cp.mounts {
        bind := fmt.Sprintf("%s:%s", mount.Source, mount.Target)
        if mount.ReadOnly {
            bind += ":ro"
        }
        hostConfig.Binds = append(hostConfig.Binds, bind)
    }

    // Create container
    resp, err := client.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
    if err != nil {
        // Check if image needs pulling
        if client.IsErrNotFound(err) {
            if err := cp.pullImage(ctx, client); err != nil {
                return "", fmt.Errorf("failed to pull image: %w", err)
            }
            // Retry create after pull
            resp, err = client.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
        }
    }

    return resp.ID, err
}

func (cp *ContainerProcess) streamLogs(ctx context.Context, client *client.Client, containerID string, yield func(Event, error) bool) {
    opts := container.LogsOptions{
        ShowStdout: true,
        ShowStderr: true,
        Follow:     true,
        Timestamps: false,
    }

    logs, err := client.ContainerLogs(ctx, containerID, opts)
    if err != nil {
        yield(Event{}, fmt.Errorf("failed to attach logs: %w", err))
        return
    }
    defer logs.Close()

    // Demultiplex stdout/stderr
    // Docker uses a multiplexed stream format - see docker/pkg/stdcopy
    stdoutReader, stderrReader := io.Pipe()

    go func() {
        stdcopy.StdCopy(stdoutReader, stderrReader, logs)
        stdoutReader.Close()
        stderrReader.Close()
    }()

    // Stream stdout
    go cp.streamOutput(stdoutReader, yield)

    // Stream stderr
    go cp.streamOutput(stderrReader, yield)
}

func (cp *ContainerProcess) streamOutput(reader io.Reader, yield func(Event, error) bool) {
    buf := make([]byte, 4096)
    for {
        n, err := reader.Read(buf)
        if n > 0 {
            data := make([]byte, n)
            copy(data, buf[:n])

            if !yield(Event{Event: &OutputData{Data: data}}, nil) {
                return
            }
        }

        if err != nil {
            if err != io.EOF {
                yield(Event{}, err)
            }
            return
        }
    }
}

func (cp *ContainerProcess) removeContainer(ctx context.Context, client *client.Client, containerID string) {
    // Silent cleanup (Drone pattern - don't fail on cleanup errors)
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    client.ContainerRemove(ctx, containerID, container.RemoveOptions{
        Force:         true,
        RemoveVolumes: true,
    })
}
```

---

### Docker SDK Integration

#### Dependencies

```go
// go.mod additions
require (
    github.com/docker/docker v24.0.7+incompatible
    github.com/docker/go-connections v0.4.0
)
```

#### Client Initialization

```go
// container_client.go

func (cp *ContainerProcess) initClient() (*client.Client, error) {
    var opts []client.Opt

    // Determine runtime endpoint
    switch cp.runtime {
    case "podman":
        opts = append(opts, client.WithHost("unix:///var/run/podman/podman.sock"))
    case "docker", "":
        // Use default Docker socket
        opts = append(opts, client.FromEnv)
    default:
        return nil, fmt.Errorf("unsupported container runtime: %s", cp.runtime)
    }

    opts = append(opts, client.WithAPIVersionNegotiation())

    return client.NewClientWithOpts(opts...)
}
```

#### Error Handling (Drone Patterns)

```go
func (cp *ContainerProcess) createContainer(...) (string, error) {
    resp, err := client.ContainerCreate(ctx, config, hostConfig, nil, nil, "")

    if err != nil {
        // Pattern 1: Auto-pull missing images
        if client.IsErrNotFound(err) {
            if pullErr := cp.pullImage(ctx, client); pullErr != nil {
                return "", fmt.Errorf("image pull failed: %w", pullErr)
            }
            // Retry
            resp, err = client.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
        }

        // Pattern 2: Detect conflict errors
        if errdefs.IsConflict(err) {
            return "", fmt.Errorf("container name conflict: %w", err)
        }
    }

    return resp.ID, err
}

func (cp *ContainerProcess) pullImage(ctx context.Context, client *client.Client) error {
    reader, err := client.ImagePull(ctx, cp.image, image.PullOptions{})
    if err != nil {
        return err
    }
    defer reader.Close()

    // Consume pull output (Docker requires reading the response)
    io.Copy(io.Discard, reader)
    return nil
}
```

---

### Usage Examples

#### Example 1: Simple Container Execution

```go
package main

import (
    "context"
    "fmt"
    consolestream "github.com/wolfeidau/console-stream"
)

func main() {
    process := consolestream.NewContainerProcess("go", []string{"version"},
        consolestream.WithContainerImage("golang:1.25"),
        consolestream.WithContainerRuntime("docker"),
    )

    for event, err := range process.ExecuteAndStream(context.Background()) {
        if err != nil {
            fmt.Printf("Error: %v\n", err)
            break
        }

        switch e := event.Event.(type) {
        case *consolestream.ContainerCreate:
            fmt.Printf("Container created: %s\n", e.ContainerID)
        case *consolestream.OutputData:
            fmt.Print(string(e.Data))
        case *consolestream.ProcessEnd:
            fmt.Printf("Exit code: %d\n", e.ExitCode)
        }
    }
}
```

#### Example 2: Container with Volume Mounts (airunner use case)

```go
// After git clone, mount code into container
clonedPath := "/tmp/airunner-abc123/myrepo"

process := consolestream.NewContainerProcess("make", []string{"test"},
    consolestream.WithContainerImage("golang:1.25"),
    consolestream.WithContainerMount(clonedPath, "/workspace", false), // rw mount
    consolestream.WithWorkingDir("/workspace"),
    consolestream.WithEnvMap(map[string]string{
        "CI": "true",
        "GOPATH": "/go",
    }),
    consolestream.WithContainerMemoryLimit(1073741824), // 1GB
    consolestream.WithContainerCPULimit(1000), // 1 CPU
)

for event, err := range process.ExecuteAndStream(ctx) {
    // Handle events...
}
```

#### Example 3: Podman Support

```go
process := consolestream.NewContainerProcess("npm", []string{"test"},
    consolestream.WithContainerImage("node:20"),
    consolestream.WithContainerRuntime("podman"), // Use Podman instead of Docker
)
```

---

### Testing Strategy

#### Unit Tests

```go
// container_process_test.go

func TestContainerProcess_ExecuteAndStream(t *testing.T) {
    // Requires Docker daemon running
    if testing.Short() {
        t.Skip("Skipping container test in short mode")
    }

    process := consolestream.NewContainerProcess("echo", []string{"hello"},
        consolestream.WithContainerImage("alpine:latest"),
    )

    var output string
    var exitCode int

    for event, err := range process.ExecuteAndStream(context.Background()) {
        require.NoError(t, err)

        switch e := event.Event.(type) {
        case *consolestream.OutputData:
            output += string(e.Data)
        case *consolestream.ProcessEnd:
            exitCode = e.ExitCode
        }
    }

    assert.Equal(t, "hello\n", output)
    assert.Equal(t, 0, exitCode)
}
```

#### Integration Tests

1. **Container lifecycle** - Create, start, stream, cleanup
2. **Volume mounts** - Verify mounted files accessible in container
3. **Resource limits** - Verify memory/CPU limits applied
4. **Error handling** - Missing image, invalid config, runtime errors
5. **Podman compatibility** - Test with Podman socket
6. **Long-running processes** - Streaming large outputs
7. **Concurrent containers** - Multiple containers simultaneously

---

### Backward Compatibility

**100% backward compatible:**

- Existing `NewProcess()` API unchanged
- New `NewContainerProcess()` is additive
- All existing options still work
- No breaking changes to event types

**Migration path:**

```go
// Before: Direct execution
process := consolestream.NewProcess("go", args, opts...)

// After: Container execution (minimal change)
process := consolestream.NewContainerProcess("go", args,
    consolestream.WithContainerImage("golang:1.25"),
    opts..., // Existing options still work
)
```

---

### Security Considerations

1. **Container Isolation**
   - Default: Non-privileged mode
   - Network isolation via bridge mode
   - Filesystem isolation via mount namespaces

2. **Resource Limits**
   - Memory limits prevent OOM on host
   - CPU limits prevent resource starvation
   - Recommended defaults: 1GB memory, 1 CPU

3. **Image Security**
   - Verify image digests when possible
   - Support private registries with authentication
   - Consider image scanning integration hooks

4. **Volume Mounts**
   - Default to read-only mounts where possible
   - Validate mount paths (prevent escape)
   - Document security implications

---

### Performance Considerations

**Based on Drone experience:**

1. **Container Creation Overhead:** ~200-500ms per container
2. **Image Pull Overhead:** Cached images ~0ms, uncached varies by size
3. **Log Streaming:** Minimal overhead with native Docker SDK
4. **Cleanup:** ~100-200ms per container

**Optimizations:**

- Image caching (Docker/Podman handle automatically)
- Reuse container runtime client (don't recreate per container)
- Parallel cleanup (goroutines)
- Stream buffering (existing console-stream patterns)

---

### Open Questions for console-stream Maintainers

1. **API Design:**
   - Is `NewContainerProcess()` the right factory name?
   - Should container and process share a common interface?
   - Alternative: `NewProcess()` with `WithContainer()` option?

2. **Event Model:**
   - Should `ContainerCreate`/`ContainerRemove` be top-level events?
   - Or nested under a `ContainerEvent` wrapper?

3. **Error Handling:**
   - Follow Drone's "silent cleanup" pattern?
   - Or return cleanup errors?

4. **Runtime Detection:**
   - Auto-detect Docker vs Podman?
   - Or require explicit `WithContainerRuntime()`?

5. **Image Pulling:**
   - Emit progress events during image pull?
   - Or silent background pull?

6. **PTY Mode:**
   - Should containers support PTY mode?
   - Docker supports it via `Config.Tty = true` and `Config.AttachStdin`

---

### Dependencies

```go
require (
    github.com/docker/docker v24.0.7+incompatible
    github.com/docker/go-connections v0.4.0
    github.com/moby/term v0.5.0 // For stdcopy
)
```

---

### References

1. **Drone CI:**
   - [drone-runner-docker source](https://github.com/drone-runners/drone-runner-docker)
   - [Engine implementation](https://github.com/drone-runners/drone-runner-docker/blob/master/engine/engine.go)
   - [Documentation](https://docs.drone.io/runner/docker/overview/)

2. **Docker SDK:**
   - [Official Go client](https://pkg.go.dev/github.com/docker/docker/client)
   - [API reference](https://docs.docker.com/engine/api/sdk/)
   - [Examples](https://docs.docker.com/engine/api/sdk/examples/)

3. **Podman Compatibility:**
   - [Docker-compatible API](https://docs.podman.io/en/latest/markdown/podman-system-service.1.html)
   - [Socket location](https://github.com/containers/podman/blob/main/docs/tutorials/socket_activation.md)

---

### Next Steps

1. **Review this specification** with console-stream maintainers
2. **Prototype the API** - Validate design feels natural
3. **Implement core functionality** - Container lifecycle, log streaming
4. **Add tests** - Unit and integration with Docker/Podman
5. **Document** - Update README with container examples
6. **Release** - Semantic versioning (v2.0.0 if breaking, v1.x.0 if additive)

---

**End of Extractable Specification**

