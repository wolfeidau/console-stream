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

	// Create a process that will generate a large burst
	process := consolestream.NewProcess("go", []string{"run", "cmd/tester/main.go", "--burst-mb=15"}, consolestream.WithCancellor(cancellor))

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Execute and stream
	fmt.Println("Starting burst test (15MB)...")
	partCount := 0
	outputCount := 0
	totalBytes := 0
	for part, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		partCount++
		switch event := part.Event.(type) {
		case *consolestream.OutputData:
			outputCount++
			totalBytes += len(event.Data)
			fmt.Printf("Part %d [OUTPUT] %s: %d bytes (total: %d bytes, %.2f MB)\n",
				outputCount, part.Timestamp.Format("15:04:05.000"), len(event.Data), totalBytes, float64(totalBytes)/(1024*1024))
		case *consolestream.ProcessStart:
			fmt.Printf("Event %d: Process started (PID: %d)\n", partCount, event.PID)
		case *consolestream.ProcessEnd:
			fmt.Printf("Event %d: Process completed (Duration: %v)\n", partCount, event.Duration)
		case *consolestream.ProcessError:
			fmt.Printf("Event %d: Process error: %s\n", partCount, event.Message)
		case *consolestream.HeartbeatEvent:
			// Ignore heartbeats in burst test
		}
	}

	fmt.Printf("Burst test completed. Total events: %d, Output parts: %d, Total bytes: %d (%.2f MB)\n",
		partCount, outputCount, totalBytes, float64(totalBytes)/(1024*1024))
}
