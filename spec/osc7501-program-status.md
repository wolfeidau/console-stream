# OSC 7501 Program Status Specification

## Overview
Add support for the OSC 7501 program status protocol to console-stream. Programs running under console-stream could then report their own state (`working`, `blocked`, `done`, `error`, progress, message) as typed `ProgramStatus` events, so nothing has to be inferred from raw output.

- Blog post: https://mitchellh.com/writing/program-status-osc7501
- Spec: https://www.superlogical.com/rex/docs/build/program-status (revision 0.4, 2026-10-10)

`github.com/creack/pty` needs no change. It passes child bytes through unchanged, so parsing belongs in console-stream.

## Value
1. **Structured state events instead of output scraping.** Consumers of `ExecuteAndStream` get typed status, such as approval pending or 60% progress. No regexes over `OutputData` that break when a tool changes its spinner or wording.
2. **Detecting `blocked` in unattended runs.** `blocked` with `kind=permission|question|auth` means a job is waiting on input that will never arrive in CI. console-stream can fail fast, alert, or surface the prompt text instead of waiting for a timeout. This includes AI agents (Claude Code, Codex) that need a human.
3. **Navigable recordings.** Status changes map to asciicast v3 `"m"` marker events. A viewer can jump to where a run stalled or failed.
4. **Richer progress than heartbeats.** `HeartbeatEvent` only reports that the process is alive. OSC 7501 adds what the process is doing, how far along it is, and sub-tasks through hierarchical ids. It works over SSH and inside containers because it travels in the terminal byte stream.
5. **Telemetry.** Adds metric dimensions such as time `blocked` vs `working`, error states by `app`, and progress over time.
6. **Terminal visibility (optional).** The runner can re-emit aggregated status to its own TTY, so jobs show up in Ghostty/Rex status UI and notifications.

There is no cost when programs don't emit it. Bytes pass through unchanged, and the feature is opt-in.

