package main

import (
	"bytes"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	consolestream "github.com/wolfeidau/console-stream"
)

func ps(state, id, kind, msg string, progress int) *consolestream.ProgramStatus {
	return &consolestream.ProgramStatus{State: state, ID: id, Kind: kind, Msg: msg, Progress: progress}
}

func seq(events ...consolestream.Event) iter.Seq2[consolestream.Event, error] {
	return func(yield func(consolestream.Event, error) bool) {
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
	}
}

func ev(e consolestream.StreamEvent) consolestream.Event {
	return consolestream.Event{Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), Event: e}
}

func TestStatusLine(t *testing.T) {
	tests := []struct {
		name string
		in   *consolestream.ProgramStatus
		want string
	}{
		{"state only", ps("idle", "", "", "", -1), "idle"},
		{"kind", ps("blocked", "", "permission", "", -1), "blocked/permission"},
		{"id", ps("working", "build", "", "", -1), "working build"},
		{"progress", ps("working", "", "", "", 50), "working 50%"},
		{"msg", ps("error", "", "", "boom", -1), "error: boom"},
		{"zero progress", ps("working", "", "", "", 0), "working 0%"},
		{"all", ps("blocked", "build", "permission", "approve?", 10), "blocked/permission build 10%: approve?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, statusLine(tt.in))
		})
	}
}

func TestStatusTracker(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		tr := newStatusTracker()
		tr.apply(ps("working", "build", "", "", -1))
		tr.apply(ps("blocked", "build", "permission", "", -1))
		require.Equal(t, "build=blocked/permission", tr.summary())
	})

	t.Run("subtree clear", func(t *testing.T) {
		tr := newStatusTracker()
		tr.apply(ps("working", "build", "", "", -1))
		tr.apply(ps("working", "build/x", "", "", -1))
		tr.apply(ps("working", "buildx", "", "", -1))
		tr.apply(ps("clear", "build", "", "", -1))
		require.Equal(t, "buildx=working", tr.summary())
	})

	t.Run("clear all", func(t *testing.T) {
		tr := newStatusTracker()
		tr.apply(ps("working", "a", "", "", -1))
		tr.apply(ps("done", "", "", "", -1))
		tr.apply(ps("clear", "", "", "", -1))
		require.Equal(t, "", tr.summary())
	})

	t.Run("sorted with root", func(t *testing.T) {
		tr := newStatusTracker()
		tr.apply(ps("done", "b", "", "", -1))
		tr.apply(ps("working", "", "", "", -1))
		tr.apply(ps("blocked", "a", "question", "", -1))
		require.Equal(t, "-=working, a=blocked/question, b=done", tr.summary())
	})
}

func TestFormatTextOutputStatus(t *testing.T) {
	t.Run("newline inserted after partial line", func(t *testing.T) {
		var buf bytes.Buffer
		err := formatTextOutput(seq(
			ev(&consolestream.OutputData{Data: []byte("abc")}),
			ev(ps("working", "build", "", "", -1)),
		), &buf)
		require.NoError(t, err)
		require.Equal(t, "[OUTPUT] 10:00:00: abc\n[STATUS] 10:00:00 working build\n", buf.String())
	})

	t.Run("no extra blank line", func(t *testing.T) {
		var buf bytes.Buffer
		err := formatTextOutput(seq(
			ev(&consolestream.OutputData{Data: []byte("abc\n")}),
			ev(ps("done", "", "", "", -1)),
		), &buf)
		require.NoError(t, err)
		require.Equal(t, "[OUTPUT] 10:00:00: abc\n[STATUS] 10:00:00 done\n", buf.String())
	})
}

func TestWrapProgramStatusFailFast(t *testing.T) {
	events := seq(
		ev(&consolestream.ProcessStart{PID: 1}),
		ev(ps("working", "build", "", "", -1)),
		ev(ps("blocked", "build", "permission", "approve deploy?", -1)),
		ev(&consolestream.OutputData{Data: []byte("after")}),
		ev(&consolestream.ProcessEnd{}),
	)
	wrapped, blocked := wrapProgramStatus(events, true, newStatusTracker(), nil)

	var got []consolestream.Event
	for e := range wrapped {
		got = append(got, e)
	}
	require.Len(t, got, 3)
	_, isStatus := got[2].Event.(*consolestream.ProgramStatus)
	require.True(t, isStatus)
	require.EqualError(t, blocked(), "program blocked (permission) build: approve deploy?")
}

func TestWrapProgramStatusNoFailFast(t *testing.T) {
	events := seq(
		ev(ps("blocked", "", "", "x", -1)),
		ev(&consolestream.ProcessEnd{}),
	)
	wrapped, blocked := wrapProgramStatus(events, false, newStatusTracker(), nil)
	require.Len(t, slices.Collect(firstOf(wrapped)), 2)
	require.NoError(t, blocked())
}

func firstOf(s iter.Seq2[consolestream.Event, error]) iter.Seq[consolestream.Event] {
	return func(yield func(consolestream.Event) bool) {
		for e := range s {
			if !yield(e) {
				return
			}
		}
	}
}

func TestWrapProgramStatusReemit(t *testing.T) {
	a := ps("working", "build", "", "", 50)
	b := ps("blocked", "build", "permission", "ok?", -1)
	var out bytes.Buffer
	wrapped, _ := wrapProgramStatus(seq(ev(a), ev(&consolestream.OutputData{Data: []byte("x")}), ev(b)), false, newStatusTracker(), &out)
	for range wrapped {
	}
	want := append(toReport(a).Encode(), toReport(b).Encode()...)
	require.Equal(t, want, out.Bytes())
}

func TestFormatJSONOutputStatus(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, formatJSONOutput(seq(ev(ps("working", "build", "", "", 10))), &buf))
	require.Contains(t, buf.String(), `"event_type":"PROGRAM_STATUS"`)
	require.Contains(t, buf.String(), `"State":"working"`)
}

func TestLoadConfigFailOnBlockedRequiresProgramStatus(t *testing.T) {
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("command: echo\nfail_on_blocked: true\n"), 0o600))
	_, err := loadConfig(bad)
	require.Error(t, err)
	require.Contains(t, err.Error(), "program_status")

	good := filepath.Join(dir, "good.yaml")
	require.NoError(t, os.WriteFile(good, []byte("command: echo\nprogram_status: true\nfail_on_blocked: true\n"), 0o600))
	cfg, err := loadConfig(good)
	require.NoError(t, err)
	require.True(t, cfg.ProgramStatus)
	require.True(t, cfg.FailOnBlocked)
}

func TestBlockedError(t *testing.T) {
	tests := []struct {
		name string
		in   consolestream.ProgramStatus
		want string
	}{
		{"bare", consolestream.ProgramStatus{State: "blocked"}, "program blocked"},
		{"kind id msg", consolestream.ProgramStatus{State: "blocked", Kind: "permission", ID: "build", Msg: "approve?"}, "program blocked (permission) build: approve?"},
		{"no msg", consolestream.ProgramStatus{State: "blocked", Kind: "question"}, "program blocked (question)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualError(t, blockedError(&tt.in), tt.want)
		})
	}
}
