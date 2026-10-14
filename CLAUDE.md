# Console Stream

A Go library for executing processes and streaming their output in real-time using Go iterators (`iter.Seq2`).

## Features

- **Real-time streaming**: Stream process output in 1-second intervals or when buffers reach 10MB
- **Iterator interface**: Uses `iter.Seq2[Event, error]` for clean consumption
- **Event-driven architecture**: Process lifecycle events, output data, and heartbeat monitoring
- **PTY support**: Run processes in pseudo-terminals to capture interactive output, ANSI escape sequences, and progress bars
- **Pluggable cancellation**: `Cancellor` interface; the default stops the whole process group (SIGTERM, then SIGKILL)
- **Container mode**: Run commands in Docker/Podman containers with image pull progress
- **Asciicast**: Convert PTY streams to asciicast v3
- **Buffer management**: Automatic flushing with configurable size limits
- **Context support**: Full context cancellation with graceful cleanup

## Usage

### Basic Example

```go
package main

import (
    "context"
    "fmt"
    "os"
    "os/signal"
    "syscall"

    consolestream "github.com/wolfeidau/console-stream"
)

func main() {
    // Children do not receive Ctrl-C; cancel ctx on SIGINT/SIGTERM instead.
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    process := consolestream.NewProcess("echo", []string{"Hello, World!"})

    for event, err := range process.ExecuteAndStream(ctx) {
        if err != nil {
            fmt.Printf("Error: %v\n", err)
            break
        }

        switch e := event.Event.(type) {
        case *consolestream.ProcessStart:
            fmt.Printf("Process started (PID: %d)\n", e.PID)
        case *consolestream.OutputData:
            fmt.Printf("[%s] %s", event.Timestamp.Format("15:04:05"), e.Data)
        case *consolestream.HeartbeatEvent:
            fmt.Printf("alive after %s\n", e.ElapsedTime)
        case *consolestream.ProcessEnd:
            fmt.Printf("Process completed (Exit Code: %d)\n", e.ExitCode)
        case *consolestream.ProcessError:
            fmt.Printf("Process error: %s\n", e.Message)
        }
    }
}
```

### PTY Example

```go
// PTY mode captures interactive output, ANSI escape sequences and progress bars.
process := consolestream.NewProcess("npm", []string{"install", "--progress"},
    consolestream.WithPTYMode(),
    consolestream.WithPTYSize(&pty.Winsize{Rows: 24, Cols: 80}), // github.com/creack/pty
    consolestream.WithEnvVar("NODE_ENV", "production"))

for event, err := range process.ExecuteAndStream(ctx) {
    if err != nil {
        break
    }
    if e, ok := event.Event.(*consolestream.OutputData); ok {
        // Raw terminal output with ANSI escape sequences preserved
        fmt.Printf("[PTY] %q\n", e.Data)
    }
}
```

### Container Example

```go
process := consolestream.NewContainerProcess("make", []string{"test"},
    consolestream.WithContainerImage("golang:1.25"),
    consolestream.WithContainerMount("/path/to/code", "/workspace", false),
    consolestream.WithContainerWorkingDir("/workspace"),
    consolestream.WithContainerEnvMap(map[string]string{"CI": "true"}))

for event, err := range process.ExecuteAndStream(ctx) {
    if err != nil {
        break
    }
    switch e := event.Event.(type) {
    case *consolestream.ImagePullProgress:
        fmt.Printf("pull %s: %d%%\n", e.Image, e.PercentComplete)
    case *consolestream.OutputData:
        fmt.Print(string(e.Data))
    }
}
```

### Asciicast Example

```go
process := consolestream.NewProcess("bash", []string{"-c", "ls --color"}, consolestream.WithPTYMode())
metadata := consolestream.AscicastV3Metadata{
    Term:    consolestream.TermInfo{Cols: 80, Rows: 24},
    Command: "ls --color",
}

f, _ := os.Create("session.cast")
defer f.Close()

err := consolestream.WriteAscicastV3(process.ExecuteAndStream(ctx), metadata, func(b []byte) error {
    _, err := f.Write(b)
    return err
})
```

## API Reference

### Process Functions and Options

