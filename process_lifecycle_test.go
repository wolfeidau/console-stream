package consolestream

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// requireGone fails unless the pid disappears within timeout.
func requireGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}, timeout, 50*time.Millisecond, "process %d still running", pid)
}

func TestProcessEarlyBreakStopsChild(t *testing.T) {
	t.Parallel()

	for name, opt := range map[string]ProcessOption{"pipe": WithPipeMode(), "pty": WithPTYMode()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			process := NewProcess("sleep", []string{"30"}, opt)

			pid := 0
			for ev, err := range process.ExecuteAndStream(context.Background()) {
				require.NoError(t, err)
				if start, ok := ev.Event.(*ProcessStart); ok {
					pid = start.PID
					break
				}
			}

			require.NotZero(t, pid)
			requireGone(t, pid, 3*time.Second)
		})
	}
}

func TestProcessContextCancelGracefulTerm(t *testing.T) {
	t.Parallel()

	script := `trap "echo got-term; exit 0" TERM; while true; do sleep 0.1; done`
	process := NewProcess("sh", []string{"-c", script}, WithPipeMode())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	time.AfterFunc(500*time.Millisecond, cancel)

	start := time.Now()
	pid := 0
	for ev, err := range process.ExecuteAndStream(ctx) {
		require.NoError(t, err)
		if ps, ok := ev.Event.(*ProcessStart); ok {
			pid = ps.PID
		}
	}

	require.Less(t, time.Since(start), 3*time.Second)
	requireGone(t, pid, time.Second)
}

func TestProcessPartialLineStreaming(t *testing.T) {
	t.Parallel()

	process := NewProcess("sh", []string{"-c", `printf "prompt> "; sleep 2`},
		WithPipeMode(), WithFlushInterval(100*time.Millisecond))

	start := time.Now()
	var seenAt time.Duration
	for ev, err := range process.ExecuteAndStream(context.Background()) {
		require.NoError(t, err)
		switch e := ev.Event.(type) {
		case *OutputData:
			if seenAt == 0 && strings.Contains(string(e.Data), "prompt> ") {
				seenAt = time.Since(start)
			}
		case *ProcessEnd:
			require.NotZero(t, seenAt, "prompt must arrive before ProcessEnd")
		}
	}

	require.Less(t, seenAt, time.Second)
}

func TestProcessPipeStderrCaptured(t *testing.T) {
	t.Parallel()

	process := NewProcess("sh", []string{"-c", "echo out; echo err 1>&2"}, WithPipeMode())

	var out strings.Builder
	for ev, err := range process.ExecuteAndStream(context.Background()) {
		require.NoError(t, err)
		if e, ok := ev.Event.(*OutputData); ok {
			out.Write(e.Data)
		}
	}

	require.Contains(t, out.String(), "out")
	require.Contains(t, out.String(), "err")
}

func TestProcessConcurrentRunsSameInstance(t *testing.T) {
	t.Parallel()

	process := NewProcess("echo", []string{"hi"}, WithPipeMode())

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out strings.Builder
			for ev, err := range process.ExecuteAndStream(context.Background()) {
				require.NoError(t, err)
				if e, ok := ev.Event.(*OutputData); ok {
					out.Write(e.Data)
				}
			}
			require.Contains(t, out.String(), "hi")
		}()
	}
	wg.Wait()
}

func TestProcessBreakOnFinalOutput(t *testing.T) {
	t.Parallel()

	process := NewProcess("echo", []string{"hi"}, WithPipeMode())
	for ev, err := range process.ExecuteAndStream(context.Background()) {
		require.NoError(t, err)
		if _, ok := ev.Event.(*OutputData); ok {
			break // must not deadlock after the child has exited
		}
	}
}

func TestLocalCancellorKillsSigtermIgnoringChild(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", `trap "" TERM; sleep 30`)
	require.NoError(t, cmd.Start())
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	time.Sleep(200 * time.Millisecond) // let sh install the trap

	start := time.Now()
	require.NoError(t, NewLocalCancellor(300*time.Millisecond).Cancel(context.Background(), cmd.Process.Pid))
	require.Less(t, time.Since(start), time.Second)

	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("child was not killed")
	}
}
