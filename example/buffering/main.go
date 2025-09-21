package main

import (
	"context"
	"fmt"
	"time"

	consolestream "github.com/wolfeidau/console-stream"
)

func main() {
	fmt.Println("=== Buffer Configuration Examples ===")

	// Example 1: High-frequency updates (faster feedback)
	fmt.Println("\n1. High-frequency updates (100ms flush interval):")

	process1 := consolestream.NewPipeProcess("echo", []string{"Fast feedback example"},
		consolestream.WithFlushInterval(100*time.Millisecond))

	ctx := context.Background()
	for part, err := range process1.ExecuteAndStream(ctx) {
		if err != nil {
			break
		}
		switch event := part.Event.(type) {
		case *consolestream.PipeOutputData:
			fmt.Printf("[FAST] %s: %s", part.Timestamp.Format("15:04:05.000"), string(event.Data))
		case *consolestream.ProcessEnd:
			fmt.Printf("Process completed in %v\n", event.Duration)
		}
	}

	// Example 2: Large buffer for batch processing
	fmt.Println("\n2. Large buffer configuration (1MB limit):")

	process2 := consolestream.NewPipeProcess("echo", []string{"Large buffer example"},
		consolestream.WithMaxBufferSize(1024*1024)) // 1MB buffer

	for part, err := range process2.ExecuteAndStream(ctx) {
		if err != nil {
			break
		}
		switch event := part.Event.(type) {
		case *consolestream.PipeOutputData:
			fmt.Printf("[LARGE] %s: %s", part.Timestamp.Format("15:04:05.000"), string(event.Data))
		case *consolestream.ProcessEnd:
			fmt.Printf("Process completed in %v\n", event.Duration)
		}
	}

	// Example 3: Custom configuration for specific needs
	fmt.Println("\n3. Custom configuration (2s interval, 512KB buffer):")

	process3 := consolestream.NewPipeProcess("echo", []string{"Custom configuration example"},
		consolestream.WithFlushInterval(2*time.Second),
		consolestream.WithMaxBufferSize(512*1024)) // 512KB buffer

	for part, err := range process3.ExecuteAndStream(ctx) {
		if err != nil {
			break
		}
		switch event := part.Event.(type) {
		case *consolestream.PipeOutputData:
			fmt.Printf("[CUSTOM] %s: %s", part.Timestamp.Format("15:04:05.000"), string(event.Data))
		case *consolestream.ProcessEnd:
			fmt.Printf("Process completed in %v\n", event.Duration)
		}
	}

	fmt.Println("\nAll buffer configuration examples completed!")
}
