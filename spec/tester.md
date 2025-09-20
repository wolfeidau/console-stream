# Test Program Specification

## Overview
A test program to exercise the console-stream implementation with various output patterns, timing, and exit scenarios.

## Location
`cmd/tester/main.go`

## Command Line Flags

### Output Control
- `--stdout-rate <int>`: Lines per second to emit to stdout (default: 1)
- `--stderr-rate <int>`: Lines per second to emit to stderr (default: 0)
- `--stdout-size <int>`: Size of each stdout line in bytes (default: 100)
- `--stderr-size <int>`: Size of each stderr line in bytes (default: 100)

### Timing Control
- `--duration <duration>`: How long to run (e.g., "5s", "2m") (default: "10s")
- `--exit-code <int>`: Exit code to return (default: 0)

### Special Scenarios
- `--burst-mb <int>`: Emit a burst of N megabytes immediately (tests 10MB limit)
- `--mixed-output`: Rapidly interleave stdout/stderr output
- `--no-output`: Run for duration but emit no output
- `--delay-start <duration>`: Wait before starting output (default: "0s")

## Test Scenarios

### Basic Streaming
```bash
# Normal output at 5 lines/sec for 10 seconds
./tester --stdout-rate=5 --duration=10s

# Mixed stdout/stderr
./tester --stdout-rate=3 --stderr-rate=2 --duration=5s
```

### Buffer Limit Testing
```bash
# Test 10MB limit with immediate burst
./tester --burst-mb=15

# Test gradual approach to 10MB
./tester --stdout-size=1048576 --stdout-rate=1 --duration=15s
```

### Error Scenarios
```bash
# Non-zero exit codes
./tester --duration=2s --exit-code=1
./tester --duration=2s --exit-code=130  # SIGINT simulation

# No output scenarios
./tester --no-output --duration=5s
```

### Cancellation Testing
```bash
# Long-running process for cancellation testing
./tester --stdout-rate=1 --duration=60s
```

### Rapid Output Testing
```bash
# High-frequency mixed output
./tester --mixed-output --stdout-rate=100 --stderr-rate=50 --duration=3s
```

## Output Format
- Each line should include timestamp and line number
- Stdout format: `[STDOUT] {timestamp} Line {n}: {padding}`
- Stderr format: `[STDERR] {timestamp} Line {n}: {padding}`
- Padding should fill to requested line size

## Implementation Notes
- Use `time.Ticker` for controlled output rates
- Generate predictable content for easier testing
- Flush output appropriately for real-time streaming
- Handle signals gracefully for cancellation testing