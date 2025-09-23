# Console Stream

A Go library for executing processes and streaming their output in real-time using Go 1.23+ iterators.

## Features

- **Real-time streaming**: Process output delivered at configurable intervals or buffer limits
- **Unified API**: Single process type supports both pipe and PTY modes
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

### Event Types

| Event | Description |
|-------|-------------|
| `ProcessStart` | Process started with PID |
| `OutputData` | Process output (combined stdout/stderr) |
| `ProcessEnd` | Process completed with exit code |
| `ProcessError` | Process execution error |
| `HeartbeatEvent` | Keep-alive when process is silent |
| `TerminalResizeEvent` | Terminal size change (PTY only) |

## Use Cases

**Pipe Mode** - Use for:
- Build tools, tests, data processing
- Simple command execution
- When you need performance

**PTY Mode** - Use for:
- Interactive applications (shells, editors)
- Programs with progress bars or colors
- Terminal session recording

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