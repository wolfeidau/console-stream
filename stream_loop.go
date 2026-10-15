package consolestream

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// streamOutput yields OutputData and Heartbeat events until done fires.
//
// It owns the batch ticker, time- and size-based batching, the heartbeat ticker
// and the final flush. On done it returns the end time (used for
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
	heartbeat time.Duration,
	maxBuf int,
	stats *ProcessStats,
) (time.Time, bool, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	hbTicker := time.NewTicker(heartbeat)
	defer hbTicker.Stop()

	// active records output delivered since the previous heartbeat tick.
	var active bool

	// deliver yields a batch in byte order: output before each status, the
	// status, then the remaining output. ts stamps OutputData; zero means now.
	deliver := func(data []byte, segs []OutputSegment, statuses []pendingStatus, ts time.Time) bool {
		out := func(d []byte, sg []OutputSegment) bool {
			t := ts
			if t.IsZero() {
				t = time.Now()
			}
			if !yield(Event{Timestamp: t, Event: &OutputData{Data: d, Segments: sg}}, nil) {
				return false
			}
			if stats != nil {
				stats.RecordOutput(ctx, int64(len(d)))
			}
			active = true
			return true
		}
		prev := 0
		for _, st := range statuses {
			off := min(max(st.Offset, prev), len(data))
			if off > prev && !out(data[prev:off], sliceSegments(segs, prev, off)) {
				return false
			}
			prev = off
			r := st.Report
			if !yield(Event{Timestamp: st.Time, Event: &ProgramStatus{
				State: r.State, ID: r.ID, Kind: r.Kind, App: r.App,
				Title: r.Title, Msg: r.Msg, Progress: r.Progress,
			}}, nil) {
				return false
			}
			if stats != nil {
				stats.RecordEvent(ctx, "program_status")
				stats.ObserveStatus(ctx, Event{Timestamp: st.Time, Event: &ProgramStatus{
					State: r.State, ID: r.ID, Kind: r.Kind, App: r.App,
					Title: r.Title, Msg: r.Msg, Progress: r.Progress,
				}})
			}
			active = true
		}
		if prev < len(data) {
			if len(statuses) == 0 {
				return out(data, segs)
			}
			return out(data[prev:], sliceSegments(segs, prev, len(data)))
		}
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return time.Time{}, false, nil

		case <-ticker.C:
			if data, segs, sts := w.flushBatch(); data != nil || sts != nil {
				if !deliver(data, segs, sts, time.Time{}) {
					return time.Time{}, false, nil
				}
			}

		case <-hbTicker.C:
			// Heartbeat when nothing was delivered or buffered since the last tick.
			if !active && w.Len() == 0 {
				if !yield(newEvent(&HeartbeatEvent{
					ProcessAlive: true,
					ElapsedTime:  time.Since(start),
				}), nil) {
					return time.Time{}, false, nil
				}
			}
			active = false

		case <-flush:
			if w.Len() >= maxBuf || w.hasStatus() {
				if data, segs, sts := w.flushBatch(); data != nil || sts != nil {
					if !deliver(data, segs, sts, time.Time{}) {
						return time.Time{}, false, nil
					}
				}
			}

		case err := <-done:
			endTime := time.Now()
			if data, segs, sts := w.flushBatch(); data != nil || sts != nil {
				if !deliver(data, segs, sts, endTime) {
					return time.Time{}, false, nil
				}
			}
			return endTime, true, err
		}
	}
}

// sliceSegments returns the segments covering data[from:to], rebased to start
// at from. The segment in effect at from (the last with Offset <= from) is
// included with Offset 0 and its original Time.
func sliceSegments(segs []OutputSegment, from, to int) []OutputSegment {
	if len(segs) == 0 {
		return nil
	}
	first := 0
	for i, sg := range segs {
		if sg.Offset <= from {
			first = i
		}
	}
	var out []OutputSegment
	for i := first; i < len(segs); i++ {
		sg := segs[i]
		if i > first && sg.Offset >= to {
			break
		}
		out = append(out, OutputSegment{Offset: max(sg.Offset-from, 0), Time: sg.Time})
	}
	return out
}

// exitCodeOf returns the exit code, using the shell convention 128+signal for
// signal-terminated processes (Go reports -1).
func exitCodeOf(exitErr *exec.ExitError) int {
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return exitErr.ExitCode()
}

// emitProcessEnd records final stats and yields ProcessEnd, or ProcessError for
// failures that are not a non-zero exit. label prefixes the error message.
func emitProcessEnd(ctx context.Context, yield func(Event, error) bool, stats *ProcessStats, err error, start, end time.Time, label string) {
	exitCode := 0
	exitErr, isExit := err.(*exec.ExitError)
	if isExit {
		exitCode = exitCodeOf(exitErr)
	}

	if stats != nil {
		stats.CloseStatus(ctx, end)
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
