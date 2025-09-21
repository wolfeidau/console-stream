package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	consolestream "github.com/wolfeidau/console-stream"
)

func main() {
	// Create a local cancellor with 5 second timeout
	cancellor := consolestream.NewLocalCancellor(5 * time.Second)

	// Create a process that will generate various events
	process := consolestream.NewPipeProcess("go", cancellor, "run", "cmd/tester/main.go", "--duration=3s", "--stdout-rate=2", "--stderr-rate=1")

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Println("=== Example 1: Uniform Event Handling with EventType() ===")

	// Execute and stream - log all events uniformly
	eventCount := 0
	for part, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		eventCount++

		// Example of uniform handling using EventType()
		switch part.EventType() {
		case consolestream.PipeOutputEvent, consolestream.ProcessStartEvent,
			consolestream.ProcessEndEvent, consolestream.ProcessErrorEvent:
			// Log all important events
			fmt.Printf("Event %d: %s\n", eventCount, part.String())
		case consolestream.HeartbeatEventType:
			// Just count heartbeats
			fmt.Printf("Event %d: [HEARTBEAT] Process alive (Elapsed: %v)\n",
				eventCount, part.Event.(*consolestream.HeartbeatEvent).ElapsedTime)
		}
	}

	fmt.Printf("\n=== Example 2: JSON Serialization using EventType() ===")

	// Create another process for JSON example
	process2 := consolestream.NewPipeProcess("echo", cancellor, "JSON example")

	for part, err := range process2.ExecuteAndStream(ctx) {
		if err != nil {
			break
		}

		// Create a JSON-friendly structure using EventType()
		eventData := map[string]any{
			"timestamp": part.Timestamp,
			"type":      part.EventType().String(),
			"event":     part.Event,
		}

		if jsonBytes, err := json.MarshalIndent(eventData, "", "  "); err == nil {
			fmt.Printf("JSON Event:\n%s\n", string(jsonBytes))
		}

		// Stop after process completes
		if part.EventType() == consolestream.ProcessEndEvent {
			break
		}
	}

	fmt.Printf("\n=== Example 3: Event Filtering using EventType() ===")

	// Create another process for filtering example
	process3 := consolestream.NewPipeProcess("go", cancellor, "run", "cmd/tester/main.go", "--duration=2s", "--stdout-rate=3")

	outputEventCount := 0
	for part, err := range process3.ExecuteAndStream(ctx) {
		if err != nil {
			break
		}

		// Only process output events, ignore lifecycle and heartbeats
		if part.EventType() == consolestream.PipeOutputEvent {
			outputEventCount++
			event := part.Event.(*consolestream.PipeOutputData)
			fmt.Printf("Output %d [%s]: %d bytes\n",
				outputEventCount, event.Stream.String(), len(event.Data))
		}

		// Stop after process completes
		if part.EventType() == consolestream.ProcessEndEvent {
			break
		}
	}

	fmt.Printf("\nCompleted all examples!\n")
}
