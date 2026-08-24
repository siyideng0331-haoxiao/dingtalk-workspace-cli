# LocalRunner A2A Streaming

LocalRunner exposes real A2A `message/stream` output for Qoder, Codex, OpenCode, and Claude Code. The implementation is attached to the LocalRunner A2A executor, before the shared DevConnect robot-delivery layer, so the A2A behavior can evolve without changing existing robot delivery semantics.

## Stream lifecycle

For a valid streaming request, LocalRunner emits standard A2A events in this order:

1. a `Task` in `submitted` state;
2. a `status-update` in `working` state;
3. one or more `artifact-update` events containing visible assistant text;
4. a terminal `status-update` in `completed`, `failed`, or `canceled` state.

The submitted `Task` omits inbound user-role history. The request message has already reached the Harness, while repeating it in an outbound task event can make consumers render the user's question as if it were Agent output. Assistant text remains confined to artifact updates.

Harness output is treated as a visible-text snapshot. Prefix growth is emitted as an artifact append; a rewritten snapshot is emitted as a replacement. Updates are coalesced for 100 milliseconds, a `working` heartbeat is emitted every 15 seconds while no visible text changes, visible text is limited to 1 MiB, and one turn is limited to 1,024 artifact events. Backend error detail is logged only after the existing redaction pass; public A2A failures use stable generic task messages.

## Debug message logs

Run LocalRunner with the root `--debug` flag to see the sanitized message content handled by its shared A2A executor:

```bash
dws --debug deap runtime start-local --harness qoder --work-dir ./project
```

`localrunner.a2a.message.inbound` records the merged text of one valid user message. For synchronous Send, `localrunner.a2a.message.outbound` records the final Agent text only after it is yielded. For Streaming, an outbound event is recorded only after each artifact update is yielded, and its `content` is the delivered delta or replacement rather than the accumulated Harness snapshot. Empty `working` heartbeats do not produce content events.

`localrunner.a2a.event.inbound` records the complete accepted compatibility-wire user message before Harness dispatch. `localrunner.a2a.event.outbound` records every successfully yielded outbound A2A event. Synchronous Send produces one outbound `message` event; Streaming records the ordered `task`, `status-update`, and `artifact-update` sequence, including `working` heartbeats and the terminal status. Each direction uses a stable sequence beginning at 1 for the request, while `event_type` and `kind` identify the wire event. `event_json` is a parseable, sanitized JSON serialization of the compatibility-wire object; use these event records instead of the text-only message events when reconstructing a frame sequence.

For Codex, `localrunner.codex.retrying` records a sanitized app-server error summary and `will_retry=true` when a matching v2 error notification says the current turn will retry. It is also debug-only and never records raw notification parameters, `codexErrorInfo`, thread/turn identifiers, or the user prompt.

These message and event logs are disabled unless `--debug` is explicitly set, including in the diagnostic file logger. Values matching authorization, cookie, token, credential, API key, context, session, prompt, or Bearer patterns are redacted through the shared free-text sanitizer. Identity-shaped JSON fields are preserved structurally but their values are replaced with `[redacted]`; context, session, message, task, artifact, and request identifiers are never recorded directly. `turn_hash` is an irreversible short SHA-256 correlation value. Message events report `content_bytes`; serialized events report `event_bytes`. Both limit their sanitized value to 8,192 Unicode characters and set `truncated=true` when the original exceeds that limit. A truncated `event_json` remains valid JSON with a sanitized preview. Streaming message logs only delivered deltas/replacements so growing snapshots do not create quadratic log volume.

## LocalRunner-owned Harness lifecycle

LocalRunner owns dedicated transport implementations under `internal/app`; it does not change or wrap the shared DevConnect forwarders. `start-local` synchronously prewarms the selected transport before publishing an A2A endpoint. A missing binary, rejected initialization, exited process, or unhealthy server therefore fails startup instead of waiting for the first user request. Stopping the Runner closes every process or server it owns.

