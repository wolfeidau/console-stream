package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/creack/pty"
	consolestream "github.com/wolfeidau/console-stream"
)

func main() {
	// Create a local cancellor with 10 second timeout
	cancellor := consolestream.NewLocalCancellor(10 * time.Second)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("=== Example 1: Basic PTY Process ===")

	// Create a PTY process that will show colors
	process1 := consolestream.NewPTYProcess("echo", []string{"-e", "\\033[31mRed text\\033[0m and \\033[32mGreen text\\033[0m"}, consolestream.WithCancellor(cancellor))

	for part, err := range process1.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		switch part.EventType() {
		case consolestream.PTYOutputEvent:
			event := part.Event.(*consolestream.PTYOutputData)
			fmt.Printf("[PTY] %s: %q\n", part.Timestamp.Format("15:04:05.000"), string(event.Data))
		case consolestream.ProcessStartEvent:
			event := part.Event.(*consolestream.ProcessStart)
			fmt.Printf("PTY Process started (PID: %d)\n", event.PID)
		case consolestream.ProcessEndEvent:
			event := part.Event.(*consolestream.ProcessEnd)
			fmt.Printf("PTY Process completed (Exit Code: %d, Duration: %v)\n", event.ExitCode, event.Duration)
		case consolestream.ProcessErrorEvent:
			event := part.Event.(*consolestream.ProcessError)
			fmt.Printf("PTY Process error: %s\n", event.Message)
		case consolestream.HeartbeatEventType:
			// Silently ignore heartbeats for now
		}
	}

	fmt.Printf("\n=== Example 2: PTY Process with Custom Size ===")

	// Create a PTY process with specific terminal size
	size := pty.Winsize{Rows: 24, Cols: 80}
	process2 := consolestream.NewPTYProcess("bash", []string{"-c", "echo 'Terminal size: '$(tput cols)'x'$(tput lines)"}, consolestream.WithCancellor(cancellor), consolestream.WithPTYSize(size))

	for part, err := range process2.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		switch part.EventType() {
		case consolestream.PTYOutputEvent:
			event := part.Event.(*consolestream.PTYOutputData)
			fmt.Printf("[PTY-SIZED] %s: %s", part.Timestamp.Format("15:04:05.000"), string(event.Data))
		case consolestream.ProcessStartEvent:
			event := part.Event.(*consolestream.ProcessStart)
			fmt.Printf("PTY Process with size started (PID: %d)\n", event.PID)
		case consolestream.ProcessEndEvent:
			event := part.Event.(*consolestream.ProcessEnd)
			fmt.Printf("PTY Process with size completed (Exit Code: %d)\n", event.ExitCode)
		case consolestream.ProcessErrorEvent:
			event := part.Event.(*consolestream.ProcessError)
			fmt.Printf("PTY Process error: %s\n", event.Message)
		case consolestream.HeartbeatEventType:
			// Silently ignore heartbeats
		}
	}

	fmt.Printf("\n=== Example 3: Interactive Command Simulation ===")

	// Simulate an interactive command that might use colors or progress bars
	process3 := consolestream.NewPTYProcess("bash", []string{"-c", `
echo -e "\033[32mStarting task...\033[0m"
for i in {1..5}; do
    echo -e "\033[34mProgress: $i/5\033[0m"
    sleep 0.5
done
echo -e "\033[32mCompleted!\033[0m"
`}, consolestream.WithCancellor(cancellor))

	fmt.Println("Running interactive command with colors...")
	eventCount := 0
	for part, err := range process3.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("Error: %v", err)
			break
		}

		eventCount++

		switch part.EventType() {
		case consolestream.PTYOutputEvent:
			event := part.Event.(*consolestream.PTYOutputData)
			// Display raw output to show ANSI sequences are preserved
			fmt.Printf("Event %d [PTY-INTERACTIVE] %s: %q\n", eventCount, part.Timestamp.Format("15:04:05.000"), string(event.Data))
		case consolestream.ProcessStartEvent:
			event := part.Event.(*consolestream.ProcessStart)
			fmt.Printf("Event %d: Interactive process started (PID: %d)\n", eventCount, event.PID)
		case consolestream.ProcessEndEvent:
			event := part.Event.(*consolestream.ProcessEnd)
			fmt.Printf("Event %d: Interactive process completed (Exit Code: %d, Duration: %v)\n", eventCount, event.ExitCode, event.Duration)
		case consolestream.HeartbeatEventType:
			event := part.Event.(*consolestream.HeartbeatEvent)
			fmt.Printf("Event %d: [HEARTBEAT] Process alive (Elapsed: %v)\n", eventCount, event.ElapsedTime)
		}
	}

	fmt.Printf("\nCompleted all PTY examples!\n")
}
