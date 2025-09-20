package consolestream

import (
	"context"
	"time"
)

type Process struct {
	cmd  string
	args []string
	pid  int
}

func NewProcess(cmd string, args ...string) *Process {
	return &Process{
		cmd:  cmd,
		args: args,
	}
}

// execute a process and stream its stdout/stderr to the console
func (p *Process) ExecuteAndStream(ctx context.Context) error {

	return nil
}

type stream int

const (
	stdout stream = iota
	stderr
)

type StreamPart struct {
	Timestamp time.Time
	Data      []byte
	Stream    stream
}
