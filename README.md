# Console Stream

A Go library for executing processes and streaming their output in real-time using Go 1.23+ iterators.

## Features

- **Real-time streaming**: Process output delivered at configurable intervals or buffer limits
- **Unified API**: Single process type supports both pipe and PTY modes
- **Container execution**: Run processes inside Docker/Podman containers with volume mounts
- **Event-driven**: Process lifecycle events, output data, and heartbeat monitoring
- **Production ready**: Context cancellation, error handling, and resource cleanup

## Quick Start

```go
package main

import (
    "context"
    "fmt"

    consolestream "github.com/wolfeidau/console-stream"
)

func main() {
    // Create and execute process
    process := consolestream.NewProcess("echo", []string{"Hello, World!"})

    ctx := context.Background()
    for event, err := range process.ExecuteAndStream(ctx) {
        if err != nil {
            fmt.Printf("Error: %v\n", err)
            break
        }

        switch e := event.Event.(type) {
        case *consolestream.OutputData:
            fmt.Printf("Output: %s", string(e.Data))
        case *consolestream.ProcessEnd:
            fmt.Printf("Process completed (Exit: %d)\n", e.ExitCode)
            return
        }
    }
}
```

## API

### Process Creation

```go
// Default (pipe mode)
process := consolestream.NewProcess("command", []string{"arg1", "arg2"})

// PTY mode for interactive programs
process := consolestream.NewProcess("bash", []string{"-l"},
    consolestream.WithPTYMode())

// With configuration options
process := consolestream.NewProcess("npm", []string{"install"},
    consolestream.WithPTYMode(),
    consolestream.WithPTYSize(pty.Winsize{Rows: 24, Cols: 80}),
    consolestream.WithEnvVar("NODE_ENV", "production"),
    consolestream.WithFlushInterval(500*time.Millisecond))
```

### Process Options

| Option | Description |
|--------|-------------|
| `WithPipeMode()` | Use standard pipes (default) |
| `WithPTYMode()` | Use pseudo-terminal for interactive programs |
| `WithPTYSize(size)` | Set terminal dimensions for PTY |
| `WithCancellor(c)` | Custom process cancellation |
| `WithEnvVar(k, v)` | Set environment variable |
| `WithFlushInterval(d)` | Output flush frequency |
| `WithMaxBufferSize(s)` | Buffer size limit |

### Container Execution

Execute processes inside Docker/Podman containers with volume mounts and environment variables:

```go
// Simple container execution
process := consolestream.NewContainerProcess("go", []string{"version"},
    consolestream.WithContainerImage("golang:1.25"))

// With volume mounts
process := consolestream.NewContainerProcess("make", []string{"test"},
    consolestream.WithContainerImage("golang:1.25"),
    consolestream.WithContainerMount("/path/to/code", "/workspace", false),
    consolestream.WithContainerWorkingDir("/workspace"),
    consolestream.WithContainerEnvMap(map[string]string{
        "CI": "true",
        "GOPATH": "/go",
    }))

// Same event streaming model
for event, err := range process.ExecuteAndStream(ctx) {
    if err != nil {
        break
    }

    switch e := event.Event.(type) {
    case *consolestream.ContainerCreate:
        fmt.Printf("Container: %s\n", e.ContainerID)
    case *consolestream.OutputData:
        fmt.Print(string(e.Data))
    case *consolestream.ProcessEnd:
        fmt.Printf("Exit: %d\n", e.ExitCode)
    }
}
```

### Container Options

| Option | Description |
|--------|-------------|
| `WithContainerImage(img)` | Container image (required) |
| `WithContainerRuntime(r)` | Runtime: "docker" (default) or "podman" |
| `WithContainerMount(src, dst, ro)` | Volume mount (source, target, read-only) |
| `WithContainerWorkingDir(d)` | Working directory in container |
| `WithContainerEnvMap(m)` | Environment variables map |
| `WithContainerFlushInterval(d)` | Output flush frequency |
| `WithContainerMaxBufferSize(s)` | Buffer size limit |

### Event Types

| Event | Description |
|-------|-------------|
| `ProcessStart` | Process started with PID |
| `OutputData` | Process output (combined stdout/stderr) |
| `ProcessEnd` | Process completed with exit code |
| `ProcessError` | Process execution error |
| `HeartbeatEvent` | Keep-alive when process is silent |
| `TerminalResizeEvent` | Terminal size change (PTY only) |
| `ContainerCreate` | Container created with ID and image |
| `ContainerRemove` | Container cleanup completed |

## Use Cases

**Pipe Mode** - Use for:
- Build tools, tests, data processing
- Simple command execution
- When you need performance

**PTY Mode** - Use for:
- Interactive applications (shells, editors)
- Programs with progress bars or colors
- Terminal session recording

**Container Mode** - Use for:
- CI/CD pipelines with isolated builds
- Running processes in reproducible environments
- Sandboxed execution with resource limits
- Testing with specific runtime versions

## Asciicast Recording

Record terminal sessions in asciicast v3 format:

```go
// Record PTY session
process := consolestream.NewProcess("bash", []string{"-l"},
    consolestream.WithPTYMode())

metadata := consolestream.AscicastV3Metadata{
    Term: consolestream.TermInfo{Cols: 80, Rows: 24},
    Command: "bash -l",
}

events := process.ExecuteAndStream(ctx)
asciicast := consolestream.ToAscicastV3(events, metadata)

// Write to file
file, _ := os.Create("session.cast")
defer file.Close()

for line, err := range asciicast {
    if err != nil { break }
    data, _ := json.Marshal(line)
    file.Write(append(data, '\n'))
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

**Requirements**: Go 1.25+, Unix-like system

## License

Apache License, Version 2.0 - Copyright [Mark Wolfe](https://www.wolfe.id.au)