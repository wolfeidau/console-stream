# Console Stream

A Go library for executing processes and streaming their output in real-time using Go iterators (Go 1.26+).

## Features

- **Real-time streaming**: Process output delivered at configurable intervals or buffer limits
- **Unified API**: Single process type supports both pipe and PTY modes
- **Container execution**: Run processes inside Docker/Podman containers with volume mounts
- **Event-driven**: Process lifecycle events, output data, and heartbeat monitoring
- **Production ready**: Context cancellation, process-group cleanup, error handling, and OpenTelemetry metrics
- **Asciicast**: Record PTY sessions in asciicast v3 format

## Quick Start

Run a command and print its output as it arrives:

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
    // Children do not receive Ctrl-C; cancel the context on SIGINT/SIGTERM.
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    process := consolestream.NewProcess("echo", []string{"Hello, World!"})

    for event, err := range process.ExecuteAndStream(ctx) {
        if err != nil {
            fmt.Printf("Error: %v\n", err)
            break
        }

        switch e := event.Event.(type) {
        case *consolestream.OutputData:
            fmt.Printf("Output: %s", e.Data)
        case *consolestream.ProcessError:
            fmt.Printf("Process error: %s\n", e.Message)
        case *consolestream.ProcessEnd:
            fmt.Printf("Process completed (Exit: %d)\n", e.ExitCode)
        }
    }
}
```

## PTY Mode

Use a pseudo-terminal for interactive programs, colors and progress bars. ANSI escape sequences are preserved.

```go
process := consolestream.NewProcess("npm", []string{"install"},
    consolestream.WithPTYMode(),
    consolestream.WithPTYSize(&pty.Winsize{Rows: 24, Cols: 80}), // github.com/creack/pty
    consolestream.WithEnvVar("NODE_ENV", "production"),
    consolestream.WithFlushInterval(500*time.Millisecond))
```

### Process Options

| Option | Description |
|--------|-------------|
| `WithPipeMode()` | Use a pipe for stdout+stderr (default) |
| `WithPTYMode()` | Use a pseudo-terminal |
| `WithPTYSize(*pty.Winsize)` | Terminal dimensions for PTY |
| `WithCancellor(c)` | Custom cancellation (default: `NewLocalCancellor(5s)`) |
| `WithEnv([]string)` / `WithEnvVar(k, v)` / `WithEnvMap(m)` | Environment variables |
| `WithWorkingDir(dir)` | Working directory |
| `WithFlushInterval(d)` | Output batch interval (default 1s): bounds live latency and event volume |
| `WithHeartbeatInterval(d)` | Heartbeat interval (default 1s), independent of the batch interval |
| `WithMaxBufferSize(n)` | Deliver a batch early when it reaches n bytes (default 10MB) |
| `WithMeter(metric.Meter)` | OpenTelemetry metrics |

## Container Execution

Run a command inside a Docker or Podman container. Missing images are pulled automatically, with progress events every 2 seconds.

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
    case *consolestream.ImagePullStart:
        fmt.Printf("Pulling image: %s\n", e.Image)
    case *consolestream.ImagePullProgress:
        fmt.Printf("Pull progress: %d%% (%d/%d bytes)\n",
            e.PercentComplete, e.BytesDownloaded, e.BytesTotal)
    case *consolestream.ImagePullComplete:
        fmt.Printf("Pull complete: %s\n", e.Digest)
    case *consolestream.ContainerCreate:
        fmt.Printf("Container: %s\n", e.ContainerID)
    case *consolestream.OutputData:
        fmt.Print(string(e.Data))
    case *consolestream.ProcessEnd:
        fmt.Printf("Exit: %d\n", e.ExitCode)
    }
}
```

Event order: `ContainerCreate → ProcessStart → OutputData/HeartbeatEvent → ProcessEnd | ProcessError → ContainerRemove`, preceded by `ImagePull*` events when the image is missing.

| Option | Description |
|--------|-------------|
| `WithContainerImage(img)` | Container image (required) |
| `WithContainerRuntime(r)` | `"docker"` (default) or `"podman"` |
| `WithContainerMount(src, dst, ro)` | Volume mount |
| `WithContainerWorkingDir(d)` | Working directory in container |
| `WithContainerEnv([]string)` / `WithContainerEnvMap(m)` | Environment variables |
| `WithContainerFlushInterval(d)` | Output batch interval (default 1s) |
| `WithContainerHeartbeatInterval(d)` | Heartbeat interval (default 1s) |
| `WithContainerMaxBufferSize(n)` | Maximum batch size (default 10MB) |
| `WithContainerMeter(metric.Meter)` | OpenTelemetry metrics |

## Asciicast Recording

