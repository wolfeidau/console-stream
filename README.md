# Console Stream

**Problem**: You need to execute external processes and monitor their complete lifecycle in real-time without blocking, buffering issues, or complex event management.

**Solution**: A Go library that streams process events using Go 1.23+ iterators with automatic buffering, heartbeat monitoring, and comprehensive lifecycle tracking.

## What You Get

- **Event-driven architecture**: Process lifecycle events, output data, and heartbeat monitoring
- **Real-time streaming**: Output delivered at configurable intervals (default 1 second) or buffer limits (default 10MB)
- **Keep-alive detection**: Heartbeat events when processes are running but silent
- **Rich process lifecycle**: Start/end events with PIDs, duration, exit codes
- **Clean iteration**: Standard Go `for range` loops over all process events
- **Smart buffering**: Automatic flushing prevents memory issues and ensures responsiveness
- **Robust cancellation**: Context-aware with pluggable termination strategies
- **Production ready**: Comprehensive error handling and resource cleanup

## Functional Options API

Console Stream uses a functional options pattern for clean, extensible configuration:

```go
// Default configuration (uses built-in 5-second timeout cancellor)
process := consolestream.NewPipeProcess("echo", []string{"hello"})

// Available functional options:
cancellor := consolestream.NewLocalCancellor(10 * time.Second)
process := consolestream.NewPipeProcess("echo", []string{"hello"},
    // Process control
    consolestream.WithCancellor(cancellor),                        // Custom cancellation

    // Environment variables
    consolestream.WithEnvVar("MY_VAR", "value"),                   // Single variable
    consolestream.WithEnv([]string{"PATH=/usr/bin", "HOME=/tmp"}), // Variable slice
    consolestream.WithEnvMap(map[string]string{                    // Variable map
        "API_KEY": "secret",
        "DEBUG":   "true",
    }),

    // Buffer configuration
    consolestream.WithFlushInterval(500*time.Millisecond), // How often to flush
    consolestream.WithMaxBufferSize(5*1024*1024))         // 5MB buffer limit

// PTY-specific options
size := pty.Winsize{Rows: 24, Cols: 80}
ptyProcess := consolestream.NewPTYProcess("bash", []string{"-l"},
    consolestream.WithCancellor(cancellor),
    consolestream.WithPTYSize(size),                // Terminal dimensions
    consolestream.WithEnvVar("TERM", "xterm-256color"))
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "time"

    consolestream "github.com/wolfeidau/console-stream"
)

func main() {
    // Create process with functional options for configuration
    cancellor := consolestream.NewLocalCancellor(5 * time.Second)
    process := consolestream.NewPipeProcess("ping", []string{"-c", "5", "google.com"},
        consolestream.WithCancellor(cancellor))

    // Stream events in real-time
    ctx := context.Background()
    for part, err := range process.ExecuteAndStream(ctx) {
        if err != nil {
            fmt.Printf("Stream error: %v\n", err)
            break
        }

        switch event := part.Event.(type) {
        case *consolestream.PipeOutputData:
            fmt.Printf("[%s] %s", event.Stream.String(), string(event.Data))
        case *consolestream.ProcessStart:
            fmt.Printf("Process started (PID: %d)\n", event.PID)
        case *consolestream.ProcessEnd:
            fmt.Printf("Process completed (Exit: %d, Duration: %v)\n", event.ExitCode, event.Duration)
        case *consolestream.HeartbeatEvent:
            fmt.Printf("Process running... (Elapsed: %v)\n", event.ElapsedTime)
        }
    }
}
```

## PTY Support for Interactive Programs

### When to Use PTY vs Regular Processes

**Use PTY (`NewPTYProcess`) when:**
- Running interactive terminal applications (editors, shells)
- Capturing programs with progress bars, colors, or ANSI escape sequences
- Working with CLI tools that detect TTY presence and change behavior
- Need to preserve terminal formatting and control sequences