| Harness | LocalRunner-owned transport | Context isolation |
|---|---|---|
| Qoder | One persistent `qodercli` streaming-input/stream-json process. | A2A context maps to a stable native `session_id`; turns are serialized over the process. |
| Codex | One initialized `codex app-server --stdio` process for the Runner lifetime. | A2A context maps to a native thread; app-server calls are serialized without restarting the server per turn. |
| OpenCode | One authenticated loopback `opencode serve` process owned by the Runner. | A2A context maps to a native OpenCode session. |
| Claude Code | A bounded context-owned process pool using streaming input/output; one prewarmed process is claimed by the first context. | Each A2A context receives its own resident process/session because one Claude streaming-input process is one conversation. |

When memory is enabled, context-to-native-session identity is stored below the profile-scoped DWS config directory so a normal Runner restart can resume the native conversation. Disabling memory keeps mappings in the current process only. The dedicated transports do not introduce a remote HTTP wrapper; OpenCode's loopback server is its official local transport.

## DevConnect compatibility

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

On the Streaming Tunnel, `request_start.deadlineEpochMs` uses `0` to mean that the proxied request has no absolute deadline. A positive value remains an absolute Unix-millisecond deadline, and a negative value is invalid protocol input. A zero-deadline request still has a cancellable context, so an explicit Tunnel `cancel`, disconnect, terminal result, or real failure can stop it without inventing a distant synthetic deadline.

The 90-second policy is streaming-only. It must not be installed through the A2A SDK's handler-wide agent-inactivity option because that option also applies to synchronous `message/send`; the existing synchronous send timeout policy remains independent.

## Change history

| Date | Change | Reason |
|---|---|---|
| 2026-08-23 | Replaced final-only LocalRunner SSE output with the standard Task/status/artifact lifecycle for Qoder, Codex, OpenCode, and Claude Code. Added an OpenCode async event-plus-poll adapter while preserving its synchronous DevConnect robot path. | Long Harness turns need visible progress and transport activity before final completion, but the shared robot layer must keep its existing behavior unless changed independently. |
| 2026-08-23 | Kept timeout configuration out of Agent Card and documented the OpenAPI server-side Send and stream policies. | A2A core Agent Card has no request-timeout field, and advertising an unnegotiated private extension would not be interoperable. |
| 2026-08-23 | Replaced the streaming absolute deadline with a 90-second sliding activity lease refreshed by 15-second `working` events; kept synchronous send unchanged. | Long-running Harness work should continue while observable activity is healthy, while a broken stream still needs a bounded inactivity failure and synchronous behavior must not change implicitly. |
| 2026-08-23 | Defined Streaming Tunnel `request_start.deadlineEpochMs=0` as no absolute deadline while retaining positive absolute deadlines and rejecting negative values. | OpenAPI streaming requests must reach LocalRunner without reintroducing the removed total-duration limit, while cancellation and non-streaming deadline semantics remain explicit and interoperable. |
| 2026-08-24 | Removed inbound user-role history from the submitted LocalRunner streaming Task while keeping the Task lifecycle and assistant artifacts unchanged. | Some A2A consumers render submitted Task history as visible Agent output, which repeated the user's question before the actual Harness answer. |
| 2026-08-24 | Added debug-only sanitized serialization logs for every successfully yielded LocalRunner A2A event, with stable per-request sequence numbers and bounded valid JSON. | Remote frame debugging needs the complete Task/status/artifact order, while credentials and raw identity values must never enter logs. |
| 2026-08-24 | Moved the four LocalRunner Harness transports to dedicated, synchronously prewarmed Runner-owned lifecycles while leaving shared DevConnect production code unchanged. | Long-lived app-server/streaming-input/server processes remove per-turn cold starts, startup failures become observable before endpoint publication, and context-specific native sessions remain isolated. |
| 2026-08-24 | Added debug-only full inbound compatibility-wire event serialization with the same recursive redaction and valid-JSON truncation used for outbound events. | Remote diagnosis needs the accepted request object as well as the delivered response sequence, without exposing credentials or raw A2A/native identifiers. |
| 2026-08-24 | Adapted the LocalRunner Codex transport to app-server v2 `error` notifications: matching `willRetry=true` events keep the turn open and emit a debug-only sanitized retry record, while terminal events return only a sanitized message summary. | Codex may recover a transient Responses WebSocket failure through retries or HTTP fallback, so a retry notification is progress rather than a terminal Harness failure; malformed notifications must still fail safely. |