Record a PTY session in asciicast v3 format:

```go
process := consolestream.NewProcess("bash", []string{"-c", "ls --color"},
    consolestream.WithPTYMode())

metadata := consolestream.AscicastV3Metadata{
    Term:    consolestream.TermInfo{Cols: 80, Rows: 24},
    Command: "ls --color",
}

file, err := os.Create("session.cast")
if err != nil {
    log.Fatal(err)
}
defer file.Close()

err = consolestream.WriteAscicastV3(process.ExecuteAndStream(ctx), metadata, func(b []byte) error {
    _, err := file.Write(b)
    return err
})
```

`ToAscicastV3` returns the lines as an iterator if you need to process them yourself. A `ProcessError` ends the recording with an `"m"` marker (`error: <message>`) instead of an `"x"` exit event.

`ToAscicastV3` maps each event type to a line: output to `"o"`, `TerminalResizeEvent` to `"r"`, and process end to `"x"`. The library does not emit resize events itself (PTY size is fixed by `WithPTYSize`); callers wrapping the event stream may inject them.

Recordings use per-read timestamps from `OutputData.Segments`, so the batch interval (`WithFlushInterval`) does not affect playback timing; there is no need to lower it for recordings. Always record with `WithPTYMode()`.

`timestamp` defaults to the recording start time (Unix seconds) when unset. `TermInfo` accepts optional `Type`, `Version` and `Theme` (`*TermTheme`); event intervals are rounded to microseconds.

## Events

| Event | Description |
|-------|-------------|
| `ProcessStart` | Process started (PID, command, args) |
| `OutputData` | Output bytes (stdout and stderr are combined); `Segments` carries per-read timing |
| `HeartbeatEvent` | Process alive, no output since the previous heartbeat tick |
| `ProcessEnd` | Exit code, duration, success. A non-zero exit is a `ProcessEnd`, not an error. A signal-terminated process reports `128 + signal` (137 for SIGKILL), matching shells and container mode |
| `ProcessError` | Failure that is not a non-zero exit |
| `TerminalResizeEvent` | Terminal size change (PTY) |
| `ContainerCreate` / `ContainerRemove` | Container created / cleaned up |
| `ImagePullStart` / `ImagePullProgress` / `ImagePullComplete` | Image pull (container) |

## Streaming Behaviour

- Output is delivered in batches: one `OutputData` event per batch interval (`WithFlushInterval`, default 1s), earlier if the buffer reaches the max size (default 10MB), and a final batch on exit.
- The batch interval trades live latency for event volume. It does not affect timing accuracy: `OutputData.Segments` records when each chunk was read.
- Heartbeats run on their own interval (`WithHeartbeatInterval`, default 1s): on each tick a `HeartbeatEvent` fires if no output was delivered or buffered since the previous tick. A short batch interval does not add heartbeats.
- Pipe mode is not line-buffered: events can split mid-line. Reassemble lines in the consumer if needed.
- Stdout and stderr share one pipe, so their relative order is preserved but they cannot be distinguished.

## Cancellation and Signals

Breaking out of the range loop or cancelling the context stops the child through the `Cancellor` (SIGTERM to the process group, SIGKILL after the timeout) and waits for all goroutines before the iterator returns. Containers are stopped and then removed.

Children run in their own process group (pipe: `Setpgid`, PTY: `Setsid`), so they do not receive Ctrl-C from your terminal. Use `signal.NotifyContext` for SIGINT/SIGTERM and cancel the context, as in the Quick Start.

Known limits:

- PTY children that create job-control subgroups (e.g. interactive shells) have descendants the group kill does not reach.
- Grandchildren that daemonize are not killed when the child exits normally.

## Error Handling

- A process that cannot start yields a `ProcessStartError` as the iterator error.
- Failures after start arrive as `ProcessError` events; non-zero exits arrive as `ProcessEnd` with `Success == false`.

```go
var startErr consolestream.ProcessStartError
if errors.As(err, &startErr) {
    fmt.Printf("could not start %s: %v\n", startErr.Cmd, startErr.Err)
}
```

## Command Line Tool

Build and use the runner tool:

```bash
# Build
go build -o runner ./cmd/runner

# Execute with YAML config
./runner exec --config task.yaml --format json --output results.json
```

Example config:
```yaml
command: "docker"
args: ["build", "-t", "myapp", "."]
process_type: "pty"
timeout: "10m"
env:
  DOCKER_BUILDKIT: "1"
```

## Installation

```bash
go get github.com/wolfeidau/console-stream
```

**Requirements**: Go 1.26+, Unix-like system

## License

Apache License, Version 2.0 - Copyright [Mark Wolfe](https://www.wolfe.id.au)