```go
// NewProcess creates a process. Default mode is pipe.
func NewProcess(cmd string, args []string, opts ...ProcessOption) *Process

// ExecuteAndStream starts the process and returns an iterator over events.
func (p *Process) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error]
func (p *Process) GetStats() ProcessStatsSnapshot

func WithPipeMode() ProcessOption                     // stdout+stderr over one pipe (default)
func WithPTYMode() ProcessOption                      // pseudo-terminal
func WithPTYSize(size *pty.Winsize) ProcessOption     // PTY dimensions (github.com/creack/pty)
func WithCancellor(c Cancellor) ProcessOption         // default: NewLocalCancellor(5s)
func WithEnv(env []string) ProcessOption              // "KEY=value" entries
func WithEnvVar(key, value string) ProcessOption
func WithEnvMap(m map[string]string) ProcessOption
func WithWorkingDir(dir string) ProcessOption
func WithFlushInterval(d time.Duration) ProcessOption // default 1s
func WithMaxBufferSize(size int) ProcessOption        // default 10MB
func WithMeter(meter metric.Meter) ProcessOption      // OpenTelemetry metrics
```

### Container Functions and Options

```go
func NewContainerProcess(cmd string, args []string, opts ...ContainerProcessOption) *ContainerProcess
func (cp *ContainerProcess) ExecuteAndStream(ctx context.Context) iter.Seq2[Event, error]

func WithContainerImage(image string) ContainerProcessOption // required
func WithContainerRuntime(runtime string) ContainerProcessOption // "docker" (default) or "podman"
func WithContainerMount(source, target string, readOnly bool) ContainerProcessOption
func WithContainerEnv(env []string) ContainerProcessOption
func WithContainerEnvMap(envMap map[string]string) ContainerProcessOption
func WithContainerWorkingDir(dir string) ContainerProcessOption
func WithContainerFlushInterval(interval time.Duration) ContainerProcessOption
func WithContainerMaxBufferSize(size int) ContainerProcessOption
func WithContainerMeter(meter metric.Meter) ContainerProcessOption
```

There is no container Cancellor option: cancellation stops the container via the runtime.

### Events

```go
type Event struct {
    Timestamp time.Time
    Event     StreamEvent
}
func (e Event) EventType() EventType

type StreamEvent interface {
    Type() EventType
    String() string
}

const (
    ProcessStartEvent EventType = iota
    ProcessEndEvent
    ProcessErrorEvent
    HeartbeatEventType
    OutputEvent
    TerminalResizeEventType
    ContainerCreateEvent
    ContainerRemoveEvent
    ImagePullStartEvent
    ImagePullProgressEvent
    ImagePullCompleteEvent
)
```

| Event struct | Fields | Notes |
|--------------|--------|-------|
| `ProcessStart` | `PID, Command, Args` | |
| `OutputData` | `Data []byte` | Combined stdout/stderr (pipe) or PTY output |
| `HeartbeatEvent` | `ProcessAlive, ElapsedTime` | Silent process keep-alive |
| `ProcessEnd` | `ExitCode, Duration, Success` | A non-zero exit is a `ProcessEnd` with `Success == false`, not an error. A signal-terminated process reports `128 + signal` (137 for SIGKILL), matching shells and container mode |
| `ProcessError` | `Error, Message` | Failure that is not a non-zero exit |
| `TerminalResizeEvent` | `Rows, Cols, X, Y` | PTY |
| `ContainerCreate` / `ContainerRemove` | `ContainerID, Image` / `ContainerID` | Container mode |
| `ImagePullStart` / `ImagePullProgress` / `ImagePullComplete` | `Image` / `Image, Status, PercentComplete, BytesDownloaded, BytesTotal` / `Image, Digest` | Container mode |

Helpers: `IsOutputEvent`, `IsHeartbeatEvent`, `IsProcessStartEvent`, `IsProcessEndEvent`, `IsProcessErrorEvent`, `IsProcessLifecycleEvent`, `IsTerminalResizeEvent`, `IsContainerCreateEvent`, `IsContainerRemoveEvent`.

### Cancellor

