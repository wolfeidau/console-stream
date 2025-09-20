# Console Stream

**Problem**: You need to execute external processes and consume their output in real-time without blocking, buffering issues, or complex stream management.

**Solution**: A Go library that streams process output using Go 1.23+ iterators with automatic buffering, timeout handling, and clean resource management.

## What You Get

- **Real-time streaming**: Output delivered in 1-second intervals or 10MB chunks
- **Clean iteration**: Standard Go `for range` loops over process output
- **Separate streams**: Stdout and stderr handled independently with timestamps
- **Smart buffering**: Automatic flushing prevents memory issues and ensures responsiveness
- **Robust cancellation**: Context-aware with pluggable termination strategies
- **Production ready**: Comprehensive error handling and resource cleanup

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
    // Create process with local cancellation (5s timeout)
    cancellor := consolestream.NewLocalCancellor(5 * time.Second)
    process := consolestream.NewProcess("ping", cancellor, "-c", "5", "google.com")

    // Stream output in real-time
    ctx := context.Background()
    for part, err := range process.ExecuteAndStream(ctx) {
        if err != nil {
            fmt.Printf("Process error: %v\n", err)
            break
        }

        fmt.Printf("[%s] %s", part.Stream.String(), string(part.Data))
    }
}
```

## Use Cases

### Build System Integration
Stream compiler output, test results, or deployment logs in real-time without memory buildup:

```go
process := consolestream.NewProcess("go", cancellor, "test", "-v", "./...")
for part, err := range process.ExecuteAndStream(ctx) {
    // Process test output as it happens
    logTestProgress(part.Data, part.Timestamp)
}
```

### Long-Running Process Monitoring
Monitor services, databases, or data processing jobs:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
defer cancel()

process := consolestream.NewProcess("docker", cancellor, "logs", "-f", "my-service")
for part, err := range process.ExecuteAndStream(ctx) {
    if containsError(part.Data) {
        alertOnError(part.Data)
    }
}
```

### CI/CD Pipeline Steps
Execute deployment commands with real-time feedback:

```go
process := consolestream.NewProcess("kubectl", cancellor, "apply", "-f", "deployment.yaml")
for part, err := range process.ExecuteAndStream(ctx) {
    updateDeploymentStatus(part.Data)
    if err != nil {
        handleDeploymentFailure(err)
        break
    }
}
```

## How It Works

1. **Process Execution**: Starts your command with separate stdout/stderr pipes
2. **Concurrent Reading**: Background goroutines read from both streams into buffers
3. **Smart Flushing**: Buffers flush every 1 second OR when they reach 10MB
4. **Iterator Protocol**: Uses Go 1.23+ `iter.Seq2[StreamPart, error]` for clean consumption
5. **Resource Management**: Automatic cleanup of pipes, processes, and goroutines

## What Can Go Wrong?

### Process Failures
- **Non-zero exit codes**: Wrapped in `ProcessFailedError` with exit code
- **Killed processes**: Wrapped in `ProcessKilledError` with signal info
- **Startup failures**: Wrapped in `ProcessStartError` with underlying error

### Resource Management
- **Context cancellation**: Uses configured `Cancellor` for graceful shutdown
- **Memory limits**: 10MB buffer limit prevents runaway memory usage
- **Goroutine leaks**: Automatic cleanup when context is cancelled or process exits

### Example Error Handling
```go
for part, err := range process.ExecuteAndStream(ctx) {
    if err != nil {
        switch e := err.(type) {
        case consolestream.ProcessFailedError:
            log.Printf("Process failed with exit code %d", e.ExitCode)
        case consolestream.ProcessKilledError:
            log.Printf("Process killed by signal %s", e.Signal)
        case consolestream.ProcessStartError:
            log.Printf("Failed to start: %v", e.Err)
        }
        break
    }
    // Handle normal output
}
```

## Extending the Library

### Custom Cancellation
Implement the `Cancellor` interface for different execution environments:

```go
type DockerCancellor struct {
    containerID string
}

func (d *DockerCancellor) Cancel(ctx context.Context, pid int) error {
    return exec.CommandContext(ctx, "docker", "stop", d.containerID).Run()
}

// Use with processes
process := consolestream.NewProcess("docker", &DockerCancellor{"my-container"}, "run", "...")
```

### Stream Processing
Add middleware for filtering, transforming, or analyzing output:

```go
func FilterErrors(stream iter.Seq2[StreamPart, error]) iter.Seq2[StreamPart, error] {
    return func(yield func(StreamPart, error) bool) {
        for part, err := range stream {
            if err != nil || containsError(part.Data) {
                if !yield(part, err) {
                    return
                }
            }
        }
    }
}

// Usage
filtered := FilterErrors(process.ExecuteAndStream(ctx))
for part, err := range filtered {
    // Only error output
}
```

## Requirements

- **Go 1.23+** (for iterator support)
- **Unix-like system** (for signal handling)

## Installation

```bash
go get github.com/wolfeidau/console-stream
```

## Acknowledgments

This library was developed with the assistance of Claude (Anthropic's AI assistant) through responsible AI collaboration. The design, implementation, and documentation reflect a partnership between human engineering judgment and AI-powered development acceleration.

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

This project is licensed under the MIT License.