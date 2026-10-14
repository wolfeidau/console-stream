package consolestream

import (
	"context"
	"os/exec"
	"time"
)

// streamOutput yields OutputData and Heartbeat events until done fires.
//
// It owns the flush ticker, time- and size-based flushing, heartbeat logic and
// the final flush. On done it returns the end time (used for
// the final OutputData and the terminal event) and the wait error, with ok == true. It returns
// ok == false if the consumer stopped iterating or ctx was cancelled; the
// caller must then stop the child and drain its goroutines.
//
// flush must be the channel passed to NewBufferWriter for w. done must fire
// only after all writers to w have finished.
func streamOutput(
	ctx context.Context,
	yield func(Event, error) bool,
	w *BufferWriter,
	flush <-chan struct{},
	done <-chan error,
	start time.Time,
	interval time.Duration,
	maxBuf int,
	stats *ProcessStats,
) (time.Time, bool, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	emit := func(data []byte) bool {
		if !yield(newEvent(&OutputData{Data: data}), nil) {
			return false
		}
		if stats != nil {
			stats.RecordOutput(ctx, int64(len(data)))
		}
		return true
	}

	// Heartbeat when there was no output in the previous and current tick.
	var emittedLastTick bool

	for {
		select {
		case <-ctx.Done():
			return time.Time{}, false, nil

		case <-ticker.C:
			emitted := false
			if data := w.FlushAndClear(); data != nil {
				if !emit(data) {
					return time.Time{}, false, nil
				}
				emitted = true
			}

			if !emittedLastTick && !emitted {
				if !yield(newEvent(&HeartbeatEvent{
					ProcessAlive: true,
					ElapsedTime:  time.Since(start),
				}), nil) {
					return time.Time{}, false, nil
				}
			}
			emittedLastTick = emitted

		case <-flush:
			if w.Len() >= maxBuf {
				if data := w.FlushAndClear(); data != nil {
					if !emit(data) {
						return time.Time{}, false, nil
					}
					emittedLastTick = true
				}
			}

		case err := <-done:
			endTime := time.Now()
			if data := w.FlushAndClear(); data != nil {
				if !yield(Event{Timestamp: endTime, Event: &OutputData{Data: data}}, nil) {
					return time.Time{}, false, nil
				}
				if stats != nil {
					stats.RecordOutput(ctx, int64(len(data)))
				}
			}
			return endTime, true, err
		}
	}
}

// emitProcessEnd records final stats and yields ProcessEnd, or ProcessError for
// failures that are not a non-zero exit. label prefixes the error message.
func emitProcessEnd(ctx context.Context, yield func(Event, error) bool, stats *ProcessStats, err error, start, end time.Time, label string) {
	exitCode := 0
	exitErr, isExit := err.(*exec.ExitError)
	if isExit {
		exitCode = exitErr.ExitCode()
	}

	if stats != nil {
		stats.Stop(ctx)
		if err == nil || isExit {
			stats.SetExitCode(exitCode)
		}
	}

	if err != nil && !isExit {
		yield(Event{Timestamp: end, Event: &ProcessError{
			Error:   err,
			Message: label + " process terminated unexpectedly",
		}}, nil)
		if stats != nil {
			stats.RecordError(ctx, "process_error")
		}
		return
	}

	yield(Event{Timestamp: end, Event: &ProcessEnd{
		ExitCode: exitCode,
		Duration: end.Sub(start),
		Success:  err == nil,
	}}, nil)
	if stats != nil {
		stats.RecordEvent(ctx, "process_end")
	}
}
