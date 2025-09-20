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

		streamType := "STDOUT"
		if part.Stream == consolestream.Stderr {
			streamType = "STDERR"
		}

		fmt.Printf("[%s] %s: %s", streamType, part.Timestamp.Format("15:04:05"), string(part.Data))
	}

	fmt.Println("Process completed.")
}
