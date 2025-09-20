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
	process := consolestream.NewProcess("go", cancellor, "run", "cmd/tester/main.go", "--burst-mb=15")

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Execute and stream
	fmt.Println("Starting burst test (15MB)...")
	partCount := 0
	totalBytes := 0
	for part, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		streamType := "STDOUT"
		if part.Stream == consolestream.Stderr {
			streamType = "STDERR"
		}

		partCount++
		totalBytes += len(part.Data)
		fmt.Printf("Part %d [%s] %s: %d bytes (total: %d bytes, %.2f MB)\n",
			partCount, streamType, part.Timestamp.Format("15:04:05.000"), len(part.Data), totalBytes, float64(totalBytes)/(1024*1024))
	}

	fmt.Printf("Burst test completed. Total parts: %d, Total bytes: %d (%.2f MB)\n",
		partCount, totalBytes, float64(totalBytes)/(1024*1024))
}
