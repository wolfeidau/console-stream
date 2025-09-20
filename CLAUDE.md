# Console Stream

A Go library for executing processes and streaming their output in real-time using Go 1.23+ iterators.

## Features

- **Real-time streaming**: Stream process output in 1-second intervals or when buffers reach 10MB
- **Iterator interface**: Uses Go 1.23+ `iter.Seq2[StreamPart, error]` for clean consumption
- **Separate streams**: Stdout and stderr are delivered as separate `StreamPart` objects
- **Pluggable cancellation**: Interface-based cancellation for different execution environments
- **Buffer management**: Automatic flushing with configurable size limits
- **Context support**: Full context cancellation with graceful cleanup

## Usage

### Basic Example

```go
package main

import (
    "context"
    "fmt"
    "time"

    consolestream "github.com/wolfeidau/console-stream"
)

func main() {
    // Create a local cancellor with 5 second timeout
    cancellor := consolestream.NewLocalCancellor(5 * time.Second)

    // Create a process
    process := consolestream.NewProcess("echo", cancellor, "Hello, World!")

    // Execute and stream
    ctx := context.Background()
    for part, err := range process.ExecuteAndStream(ctx) {
        if err != nil {
            fmt.Printf("Error: %v\n", err)
            break
        }

        fmt.Printf("[%s] %s: %s", part.Stream.String(), part.Timestamp.Format("15:04:05"), string(part.Data))
    }
}
```

### Streaming Example

```go
// Stream a long-running process
process := consolestream.NewProcess("go", cancellor, "run", "cmd/tester/main.go", "--duration=5s", "--stdout-rate=3")

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

for part, err := range process.ExecuteAndStream(ctx) {
    if err != nil {
        log.Printf("Error: %v", err)
        break
    }

    fmt.Printf("Received %d bytes from %s at %s\n",
        len(part.Data),
        streamName(part.Stream),
        part.Timestamp.Format("15:04:05.000"))
}
```

## API Reference

### Types

```go
type Process struct {
    // Contains filtered or unexported fields
}

type StreamPart struct {
    Timestamp time.Time
    Data      []byte
    Stream    StreamType  // Stdout or Stderr
}

type StreamType int

const (
    Stdout StreamType = iota
    Stderr
)

func (s StreamType) String() string

type Cancellor interface {
    Cancel(ctx context.Context, pid int) error
}
```

### Functions

```go
// NewProcess creates a new process with the given command, cancellor, and arguments
func NewProcess(cmd string, cancellor Cancellor, args ...string) *Process

// NewLocalCancellor creates a cancellor for local processes with SIGTERM/SIGKILL timeout
func NewLocalCancellor(terminateTimeout time.Duration) *LocalCancellor

// ExecuteAndStream starts the process and returns an iterator over StreamPart objects
func (p *Process) ExecuteAndStream(ctx context.Context) iter.Seq2[StreamPart, error]
```

### Error Types

```go
type ProcessStartError struct {
    Cmd string
    Err error
}

type ProcessFailedError struct {
    Cmd      string
    ExitCode int
}

type ProcessKilledError struct {
    Cmd    string
    Signal string
}
```

## Behavior

### Streaming Rules

1. **Time-based flushing**: Buffers are flushed every 1 second if they contain data
2. **Size-based flushing**: Buffers are flushed immediately when they reach 10MB
3. **Final flushing**: Any remaining buffer data is flushed when the process exits
4. **Separate streams**: Stdout and stderr are buffered and flushed independently

### Process Lifecycle

1. Process starts and PID is stored
2. Stdout/stderr are read concurrently into separate buffers
3. StreamParts are yielded based on time/size triggers
4. On context cancellation, the configured Cancellor is used
5. Process exit is handled with appropriate error types

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

### Building

```bash
go build .
```

### Examples

See the `example/` directory for usage examples:

- `example/simple/`: Basic echo example
- `example/stream/`: Streaming output example
- `example/burst/`: Large buffer testing

## Requirements

- Go 1.23+ (for `iter` package support)
- Unix-like system (for signal handling in LocalCancellor)

## License

This project is part of the console-stream library by wolfeidau.