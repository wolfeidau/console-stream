package main

import (
	"context"
	"fmt"
	"log"
	"time"

	consolestream "github.com/wolfeidau/console-stream"
)

func main() {
	// Create a local cancellor with 5 second timeout
	cancellor := consolestream.NewLocalCancellor(5 * time.Second)

	// Create a process that will echo some text
	process := consolestream.NewProcess("echo", cancellor, "Hello, World!")

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute and stream
	fmt.Println("Starting process...")
	for part, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		switch part.EventType() {
		case consolestream.OutputEvent:
			event := part.Event.(*consolestream.OutputData)
			fmt.Printf("[%s] %s: %s", event.Stream.String(), part.Timestamp.Format("15:04:05"), string(event.Data))
		case consolestream.ProcessStartEvent:
			event := part.Event.(*consolestream.ProcessStart)
			fmt.Printf("Process started (PID: %d)\n", event.PID)
		case consolestream.ProcessEndEvent:
			event := part.Event.(*consolestream.ProcessEnd)
			fmt.Printf("Process completed (Exit Code: %d, Duration: %v)\n", event.ExitCode, event.Duration)
		case consolestream.ProcessErrorEvent:
			event := part.Event.(*consolestream.ProcessError)
			fmt.Printf("Process error: %s\n", event.Message)
		case consolestream.HeartbeatEventType:
			// Silently ignore heartbeats in simple example
		}
	}
}
