package consolestream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var timerT0 = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

func statusEv(at time.Duration, state, id string) Event {
	return Event{Timestamp: timerT0.Add(at), Event: &ProgramStatus{State: state, ID: id, Progress: -1}}
}

func endEv(at time.Duration) Event {
	return Event{Timestamp: timerT0.Add(at), Event: &ProcessEnd{Success: true}}
}

func TestStatusTimer(t *testing.T) {
	s := time.Second
	tests := []struct {
		name   string
		events []Event
		want   StatusDurations
	}{
		{"single working", []Event{statusEv(0, "working", "a"), statusEv(2*s, "done", "a")}, StatusDurations{Working: 2 * s}},
		{"blocked beats working", []Event{
			statusEv(0, "working", "build"), statusEv(3*s, "blocked", "deploy"),
			statusEv(5*s, "done", "deploy"), statusEv(10*s, "done", "build"),
		}, StatusDurations{Working: 8 * s, Blocked: 2 * s}},
		{"clear prefix", []Event{
			statusEv(0, "working", "a/b"), statusEv(1*s, "clear", "a"), statusEv(5*s, "done", "x"),
		}, StatusDurations{Working: 1 * s}},
		{"clear all", []Event{
			statusEv(0, "working", "a"), statusEv(0, "blocked", "b"), statusEv(2*s, "clear", ""), statusEv(9*s, "done", "x"),
		}, StatusDurations{Blocked: 2 * s}},
		{"idle counts", []Event{statusEv(0, "idle", ""), statusEv(3*s, "done", "")}, StatusDurations{Idle: 3 * s}},
		{"gap not counted", []Event{
			statusEv(0, "working", "a"), statusEv(1*s, "done", "a"),
			statusEv(5*s, "working", "a"), statusEv(6*s, "done", "a"),
		}, StatusDurations{Working: 2 * s}},
		{"process end closes", []Event{
			statusEv(1*s, "working", "a"), endEv(4 * s), statusEv(8*s, "blocked", "a"), endEv(9 * s),
		}, StatusDurations{Working: 3 * s}},
		{"backwards timestamp", []Event{
			statusEv(5*s, "working", "a"), statusEv(2*s, "working", "b"), statusEv(7*s, "done", "a"), statusEv(7*s, "done", "b"),
		}, StatusDurations{Working: 2 * s}},
		{"unchanged aggregate keeps counting", []Event{
			statusEv(0, "working", "a"), statusEv(1*s, "working", "a"), statusEv(2*s, "done", "a"),
		}, StatusDurations{Working: 2 * s}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tm StatusTimer
			var sum StatusDurations
			tm.onAccrue = func(state string, d time.Duration) {
				require.Positive(t, d)
				switch state {
				case "working":
					sum.Working += d
				case "blocked":
					sum.Blocked += d
				case "idle":
					sum.Idle += d
				}
			}
			for _, e := range tt.events {
				tm.Observe(e)
			}
			require.Equal(t, tt.want, tm.Totals())
			require.Equal(t, tt.want, sum)
		})
	}
}

func TestStatusTimerCloseIdempotent(t *testing.T) {
	var tm StatusTimer
	tm.Observe(statusEv(0, "working", "a"))
	tm.Close(timerT0.Add(2 * time.Second))
	tm.Close(timerT0.Add(9 * time.Second))
	require.Equal(t, StatusDurations{Working: 2 * time.Second}, tm.Totals())
	require.Equal(t, tm.Totals(), tm.Totals())
}

func TestProcessStatusTimeMetric(t *testing.T) {
	t.Parallel()

	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	script := `printf '\033]7501;state=working:id=b\033\\'; sleep 0.3; printf '\033]7501;state=blocked:id=b\033\\'; sleep 0.3; printf '\033]7501;state=done:id=b\033\\'`
	p := NewProcess("bash", []string{"-c", script}, WithPTYMode(), WithProgramStatus(), WithMeter(mp.Meter("test")))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, err := range p.ExecuteAndStream(ctx) {
		require.NoError(t, err)
	}

	st := p.GetStats().StatusTime
	require.GreaterOrEqual(t, st.Working, 250*time.Millisecond)
	require.GreaterOrEqual(t, st.Blocked, 250*time.Millisecond)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	got := map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "process.program_status.duration" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[float64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value("state")
				got[v.AsString()] += dp.Value
			}
		}
	}
	require.GreaterOrEqual(t, got["working"], 0.25)
	require.GreaterOrEqual(t, got["blocked"], 0.25)
}

func TestProcessStatsStatusTimeResetsPerRun(t *testing.T) {
	ctx := context.Background()
	stats, err := NewProcessStats(metric.NewMeterProvider().Meter("test"), ProcessInfo{Command: "x"})
	require.NoError(t, err)

	t0 := time.Unix(1000, 0)
	run := func(d time.Duration) {
		stats.Start(ctx)
		stats.ObserveStatus(ctx, Event{Timestamp: t0, Event: &ProgramStatus{State: "working", Progress: -1}})
		stats.CloseStatus(ctx, t0.Add(d))
	}

	run(2 * time.Second)
	require.Equal(t, 2*time.Second, stats.GetSnapshot().StatusTime.Working)

	run(3 * time.Second)
	require.Equal(t, 3*time.Second, stats.GetSnapshot().StatusTime.Working)
}