**Use Regular Process (`NewPipeProcess`) when:**
- Simple data pipeline between processes
- Performance is critical (PTY has slight overhead)
- You need separate stdout/stderr streams

### PTY Examples

```go
// Basic PTY usage - preserves colors and formatting
ptyProcess := consolestream.NewPTYProcess("npm", []string{"install"})
for part, err := range ptyProcess.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.PTYOutputData:
        // Raw terminal output with ANSI escape sequences preserved
        handleTerminalOutput(event.Data, part.Timestamp)
    case *consolestream.ProcessEnd:
        logInstallCompletion(event.ExitCode, event.Duration)
    }
}

// PTY with specific terminal size and environment variables
size := pty.Winsize{Rows: 24, Cols: 80}
ptyProcess := consolestream.NewPTYProcess("top", []string{"-n", "1"},
    consolestream.WithPTYSize(size),
    consolestream.WithEnvVar("TERM", "xterm-256color"))
```

## Recording Sessions (Asciicast v3)

Console Stream supports converting PTY process events into asciicast v3 format for session recording and playback:

```go
// Record a terminal session to asciicast v3 format
ptyProcess := consolestream.NewPTYProcess("bash", []string{"-l"})

metadata := consolestream.AscicastV3Metadata{
    Term: consolestream.TermInfo{Cols: 80, Rows: 24},
    Command: "bash -l",
    Title: "Interactive Shell Session",
}

// Convert events to asciicast format
eventStream := ptyProcess.ExecuteAndStream(ctx)
ascicastStream := consolestream.ToAscicastV3(eventStream, metadata)

// Write to file (JSON Lines format)
file, err := os.Create("session.cast")
if err != nil {
    log.Fatal(err)
}
defer file.Close()

for line, err := range ascicastStream {
    if err != nil {
        log.Printf("Error: %v", err)
        break
    }

    data, _ := json.Marshal(line)
    file.Write(append(data, '\n'))
}

// Or use the convenience function
err = consolestream.WriteAscicastV3(eventStream, metadata, func(data []byte) error {
    _, err := file.Write(data)
    return err
})
```

### Session Recording and Analysis
Record terminal sessions for debugging, training, or compliance:

```go
// Record a development session with full metadata
metadata := consolestream.AscicastV3Metadata{
    Term: consolestream.TermInfo{Cols: 120, Rows: 30},
    Command: "npm run build",
    Title: "Production Build Session",
    Env: map[string]string{
        "NODE_ENV": "production",
        "CI": "true",
    },
    Tags: []string{"build", "production", "ci"},
}

ptyProcess := consolestream.NewPTYProcess("npm", []string{"run", "build"},
    consolestream.WithEnvVar("NODE_ENV", "production"))

// Record to file for later analysis
file, _ := os.Create("build-session.cast")
defer file.Close()

eventStream := ptyProcess.ExecuteAndStream(ctx)
err := consolestream.WriteAscicastV3(eventStream, metadata, func(data []byte) error {
    _, err := file.Write(data)
    return err
})

// The resulting .cast file can be played with: asciinema play build-session.cast
```

