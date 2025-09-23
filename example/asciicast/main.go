package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/creack/pty"
	consolestream "github.com/wolfeidau/console-stream"
)

func main() {
	fmt.Println("=== Asciicast v3 Export Example ===")

	// Create cancellor for all examples
	cancellor := consolestream.NewLocalCancellor(5 * time.Second)

	// Example 1: Basic PTY session to asciicast
	fmt.Println("\n1. Recording a simple command to asciicast...")

	// Create PTY process with known terminal size
	size := pty.Winsize{Rows: 24, Cols: 80}
	process := consolestream.NewProcess("echo", []string{"Hello from asciicast!"},
		consolestream.WithCancellor(cancellor),
		consolestream.WithPTYMode(),
		consolestream.WithPTYSize(size))

	// Execute and get events
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := process.ExecuteAndStream(ctx)

	// Transform to asciicast v3 format
	metadata := consolestream.AscicastV3Metadata{
		Term: consolestream.TermInfo{
			Cols: int(size.Cols),
			Rows: int(size.Rows),
		},
		Command: "echo 'Hello from asciicast!'",
		Title:   "Simple Echo Example",
		Env: map[string]string{
			"TERM": "xterm-256color",
		},
		Tags: []string{"demo", "echo"},
	}

	asciicast := consolestream.ToAscicastV3(events, metadata)

	// Write to file
	file, err := os.Create("simple_echo.cast")
	if err != nil {
		fmt.Printf("Error creating file: %v\n", err)
		return
	}
	defer file.Close()

	lineCount := 0
	for line, err := range asciicast {
		if err != nil {
			fmt.Printf("Error processing event: %v\n", err)
			break
		}

		data, err := line.MarshalJSON()
		if err != nil {
			fmt.Printf("Error marshaling JSON: %v\n", err)
			break
		}

		// Write JSON line
		if _, err := file.Write(data); err != nil {
			fmt.Printf("Error writing to file: %v\n", err)
			break
		}
		if _, err := file.WriteString("\n"); err != nil {
			fmt.Printf("Error writing newline: %v\n", err)
			break
		}

		lineCount++
		fmt.Printf("  Wrote line %d: %s\n", lineCount, string(data))
	}

	fmt.Printf("Recorded %d lines to simple_echo.cast\n", lineCount)

	// Example 2: Interactive command with colors
	fmt.Println("\n2. Recording an interactive command with colors...")

	process2 := consolestream.NewProcess("bash", []string{"-c", `
echo -e "\033[32mStarting colored demo...\033[0m"
echo -e "\033[34mThis is blue text\033[0m"
echo -e "\033[31mThis is red text\033[0m"
echo -e "\033[33mDemo completed!\033[0m"
`}, consolestream.WithCancellor(cancellor), consolestream.WithPTYSize(size))

	events2 := process2.ExecuteAndStream(ctx)

	metadata2 := consolestream.AscicastV3Metadata{
		Term: consolestream.TermInfo{
			Cols: int(size.Cols),
			Rows: int(size.Rows),
		},
		Command: "bash -c 'colored demo script'",
		Title:   "Colored Output Demo",
		Tags:    []string{"demo", "colors", "bash"},
	}

	// Use the convenience function to write directly
	file2, err := os.Create("colored_demo.cast")
	if err != nil {
		fmt.Printf("Error creating file: %v\n", err)
		return
	}
	defer file2.Close()

	err = consolestream.WriteAscicastV3(events2, metadata2, func(data []byte) error {
		_, err := file2.Write(data)
		return err
	})

	if err != nil {
		fmt.Printf("Error writing asciicast: %v\n", err)
	} else {
		fmt.Println("Successfully recorded colored_demo.cast")
	}

	// Example 3: Show terminal resize handling
	fmt.Println("\n3. Demonstrating terminal resize events...")

	// Start with smaller size
	smallSize := pty.Winsize{Rows: 20, Cols: 60}
	process3 := consolestream.NewProcess("bash", []string{"-c", "tput cols && tput lines && echo 'Terminal info displayed'"},
		consolestream.WithCancellor(cancellor),
		consolestream.WithPTYSize(smallSize))

	events3 := process3.ExecuteAndStream(ctx)

	metadata3 := consolestream.AscicastV3Metadata{
		Term: consolestream.TermInfo{
			Cols: int(smallSize.Cols),
			Rows: int(smallSize.Rows),
		},
		Command: "tput cols && tput lines",
		Title:   "Terminal Size Demo",
		Tags:    []string{"demo", "resize"},
	}

	// Count different event types
	outputEvents := 0
	resizeEvents := 0
	exitEvents := 0

	for line, err := range consolestream.ToAscicastV3(events3, metadata3) {
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		data, _ := line.MarshalJSON()
		lineStr := string(data)

		switch lineStr[0] {
		case '{':
			fmt.Printf("  Header: %s\n", lineStr)
		case '[':
			// Parse event type from JSON array
			eventType := lineStr[len(lineStr)-5 : len(lineStr)-2]
			switch eventType {
			case `"o"`:
				outputEvents++
			case `"r"`:
				resizeEvents++
			case `"x"`:
				exitEvents++
			}
		}
	}

	fmt.Printf("  Events recorded: %d output, %d resize, %d exit\n", outputEvents, resizeEvents, exitEvents)

	fmt.Println("\n=== Examples completed! ===")
	fmt.Println("Generated files:")
	fmt.Println("  - simple_echo.cast")
	fmt.Println("  - colored_demo.cast")
	fmt.Println("\nThese can be played back with: asciinema play <filename>")
}