## Protocol Essentials
- Report: `ESC ] 7501 ; k=v:k=v ST`.
  - ST is `ESC \` or `BEL`.
  - The whole sequence is at most 4096 bytes.
  - Keys are `[a-z]+`, at most 16 bytes. Values use `[A-Za-z0-9_.,+/=-]`.
  - Whitespace around keys and values is stripped. Unknown keys are ignored.
- Keys:
  - `state` (required): `idle|working|done|blocked|error|clear`. A missing or unknown state ignores the whole report.
  - `id`: `/` path, at most 8 levels and 128 bytes, each segment `[A-Za-z0-9_.+-]{1,32}`. Absent means the root record. An invalid id ignores the whole report.
  - `kind`: `permission|question|auth`, used only with `blocked`. Unrecognised means absent.
  - `progress`: integer 0–100, used only with `working`/`blocked`. Absent or invalid means indeterminate (-1).
  - `app`: `[A-Za-z0-9_.+-]{1,32}`. Invalid means absent. Children inherit it from the nearest ancestor.
  - `title`: base64 (standard alphabet, padding optional) UTF-8, at most 256 bytes encoded and 192 decoded. No control characters.
  - `msg`: base64 (standard alphabet, padding optional) UTF-8, at most 2732 bytes encoded and 2048 decoded. No control characters.
- Parsing:
  - Malformed pairs (no `=`, bad key or value characters) are skipped and the rest is processed.
  - If a key repeats, the last value wins.
  - A `title`/`msg` over its limit, bad base64, invalid UTF-8 or a control character (C0, DEL, C1) discards the whole report.
- Records:
  - Each report fully replaces its record.
  - `clear` removes the record and its subtree. With no `id` it removes all records.
  - A terminal holds at most 256 records, evicting the least recently updated.
- Lifetime: on process exit, `working`/`blocked` records are dropped. `done`/`error` persist.
- Feature query: the program sends `OSC 7501;? ST`, and a supporting terminal replies `OSC 7501;? ST`, which may list supported states and kinds. Programs follow the query with `CSI c`, so a DA reply arriving first means the protocol is unsupported. Since rev 0.3 programs MAY emit reports without querying first.

## Integration Points
All modes write through `BufferWriter` (`buffer_writer.go`):
- Pipe and PTY: `io.Copy(w, out)` in `process.go`.
- Container: `stdcopy.StdCopy(w, w, …)` in `container_process.go`.
- Events are produced by `streamOutput` in `stream_loop.go`, which drains `BufferWriter` on the flush ticker (default 1s) or at the buffer limit.
- `console.go`: the `StreamEvent`/`EventType` enum and event structs. Follow the `TerminalResizeEvent`/`ImagePull*` pattern.
- `asciicast.go` `ToAscicastV3`: map status to `"m"` marker events.
- `cmd/runner/main.go`, `prettylog.go`: render status in text/json output.

Many programs emit OSC only when stdout is a TTY, so PTY mode sees the most traffic, but the single `BufferWriter` insertion point covers every mode.

## Design
1. **`github.com/wolfeidau/osc7501` package**: pure Go, no dependencies.
   - `Report{State, ID, Kind, App, Title, Msg string; Progress int /* -1 = indeterminate */}`
   - `Parse(body []byte) (Report, bool)` (false means ignore the report), `Report.Encode() []byte`
   - `Scanner`: an `io.Writer` filter that wraps the downstream writer (`BufferWriter`).
     - It is a byte-at-a-time state machine that handles sequences split across writes, both terminators and the 4096-byte cap.
     - It aborts on CAN/SUB/stray ESC (an `ESC ]` restarts a new OSC).
     - It forwards all bytes unchanged, which keeps asciicast replay faithful. There is no strip option in v1.
     - Callbacks: `OnReport(Report)`, `OnQuery()`. Each fires after all bytes up to and including the terminator have been written downstream.
2. **`ProgramStatus` event** in `console.go`: `ProgramStatusEvent` / `"PROGRAM_STATUS"`, carrying the report fields with `Title`/`Msg` decoded. The event timestamp is the read time.
3. **Wiring** behind `WithProgramStatus()`, in all modes:
   - The scanner wraps the `BufferWriter`. On `OnReport` it calls `BufferWriter.addStatus` (unexported), synchronously, after the preceding bytes were forwarded, so the status records its byte offset in the current batch.
   - `streamOutput` splits the batch at the status offsets, yielding `OutputData` before each `ProgramStatus` in byte order. Adding a status signals a flush so it is delivered promptly.
   - A channel from the callback to the event loop is not used: the copy goroutine keeps writing, so a channel races with the buffered bytes and breaks ordering.
4. **Lifetime**: console-stream keeps no terminal record store and emits no synthetic `idle` on exit. Consumers apply the lifetime rule themselves after `ProcessEnd`/`ProcessError`.
5. **Feature-detect reply** (PTY only, behind `WithProgramStatus()`): on `OnQuery`, write `osc7501.QueryReply` to the PTY master so the child reads it on stdin.
   - Bytes: `ESC ] 7501 ; ? : states=idle,working,done,blocked,error : kinds=permission,question,auth ESC \\` (the rev 0.4 defaults).
   - Echo safety: the body differs from the query, so if the tty echoes the reply, `Scanner` does not take it for a new query and there is no reply loop.
   - The reader goroutine only signals a 1-slot channel (a pending reply covers coalesced queries). A separate goroutine writes, so a child that does not read stdin cannot block output draining. Closing the master unblocks a stuck write.
   - Pipe and container modes have no stdin back-channel, so no reply is sent; programs there should report without querying (allowed since rev 0.3).
   - `CSI c` (DA1) is not answered. Our reply arrives first, so DA1-fallback programs conclude the protocol is supported. Programs that also wait for the DA1 reply wait until their own timeout.
6. **Asciicast**: map `ProgramStatus` to an `"m"` marker labelled `status: <state>[/<kind>][ <id>][ <progress>%][: <msg>]`, only when `(id, state, kind)` changes; progress-only updates get no marker.
7. **Runner** (done): `cmd/runner` renders `[STATUS]` lines, summarises final state per ID, can fail fast on `blocked`, and re-emits reports to its own TTY (stderr) when it is a terminal, prefixing ids with a run id and relaying its own run record (`working` at start, `done` or `error` at end).

## Phasing
- **v1:** `github.com/wolfeidau/osc7501` parser/scanner, the `ProgramStatus` event and wiring for all modes (pipe, PTY and container share `BufferWriter`), and asciicast markers. The parser/scanner lands first as its own PR.
- **Query reply:** PTY mode answers the feature query.
- **Runner:** rendering, summary, fail-fast and re-emit.
- **Later:** metrics.

## Effort
- `github.com/wolfeidau/osc7501` package: about 300 LOC, plus about 300 LOC of table and fuzz tests.
- Wiring across `console.go`, `buffer_writer.go`, `stream_loop.go` and `asciicast.go`: about 100–150 LOC.
- Roughly a day overall, mostly spent on scanner edge cases.

## Risks
- **Early adoption.** Current emitters are PoC plugins or forks for Claude Code, Terraform, Homebrew and Codex. The value grows with adoption.
- **Spec churn.** The spec is at revision 0.4, and the terminfo cap has already been removed. Keep the public surface small and opt-in.
- **OSC-only scanner.** It can mis-frame `ESC ]` embedded inside DCS/APC strings. Document this as a limitation; a full VT parser is out of scope.
- **Non-TTY traffic.** Pipe mode and non-TTY container mode will see little traffic.
- **Event noise.** Frequent status updates split batches into extra small `OutputData` events and trigger extra flushes. Coalesce repeats for the same id within a tick if needed.

## Verification
- `go test ./...` in github.com/wolfeidau/osc7501: table tests for every parse rule and limit. `FuzzScanner` asserts that output equals input and that nothing panics.
- An end-to-end test in `process_test.go` runs `printf '\033]7501;state=working:msg=%s\033\\'` under `WithPTYMode(), WithProgramStatus()`. It asserts that `ProgramStatus` arrives after the preceding `OutputData` and that `Msg` is decoded. Equivalent pipe and container tests cover the shared `BufferWriter` path.
- `cmd/runner exec --format asciicast` against a script that emits states, confirming the `"m"` markers appear in the `.cast`.
- `make lint` and `make test`.