**Features:**
- **Standard format**: Compatible with [asciinema](https://asciinema.org/) players and tools
- **Complete sessions**: Captures output, terminal resizes, and exit codes
- **Streaming conversion**: Transform events in real-time without buffering entire sessions
- **Metadata support**: Include command, title, environment variables, and tags

## Use Cases

### Build System Integration
Monitor compiler output, test results, or deployment logs with comprehensive lifecycle tracking:

```go
process := consolestream.NewPipeProcess("go", []string{"test", "-v", "./..."},
    consolestream.WithEnvVar("CGO_ENABLED", "0"),
    consolestream.WithFlushInterval(500*time.Millisecond)) // Faster feedback for tests

for part, err := range process.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.PipeOutputData:
        logTestProgress(event.Data, part.Timestamp)
    case *consolestream.ProcessEnd:
        logBuildCompletion(event.ExitCode, event.Duration)
    case *consolestream.HeartbeatEvent:
        updateProgressIndicator(event.ElapsedTime)
    }
}
```

### Long-Running Process Monitoring
Monitor services, databases, or data processing jobs with keep-alive detection:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
defer cancel()

process := consolestream.NewPipeProcess("docker", []string{"logs", "-f", "my-service"})
lastActivity := time.Now()

for part, err := range process.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.PipeOutputData:
        lastActivity = part.Timestamp
        if containsError(event.Data) {
            alertOnError(event.Data)
        }
    case *consolestream.HeartbeatEvent:
        // Process is alive but silent - check for stalls
        if time.Since(lastActivity) > 5*time.Minute {
            alertOnSilentProcess(event.ElapsedTime)
        }
    case *consolestream.ProcessEnd:
        if !event.Success {
            alertOnProcessFailure(event.ExitCode)
        }
    }
}
```

### Interactive Terminal Applications
Monitor CLI tools with progress bars, colors, and interactive elements:

```go
// Run interactive installer with PTY to capture progress bars and colors
ptyProcess := consolestream.NewPTYProcess("npm", []string{"install", "--progress"})
for part, err := range ptyProcess.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.PTYOutputData:
        // Output contains ANSI escape sequences for colors and progress bars
        logInteractiveOutput(event.Data)
        if containsProgressBar(event.Data) {
            updateProgressDisplay(event.Data)
        }
    case *consolestream.ProcessEnd:
        if event.Success {
            logInstallSuccess(event.Duration)
        } else {
            handleInstallFailure(event.ExitCode)
        }
    case *consolestream.HeartbeatEvent:
        showInstallProgress(event.ElapsedTime)
    }
}
```

### CI/CD Pipeline Steps
Execute deployment commands with comprehensive monitoring and real-time feedback:

```go
process := consolestream.NewPipeProcess("kubectl", []string{"apply", "-f", "deployment.yaml"})
for part, err := range process.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.PipeOutputData:
        updateDeploymentStatus(event.Data)
    case *consolestream.ProcessStart:
        logDeploymentStart(event.PID)
    case *consolestream.ProcessEnd:
        if event.Success {
            logDeploymentSuccess(event.Duration)
        } else {
            handleDeploymentFailure(event.ExitCode)
        }
    case *consolestream.HeartbeatEvent:
        showDeploymentProgress(event.ElapsedTime)
    }
}
```

## Command Line Runner Tool

The `cmd/runner` tool provides a command-line interface for executing processes using console-stream with YAML configuration files. This tool is useful for scripting, CI/CD pipelines, and interactive terminal recording.

### Installation and Building

```bash
# Build the runner tool
make build

# Or build manually
go build -o bin/runner ./cmd/runner
```

### Basic Usage

```bash
# Execute a command with YAML configuration
./bin/runner exec --config task.yaml

# Use short flags
./bin/runner exec -c task.yaml -f json -o output.json
```

### Output Formats

The runner supports three output formats optimized for different use cases:

#### Text Format (Default)
Human-readable output with timestamps and labeled streams:
```bash
./bin/runner exec --config task.yaml --format text
```
**Example output:**
```
Process started (PID: 12345, Command: docker)
[STDOUT] 18:45:12: latest: Pulling from devcontainers/go
[STDOUT] 18:45:13: d1e404420307: Already exists
Process completed (Exit Code: 0, Duration: 2.543s)
```

#### JSON Format
Structured output for programmatic consumption and log analysis:
```bash
./bin/runner exec --config task.yaml --format json --output execution.json
```
**Example output:**
```json
{"timestamp":"2025-09-22T18:45:12Z","event_type":"ProcessStart","event":{"PID":12345,"Command":"docker","Args":["pull","image"]}}
{"timestamp":"2025-09-22T18:45:12Z","event_type":"PipeOutputEvent","event":{"Data":"latest: Pulling...\n","Stream":"STDOUT"}}
{"timestamp":"2025-09-22T18:45:15Z","event_type":"ProcessEnd","event":{"ExitCode":0,"Duration":"2.543210s","Success":true}}
```

#### Asciicast Format
Terminal session recording compatible with [asciinema](https://asciinema.org/) players:
```bash
./bin/runner exec --config task.yaml --format asciicast --output recording.cast

