package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/creack/pty"
	consolestream "github.com/wolfeidau/console-stream"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Command     string            `yaml:"command"`
	Args        []string          `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	ProcessType string            `yaml:"process_type"` // "pipe" or "pty"
	Timeout     string            `yaml:"timeout"`      // e.g., "30s"
	PTYSize     *PTYSize          `yaml:"pty_size,omitempty"`
}

type PTYSize struct {
	Rows int `yaml:"rows"`
	Cols int `yaml:"cols"`
}

type ExecFlags struct {
	Config  string
	Format  string
	Output  string
	Timeout string
	Verbose bool
	Quiet   bool
}

// ExecutionStats tracks metrics during execution
type ExecutionStats struct {
	StartTime   time.Time
	EventCount  int
	ErrorCount  int
	OutputBytes int64
	ExitCode    *int
	Duration    time.Duration
}

// setupLogger initializes the pretty log handler based on flags
func setupLogger(verbose, quiet bool) *slog.Logger {
	var level slog.Level
	switch {
	case quiet:
		level = slog.LevelError
	case verbose:
		level = slog.LevelDebug
	default:
		level = slog.LevelInfo
	}

	handler := NewHandler(&slog.HandlerOptions{
		Level: level,
	})

	return slog.New(handler)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <subcommand> [flags]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nAvailable subcommands:\n")
		fmt.Fprintf(os.Stderr, "  exec    Execute a command using YAML configuration\n")
		os.Exit(1)
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "exec":
		execCommand()
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subcommand)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Printf("Console Stream Runner\n\n")
	fmt.Printf("Usage: %s <subcommand> [flags]\n\n", os.Args[0])
	fmt.Printf("Available subcommands:\n")
	fmt.Printf("  exec    Execute a command using YAML configuration\n\n")
	fmt.Printf("Use '%s <subcommand> --help' for more information about a subcommand.\n", os.Args[0])
}

func execCommand() {
	var flags ExecFlags
	execFS := flag.NewFlagSet("exec", flag.ExitOnError)

	execFS.StringVar(&flags.Config, "config", "", "YAML configuration file (required)")
	execFS.StringVar(&flags.Config, "c", "", "YAML configuration file (required)")
	execFS.StringVar(&flags.Format, "format", "text", "Output format: text, json, asciicast")
	execFS.StringVar(&flags.Format, "f", "text", "Output format: text, json, asciicast")
	execFS.StringVar(&flags.Output, "output", "", "Output file (default: stdout)")
	execFS.StringVar(&flags.Output, "o", "", "Output file (default: stdout)")
	execFS.StringVar(&flags.Timeout, "timeout", "", "Timeout override (e.g., 30s)")
	execFS.StringVar(&flags.Timeout, "t", "", "Timeout override (e.g., 30s)")
	execFS.BoolVar(&flags.Verbose, "verbose", false, "Enable verbose logging")
	execFS.BoolVar(&flags.Verbose, "v", false, "Enable verbose logging")
	execFS.BoolVar(&flags.Quiet, "quiet", false, "Suppress info logs (errors only)")
	execFS.BoolVar(&flags.Quiet, "q", false, "Suppress info logs (errors only)")

	execFS.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s exec [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Execute a command using YAML configuration\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		execFS.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  %s exec --config task.yaml\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s exec --config task.yaml --format asciicast --output demo.cast\n", os.Args[0])
	}

	if err := execFS.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	if flags.Config == "" {
		fmt.Fprintf(os.Stderr, "Error: --config flag is required\n\n")
		execFS.Usage()
		os.Exit(1)
	}

	// Setup logger based on flags
	logger := setupLogger(flags.Verbose, flags.Quiet)

	if err := runExec(flags, logger); err != nil {
		logger.Error("Execution failed", "error", err)
		os.Exit(1)
	}
}

func runExec(flags ExecFlags, logger *slog.Logger) error {
	// Initialize execution stats
	stats := &ExecutionStats{StartTime: time.Now()}

	outputDest := "stdout"
	if flags.Output != "" {
		outputDest = flags.Output
	}

	logger.Info("Starting runner execution",
		"config", flags.Config,
		"format", flags.Format,
		"output", outputDest,
		"timeout", flags.Timeout)

	// Load and parse YAML configuration
	config, err := loadConfig(flags.Config)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	logger.Info("Configuration loaded",
		"command", config.Command,
		"args", config.Args,
		"type", config.ProcessType,
		"env_vars", len(config.Env))

	// Apply timeout override if provided
	timeout := config.Timeout
	if flags.Timeout != "" {
		timeout = flags.Timeout
	}

	// Parse timeout duration
	var timeoutDuration time.Duration
	if timeout != "" {
		timeoutDuration, err = time.ParseDuration(timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout duration '%s': %w", timeout, err)
		}
	} else {
		timeoutDuration = 30 * time.Second // Default timeout
	}

	logger.Debug("Timeout configuration",
		"timeout", timeoutDuration,
		"source", func() string {
			if flags.Timeout != "" {
				return "flag_override"
			}
			if config.Timeout != "" {
				return "config_file"
			}
			return "default"
		}())

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration)
	defer cancel()

	logger.Info("Executing process",
		"timeout", timeoutDuration)

	// Execute the process
	events, err := executeProcess(ctx, config, flags.Format, logger)
	if err != nil {
		return fmt.Errorf("failed to execute process: %w", err)
	}

	// Format and output results
	return formatOutput(events, flags.Format, flags.Output, config, logger, stats)
}

func loadConfig(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Validate required fields
	if config.Command == "" {
		return nil, fmt.Errorf("command is required in configuration")
	}

	// Set defaults
	if config.ProcessType == "" {
		config.ProcessType = "pipe"
	}

	if config.PTYSize == nil && config.ProcessType == "pty" {
		config.PTYSize = &PTYSize{Rows: 24, Cols: 80}
	}

	return &config, nil
}

func executeProcess(ctx context.Context, config *Config, format string, logger *slog.Logger) (iter.Seq2[consolestream.Event, error], error) {
	// Create cancellor
	cancellor := consolestream.NewLocalCancellor(600 * time.Second)

	// Prepare process options
	var opts []consolestream.ProcessOption
	opts = append(opts, consolestream.WithCancellor(cancellor))

	// Add environment variables
	if len(config.Env) > 0 {
		opts = append(opts, consolestream.WithEnvMap(config.Env))
	}

	if format == "asciicast" {
		// For asciicast, use a smaller flush interval to capture more granular output
		opts = append(opts, consolestream.WithFlushInterval(100*time.Millisecond))
		logger.Debug("Using optimized flush interval for asciicast", "interval", "100ms")
	}

	logger.Debug("Process options configured",
		"env_vars", len(config.Env),
		"flush_interval", func() string {
			if format == "asciicast" {
				return "100ms"
			}
			return "default"
		}())

	// Execute based on process type
	switch config.ProcessType {
	case "pipe":
		process := consolestream.NewPipeProcess(config.Command, config.Args, opts...)
		return process.ExecuteAndStream(ctx), nil
	case "pty":
		// Add PTY size if specified
		if config.PTYSize != nil {
			// Validate ranges to prevent overflow
			rows := config.PTYSize.Rows
			cols := config.PTYSize.Cols
			if rows < 0 || rows > 65535 {
				return nil, fmt.Errorf("invalid PTY rows %d, must be 0-65535", rows)
			}
			if cols < 0 || cols > 65535 {
				return nil, fmt.Errorf("invalid PTY cols %d, must be 0-65535", cols)
			}
			opts = append(opts, consolestream.WithPTYSize(pty.Winsize{
				Rows: uint16(rows), //nolint:gosec // Range validated above
				Cols: uint16(cols), //nolint:gosec // Range validated above
			}))
		}
		process := consolestream.NewPTYProcess(config.Command, config.Args, opts...)
		return process.ExecuteAndStream(ctx), nil
	default:
		return nil, fmt.Errorf("invalid process_type '%s', must be 'pipe' or 'pty'", config.ProcessType)
	}
}

func formatOutput(events iter.Seq2[consolestream.Event, error], format, outputPath string, config *Config, logger *slog.Logger, stats *ExecutionStats) error {
	var writer io.Writer = os.Stdout
	var closeFunc func() error

	// Setup output destination
	if outputPath != "" {
		logger.Info("Writing output to file", "path", outputPath, "format", format)
		file, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		writer = file
		closeFunc = file.Close
		defer func() {
			if closeFunc != nil {
				if closeErr := closeFunc(); closeErr != nil {
					logger.Error("Failed to close output file", "error", closeErr)
				} else {
					// Log file completion info
					if fileInfo, statErr := file.Stat(); statErr == nil {
						logger.Info("File written successfully",
							"path", outputPath,
							"size_bytes", fileInfo.Size(),
							"events_processed", stats.EventCount)
					}
				}
			}
		}()
	} else {
		logger.Debug("Writing output to stdout", "format", format)
	}

	// Wrap events iterator to collect stats
	wrappedEvents := func(yield func(consolestream.Event, error) bool) {
		for event, err := range events {
			stats.EventCount++
			if err != nil {
				stats.ErrorCount++
			} else {
				// Track output bytes for data events
				switch e := event.Event.(type) {
				case *consolestream.PipeOutputData:
					stats.OutputBytes += int64(len(e.Data))
				case *consolestream.PTYOutputData:
					stats.OutputBytes += int64(len(e.Data))
				case *consolestream.ProcessEnd:
					stats.ExitCode = &e.ExitCode
				}
			}
			if !yield(event, err) {
				return
			}
		}
	}

	// Format output based on requested format
	var err error
	switch format {
	case "text":
		err = formatTextOutput(wrappedEvents, writer)
	case "json":
		err = formatJSONOutput(wrappedEvents, writer)
	case "asciicast":
		err = formatAscicastOutput(wrappedEvents, writer, config)
	default:
		return fmt.Errorf("invalid output format '%s', must be 'text', 'json', or 'asciicast'", format)
	}

	// Calculate duration after processing is complete
	stats.Duration = time.Since(stats.StartTime)

	// Log execution summary
	outputDest := "stdout"
	if outputPath != "" {
		outputDest = "file"
	}

	exitCode := "unknown"
	if stats.ExitCode != nil {
		exitCode = fmt.Sprintf("%d", *stats.ExitCode)
	}

	logger.Info("Execution completed",
		"duration", stats.Duration.String(),
		"exit_code", exitCode,
		"output_events", stats.EventCount,
		"error_events", stats.ErrorCount,
		"output_bytes", stats.OutputBytes,
		"output_destination", outputDest)

	return err
}

func formatTextOutput(events iter.Seq2[consolestream.Event, error], writer io.Writer) error {
	for event, err := range events {
		if err != nil {
			fmt.Fprintf(writer, "Error: %v\n", err)
			continue
		}

		switch event.EventType() {
		case consolestream.PipeOutputEvent:
			data := event.Event.(*consolestream.PipeOutputData)
			fmt.Fprintf(writer, "[%s] %s: %s", data.Stream.String(), event.Timestamp.Format("15:04:05"), string(data.Data))
		case consolestream.PTYOutputEvent:
			data := event.Event.(*consolestream.PTYOutputData)
			fmt.Fprintf(writer, "[PTY] %s: %s", event.Timestamp.Format("15:04:05"), string(data.Data))
		case consolestream.ProcessStartEvent:
			data := event.Event.(*consolestream.ProcessStart)
			fmt.Fprintf(writer, "Process started (PID: %d, Command: %s)\n", data.PID, data.Command)
		case consolestream.ProcessEndEvent:
			data := event.Event.(*consolestream.ProcessEnd)
			fmt.Fprintf(writer, "Process completed (Exit Code: %d, Duration: %v)\n", data.ExitCode, data.Duration)
		case consolestream.ProcessErrorEvent:
			data := event.Event.(*consolestream.ProcessError)
			fmt.Fprintf(writer, "Process error: %s\n", data.Message)
		case consolestream.HeartbeatEventType:
			// Silently ignore heartbeats in text output
		}
	}
	return nil
}

func formatJSONOutput(events iter.Seq2[consolestream.Event, error], writer io.Writer) error {
	encoder := json.NewEncoder(writer)
	for event, err := range events {
		if err != nil {
			// Encode error as JSON object
			errorObj := map[string]any{
				"error":     true,
				"message":   err.Error(),
				"timestamp": time.Now(),
			}
			if encErr := encoder.Encode(errorObj); encErr != nil {
				return fmt.Errorf("failed to encode error: %w", encErr)
			}
			continue
		}

		// Create JSON representation of event
		eventObj := map[string]any{
			"timestamp":  event.Timestamp,
			"event_type": event.EventType().String(),
			"event":      event.Event,
		}

		if err := encoder.Encode(eventObj); err != nil {
			return fmt.Errorf("failed to encode event: %w", err)
		}
	}
	return nil
}

func buildCommandString(command string, args []string) string {
	if len(args) == 0 {
		return command
	}
	return command + " " + strings.Join(args, " ")
}

func formatAscicastOutput(events iter.Seq2[consolestream.Event, error], writer io.Writer, config *Config) error {
	// Create asciicast metadata
	now := time.Now().Unix()
	metadata := consolestream.AscicastV3Metadata{
		Command:   buildCommandString(config.Command, config.Args),
		Title:     fmt.Sprintf("Execution of %s", config.Command),
		Timestamp: &now,
	}

	// Set terminal size from config or defaults
	if config.PTYSize != nil {
		metadata.Term = consolestream.TermInfo{
			Cols: config.PTYSize.Cols,
			Rows: config.PTYSize.Rows,
		}
	} else {
		metadata.Term = consolestream.TermInfo{Cols: 80, Rows: 24}
	}

	// Transform events to asciicast format
	asciicastEvents := consolestream.ToAscicastV3(events, metadata)

	// Write asciicast lines
	for line, err := range asciicastEvents {
		if err != nil {
			return fmt.Errorf("error processing asciicast event: %w", err)
		}

		data, err := line.MarshalJSON()
		if err != nil {
			return fmt.Errorf("error marshaling asciicast line: %w", err)
		}

		if _, err := writer.Write(data); err != nil {
			return fmt.Errorf("error writing asciicast data: %w", err)
		}
		if _, err := writer.Write([]byte("\n")); err != nil {
			return fmt.Errorf("error writing newline: %w", err)
		}
	}

	return nil
}
