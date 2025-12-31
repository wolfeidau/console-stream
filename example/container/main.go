package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	consolestream "github.com/wolfeidau/console-stream"
)

func main() {
	fmt.Println("=== Example 1: Simple Container Execution ===")
	simpleExample()

	fmt.Println("\n=== Example 2: Container with Mount ===")
	mountExample()

	fmt.Println("\n=== Example 3: Container with Environment Variables ===")
	envExample()
}

func simpleExample() {
	process := consolestream.NewContainerProcess("echo", []string{"Hello from container!"},
		consolestream.WithContainerImage("alpine:latest"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for event, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		switch event.EventType() {
		case consolestream.ContainerCreateEvent:
			e := event.Event.(*consolestream.ContainerCreate)
			fmt.Printf("Container created: %s (Image: %s)\n", e.ContainerID[:12], e.Image)
		case consolestream.ProcessStartEvent:
			e := event.Event.(*consolestream.ProcessStart)
			fmt.Printf("Process started (PID: %d)\n", e.PID)
		case consolestream.OutputEvent:
			e := event.Event.(*consolestream.OutputData)
			fmt.Printf("[OUTPUT] %s", string(e.Data))
		case consolestream.ProcessEndEvent:
			e := event.Event.(*consolestream.ProcessEnd)
			fmt.Printf("Process completed (Exit Code: %d, Duration: %v)\n", e.ExitCode, e.Duration)
		case consolestream.ContainerRemoveEvent:
			e := event.Event.(*consolestream.ContainerRemove)
			fmt.Printf("Container removed: %s\n", e.ContainerID[:12])
		}
	}
}

func mountExample() {
	// Create a temporary file to demonstrate mounting
	tmpFile, err := os.CreateTemp("", "example-*.txt")
	if err != nil {
		log.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write some content to the file
	content := "Hello from the host filesystem!"
	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		log.Printf("Failed to write to temp file: %v", err)
		return
	}
	tmpFile.Close()

	// Mount the temp directory into the container
	tmpDir := filepath.Dir(tmpFile.Name())
	tmpFileName := filepath.Base(tmpFile.Name())

	process := consolestream.NewContainerProcess("cat", []string{"/workspace/" + tmpFileName},
		consolestream.WithContainerImage("alpine:latest"),
		consolestream.WithContainerMount(tmpDir, "/workspace", true)) // Read-only mount

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for event, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		if event.EventType() == consolestream.OutputEvent {
			e := event.Event.(*consolestream.OutputData)
			fmt.Printf("[OUTPUT] %s", string(e.Data))
		}

		if event.EventType() == consolestream.ProcessEndEvent {
			e := event.Event.(*consolestream.ProcessEnd)
			fmt.Printf("Exit code: %d\n", e.ExitCode)
			break
		}
	}
}

func envExample() {
	process := consolestream.NewContainerProcess("sh", []string{"-c", "echo Name: $NAME, Role: $ROLE"},
		consolestream.WithContainerImage("alpine:latest"),
		consolestream.WithContainerEnvMap(map[string]string{
			"NAME": "Docker Container",
			"ROLE": "Example",
		}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for event, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		switch event.EventType() {
		case consolestream.OutputEvent:
			e := event.Event.(*consolestream.OutputData)
			fmt.Printf("[OUTPUT] %s", string(e.Data))
		case consolestream.ProcessEndEvent:
			e := event.Event.(*consolestream.ProcessEnd)
			if e.Success {
				fmt.Println("Completed successfully!")
			} else {
				fmt.Printf("Failed with exit code: %d\n", e.ExitCode)
			}
			return
		}
	}
}