# Play the recording
asciinema play recording.cast
```

**Features:**
- Compatible with asciinema ecosystem
- Preserves ANSI escape sequences and terminal formatting
- Includes metadata (command, timestamp, terminal size)
- Optimized 100ms flush interval for smooth playback

### Configuration File Format

Create YAML configuration files to define process execution parameters:

```yaml
# Basic configuration
command: "docker"
args: ["pull", "mcr.microsoft.com/devcontainers/go:latest"]
process_type: "pty"  # Use "pipe" for simple processes, "pty" for interactive
timeout: "15m"

# Environment variables
env:
  NODE_ENV: "production"
  DEBUG: "true"
  API_KEY: "secret-value"

# PTY-specific settings (when process_type: "pty")
pty_size:
  rows: 24
  cols: 80
```

#### Configuration Options

| Field | Type | Description | Default |
|-------|------|-------------|---------|
| `command` | string | **Required.** The command to execute | - |
| `args` | []string | Command line arguments | `[]` |
| `process_type` | string | Process type: `"pipe"` or `"pty"` | `"pipe"` |
| `timeout` | string | Maximum execution time (e.g., "30s", "5m") | `"30s"` |
| `env` | map[string]string | Environment variables | `{}` |
| `pty_size.rows` | int | Terminal rows (PTY only) | `24` |
| `pty_size.cols` | int | Terminal columns (PTY only) | `80` |

### Use Cases and Examples

#### CI/CD Pipeline Integration
Record build processes for debugging and compliance:
```yaml
# build-config.yaml
command: "npm"
args: ["run", "build"]
env:
  NODE_ENV: "production"
  CI: "true"
process_type: "pty"
timeout: "10m"
pty_size:
  rows: 30
  cols: 120
```

```bash
# Execute and record build process
./bin/runner exec --config build-config.yaml \
  --format asciicast \
  --output "build-$(date +%Y%m%d).cast"
```

#### Docker Operations
Monitor container operations with progress bars and colored output:
```yaml
# docker-config.yaml
command: "docker"
args: ["build", "-t", "myapp:latest", "."]
process_type: "pty"
timeout: "20m"
env:
  DOCKER_BUILDKIT: "1"
```

#### Database Migrations
Track migration execution with detailed logging:
```yaml
# migration-config.yaml
command: "migrate"
args: ["-path", "./migrations", "-database", "postgres://...", "up"]
process_type: "pipe"
timeout: "5m"
env:
  DB_HOST: "localhost"
  DB_PORT: "5432"
```

```bash
# Execute with JSON output for log aggregation
./bin/runner exec --config migration-config.yaml \
  --format json \
  --output migration-log.json
```

### Advanced Features

#### Timeout Management
Override configuration timeouts at runtime:
```bash
# Override timeout for slow operations
./bin/runner exec --config task.yaml --timeout 1h
```

#### Output Redirection
Capture execution results to files:
```bash
# Save structured output
./bin/runner exec --config task.yaml --format json --output results.json

# Create session recordings
./bin/runner exec --config task.yaml --format asciicast --output session.cast
```

#### Error Handling
The runner provides detailed error reporting and appropriate exit codes:
- **Exit 0**: Successful execution
- **Exit 1**: Configuration, execution, or formatting errors
- **Process exit code**: Passed through when the executed command fails

### Integration Examples

#### Makefile Integration
```makefile
.PHONY: test-with-recording
test-with-recording:
	./bin/runner exec --config test-config.yaml \
		--format asciicast \
		--output test-session-$(shell date +%Y%m%d).cast
