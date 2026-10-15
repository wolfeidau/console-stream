package main

import (
	"bytes"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	consolestream "github.com/wolfeidau/console-stream"
	"github.com/wolfeidau/osc7501"
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
		), &buf, false)
		require.NoError(t, err)
		require.Equal(t, "[OUTPUT] 10:00:00: abc\n[STATUS] 10:00:00 working build\n", buf.String())
	})

	t.Run("no extra blank line", func(t *testing.T) {
		var buf bytes.Buffer
		err := formatTextOutput(seq(
			ev(&consolestream.OutputData{Data: []byte("abc\n")}),
			ev(ps("done", "", "", "", -1)),
		), &buf, false)
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

func runRelay(t *testing.T, failFast bool, tracker *statusTracker, events ...consolestream.Event) (*bytes.Buffer, func() error) {
	t.Helper()
	var out bytes.Buffer
	rl := &relay{w: &out, runID: "run-1", title: "make all"}
	wrapped, blocked := wrapProgramStatus(seq(events...), failFast, tracker, rl)
	for range wrapped {
	}
	return &out, blocked
}

func enc(rs ...osc7501.Report) []byte {
	var b []byte
	for _, r := range rs {
		b = append(b, r.Encode()...)
	}
	return b
}

func TestRelayPrefixesChildIDs(t *testing.T) {
	out, _ := runRelay(t, false, newStatusTracker(),
		ev(ps("working", "", "", "", 5)),
		ev(&consolestream.OutputData{Data: []byte("x")}),
		ev(ps("working", "build", "", "", 50)),
		ev(ps("blocked", "build/lint", "permission", "ok?", -1)),
		ev(ps("clear", "", "", "", -1)),
	)
	want := enc(
		osc7501.Report{State: "working", ID: "run-1", Progress: 5},
		osc7501.Report{State: "working", ID: "run-1/build", Progress: 50},
		osc7501.Report{State: "blocked", ID: "run-1/build/lint", Kind: "permission", Msg: "ok?", Progress: -1},
		osc7501.Report{State: "clear", ID: "run-1", Progress: -1},
	)
	require.Equal(t, want, out.Bytes())
}

func TestRelaySkipsTooDeepIDs(t *testing.T) {
	deep := "a/b/c/d/e/f/g/h" // 8 levels; prefixing makes 9
	tracker := newStatusTracker()
	out, _ := runRelay(t, false, tracker, ev(ps("working", deep, "", "", -1)))
	require.Empty(t, out.Bytes())
	require.Equal(t, deep+"=working", tracker.summary())
}

func TestRelayRunRecords(t *testing.T) {
	tests := []struct {
		name string
		in   consolestream.StreamEvent
		want osc7501.Report
	}{
		{"start", &consolestream.ProcessStart{PID: 1}, osc7501.Report{State: "working", ID: "run-1", Title: "make all", Progress: -1}},
		{"end ok", &consolestream.ProcessEnd{Success: true}, osc7501.Report{State: "done", ID: "run-1", Progress: -1}},
		{"end fail", &consolestream.ProcessEnd{ExitCode: 3}, osc7501.Report{State: "error", ID: "run-1", Msg: "exit code 3", Progress: -1}},
		{"error", &consolestream.ProcessError{Message: "boom\nbad"}, osc7501.Report{State: "error", ID: "run-1", Msg: "boom bad", Progress: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runRelay(t, false, newStatusTracker(), ev(tt.in))
			require.Equal(t, enc(tt.want), out.Bytes())
		})
	}
}

func TestRelayFailFast(t *testing.T) {
	b := ps("blocked", "build", "permission", "approve?", -1)
	out, blocked := runRelay(t, true, newStatusTracker(), ev(b), ev(&consolestream.ProcessEnd{Success: true}))
	want := enc(
		osc7501.Report{State: "blocked", ID: "run-1/build", Kind: "permission", Msg: "approve?", Progress: -1},
		osc7501.Report{State: "error", ID: "run-1", Msg: "program blocked (permission) build: approve?", Progress: -1},
	)
	require.Equal(t, want, out.Bytes())
	require.EqualError(t, blocked(), "program blocked (permission) build: approve?")
}

func TestNilRelay(t *testing.T) {
	wrapped, _ := wrapProgramStatus(seq(ev(&consolestream.ProcessStart{}), ev(ps("done", "", "", "", -1))), false, newStatusTracker(), nil)
	require.Len(t, slices.Collect(firstOf(wrapped)), 2)
	wrapped, _ = wrapProgramStatus(seq(ev(&consolestream.ProcessStart{})), false, newStatusTracker(), &relay{runID: "x"})
	require.Len(t, slices.Collect(firstOf(wrapped)), 1)
}

func TestFormatTextOutputStrip(t *testing.T) {
	seqA := "\x1b]7501;state=working:id=build\x1b\\"
	var buf bytes.Buffer
	err := formatTextOutput(seq(
		ev(&consolestream.OutputData{Data: []byte(seqA)}),
		ev(&consolestream.OutputData{Data: []byte("hi\n\x1b]7501;state=do")}),
		ev(&consolestream.OutputData{Data: []byte("ne:id=build\x07bye\n")}),
		ev(ps("working", "build", "", "", -1)),
		ev(&consolestream.OutputData{Data: []byte("tail\n")}),
		ev(&consolestream.ProcessEnd{Success: true}),
	), &buf, true)
	require.NoError(t, err)
	out := buf.String()
	require.NotContains(t, out, "\x1b]7501")
	require.Contains(t, out, "[STATUS] 10:00:00 working build\n")
	require.NotContains(t, out, "[OUTPUT] 10:00:00: \n")
	require.NotContains(t, out, "[OUTPUT] 10:00:00: [")
	require.Contains(t, out, "[OUTPUT] 10:00:00: hi\n")
	require.Contains(t, out, "bye\n")
}

func TestLoadConfigStatusID(t *testing.T) {
	dir := t.TempDir()
	load := func(body string) (*Config, error) {
		p := filepath.Join(dir, "c.yaml")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return loadConfig(p)
	}

	_, err := load("command: echo\nstatus_id: x\n")
	require.EqualError(t, err, "status_id requires program_status")

	_, err = load("command: echo\nprogram_status: true\nstatus_id: \"bad id\"\n")
	require.EqualError(t, err, `invalid status_id "bad id": must match [A-Za-z0-9_.+-]{1,32}`)

	cfg, err := load("command: echo\nprogram_status: true\nstatus_id: mine\n")
	require.NoError(t, err)
	require.Equal(t, "mine", cfg.StatusID)

	cfg, err = load("command: echo\n")
	require.NoError(t, err)
	require.Empty(t, cfg.StatusID)
}

func TestDefaultStatusID(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9_.+-]+-[0-9a-f]{4}$`)
	for _, cmd := range []string{"/usr/local/bin/my tool!", strings.Repeat("x", 100), "", "echo"} {
		id := defaultStatusID(cmd)
		require.Regexp(t, re, id)
		require.LessOrEqual(t, len(id), 32)
		require.True(t, osc7501.ValidSegment(id))
	}
	require.True(t, strings.HasPrefix(defaultStatusID("/usr/local/bin/my tool!"), "my-tool--"))
	require.True(t, strings.HasPrefix(defaultStatusID(""), "run-") || strings.HasPrefix(defaultStatusID(""), ".-"))

	cfgPath := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("command: echo\nprogram_status: true\n"), 0o600))
	cfg, err := loadConfig(cfgPath)
	require.NoError(t, err)
	require.Regexp(t, re, cfg.StatusID)
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
