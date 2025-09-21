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

	// Create a process that will generate output over time
	process := consolestream.NewProcess("go", cancellor, "run", "cmd/tester/main.go", "--duration=5s", "--stdout-rate=3", "--stderr-rate=2")

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Execute and stream
	fmt.Println("Starting streaming test...")
	partCount := 0
	outputCount := 0
	for part, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		partCount++
		switch event := part.Event.(type) {
		case *consolestream.OutputData:
			outputCount++
			fmt.Printf("Part %d [%s] %s: %d bytes\n%s\n",
				outputCount, event.Stream.String(), part.Timestamp.Format("15:04:05.000"), len(event.Data), string(event.Data))
		case *consolestream.ProcessStart:
			fmt.Printf("Event %d: Process started (PID: %d, Command: %s)\n", partCount, event.PID, event.Command)
		case *consolestream.ProcessEnd:
			fmt.Printf("Event %d: Process completed (Exit Code: %d, Duration: %v, Success: %t)\n",
				partCount, event.ExitCode, event.Duration, event.Success)
		case *consolestream.ProcessError:
			fmt.Printf("Event %d: Process error: %s\n", partCount, event.Message)
		case *consolestream.HeartbeatEvent:
			fmt.Printf("Event %d: Heartbeat (Elapsed: %v)\n", partCount, event.ElapsedTime)
		}
	}

	fmt.Printf("Streaming completed. Total events: %d, Output parts: %d\n", partCount, outputCount)
}