```

#### GitHub Actions
```yaml
- name: Execute and Record Process
  run: |
    ./bin/runner exec --config .github/workflows/task.yaml \
      --format json \
      --output execution-log.json
```

#### Shell Scripting
```bash
#!/bin/bash
set -e

# Execute process and capture exit code
if ./bin/runner exec --config deploy.yaml --format json --output deploy.json; then
    echo "Deployment successful"
    # Process success logs
    jq '.[] | select(.event_type=="ProcessEnd")' deploy.json
else
    echo "Deployment failed"
    # Process error logs
    jq '.[] | select(.error==true)' deploy.json
    exit 1
fi
```

## How It Works

1. **Process Execution**: Starts your command with separate stdout/stderr pipes or PTY
2. **Event Generation**: Emits ProcessStart event with PID and command details
3. **Concurrent Reading**: Background goroutines read output streams into buffers
4. **Smart Flushing**: Buffers flush at configurable intervals (default 1 second) OR when they reach configurable size (default 10MB)
5. **Heartbeat Monitoring**: Emits HeartbeatEvent at flush intervals when no output occurs
6. **Lifecycle Tracking**: Emits ProcessEnd event with exit code, duration, and success status
7. **Iterator Protocol**: Uses Go 1.23+ `iter.Seq2[Event, error]` for clean event consumption
8. **Resource Management**: Automatic cleanup of pipes, processes, and goroutines

## Error Handling

### Process Failures
```go
for part, err := range process.ExecuteAndStream(ctx) {
    if err != nil {
        switch e := err.(type) {
        case consolestream.ProcessStartError:
            log.Printf("Failed to start: %v", e.Err)
        }
        break
    }

    switch event := part.Event.(type) {
    case *consolestream.PipeOutputData:
        processOutput(event.Data, event.Stream)
    case *consolestream.ProcessEnd:
        if !event.Success {
            log.Printf("Process failed with exit code %d", event.ExitCode)
        }
        return
    case *consolestream.HeartbeatEvent:
        if event.ElapsedTime > 10*time.Minute {
            log.Printf("Process may be stuck (running for %v)", event.ElapsedTime)
        }
    }
}
```

## Extending the Library

### Custom Cancellation
```go
type DockerCancellor struct {
    containerID string
}

func (d *DockerCancellor) Cancel(ctx context.Context, pid int) error {
    return exec.CommandContext(ctx, "docker", "stop", d.containerID).Run()
}

process := consolestream.NewPipeProcess("docker", []string{"run", "..."},
    consolestream.WithCancellor(&DockerCancellor{"my-container"}))
```

## Requirements

- **Go 1.23+** (for iterator support)
- **Unix-like system** (for signal handling and PTY support)
- **github.com/creack/pty** (for PTY functionality - automatically included)

## Installation

```bash
go get github.com/wolfeidau/console-stream
```

## Acknowledgments

This library was developed with the assistance of [Claude](https://claude.ai/) (Anthropic's AI assistant) through responsible AI collaboration. The design, implementation, and documentation reflect a partnership between human engineering judgment and AI-powered development acceleration.

**Human Contributions:**
- Project requirements and architectural decisions
- Code review and validation of AI-generated implementations
- Testing strategy and quality assurance
- Production readiness considerations and operational concerns

**AI Contributions:**
- Code generation following specified patterns and requirements
- Documentation creation adhering to Amazon engineering principles
- Test program development and comprehensive examples
- Code refactoring and optimization suggestions

This approach demonstrates responsible AI usage in software development - leveraging AI capabilities while maintaining human oversight, validation, and decision-making authority. The resulting code has been thoroughly tested and reviewed to ensure production quality and maintainability.

# License

This project is copyright [Mark Wolfe](https://www.wolfe.id.au) and licensed under the [Apache License, Version 2.0](LICENSE).