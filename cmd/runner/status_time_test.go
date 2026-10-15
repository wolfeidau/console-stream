package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	consolestream "github.com/wolfeidau/console-stream"
)

func TestStatusTrackerTimeSummary(t *testing.T) {
	at := func(d time.Duration) time.Time { return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC).Add(d) }
	stEv := func(d time.Duration, state, id string) consolestream.Event {
		return consolestream.Event{Timestamp: at(d), Event: ps(state, id, "", "", -1)}
	}

	t.Run("empty", func(t *testing.T) {
		require.Equal(t, "", newStatusTracker().timeSummary())
	})
	t.Run("order rounding omission", func(t *testing.T) {
		tr := newStatusTracker()
		tr.timer.Observe(stEv(0, "idle", "i"))
		tr.timer.Observe(stEv(240*time.Millisecond, "done", "i"))
		tr.timer.Observe(stEv(time.Second, "blocked", "b"))
		tr.timer.Observe(stEv(time.Second+4140*time.Millisecond, "done", "b"))
		tr.timer.Observe(stEv(10*time.Second, "working", "w"))
		tr.timer.Observe(stEv(22300*time.Millisecond, "done", "w"))
		require.Equal(t, "working=12.3s blocked=4.1s idle=200ms", tr.timeSummary())
	})
	t.Run("rounds to zero omitted", func(t *testing.T) {
		tr := newStatusTracker()
		tr.timer.Observe(stEv(0, "working", "w"))
		tr.timer.Observe(stEv(40*time.Millisecond, "done", "w"))
		require.Equal(t, "", tr.timeSummary())
	})
}

func TestWrapProgramStatusFeedsTimer(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	tr := newStatusTracker()
	events := seq(
		consolestream.Event{Timestamp: t0, Event: ps("working", "a", "", "", -1)},
		consolestream.Event{Timestamp: t0.Add(2 * time.Second), Event: ps("blocked", "a", "", "", -1)},
		consolestream.Event{Timestamp: t0.Add(3 * time.Second), Event: &consolestream.ProcessEnd{Success: true}},
	)
	wrapped, _ := wrapProgramStatus(events, false, tr, nil)
	for _, err := range wrapped {
		require.NoError(t, err)
	}
	require.Equal(t, "working=2s blocked=1s", tr.timeSummary())
}
