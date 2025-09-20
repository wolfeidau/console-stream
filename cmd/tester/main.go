package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

var (
	stdoutRate  = flag.Int("stdout-rate", 1, "Lines per second to emit to stdout")
	stderrRate  = flag.Int("stderr-rate", 0, "Lines per second to emit to stderr")
	stdoutSize  = flag.Int("stdout-size", 100, "Size of each stdout line in bytes")
	stderrSize  = flag.Int("stderr-size", 100, "Size of each stderr line in bytes")
	duration    = flag.Duration("duration", 10*time.Second, "How long to run")
	exitCode    = flag.Int("exit-code", 0, "Exit code to return")
	burstMB     = flag.Int("burst-mb", 0, "Emit a burst of N megabytes immediately")
	mixedOutput = flag.Bool("mixed-output", false, "Rapidly interleave stdout/stderr output")
	noOutput    = flag.Bool("no-output", false, "Run for duration but emit no output")
	delayStart  = flag.Duration("delay-start", 0, "Wait before starting output")
)

func main() {
	flag.Parse()

	// Delay start if requested
	if *delayStart > 0 {
		time.Sleep(*delayStart)
	}

	// Handle burst mode
	if *burstMB > 0 {
		handleBurst()
		os.Exit(*exitCode)
	}

	// Handle no output mode
	if *noOutput {
		time.Sleep(*duration)
		os.Exit(*exitCode)
	}

	// Handle mixed output mode
	if *mixedOutput {
		handleMixedOutput()
		os.Exit(*exitCode)
	}

	// Normal output mode
	handleNormalOutput()
	os.Exit(*exitCode)
}

func handleBurst() {
	burstSize := *burstMB * 1024 * 1024
	chunkSize := 1024 // 1KB chunks
	chunks := burstSize / chunkSize

	data := strings.Repeat("X", chunkSize)

	fmt.Fprintf(os.Stdout, "[STDOUT] %s Starting %dMB burst\n", time.Now().Format("15:04:05.000"), *burstMB)

	for i := 0; i < chunks; i++ {
		fmt.Fprint(os.Stdout, data)
		if i%100 == 0 {
			fmt.Fprintf(os.Stdout, "\n[STDOUT] %s Burst progress: %d/%d chunks\n",
				time.Now().Format("15:04:05.000"), i, chunks)
		}
	}

	fmt.Fprintf(os.Stdout, "\n[STDOUT] %s Completed %dMB burst\n", time.Now().Format("15:04:05.000"), *burstMB)
}

func handleMixedOutput() {
	totalRate := *stdoutRate + *stderrRate
	if totalRate == 0 {
		totalRate = 10 // Default mixed rate
	}

	interval := time.Second / time.Duration(totalRate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	timer := time.NewTimer(*duration)
	defer timer.Stop()

	stdoutCounter := 0
	stderrCounter := 0

	for {
		select {
		case <-timer.C:
			return
		case now := <-ticker.C:
			// Alternate between stdout and stderr
			if stdoutCounter*(*stderrRate) <= stderrCounter*(*stdoutRate) && *stdoutRate > 0 {
				stdoutCounter++
				padding := strings.Repeat("A", *stdoutSize-50) // Reserve space for prefix
				fmt.Fprintf(os.Stdout, "[STDOUT] %s Line %d: %s\n",
					now.Format("15:04:05.000"), stdoutCounter, padding)
			} else if *stderrRate > 0 {
				stderrCounter++
				padding := strings.Repeat("E", *stderrSize-50) // Reserve space for prefix
				fmt.Fprintf(os.Stderr, "[STDERR] %s Line %d: %s\n",
					now.Format("15:04:05.000"), stderrCounter, padding)
			}
		}
	}
}

func handleNormalOutput() {
	var stdoutTicker, stderrTicker *time.Ticker

	if *stdoutRate > 0 {
		stdoutTicker = time.NewTicker(time.Second / time.Duration(*stdoutRate))
		defer stdoutTicker.Stop()
	}

	if *stderrRate > 0 {
		stderrTicker = time.NewTicker(time.Second / time.Duration(*stderrRate))
		defer stderrTicker.Stop()
	}

	timer := time.NewTimer(*duration)
	defer timer.Stop()

	stdoutCounter := 0
	stderrCounter := 0

	for {
		select {
		case <-timer.C:
			return

		case now := <-func() <-chan time.Time {
			if stdoutTicker != nil {
				return stdoutTicker.C
			}
			return make(chan time.Time) // Never sends
		}():
			stdoutCounter++
			paddingLen := *stdoutSize - 50 // Reserve space for prefix
			if paddingLen < 0 {
				paddingLen = 0
			}
			padding := strings.Repeat("A", paddingLen)
			fmt.Fprintf(os.Stdout, "[STDOUT] %s Line %d: %s\n",
				now.Format("15:04:05.000"), stdoutCounter, padding)

		case now := <-func() <-chan time.Time {
			if stderrTicker != nil {
				return stderrTicker.C
			}
			return make(chan time.Time) // Never sends
		}():
			stderrCounter++
			paddingLen := *stderrSize - 50 // Reserve space for prefix
			if paddingLen < 0 {
				paddingLen = 0
			}
			padding := strings.Repeat("E", paddingLen)
			fmt.Fprintf(os.Stderr, "[STDERR] %s Line %d: %s\n",
				now.Format("15:04:05.000"), stderrCounter, padding)
		}
	}
}
