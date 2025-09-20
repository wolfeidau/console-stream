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
		fmt.Printf("Part %d [%s] %s: %d bytes\n%s\n",
			partCount, streamType, part.Timestamp.Format("15:04:05.000"), len(part.Data), string(part.Data))
	}

	fmt.Printf("Streaming completed. Total parts received: %d\n", partCount)
}
