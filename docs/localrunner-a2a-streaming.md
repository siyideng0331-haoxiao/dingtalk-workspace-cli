# LocalRunner A2A Streaming

LocalRunner exposes real A2A `message/stream` output for Qoder, Codex, OpenCode, and Claude Code. The implementation is attached to the LocalRunner A2A executor, before the shared DevConnect robot-delivery layer, so the A2A behavior can evolve without changing existing robot delivery semantics.

## Stream lifecycle

For a valid streaming request, LocalRunner emits standard A2A events in this order:

1. a `Task` in `submitted` state;
2. a `status-update` in `working` state;
3. one or more `artifact-update` events containing visible assistant text;
4. a terminal `status-update` in `completed`, `failed`, or `canceled` state.

Harness output is treated as a visible-text snapshot. Prefix growth is emitted as an artifact append; a rewritten snapshot is emitted as a replacement. Updates are coalesced for 100 milliseconds, a `working` heartbeat is emitted every 15 seconds while no visible text changes, visible text is limited to 1 MiB, and one turn is limited to 1,024 artifact events. Backend error detail is logged only after the existing redaction pass; public A2A failures use stable generic task messages.

## Harness adapters

| Harness | Incremental source | Existing DevConnect robot behavior |
|---|---|---|
| Qoder | persistent `stream-json` process and assistant snapshots | Existing card streaming remains enabled. |
| Codex | `codex app-server --stdio` agent-message deltas | Existing card streaming remains enabled. |
| Claude Code | `claude -p --output-format stream-json --include-partial-messages` visible text deltas | Existing card streaming remains enabled. |
| OpenCode | `prompt_async`, `/event` wakeups, and 150 ms message/status polling fallback | Robot delivery remains synchronous because `canStream()` stays false; only LocalRunner A2A supplies the stream callback. |

The OpenCode polling fallback is intentional: event delivery accelerates updates when available, while bounded polling keeps incremental A2A output working when an installed OpenCode version closes or omits useful `/event` notifications. Cancellation aborts an incomplete OpenCode session turn.

## Timeout boundary

DWS does not add a timeout field to the Agent Card. The A2A core Agent Card schema does not define per-agent request timeouts, and this chain does not negotiate a timeout extension.

Streaming has no total-duration limit. LocalRunner emits a `working` event every 15 seconds while the Harness has no visible-text update; each event refreshes the streaming consumer's 90-second sliding activity lease. The stream ends only after a terminal Harness result, explicit cancellation, a real backend failure, or 90 consecutive seconds without an A2A event. The A2A server detaches execution from the incoming request deadline before invoking the Harness, so a former 30-minute OpenAPI request deadline must not become the Harness context deadline.

The 90-second policy is streaming-only. It must not be installed through the A2A SDK's handler-wide agent-inactivity option because that option also applies to synchronous `message/send`; the existing synchronous send timeout policy remains independent.

## Change history

| Date | Change | Reason |
|---|---|---|
| 2026-08-23 | Replaced final-only LocalRunner SSE output with the standard Task/status/artifact lifecycle for Qoder, Codex, OpenCode, and Claude Code. Added an OpenCode async event-plus-poll adapter while preserving its synchronous DevConnect robot path. | Long Harness turns need visible progress and transport activity before final completion, but the shared robot layer must keep its existing behavior unless changed independently. |
| 2026-08-23 | Kept timeout configuration out of Agent Card and documented the OpenAPI server-side Send and stream policies. | A2A core Agent Card has no request-timeout field, and advertising an unnegotiated private extension would not be interoperable. |
| 2026-08-23 | Replaced the streaming absolute deadline with a 90-second sliding activity lease refreshed by 15-second `working` events; kept synchronous send unchanged. | Long-running Harness work should continue while observable activity is healthy, while a broken stream still needs a bounded inactivity failure and synchronous behavior must not change implicitly. |
