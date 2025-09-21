# Asciicast v3 Export Implementation

## Overview

This specification documents the implementation of asciicast v3 format export functionality for the console-stream library. The implementation provides a standalone iterator transformer that converts console-stream events into asciicast v3 format without coupling to the core PTY/Pipe process structs.

## Architecture

### Design Principles

1. **Separation of Concerns**: Asciicast export is implemented as a pure transformation layer operating on iterators
2. **Composability**: Uses Go 1.23+ iterator pattern for clean stream processing pipelines
3. **Reusability**: Works with any `iter.Seq2[Event, error]` source
4. **Format Compliance**: Implements asciicast v3 specification exactly

### Core Components

```go
// Primary transformer function
func ToAscicastV3(events iter.Seq2[Event, error], metadata AscicastV3Metadata) iter.Seq2[AscicastV3Line, error]

// Supporting types
type AscicastV3Line interface {
    MarshalJSON() ([]byte, error)
}

type AscicastV3Header struct {
    Version int `json:"version"`
    AscicastV3Metadata
}

type AscicastV3Event [3]any // [interval, code, data]
```

## Event Mapping

### Supported Mappings

| Console Stream Event | Asciicast v3 Event | Description |
|---------------------|-------------------|-------------|
| `PTYOutputData` | `["interval", "o", "data"]` | Terminal output with ANSI sequences preserved |
| `TerminalResizeEvent` | `["interval", "r", "cols rows"]` | Terminal window resize |
| `ProcessEnd` | `["interval", "x", "exit_code"]` | Process exit with status code |

### Filtered Events

The following console-stream events are filtered out as they don't map to asciicast format:

- `ProcessStart` - Internal lifecycle event
- `ProcessError` - Internal error event
- `HeartbeatEvent` - Keep-alive signal
- `PipeOutputData` - Pipe-specific output (doesn't fit terminal session model)

## Format Specification Compliance

### Header Format

```json
{
  "version": 3,
  "term": {
    "cols": 80,
    "rows": 24
  },
  "timestamp": "2024-01-01T00:00:00Z",
  "command": "bash -l",
  "title": "Terminal Session",
  "env": {
    "TERM": "xterm-256color"
  },
  "tags": ["demo", "example"]
}
```

### Event Format

```json
[0.248848, "o", "Hello World!"]
[1.001376, "r", "120 30"]
[2.543210, "x", "0"]
```

## Implementation Details

### Session State Tracking

- Session start time is captured from the first event timestamp
- All subsequent events calculate relative intervals from session start
- State is maintained within the iterator closure

### Timestamp Calculation

```go
interval := event.Timestamp.Sub(sessionStart).Seconds()
```

### JSON Lines Format

- Header is emitted as the first line
- Each event is emitted as a separate JSON line
- Newlines separate each JSON object
- UTF-8 encoding throughout

## Usage Examples

### Basic Usage

```go
process := consolestream.NewPTYProcess("vim", []string{"README.md"})
events := process.ExecuteAndStream(ctx)

asciicast := consolestream.ToAscicastV3(events, consolestream.AscicastV3Metadata{
    Term:    consolestream.TermInfo{Cols: 80, Rows: 24},
    Command: "vim README.md",
    Title:   "Editing README",
})

for line, err := range asciicast {
    json.NewEncoder(file).Encode(line)
}
```

### Convenience Function

```go
err := consolestream.WriteAscicastV3(events, metadata, func(data []byte) error {
    return file.Write(data)
})
```

## Limitations

### Current Limitations

1. **Input Events**: Asciicast v3 supports `"i"` (input) events, but console-stream only captures output
2. **Marker Events**: Asciicast v3 supports `"m"` (marker) events for annotations, not currently implemented
3. **PTY Only**: Only `PTYProcess` output makes sense for asciicast (unified terminal output)

### Future Enhancements

1. **Input Capture**: Could add PTY input monitoring for complete session recording
2. **Marker Injection**: Could add API for inserting custom markers during recording
3. **Compression**: Could add compression support for large recordings
4. **Validation**: Could add asciicast v3 format validation

## Testing

### Test Coverage

- [ ] Header generation with all metadata fields
- [ ] Event transformation for each supported event type
- [ ] Timestamp interval calculation accuracy
- [ ] JSON Lines format compliance
- [ ] Error propagation through iterator chain
- [ ] Large session handling

### Example Test

```go
func TestToAscicastV3(t *testing.T) {
    events := mockEventStream()
    metadata := AscicastV3Metadata{
        Term: TermInfo{Cols: 80, Rows: 24},
    }

    lines := collectLines(ToAscicastV3(events, metadata))

    assert.Equal(t, 1, countHeaders(lines))
    assert.True(t, hasExitEvent(lines))
    assert.JSONEq(t, expectedHeader, lines[0])
}
```

## File Structure

```
/Users/markw/Code/notgopath/console-stream/
├── asciicast.go                    # Core implementation
├── example/asciicast/main.go       # Demonstration examples
└── spec/asciicast_v3.md           # This specification
```

## References

- [Asciicast v3 Format Specification](https://docs.asciinema.org/manual/asciicast/v3/)
- [Go 1.23+ Iterator Patterns](https://go.dev/blog/iter)
- [JSON Lines Format](https://jsonlines.org/)