```go
type Cancellor interface {
    Cancel(ctx context.Context, pid int) error
}

// SIGTERM to the process group, SIGKILL after terminateTimeout or ctx done.
func NewLocalCancellor(terminateTimeout time.Duration) *LocalCancellor
```

### Asciicast

```go
func ToAscicastV3(events iter.Seq2[Event, error], metadata AscicastV3Metadata) iter.Seq2[AscicastV3Line, error]
func WriteAscicastV3(events iter.Seq2[Event, error], metadata AscicastV3Metadata, writeFunc func([]byte) error) error
func NewOutputEvent(interval float64, data string) AscicastV3Event
func NewResizeEvent(interval float64, cols, rows uint16) AscicastV3Event
func NewExitEvent(interval float64, exitCode int) AscicastV3Event
```

### Errors

```go
// Yielded as the iterator error when the process or container cannot start.
type ProcessStartError struct {
    Cmd string
    Err error
}
```

Runtime failures after start are reported as `ProcessError` events; non-zero exits as `ProcessEnd`.

## Behavior

### Streaming Rules

1. **Time-based flushing**: Buffers flush every flush interval (default 1s) if they contain data
2. **Size-based flushing**: Buffers flush immediately at the max buffer size (default 10MB)
3. **Final flushing**: Remaining data is flushed when the process exits
4. **Combined output**: In pipe mode stdout and stderr share one pipe, so relative order is preserved and they cannot be told apart. A single `OutputData` event type carries both.
5. **Not line-buffered**: Events may split mid-line; partial lines stream on the flush interval. Consumers that need lines must reassemble them.
6. **Heartbeat**: a `HeartbeatEvent` is emitted on a tick when there was no output in the previous tick and none in the current one.

### Process Lifecycle

1. `ProcessStart` is yielded with the PID
2. Output is read into a `BufferWriter`; `OutputData` and `HeartbeatEvent` are yielded on flush ticks or size triggers
3. On exit, final output is flushed, then `ProcessEnd` (or `ProcessError` for non-exit failures)
4. Breaking out of the range loop or cancelling ctx stops the child via the Cancellor (SIGTERM to the process group, SIGKILL after the timeout, which is honoured on ctx cancel too) and waits for all goroutines before the iterator returns. A reaped pid is never signalled.
5. `ExecuteAndStream` keeps per-run state locally, so concurrent calls on one `Process` are safe.

### Process Groups and Signals

- Pipe children start with `Setpgid`; PTY children with `Setsid`. They do not receive terminal-generated signals (Ctrl-C). Callers should use `signal.NotifyContext` for SIGINT/SIGTERM and cancel the ctx.
- `LocalCancellor` signals the group (`kill(-pid)`), falling back to the bare pid for non-leaders. The group is also signalled after the leader is reaped, so grandchildren holding the pipe/tty are terminated on early break or cancel.
- Known limits: PTY children that create job-control subgroups (e.g. interactive shells) have descendants outside the group that are not reached; grandchildren that daemonize are not killed after a normal child exit.

### Container Lifecycle

Event order: `ContainerCreate → ProcessStart → OutputData/Heartbeat → ProcessEnd | ProcessError → ContainerRemove`. If the image is missing, `ImagePullStart → ImagePullProgress (every 2s) → ImagePullComplete` precede creation. Breaking out of the loop or cancelling ctx stops the container (ContainerStop), drains goroutines, then force-removes it. Only the iterator goroutine yields events. Container logs are demultiplexed into the same combined output stream.

## Testing

The library includes a comprehensive test program at `cmd/tester/main.go` with various output patterns:

```bash
# Normal streaming
go run cmd/tester/main.go --stdout-rate=5 --duration=10s

# Test 10MB buffer limit
go run cmd/tester/main.go --burst-mb=15

# Mixed output streams
go run cmd/tester/main.go --mixed-output --stdout-rate=10 --stderr-rate=5

# Different exit codes
go run cmd/tester/main.go --duration=2s --exit-code=1
```

## Development

### Breaking Changes Policy

**Pre-1.0 Development**: This library is currently in pre-1.0 development phase, which means:

