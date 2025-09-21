# Console Stream

**Problem**: You need to execute external processes and monitor their complete lifecycle in real-time without blocking, buffering issues, or complex event management.

**Solution**: A Go library that streams process events using Go 1.23+ iterators with automatic buffering, heartbeat monitoring, and comprehensive lifecycle tracking.

## What You Get

- **Event-driven architecture**: Process lifecycle events, output data, and heartbeat monitoring
- **Real-time streaming**: Output delivered in 1-second intervals or 10MB chunks
- **Keep-alive detection**: Heartbeat events when processes are running but silent
- **Rich process lifecycle**: Start/end events with PIDs, duration, exit codes
- **Clean iteration**: Standard Go `for range` loops over all process events
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

    // Stream events in real-time
    ctx := context.Background()
    for part, err := range process.ExecuteAndStream(ctx) {
        if err != nil {
            fmt.Printf("Stream error: %v\n", err)
            break
        }

        switch event := part.Event.(type) {
        case *consolestream.OutputData:
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

## Use Cases

### Build System Integration
Monitor compiler output, test results, or deployment logs with comprehensive lifecycle tracking:

```go
process := consolestream.NewProcess("go", cancellor, "test", "-v", "./...")
for part, err := range process.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.OutputData:
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

process := consolestream.NewProcess("docker", cancellor, "logs", "-f", "my-service")
lastActivity := time.Now()

for part, err := range process.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.OutputData:
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

### CI/CD Pipeline Steps
Execute deployment commands with comprehensive monitoring and real-time feedback:

```go
process := consolestream.NewProcess("kubectl", cancellor, "apply", "-f", "deployment.yaml")
for part, err := range process.ExecuteAndStream(ctx) {
    switch event := part.Event.(type) {
    case *consolestream.OutputData:
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

## How It Works

1. **Process Execution**: Starts your command with separate stdout/stderr pipes
2. **Event Generation**: Emits ProcessStart event with PID and command details
3. **Concurrent Reading**: Background goroutines read from both streams into buffers
4. **Smart Flushing**: Buffers flush every 1 second OR when they reach 10MB as OutputData events
5. **Heartbeat Monitoring**: Emits HeartbeatEvent every second when no output occurs
6. **Lifecycle Tracking**: Emits ProcessEnd event with exit code, duration, and success status
7. **Iterator Protocol**: Uses Go 1.23+ `iter.Seq2[Event, error]` for clean event consumption
8. **Resource Management**: Automatic cleanup of pipes, processes, and goroutines

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
        case consolestream.ProcessStartError:
            log.Printf("Failed to start: %v", e.Err)
        }
        break
    }

    switch event := part.Event.(type) {
    case *consolestream.OutputData:
        // Handle normal output
        processOutput(event.Data, event.Stream)
    case *consolestream.ProcessEnd:
        if !event.Success {
            log.Printf("Process failed with exit code %d", event.ExitCode)
        }
        return // Process completed
    case *consolestream.ProcessError:
        log.Printf("Process error: %s", event.Message)
        return
    case *consolestream.HeartbeatEvent:
        // Monitor for stuck processes
        if event.ElapsedTime > 10*time.Minute {
            log.Printf("Process may be stuck (running for %v)", event.ElapsedTime)
        }
    }
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

### Event Processing
Add middleware for filtering, transforming, or analyzing events:

```go
func FilterOutputEvents(stream iter.Seq2[Event, error]) iter.Seq2[Event, error] {
    return func(yield func(Event, error) bool) {
        for part, err := range stream {
            if err != nil {
                yield(part, err)
                return
            }

            // Only yield OutputData events
            if _, ok := part.Event.(*consolestream.OutputData); ok {
                if !yield(part, err) {
                    return
                }
            }
        }
    }
}

func MonitorHeartbeats(stream iter.Seq2[Event, error], threshold time.Duration) iter.Seq2[Event, error] {
    return func(yield func(Event, error) bool) {
        for part, err := range stream {
            if !yield(part, err) {
                return
            }

            // Alert on long-running processes
            if hb, ok := part.Event.(*consolestream.HeartbeatEvent); ok {
                if hb.ElapsedTime > threshold {
                    // Could emit custom alert events here
                    log.Printf("Process running for %v", hb.ElapsedTime)
                }
            }
        }
    }
}

// Usage
filtered := FilterOutputEvents(process.ExecuteAndStream(ctx))
monitored := MonitorHeartbeats(filtered, 5*time.Minute)
for part, err := range monitored {
    // Only output events with heartbeat monitoring
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