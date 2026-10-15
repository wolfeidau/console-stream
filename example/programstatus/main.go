// Command programstatus shows both sides of OSC 7501 program status: a child
// that reports its status with the project's internal/osc7501 emitter, and a
// parent that receives the reports as ProgramStatus events and stops the child
// when it blocks on a prompt. Programs outside this module cannot import
// internal packages; they can use github.com/charmbracelet/x/ansi
// (SetProgramStatus) or write the sequence directly.
//
// The parent runs this same binary in "child" mode under a PTY:
//
//	go run ./example/programstatus
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	consolestream "github.com/wolfeidau/console-stream"
	"github.com/wolfeidau/console-stream/internal/osc7501"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		child()
		return
	}
	parent()
}

// child is the monitored program. It reports status by writing OSC 7501
// sequences to stdout, the same stream as its normal output.
func child() {
	rep := osc7501.NewReporter(os.Stdout, "demo")

	for p := 0; p <= 100; p += 25 {
		_ = rep.Working("build", p, "compiling")
		fmt.Printf("build %d%%\n", p)
		time.Sleep(200 * time.Millisecond)
	}
	_ = rep.Done("build", "")
	fmt.Println("build finished")

	// Waiting for a person. Unattended, nobody answers, so the parent stops us.
	_ = rep.Blocked("deploy", osc7501.KindPermission, "deploy to prod?")
	fmt.Print("deploy to prod? [y/N] ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	_ = rep.Done("deploy", "")
}

// parent runs the child under a PTY with program status parsing enabled.
func parent() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("find executable: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	process := consolestream.NewProcess(exe, []string{"child"},
		consolestream.WithPTYMode(),
		consolestream.WithProgramStatus())

	blocked := false
	for event, err := range process.ExecuteAndStream(ctx) {
		if err != nil {
			log.Printf("error: %v", err)
			break
		}
		switch e := event.Event.(type) {
		case *consolestream.OutputData:
			// Report bytes pass through unchanged; %q makes them visible.
			fmt.Printf("[OUTPUT] %q\n", e.Data)
		case *consolestream.ProgramStatus:
			fmt.Printf("[STATUS] state=%s id=%s kind=%s progress=%d msg=%q\n",
				e.State, e.ID, e.Kind, e.Progress, e.Msg)
			if e.State == osc7501.StateBlocked {
				fmt.Printf("blocked (%s): %s; stopping the child\n", e.Kind, e.Msg)
				blocked = true
				cancel()
			}
		case *consolestream.ProcessEnd:
			fmt.Printf("exit code %d after %s\n", e.ExitCode, e.Duration.Round(time.Millisecond))
		case *consolestream.ProcessError:
			fmt.Printf("process error: %s\n", e.Message)
		}
	}
	// Cancelling ctx stops the child and ends the stream without a ProcessEnd.
	if blocked {
		fmt.Println("child stopped while blocked")
	}
}