- **Breaking changes are acceptable** and expected as the API evolves
- **All breaking changes must be clearly documented** in commit messages and release notes
- **Major API changes should be discussed** before implementation when possible
- **Semantic versioning** will be followed: breaking changes increment the minor version (0.x.0)

**Post-1.0 Promise**: Once version 1.0 is released, breaking changes will follow semantic versioning (major version increments) and include migration guides.

### Documentation Style

When documenting this project or updating the README, follow Amazon engineering documentation principles:

- **Customer-obsessed**: Start with what the customer (developer) wants to achieve
- **Work backwards from the customer**: Begin with use cases, then explain implementation
- **Be precise and unambiguous**: Use specific technical language and avoid vague terms
- **Provide complete examples**: Include runnable code snippets that demonstrate real scenarios
- **Operational excellence**: Document error handling, monitoring, and troubleshooting
- **Mechanism over good intentions**: Explain how things work, not just what they do
- **Think big, start simple**: Show the simplest working example first, then build complexity
- **Ownership mindset**: Include maintenance considerations and future extensibility points

Documentation should answer: "What problem does this solve?", "How do I use it?", "What can go wrong?", and "How do I extend it?"

### Code Quality Tools

This project uses the following linters and tools:

- **golangci-lint**: Comprehensive Go linter suite
  ```bash
  golangci-lint run ./...
  golangci-lint run --fix ./...
  ```

- **gopls**: Go language server for additional checks
  ```bash
  gopls check console.go
  ```

- **Standard Go tools**:
  ```bash
  go build ./...
  go vet ./...
  go mod tidy
  ```

### Testing Standards

This project uses **testify/require** for all unit tests to ensure clear, readable assertions and proper test failures.

**Required testing dependencies:**
```bash
go get github.com/stretchr/testify/require
```

**Test structure example:**
```go
package consolestream

import (
    "testing"
    "github.com/stretchr/testify/require"
)

func TestAscicastV3HeaderGeneration(t *testing.T) {
    metadata := AscicastV3Metadata{
        Term: TermInfo{Cols: 80, Rows: 24},
        Command: "echo test",
        Title: "Test Session",
    }

    header := AscicastV3Header{
        Version: 3,
        AscicastV3Metadata: metadata,
    }

    data, err := header.MarshalJSON()
    require.NoError(t, err)
    require.Contains(t, string(data), `"version":3`)
    require.Contains(t, string(data), `"command":"echo test"`)
    require.Contains(t, string(data), `"cols":80`)
}

func TestToAscicastV3EventTransformation(t *testing.T) {
    events := mockEventStream()
    metadata := AscicastV3Metadata{
        Term: TermInfo{Cols: 80, Rows: 24},
    }

    lines := collectLines(ToAscicastV3(events, metadata))

    require.NotEmpty(t, lines)
    require.Equal(t, 1, countHeaders(lines))
    require.True(t, hasExitEvent(lines))
}
```

**Testing guidelines:**
- Use `require.NoError(t, err)` instead of manual error checking
- Use `require.Equal(t, expected, actual)` for exact matches
- Use `require.Contains(t, haystack, needle)` for substring/element checks
- Use `require.True(t, condition)` and `require.False(t, condition)` for boolean assertions
- Use `require.NotEmpty(t, slice)` and `require.Empty(t, slice)` for collection checks
- Always use `require.*` assertions instead of `assert.*` to fail fast on first assertion failure

### Building

```bash
go build ./...
make check    # go vet, go mod tidy -diff
make quality  # check + lint + test
```

### Examples

See the `example/` directory for usage examples:

- `example/simple/`: Basic echo example
- `example/stream/`: Streaming output example
- `example/burst/`: Large buffer testing
- `example/buffering/`: Buffer flushing behaviour
- `example/pty/`: PTY examples with interactive terminal features
- `example/container/`: Container execution with mounts and env
- `example/asciicast/`: Asciicast v3 recording
- `example/logging/`: Event type demonstration and JSON serialization

`cmd/runner` executes a YAML-configured command; `cmd/tester` generates test output.

## Requirements

- Go 1.26+ (see `go.mod`)
- Unix-like system (for signal handling in LocalCancellor)

## License

This project is part of the console-stream library by wolfeidau.