# LocalRunner A2A CLI First-Batch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `test-driven-development` to implement each task and `verification-before-completion` before making any completion claim. Steps use checkbox (`- [ ]`) syntax for tracking. The current branch has a failing clean baseline, so production-code steps remain blocked until the user explicitly authorizes a focused-baseline exception or the repository baseline is repaired.

**Goal:** Add the independently testable DWS-side LocalRunner contract foundation that is wire-compatible with the Albert OpenAPI gateway and structurally enforces `Runner : Endpoint : WSS = 1 : 1 : 1`.

**Architecture:** The contract and transport core lives in `internal/localrunner`: one runner/endpoint identity, exact OpenAPI lifecycle envelopes, memory-only secrets, the shared text-control/binary-chunk tunnel codec, a single-endpoint state machine, HTTP/WSS adapters, reconnect orchestration, Agent Card rewrite, and multiplexed loopback proxy. `internal/app` mounts one `deap runtime` command group behind an injected runtime boundary and serves local A2A through the official SDK compatibility handler. `internal/helpers` owns the shared local-agent process/forwarder/session lifecycle used by both `dev connect` streaming ingress and LocalRunner A2A ingress. Remote OpenAPI/WSS/public-RPC interoperability remains an external gate.

**Tech Stack:** Go 1.25.9 with `GOTOOLCHAIN=auto`, `github.com/a2aproject/a2a-go/v2` with its `a2acompat/a2av0` compatibility layer, standard library JSON/binary/HTTP/URL/concurrency packages, the repository's existing Gorilla WebSocket dependency and declared Cobra command framework, and the standard `testing` package.

---

## 1. Execution Metadata and Current Gate

- Worktree: `/Users/wangxin/.codex/worktrees/7346/dingtalk-workspace-cli`
- Branch: `feature/20260819_localrunner_a2a_cli_1`
- Baseline HEAD: `ede8246de1a1007c9fa6934132404d130661896e`
- Remote default branch: `main`
- Fetched `origin/main`: `ede8246de1a1007c9fa6934132404d130661896e`
- Remote `master`: absent. `git fetch origin master` failed with `fatal: couldn't find remote ref master`; the branch therefore uses the repository's actual default trunk, `origin/main`.
- Branch ownership: this Codex worktree is the only registered worktree using the target branch.
- OpenAPI peer task: `01a018ba-a113-7b53-9f7c-3ec6834f9404`
- OpenAPI peer worktree, read-only from this task: `/Users/wangxin/.codex/worktrees/c3c7/albert-open-api`
- Shared wire-contract source of truth: `/Users/wangxin/.codex/worktrees/c3c7/albert-open-api/docs/superpowers/plans/2026-08-19-localrunner-openapi-gateway.md`
- Cross-project architecture: `/Users/wangxin/Documents/projects/work/deap/docs/superpowers/plans/2026-08-19-deap-localrunner-a2a-relay.md`
- Repository visibility note: `.gitignore:31` ignores directories named `plans`, so this required local plan exists at the exact path but does not appear in `git status`; it is not staged or committed.

### 1.1 Clean baseline evidence

The required clean baseline was run before any repository file changed:

```bash
GOTOOLCHAIN=auto DWS_PACKAGE_VERSION=0.0.0-test go test ./...
```

Result: exit code `1`. Most packages passed, but `github.com/DingTalk-Real-AI/dingtalk-workspace-cli/test/scripts` failed after `569.177s`.

The dominant failures are baseline repository-state mismatches:

- Contract tests require `.github/workflows/ci.yml`, `release.yml`, `mirror-to-gitee.yml`, `ai-behavior-check.yml`, `reviewer-router.yml`, `withdraw-release.yml`, and `eval-dispatch.yml`; the fetched tree contains none of them.
- The fetched tree contains only `.github/workflows/build-release.yml`; the worktree is not sparse.
- `TestGitHubScriptWorkflowJavaScriptParses` found no `actions/github-script` steps because the expected workflow files are absent.
- `TestReleaseCommandReusesExactPreflightProof` and `TestReleaseCommandCleansLocalTagWhenPushFails` expected simulated tag pushes to be rejected, but the test release commands pushed the test tags successfully.

This failure predates and is unrelated to LocalRunner. Per the handoff boundary, no production or test Go files may be added until either:

1. the clean full baseline passes on a repaired/newer trunk; or
2. the user explicitly authorizes continuing from this recorded failure with a narrower focused baseline.

Documentation work is allowed while blocked. A focused LocalRunner GREEN must never be reported as replacing this failed full baseline.

### 1.2 Hard boundaries

- Do not run `gofmt`, `goimports`, a Markdown formatter, or any other formatter without a new explicit instruction.
- Do not commit, push, create a CR, publish, or deploy.
- Do not modify the OpenAPI worktree.
- Do not print, persist, format into errors, or include in test failure values: user OAuth bearer, OpenAPI connection ticket, ticket signature/claims/JTI, local `Authorization`, A2A request body, SSE content, or raw tunnel payload.
- Do not import the existing event ACK, `sessionWebhook`, DingTalk Stream `clientId/clientSecret`, or robot-reply business layer into `internal/localrunner`.
- The first batch does not edit `internal/event/source/personal.go`, `internal/event/source/portal_ticket.go`, or `internal/helpers/connect_stream.go`. Generic OAuth, dial, reconnect, and frame mechanics may be extracted only in a later separately tested batch.
- Do not claim real WSS, localhost proxy, SSE bridge, reconnect, endpoint registration, persistent configuration, or a complete CLI command.
- Any change to ticket claims, `connections/open` envelopes, frame fields/encoding, request-ID rules, or state-machine identity semantics must first be coordinated with the OpenAPI peer and appended to both plans' histories.

## 2. Frozen Shared Contracts

### 2.1 Runner/endpoint identity and one-to-one shape

```go
type RunnerEndpointIdentity struct {
	TenantID       string `json:"tenantId"`
	OperatorUserID string `json:"operatorUserId"`
	RunnerID       string `json:"runnerId"`
	EndpointID     string `json:"endpointId"`
}
```

All four fields are required after `strings.TrimSpace`. `RunnerEndpointConfig` contains exactly one identity value; it has no endpoint slice, endpoint map, secondary endpoint, or mutable endpoint setter. Another endpoint requires another config and another future WSS lifecycle.

The config contains identifiers only. It does not contain any bearer, ticket, endpoint credential, local authorization, body, or SSE data.

### 2.2 `connections/open` request and success envelope

```http
POST /v1/assistant/local-runners/{runnerId}/connections/open
Authorization: Bearer <user-oauth-token>
Content-Type: application/json

{"endpointId":"lre_01..."}
```

The path `runnerId` and body `endpointId` are required non-blank strings. `tenantId` and `operatorUserId` must not appear in the CLI request body. Studio derives the authenticated caller identity exclusively from the verified DWS user OAuth bearer. The CLI does not send the optional `X-Dingtalk-Corp-Id` or `X-Dingtalk-User-Id` cross-check headers because its stored `TokenData.UserID` is not proof of Studio's trusted numeric `uid`.

HTTP 200 success is exactly:

```json
{
  "success": true,
  "data": {
    "runnerId": "lr_01...",
    "endpointId": "lre_01...",
    "webSocketUrl": "wss://pre-deap.dingtalk.com/v1/local-runners/connections/lr_01...",
    "connectionTicket": "lr1.<payload>.<signature>",
    "ticketExpiresAtEpochSecond": 1787068920
  }
}
```

Exact first-batch DTOs:

```go
type OpenConnectionRequest struct {
	EndpointID string `json:"endpointId"`
}

type OpenConnectionSuccess struct {
	Success *bool               `json:"success"`
	Data    *OpenConnectionData `json:"data"`
}

type OpenConnectionData struct {
	RunnerID                    string           `json:"runnerId"`
	EndpointID                  string           `json:"endpointId"`
	WebSocketURL                string           `json:"webSocketUrl"`
	ConnectionTicket            *ConnectionTicket `json:"connectionTicket"`
	TicketExpiresAtEpochSecond int64            `json:"ticketExpiresAtEpochSecond"`
}
```

`Success` is a pointer so missing is distinguishable from explicit false. Validation requires present/true `success`, non-null `data`, exact echoed runner/endpoint, an absolute server-selected `wss://` URL, non-empty ticket, and an expiry strictly later than the injected current UTC Unix second.

Studio's shared pre-release public connection base is
`wss://pre-deap.dingtalk.com/v1/local-runners/connections`, with `/{runnerId}`
appended by the server. The CLI treats the resulting `webSocketUrl` as opaque,
does not construct/rewrite it, does not decode the `lr1` ticket, and does not
accept `expiresIn` or millisecond alternatives. A ticket is usable for one
handshake attempt only. A failed or uncertain handshake discards it and
requires a fresh `connections/open` call.

### 2.3 `connections/open` error envelope

Failures contain neither `success` nor `data`:

```json
{"error":{"code":"localRunnerNotFound","message":"LocalRunner binding was not found"}}
```

```go
type OpenConnectionFailure struct {
	Error *OpenConnectionError `json:"error"`
}

type OpenConnectionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
```

Stable branch keys are only HTTP status plus `error.code`:

| HTTP | `error.code` | Meaning |
|---:|---|---|
| 400 | `invalidParameter` | Blank/missing runner or endpoint, malformed JSON, invalid shape |
| 401 | `unauthorized` | OAuth missing, invalid, or expired |
| 404 | `localRunnerNotFound` | Absent, foreign-owned, or non-matching stored 1:1 binding; deliberately indistinguishable |
| 410 | `localRunnerRevoked` | Current owner's exact binding is revoked |
| 429 | `rateLimitExceeded` | Ticket opening is rate limited |
| 550 | `internalError` | Unexpected OpenAPI or issuance failure |

`message` is diagnostic and not a program key. Decode errors are static and never embed response bodies. Failure bodies must not contain credentials or credential-bearing URLs.

### 2.4 In-memory connection ticket boundary

```go
type ConnectionTicket struct {
	raw  []byte
	used bool
}

func (t *ConnectionTicket) ApplyAuthorization(header http.Header) error
func (t ConnectionTicket) String() string
func (t ConnectionTicket) GoString() string
func (t ConnectionTicket) MarshalJSON() ([]byte, error)
func (t *ConnectionTicket) UnmarshalJSON(data []byte) error
```

`ApplyAuthorization` sets `Authorization: Bearer <ticket>` once, marks the ticket used, and clears the private copied bytes. A second call returns static `connection_ticket_already_used`. It never returns the raw ticket. `String`, `GoString`, and `MarshalJSON` render `[REDACTED]`.

No first-batch API writes the success DTO or ticket to disk, keychain, stdout, stderr, normal JSON output, or logs.

### 2.5 Tunnel frame encoding

Every frame carries `v`, `type`, `runnerId`, `endpointId`, `seq`, and `timestamp`; request-scoped frames also carry `requestId`.

Allowed types:

```text
hello hello_ack request_start request_chunk request_end
response_start response_chunk response_end cancel error
heartbeat heartbeat_ack endpoint_revoke
```

Rules:

- `runnerId` and `endpointId` are required on every frame, including `hello_ack`.
- `requestId` is required for request/response start/chunk/end and `cancel`.
- `requestId` is absent for `hello`, `hello_ack`, `heartbeat`, `heartbeat_ack`, and `endpoint_revoke`.
- `error` may be request-scoped or connection-scoped.
- `seq` is a signed 64-bit JSON integer and must be non-negative. Strict monotonic per-request checks belong to the later active-session layer.
- Only `request_chunk` and `response_chunk` are binary; all other types are UTF-8 JSON text.
- Binary layout is `4-byte unsigned big-endian JSON header length + UTF-8 common header JSON + opaque bytes`.
- Total encoded text or binary message size must not exceed `262144` bytes.
- Reserved keys `v`, `type`, `runnerId`, `endpointId`, `requestId`, `seq`, and `timestamp` cannot appear in the control attributes map.
- Safe summaries include only type, IDs, sequence, and payload length; never attributes or payload bytes.

Stable shared first-batch error codes mirror OpenAPI:

```text
invalid_identity ticket_malformed ticket_signature_invalid
ticket_audience_invalid ticket_not_yet_valid ticket_expired
ticket_lifetime_invalid ticket_binding_mismatch ticket_replayed
frame_malformed frame_unsupported_version frame_too_large
frame_type_mismatch session_conflict
```

The CLI does not issue or verify HMAC tickets in this batch. `connection_ticket_already_used` is local-only and is not sent on the shared wire.

### 2.6 Single-endpoint connection state machine

```go
type ConnectionState string

const (
	ConnectionStateIdle             ConnectionState = "idle"
	ConnectionStateOpening          ConnectionState = "opening"
	ConnectionStateAwaitingHelloAck ConnectionState = "awaiting_hello_ack"
	ConnectionStateReady            ConnectionState = "ready"
	ConnectionStateDisconnected     ConnectionState = "disconnected"
	ConnectionStateStopped          ConnectionState = "stopped"
)

type ConnectionSnapshot struct {
	Identity     RunnerEndpointIdentity `json:"identity"`
	State        ConnectionState        `json:"state"`
	ConnectionID string                 `json:"connectionId,omitempty"`
}

type ConnectionStateMachine interface {
	Snapshot() ConnectionSnapshot
	BeginOpen(OpenConnectionData, time.Time) error
	MarkHandshakeStarted() error
	AcceptHelloAck(TunnelFrame) error
	MarkDisconnected() error
	Stop()
}
```

`SingleEndpointConnectionStateMachine` records one immutable configured identity. It validates response identity/expiry, requires a matching `hello_ack` with `accepted=true`, non-empty `connectionId`, `heartbeatIntervalMs=15000`, and `maxFrameBytes=262144`, and rejects a second open while active. It does not switch endpoints, initiate reconnect, reuse a ticket, or schedule heartbeat work.

## 3. First-Batch File Map

```text
internal/localrunner/identity.go
internal/localrunner/identity_test.go
internal/localrunner/connection.go
internal/localrunner/connection_test.go
internal/localrunner/frame.go
internal/localrunner/frame_test.go
internal/localrunner/codec.go
internal/localrunner/codec_test.go
internal/localrunner/state.go
internal/localrunner/state_test.go
```

- `identity.go`: singular identity/config and non-blank validation.
- `connection.go`: exact success/error envelopes, response binding, in-memory redacted one-shot ticket.
- `frame.go`: frame types, request-ID/type rules, protocol errors, safe summaries.
- `codec.go`: JSON text and length-prefixed binary encoding under the 262144-byte cap.
- `state.go`: no-I/O state-machine interface and single-identity implementation.

The first batch does not modify `internal/event/**`, `internal/helpers/**`, `internal/app/**`, `internal/cli/**`, `skills/**`, `go.mod`, or `go.sum`.

## 4. Task 0: Restore or Explicitly Waive the Baseline Gate

- [x] **Step 1: Reconfirm branch and trunk**

```bash
git fetch origin main:refs/remotes/origin/main
git rev-parse HEAD
git rev-parse origin/main
git status --short --branch
```

Expected: the exact HEAD/trunk relation is reviewed. Because `.gitignore:31` ignores this plan path, a clean short status is expected until authorized Go files are added; verify the plan independently with `test -f` and its checksum.

- [x] **Step 2: Resolve the baseline decision**

```bash
GOTOOLCHAIN=auto DWS_PACKAGE_VERSION=0.0.0-test go test ./...
```

Preferred result: exit code `0`. If it still fails for the recorded workflow/release mismatch, stop unless the user explicitly accepts that failure and names an allowed focused baseline. Do not silently exclude `test/scripts`.

## 5. Task 1: Strict Runner/Endpoint Configuration

**Files:** test `internal/localrunner/identity_test.go`, then create `internal/localrunner/identity.go`.

- [x] **Step 1: Write failing tests**

```go
func TestRunnerEndpointConfigRejectsBlankIdentityFields(t *testing.T) {
	tests := []RunnerEndpointIdentity{
		{OperatorUserID: "operator", RunnerID: "runner", EndpointID: "endpoint"},
		{TenantID: "tenant", RunnerID: "runner", EndpointID: "endpoint"},
		{TenantID: "tenant", OperatorUserID: "operator", EndpointID: "endpoint"},
		{TenantID: "tenant", OperatorUserID: "operator", RunnerID: "runner"},
	}
	for _, identity := range tests {
		if _, err := NewRunnerEndpointConfig(identity); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("error = %v, want ErrInvalidIdentity", err)
		}
	}
}

func TestRunnerEndpointConfigContainsOneExactIdentity(t *testing.T) {
	identity := testIdentity()
	config, err := NewRunnerEndpointConfig(identity)
	if err != nil {
		t.Fatal(err)
	}
	if config.Identity() != identity {
		t.Fatalf("identity mismatch")
	}
}
```

- [x] **Step 2: Verify RED**

```bash
GOTOOLCHAIN=auto go test ./internal/localrunner -run 'TestRunnerEndpointConfig' -count=1
```

Expected RED: compilation fails only because the new identity/config API does not exist.

- [x] **Step 3: Implement the minimal singular config**

Use one private identity field and one accessor. Trim/validate four fields and return static errors. Do not add endpoint collections or persistence.

- [x] **Step 4: Re-run and verify GREEN**

Expected: all `TestRunnerEndpointConfig*` tests pass.

## 6. Task 2: `connections/open` Envelopes and In-Memory Ticket

**Files:** test `internal/localrunner/connection_test.go`, then create `internal/localrunner/connection.go`.

- [x] **Step 1: Write failing success-envelope tests**

```go
func TestOpenConnectionSuccessMatchesFrozenEnvelope(t *testing.T) {
	var response OpenConnectionSuccess
	err := json.Unmarshal([]byte(`{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","webSocketUrl":"wss://gateway.example.test/v1/local-runners/connections/runner-1","connectionTicket":"lr1.payload.signature","ticketExpiresAtEpochSecond":200}}`), &response)
	if err != nil {
		t.Fatal(err)
	}
	if response.Success == nil || !*response.Success || response.Data == nil {
		t.Fatal("success envelope was not preserved")
	}
	if err := response.Data.ValidateFor(testIdentity(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestOpenConnectionSuccessRejectsIdentityDriftBeforeDial(t *testing.T) {
	data := validOpenConnectionData(t)
	identity := testIdentity()
	identity.RunnerID = "runner-2"
	if err := data.ValidateFor(identity, time.Unix(100, 0)); !errors.Is(err, ErrTicketBindingMismatch) {
		t.Fatalf("error = %v, want binding mismatch", err)
	}
}
```

Also reject missing/false `success`, missing `data`, non-`wss` or relative URL, empty ticket, non-future expiry, and unknown envelope/data fields. The frozen response is exact rather than an extensible client-owned shape.

- [x] **Step 2: Write failing error-envelope tests**

Decode each frozen status/code pair and assert branching uses status plus code only. A 404 test must not expose which of absent, foreign-owned, or mismatched binding occurred. Malformed failure JSON returns a static parse error without embedding body text.

- [x] **Step 3: Write failing ticket secrecy/one-shot tests**

```go
func TestConnectionTicketIsRedactedAndAppliedOnce(t *testing.T) {
	data := validOpenConnectionData(t)
	if strings.Contains(fmt.Sprint(data.ConnectionTicket), "lr1.") {
		t.Fatal("String exposed ticket")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("lr1.")) {
		t.Fatal("JSON exposed ticket")
	}
	header := make(http.Header)
	if err := data.ConnectionTicket.ApplyAuthorization(header); err != nil {
		t.Fatal(err)
	}
	if err := data.ConnectionTicket.ApplyAuthorization(make(http.Header)); !errors.Is(err, ErrConnectionTicketAlreadyUsed) {
		t.Fatalf("second apply error = %v", err)
	}
}
```

Do not print the first header's value even on failure; assert only presence/prefix length facts.

- [x] **Step 4: Verify RED**

```bash
GOTOOLCHAIN=auto go test ./internal/localrunner -run 'TestOpenConnection|TestConnectionTicket' -count=1
```

Expected RED: compilation fails only because envelope/ticket types do not exist.

- [x] **Step 5: Implement minimal DTO validation and secret handling**

Implement exact tags and strict success/failure parsing. `ConnectionTicket.UnmarshalJSON` accepts one non-blank JSON string into private copied bytes. `ApplyAuthorization` is pointer-only, writes once, clears bytes, and returns static sentinel errors. Redacted formatting never depends on ticket content.

- [x] **Step 6: Re-run and verify GREEN**

Expected: envelope, status/code, binding, expiry, URL, redaction, and one-shot tests pass.

## 7. Task 3: Tunnel Frame Contract and Codec

**Files:** tests `frame_test.go`/`codec_test.go`, then production `frame.go`/`codec.go` under `internal/localrunner`.

- [x] **Step 1: Write failing frame-shape tests**

Test all frame types, exact wire values, runner/endpoint on every type, non-negative sequence, the required/forbidden/optional request-ID table, reserved-key collisions, and safe summaries.

```go
func TestTunnelFrameRequiresIdentityOnEveryType(t *testing.T) {
	for _, typ := range AllTunnelFrameTypes() {
		frame := TunnelFrame{Version: 1, Type: typ, EndpointID: "endpoint", Sequence: 0}
		if err := frame.Validate(); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("type %s accepted missing runner", typ)
		}
	}
}
```

- [x] **Step 2: Write failing text/binary round-trip tests**

```go
func TestControlFrameRoundTripsAsText(t *testing.T) {
	frame := validFrame(FrameRequestStart)
	frame.Attributes = map[string]json.RawMessage{"method": json.RawMessage(`"POST"`), "path": json.RawMessage(`"/rpc"`)}
	encoded, err := NewTunnelCodec(DefaultMaxFrameBytes).Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Kind != MessageText {
		t.Fatalf("kind = %v, want text", encoded.Kind)
	}
	decoded, err := NewTunnelCodec(DefaultMaxFrameBytes).DecodeText(encoded.Data)
	if err != nil {
		t.Fatal(err)
	}
	assertFrameMetadataEqual(t, decoded, frame)
}

func TestResponseChunkRoundTripsOpaqueBinary(t *testing.T) {
	payload := []byte{0xff, 0x00, 0xfe, '\n'}
	frame := validFrame(FrameResponseChunk)
	frame.Payload = payload
	encoded, err := NewTunnelCodec(DefaultMaxFrameBytes).Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := NewTunnelCodec(DefaultMaxFrameBytes).DecodeBinary(encoded.Data)
	if err != nil || !bytes.Equal(decoded.Payload, payload) {
		t.Fatal("opaque payload did not round-trip")
	}
}
```

Also reject chunk-as-text, control-as-binary, unsupported version, negative sequence, invalid binary header length, and over-262144 messages before unbounded allocation or JSON parsing.

- [x] **Step 3: Verify RED**

```bash
GOTOOLCHAIN=auto go test ./internal/localrunner -run 'TestTunnelFrame|TestControlFrame|TestResponseChunk' -count=1
```

Expected RED: compilation fails only because frame/codec types do not exist.

- [x] **Step 4: Implement frame validation and static errors**

`TunnelProtocolError` carries a code and static message only; no raw input or cause chain. Copy attributes/payload on construction and decode. Do not add sequence history to the codec.

- [x] **Step 5: Implement encoders/decoders**

Text uses JSON. Binary uses `binary.BigEndian.PutUint32`, header JSON, then opaque bytes. Both decoders enforce total size before parsing; binary enforces header bounds before slicing.

- [x] **Step 6: Re-run and verify GREEN**

Expected: all frame and codec tests pass with no sensitive content in output.

## 8. Task 4: Single-Endpoint Connection State Machine

**Files:** test `internal/localrunner/state_test.go`, then create `internal/localrunner/state.go`.

- [x] **Step 1: Write the failing ready-path test**

```go
func TestSingleEndpointStateMachineNeedsMatchingHelloAck(t *testing.T) {
	machine := newTestMachine(t)
	data := validOpenConnectionData(t)
	if err := machine.BeginOpen(data, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := machine.MarkHandshakeStarted(); err != nil {
		t.Fatal(err)
	}
	ack := validFrame(FrameHelloAck)
	ack.Attributes = map[string]json.RawMessage{"accepted": json.RawMessage(`true`), "connectionId": json.RawMessage(`"connection-1"`), "heartbeatIntervalMs": json.RawMessage(`15000`), "maxFrameBytes": json.RawMessage(`262144`)}
	if err := machine.AcceptHelloAck(ack); err != nil {
		t.Fatal(err)
	}
	if snapshot := machine.Snapshot(); snapshot.State != ConnectionStateReady || snapshot.ConnectionID != "connection-1" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
```

- [x] **Step 2: Write failing conflict/terminal tests**

Prove mismatched response identity and mismatched hello-ack identity are rejected, `accepted=false` or missing connection ID never becomes ready, second active open conflicts, disconnect clears ephemeral session data, the old ticket cannot be reused, stop is terminal, and no method replaces the configured endpoint.

- [x] **Step 3: Verify RED**

```bash
GOTOOLCHAIN=auto go test ./internal/localrunner -run 'TestSingleEndpointStateMachine' -count=1
```

Expected RED: compilation fails only because state types do not exist.

- [x] **Step 4: Implement the synchronized no-I/O machine**

Use one `sync.RWMutex`, immutable identity, state, and connection ID. Validate every transition under the same lock. Do not make HTTP calls, dial, reconnect, or schedule heartbeats.

- [x] **Step 5: Re-run and verify GREEN**

Expected: ready path and all conflict/terminal cases pass.

## 9. Task 5: Verification and Handoff

This task starts only after Tasks 0-4 are authorized and implemented.

- [x] **Step 1: Focused package**

```bash
GOTOOLCHAIN=auto go test ./internal/localrunner -count=1
```

- [x] **Step 2: Unchanged reusable neighbors**

```bash
GOTOOLCHAIN=auto go test ./internal/event/source ./internal/helpers -run 'PortalTicket|Personal|ConnectStream' -count=1
```

This proves existing mechanisms remain unchanged; it does not prove LocalRunner uses them.

- [x] **Step 3: Full regression and build**

```bash
GOTOOLCHAIN=auto DWS_PACKAGE_VERSION=0.0.0-test go test ./...
GOTOOLCHAIN=auto make build
```

Report actual results. Under an explicitly authorized focused-baseline exception, list unchanged baseline failures separately; never relabel them as GREEN.

- [x] **Step 4: Scope/security audit**

```bash
git diff --check
git status --short --branch
rg -n 'sessionWebhook|clientSecret|DataFrame|WriteJSON|log\.|slog\.|fmt\.Printf|Println' internal/localrunner
rg -n 'connectionTicket|Authorization|Payload|payload|body|SSE|RawMessage' internal/localrunner
```

Expected: this ignored plan, the declared `internal/app` command skeleton/mount, and the new `internal/localrunner` package; no event ACK/client-secret dependency; no logging/printing; every ticket/raw/payload occurrence has a redaction, copy, or size-bound purpose.

- [x] **Step 5: Final report**

Report worktree/branch/SHA and missing-master deviation; original baseline; each RED; focused GREEN; full regression/build; files; exact OpenAPI alignment; and unimplemented boundaries. Do not add a commit step.

## 10. Authorized Phase 1 Vertical Slice

The user explicitly authorized continuing under the recorded focused-baseline
exception. The existing `test/scripts` failures remain evidence and must stay
separate from every focused GREEN result. This authorization supersedes only
the earlier plan-only pause; it does not relax any wire, security, formatting,
publication, or deployment boundary.

### 10.1 Authenticated control envelopes and lifecycle DTOs

- Every authenticated control success is exactly
  `{"success":true,"data":{...}}`; every failure remains exactly
  `{"error":{"code":"...","message":"..."}}`.
- `POST /v1/assistant/local-runners` request fields are exactly the required
  `localAgentId:string`, `displayName:string`, and `agentCard:object`. HTTP 201
  data is exactly `runnerId`, `endpointId`, `agentCardUrl`, `endpointBearer`,
  and `status:"ACTIVE"`.
- `endpointBearer` is a one-time response secret for the standard public RPC
  endpoint. The CLI may transfer it only to an injected system-keyring
  boundary. It must never enter a URL, log, normal CLI JSON/output, or disk
  configuration. Failure or uncertainty while saving requires explicit
  recovery; the response value must not be silently printed.
- GET runner and PUT runner agent-card return exact data fields `runnerId`,
  `endpointId`, `localAgentId`, `displayName`, `status`, `agentCardUrl`,
  `agentCardSha256`, `connected`, and nullable integer
  `lastHeartbeatAtEpochSecond`. PUT request is exactly `{agentCard:object}`.
- DELETE runner returns `runnerId`, `endpointId`, and `status:"REVOKED"`; it
  revokes the endpoint and closes the connection.
- GET connection returns `runnerId`, `endpointId`, `connected`, nullable
  `connectionId`, nullable integer `connectedAtEpochSecond`, and nullable
  integer `lastHeartbeatAtEpochSecond`.
- DELETE connection is idempotent and returns `runnerId`, `endpointId`, and
  `disconnected:boolean`; it never revokes the endpoint.
- `connections/open` retains the already-frozen five-field data envelope and
  one-shot in-memory ticket rules.

TDD sequence: add exact marshal/decode validation tests, watch them fail, add
the minimal DTO/client implementation, then run the focused package tests.

### 10.2 OpenAPI control client and secret sinks

- Define a narrow OAuth token provider and control-client interface rather than
  importing event/personal business payloads.
- Implement only the frozen paths above. Decode the stable HTTP status plus
  `error.code`; treat error `message` as display-only and never branch on it.
- Allow at most one OAuth refresh retry on 401 if the injected provider
  supports it. A retry for `connections/open` obtains a new response and never
  reuses a connection ticket.
- Keep endpoint-bearer keyring operations behind an interface. Phase 1 may use
  an in-memory fake in tests, but production code must not supply a disk-backed
  fallback.

### 10.3 WSS transport/session

- Dial only the opaque absolute `wss://` URL returned by OpenAPI and place the
  connection ticket only in the `Authorization: Bearer` header.
- The first outbound frame is matching `hello` with `agentCardSha256`, positive
  `maxConcurrent`, and `streaming:boolean`. Readiness requires matching
  `hello_ack` plus `accepted:true`, `connectionId`,
  `heartbeatIntervalMs:15000`, and `maxFrameBytes:262144`.
- After accepting `hello_ack`, the CLI starts one active heartbeat ticker at
  the advertised `heartbeatIntervalMs=15000`. Each outbound `heartbeat` uses
  the next client-to-server connection sequence; each inbound
  `heartbeat_ack` uses the independent server-to-client connection sequence.
  The CLI continues to answer any inbound `heartbeat` with `heartbeat_ack`.
  Heartbeat sequences never advance request-scoped sequences, and the ticker
  stops before the attempt returns on context cancellation, disconnect, or
  endpoint revocation. Any identity, direction, protocol, or sequence
  violation closes the session and fails all inflight requests. A second
  active connection conflicts.
- A failed or uncertain dial/handshake consumes the ticket. Reconnect must call
  `connections/open` and use a fresh ticket; no retry path may retain or replay
  the old value.

### 10.4 Localhost A2A proxy and multiplexing

- Frozen control attributes are exact: `request_start` requires
  `method:string`, `path:string`, `query:string` without the leading `?`,
  `headers:object<string,array<string>>`, `contentLength:int64`, and
  `deadlineEpochMs:int64`; `response_start` requires `status:integer` in
  `100..599` and the same header shape. `request_end`, `response_end`, and
  `cancel` have empty attributes. `error` requires non-blank static/safe
  `code` and `message` plus `retryable:boolean`, and may be request- or
  connection-scoped.
- Header names are lowercase and every value is an array whose order is
  preserved. Scalar, null, and comma-joined substitutes are rejected.
  `authorization`, `cookie`, `set-cookie`, `host`, `connection`, `upgrade`,
  `proxy-authorization`, `proxy-authenticate`, `te`, `trailer`,
  `transfer-encoding`, and every `x-forwarded-*` header are stripped in both
  directions.
- Sequence domains are independent by request and direction. Gateway to
  Runner starts each request at `request_start=0`, then increments for chunks,
  end, and later cancel. Runner to Gateway starts each request at
  `response_start=0`, then increments for chunks, end, and request error.
  Repeated, skipped, or decreasing sequence fails only that request and never
  advances its opposite direction. Hello/hello-ack start at zero in their own
  connection directions; heartbeat counters remain connection-scoped and do
  not change request sequence.
- Accept only configured loopback HTTP(S) upstream origins. Strip caller
  authorization, cookie, host, proxy, and hop-by-hop headers before injecting
  local authorization internally; none may be logged.
- Forward request/response start, chunks, and end with per-direction strictly
  increasing sequence numbers and bounded frames. Route by `requestId` so
  concurrent requests cannot cross.
- Flush the first SSE/body response chunk immediately. Client cancellation
  sends exactly one `cancel`, terminates local work, and removes inflight state.
- Never print or persist A2A request bodies, response bodies, SSE events, raw
  payloads, local Authorization, endpoint bearer, or connection ticket.

### 10.5 Agent Card and public RPC compatibility boundary

- Supported card protocols are exactly 0.3.0 and 1.0. Local structural URL
  validation accepts only loopback destinations; the public snapshot rewrites
  the RPC URL to `/v1/a2a/local-runners/{endpointId}/rpc` deterministically and
  uses the matching SHA-256 contract.
- Every Relay snapshot removes top-level source `authentication`,
  `securitySchemes`, and `security`, plus each `skills[*].security` override,
  and does not add a LocalRunner bearer scheme. This is a structural Agent Card
  operation, not a recursive same-key deletion: business `metadata.security`
  remains untouched. The published Card/RPC remains unauthenticated at this
  Relay boundary; WSS keeps its separate OAuth plus one-attempt
  connection-ticket handshake.
- After structurally rewriting only the root, `supportedInterfaces`, and
  `additionalInterfaces` callable URLs, recursively inspect every remaining
  card value. Any residual HTTP/HTTPS URL whose lexical host is loopback makes
  the card `invalidAgentCard`; documentation, icon, extension, or unknown URLs
  are neither retained nor guessed/replaced. This prevents a local URL from
  leaking through an unrelated field into the public snapshot.
- Public Card GET remains raw JSON with ETag even while the runner is offline.
  Public RPC maps not-found, revoked, offline, payload-limit, timeout, and
  protocol failures to the frozen OpenAPI statuses and codes without a Relay
  bearer. These are server-observable compatibility requirements, not a claim
  that CLI can locally exercise the public OpenAPI route.

### 10.6 CLI commands and production runtime

- Mount `expose`, `status`, `revoke`, `connect`, and `start-local` only under
  the repository's declared `dws deap runtime` group. The overlapping
  `dws deap local-runner` group is absent from Cobra, Help, Schema, CLI paths,
  and examples. The default provider constructs the production runtime rather
  than an unavailable placeholder, and every command exposes no secret-bearing
  value.
- Resolve OAuth access tokens through the existing DWS `TokenManager` and use
  the existing force-refresh path for one rejected-token retry. Resolve the
  current tenant/operator identity from the same active auth profile only in
  memory when constructing the strict connection identity; do not copy app
  `clientId`/`clientSecret` or persist owner identity in Runner config.
- If the legacy create response returns `endpointBearer`, retain its existing
  encrypted `internal/keychain` compatibility handling under a hashed
  Runner/Endpoint account. It is never a local Agent startup input and never
  appears in the published Card or `start-local` configuration summary; do not
  add a plaintext file fallback.
- Persist one non-sensitive JSON file per Runner under
  `<DWS config dir>/local-runners/` with mode 0600 and atomic rename. The exact
  content is Runner/Endpoint/local-agent/display identity, local Card URL,
  callable loopback origin, OpenAPI base, and Agent Card SHA-256; it contains
  no endpoint bearer, connection ticket, OAuth/local Authorization, body, or
  SSE data.
- Neither `expose` nor `start-local` publishes `--openapi-base`. New
  registrations resolve one control-plane base through
  `pkg/config.GetDEAPOpenAPIBaseURL`: the production default is
  `https://deap-open-api.dingtalk.com`; an exact nonblank value in
  `$DWS_CONFIG_DIR/deap_openapi_url` (default
  `~/.dws/deap_openapi_url`) overrides it, including the pre-release value
  `https://pre-deap-open-api.dingtalk.com`. Existing `status`, `revoke`, and
  `connect` operations continue using the base stored with their binding.
  `expose` fetches a loopback Agent Card without following it outside loopback,
  validates the frozen Card contract, creates the Runner, transfers any legacy
  one-time bearer response to keyring, fetches the authoritative Runner
  view/hash, and saves non-sensitive config.
- `status` performs both GET Runner and GET connection and reports their
  sanitized exact views. `revoke` performs DELETE Runner and then removes the
  exact local config/keyring entries. `connect` validates all supplied identity,
  hash, and target fields against config, opens a fresh ticket, performs the
  WSS session, runs the fixed-origin loopback proxy, and reconnects with a new
  ticket until context cancellation.
- Cleartext OpenAPI base URLs are accepted only for lexical loopback tests;
  production remote bases require HTTPS. Card document redirects and callable
  targets must remain lexical loopback. The stored loopback origin comes from
  the Card callable URL, not necessarily the Card document URL.
- Command tests prove tree/Schema presence, required inputs, no secret output,
  production-provider construction, and a local TLS OpenAPI/WSS plus HTTP/SSE
  `expose -> connect -> status -> revoke` lifecycle. They do not claim remote
  OpenAPI deployment acceptance.

### 10.7 Phase 1 completion boundary

Local unit/fake-server tests may establish DTO, OAuth control, handshake, heartbeat,
fresh-ticket reconnect, multiplexing, streaming/cancel, and command-delegation
behavior. Real OpenAPI HTTP/WSS/public-RPC interoperability, public DNS/TLS,
outer public-gateway WebSocket Upgrade preservation, load-balancer idle
timeouts, server-side card rewrite/hash equality, remote limits, and deployment
remain external integration gates and must be reported as unverified.

### 10.8 One-command local runtime orchestration

Add `dws deap runtime start-local <agent-ref>` as a declared composite leaf in
the sole LocalRunner command group. The positional accepts a supported local
agent channel, `test-echo`, or a lexical-loopback Card URL. It has no
`--openapi-base`; optional identity, display, concurrency, streaming, workdir,
model, memory, yolo, and timeout overrides are declared only where meaningful.
With no URL identity/name override, the production runtime
derives a stable `local-<sha256-prefix>` ID from the normalized Card URL and
uses the validated Card's non-blank `name`.

The runtime preparation stage reads and validates the Card once, then performs
a unique validated config lookup by `localAgentId`. If no stored binding
exists, it creates the Runner/Endpoint through the existing control client,
transfers the one-time endpoint bearer directly to the system keyring, and
saves the same non-sensitive config used by `connect`. If a binding exists, it
does not call CreateRunner: it GETs the stored Runner, requires exact
runner/endpoint/local-agent/display-name identity and requires the remote Card
hash to remain equal to the old stored hash before recomputing the public Card
from the current local snapshot. It fetches the exact server-returned HTTPS
public Card without credentials or redirects, requires HTTP 200 JSON bounded
to 1 MiB, and binds its raw bytes to the server-returned
`sha256:<64 lowercase hex>` digest. JSON semantic equality, rather than object
key order or raw-byte equality, decides whether the binding is unchanged. A
semantic change calls the existing agent-card update on that same Runner, then
requires the response to preserve every identity, public Card URL, and
`ACTIVE` status; it re-fetches the public Card, requires semantic equality with
the desired snapshot and raw-byte equality with the returned digest, and
atomically saves that server digest. Any pre-update remote digest drift,
invalid or redirected public Card response, post-update semantic/digest drift,
duplicate candidate, Card URL/origin mismatch, or OpenAPI-base mismatch fails
closed instead of overwriting an unknown change or creating a second binding.
The store uses a validated directory scan rather than a second persistent
index, avoiding index/config divergence.

Both paths return only a sanitized A2A summary and private in-process connect
options. The command JSON-encodes that summary before invoking the existing
blocking `Connect` path, so users can copy the public configuration while the
WSS/proxy stays active. The summary is exactly lowerCamelCase and includes
`type="A2A"`, public `agentCardUrl`, and the singular Runner/Endpoint with
pre-connection `CONNECTING` status. It contains no authentication, security,
credential storage, or exported credential declaration.

No failure after registration implicitly calls `Revoke`: a failed connection
returns its stable error while config and keyring state remain available for
`status`, `connect`, or explicit `revoke`. Context cancellation continues
through the existing reconnect/session/proxy stack and returns cleanly. The
command never prints or persists endpoint bearer, connection ticket, OAuth or
local Authorization, A2A body, or SSE content.

Implementation and verification steps:

- [x] Add command/help/Schema/argument tests proving the exact hierarchy,
  single positional, optional overrides, production provider, absence of the
  overlapping legacy group, and stable summary-before-connect ordering.
- [x] Add runtime tests proving deterministic URL-derived ID, Card-name default,
  one-time Card read, real control/keyring/config preparation, private connect
  options, clean cancellation, and no automatic revoke on connect failure.
- [x] Implement only the new runtime preparation method, the sanitized summary
  DTO, and the `deap runtime start-local` Tier 2 Cobra declaration; reuse
  existing `Expose` internals and `Connect` without changing HTTP/WSS wire
  contracts.
- [x] Run focused RED then GREEN, attempt `go test -race ./internal/cli
  -count=1`, and preserve its existing cumulative lazy-child slowdown evidence:
  the bounded diagnostic timed out at package level while both isolated slow
  tests passed under race. Run the LocalRunner-focused app race, `go test
  ./internal/app ./internal/cli -count=1`, and an independent-version `go build
  -o <temporary-output> ./cmd`, followed by tracked/no-index diff checks without
  any formatter.

### 10.9 In-process `test-echo` acceptance agent

Supersede the URL-only positional UX with
`dws deap runtime start-local <agent-ref>`. The existing lexical-loopback Agent
Card URL remains a supported external-agent compatibility form, while the exact
reference `test-echo` starts a self-contained acceptance agent inside the same
dws process. No Python, child process, local manifest, or general agent-ID
resolver is introduced in this phase.

`internal/app/localrunner_echo_agent.go` owns one bounded HTTP server. A first
registration uses `net.Listen("tcp", "127.0.0.1:0")`; recovery of a stored
`test-echo` binding reopens its exact validated loopback origin so the persisted
Card URL and proxy target remain unchanged. It exposes only:

- `GET /.well-known/agent-card.json`, returning a dynamic Card with fixed agent
  `version="1.0.0"` and distinct A2A `protocolVersion="0.3.0"`, whose callable
  JSON-RPC URL is the server's exact loopback `/rpc` URL;
- `POST /rpc` with `Content-Type: application/json`, accepting only JSON-RPC
  2.0 `message/send` and `message/stream` requests with text message parts;
- a direct A2A Message echo response for `message/send`, and exactly one
  flushed `data:` JSON-RPC Message event for `message/stream` before closing.

Every other path, method, media type, malformed shape, or oversized body is
rejected with a bounded static response. The handler has no logger and never
prints request bodies, credentials, headers, or response content. The request
body limit is independent of the tunnel's larger generic proxy bound.

The tunnel-to-loopback proxy emits exactly one console-visible
`localrunner.request.completed` record for every accepted or rejected request.
It contains only a one-way request-ID hash, normalized method, fixed path
template, HTTP status, response byte count, elapsed milliseconds, streaming
flag, outcome, and a static error category. It never includes a raw request ID,
endpoint ID, query, header name or value, credential, body, response content,
or raw error text. Cancellation, protocol failure, capacity rejection, and
disconnect use the same completion record without changing tunnel frames.

`productionLocalRunnerCommandRuntime.StartLocal` resolves `test-echo` before
the existing Card-read/create/keyring/config path. The built-in reference,
rather than its random Card URL, is the deterministic default identity seed;
the default display name is the built-in Card name. A private closer travels in
the in-process start result but is absent from the JSON summary. The runtime
closes the server if Card loading or registration fails, and the command defers
the same idempotent close across summary encoding, normal Connect return,
Connect failure, and context cancellation. Registration remains intact after a
Connect failure, matching Section 10.8; only the ephemeral built-in HTTP server
is stopped.

Implementation and verification steps:

- [x] Add failing real-HTTP tests for loopback/random-port binding, dynamic
  Card, `message/send`, first-flushed `message/stream`, path/method/media/body
  limits, and idempotent close.
- [x] Add failing runtime/command tests for single-command `test-echo`
  preparation, stable ID/name independent of port, summary-before-Connect,
  cleanup after Connect/cancel, and cleanup when registration fails.
- [x] Implement the bounded Echo handler and the minimal agent-reference
  resolution/closer lifecycle without changing control, WSS, tunnel, or public
  Agent Card wire contracts.
- [x] Run focused RED/GREEN, LocalRunner-focused race, `internal/app` and
  `internal/localrunner` regression, independent temporary-output build, and
  tracked/no-index diff checks without a formatter.

### 10.10 Shared local-agent backend and official A2A compatibility server

Extend the same long-running entry point with the local agent channel registry:

```text
dws deap runtime start-local opencode --workdir <project> [--model <provider/model>]
```

The registry accepts `opencode`, `codex`, `claudecode`, `qoder`, `qoderwork`,
`codebuddy`, `workbuddy`, and `custom`, plus the existing `test-echo` and
lexical-loopback Card URL compatibility forms. `gemini` remains available to
`dev connect` but is explicitly excluded from LocalRunner because that channel
uses a remote API rather than a DWS-owned local process. `--workdir` is required
for process-backed channel references. The command resolves a relative value
against the current directory, cleans it, requires an existing directory, and
passes only the absolute value into the runtime; empty directories are valid.

`internal/helpers.LocalAgentBackend` is the shared lifecycle boundary. It uses
the existing `agentSpecs`, `resolveExecAgent`, `forwarderForChannel`,
`forwardConnectTurn`, streaming/attachment interfaces, and `forwarderCloser`;
it does not copy a launcher or agent-specific HTTP client. Both `dev connect`
and LocalRunner obtain their process/session handle through this factory.
Channel, workdir, model, memory, yolo, and timeout options enter the same
backend, while prompt content, session IDs, credentials, and environment remain
in memory and out of config/logs.

The bounded loopback A2A surface uses official
`github.com/a2aproject/a2a-go/v2` message/Card/executor types and server handler,
with `a2acompat/a2av0.NewJSONRPCHandler` retaining the current DEAP JSON-RPC/SSE
wire. A thin Card producer normalizes the SDK compatibility spelling `0.3` to
the existing `0.3.0` contract at the root and in `supportedInterfaces`; it does
not upgrade the wire to A2A 1.0. The Card is streaming-capable, text-only, and
contains neither project path nor authentication/security declarations. The
official executor accepts only user Message text parts, joins ordered parts
with one newline, preserves nonblank `contextId` as the backend session key,
creates an agent Message reply, and emits one final official SSE event when the
backend has only a final response.

Invalid requests and unsupported parts use static JSON-RPC errors. Cancellation,
deadline, and other backend failures map to distinct static categories without
including prompts, response bodies, server credentials, environment, headers,
or raw errors. The adapter limits request bodies, binds only loopback, exposes
only the Card and RPC paths, and closes HTTP ingress plus the DWS-owned backend
exactly once on registration failure, Connect return, or context-driven command
exit. `test-echo` is a thin backend on this same official handler rather than a
second handwritten JSON-RPC/SSE server.

The default local agent ID is `<channel>-<first 16 lowercase sha256 hex>`, with
the digest computed from the normalized absolute workdir, independent of the random
loopback port. An explicit ID remains allowed. Stored config adds only the
exact `agentKind` and absolute `workDir`; model and all credentials are not
persisted. Resume first requires the same kind/workdir and then reuses the
existing strict runner/endpoint/origin/status/raw-digest/public-Card semantic
checks. A matching binding reopens the stored loopback origin without Create;
any kind/workdir or existing remote drift fails closed before an unknown project
can be attached. Control OAuth, endpoint-bearer compatibility storage, WSS
ticket use, tunnel frames, Relay Card normalization, and unauthenticated public
RPC semantics are unchanged.

Implementation and verification steps:

- [x] Add shared helper-factory RED/GREEN coverage for registry selection,
  session forwarding, streaming/attachment capability preservation, one-time
  cleanup, and `dev connect` factory usage.
- [x] Add official-executor/real-loopback RED/GREEN coverage for Card, send,
  one final SSE event, text-only validation, context mapping,
  cancellation/timeout/error redaction, and one-time cleanup.
- [x] Add command/config/runtime RED/GREEN coverage for Help/Schema flags,
  relative normalization, empty-directory startup, stable ID, registration
  cleanup, stored kind/workdir drift rejection, and no-create/no-update resume.
- [x] Run final focused, race, full related-package, Schema drift, independent
  build, and Git diff gates without a formatter; record exact evidence in the
  delivery report.

## 11. Protocol and Plan Change History

| Date | Change | Reason |
|---|---|---|
| 2026-08-19 | Created the CLI plan and limited the first batch to singular config, connection envelopes, in-memory ticket, tunnel codec, and no-I/O state machine. | These units are independently testable without claiming WSS, proxy, SSE, reconnect, or commands. |
| 2026-08-19 | Recorded that the remote has no `master` and based the target branch on fetched `origin/main` SHA `ede8246de1a1007c9fa6934132404d130661896e`. | The handoff named `origin/master`, but inventing it would obscure the actual default trunk. |
| 2026-08-19 | Froze strict one-to-one identity and required every frame, including `hello_ack`, to carry matching runner and endpoint IDs. | Uniform connection identity prevents endpoint switching and aligns both implementations. |
| 2026-08-19 | Froze UTF-8 JSON control frames and only request/response chunks as four-byte big-endian header length, header JSON, and opaque bytes, capped at 262144 bytes. | Match OpenAPI exactly, preserve arbitrary bytes, and bound allocation. |
| 2026-08-19 | Initially received a direct five-field success-object proposal, then replaced it before implementation with final `{"success":true,"data":{...}}` and standard error envelope/status-code mapping. | The OpenAPI peer reconciled the inner five-field contract with existing public envelope conventions; CLI must not retain the superseded shape. |
| 2026-08-19 | Required pre-dial echo validation, opaque server-selected WSS URL, signed 64-bit UTC-second expiry, and fresh ticket after failed/uncertain handshake. | Avoid identity drift, URL invention, time-unit ambiguity, and one-time-ticket replay. |
| 2026-08-19 | Made the ticket private, redacted, one-shot, and memory-only. | Bearer-equivalent material must remain usable for the handshake without entering persistence, output, or logs. |
| 2026-08-19 | Recorded the untouched full-suite failure and blocked all Go implementation pending repair or explicit focused-baseline authorization. | `test/scripts` expects absent workflows and has two release rejection failures; the handoff explicitly requires pausing production code on baseline failure. |
| 2026-08-19 | Recorded that `.gitignore:31` ignores this required `docs/superpowers/plans/...` file and left the index untouched. | The plan must exist at the handoff path, but staging it with force or changing ignore rules would exceed the no-commit/no-unrequested-Git-mutation boundary. |
| 2026-08-19 | Froze create, lifecycle, connection, Card/RPC, and WSS Phase 1 DTO/state semantics from OpenAPI Sections 2.6/2.7, including the one-time `endpointBearer` keyring boundary. | The CLI needs exact lifecycle shapes and two distinct secret lifetimes before implementing its vertical slice; `endpointBearer` is a public-RPC credential, while the connection ticket is a one-attempt WSS credential. |
| 2026-08-19 | Superseded the earlier plan-only pause with the user's explicit focused-baseline implementation authorization while retaining the original full-suite failure evidence. | The OpenAPI peer's earlier stop wording reflected stale authorization state; the correction permits production TDD but does not convert the unrelated `test/scripts` baseline failures into GREEN. |
| 2026-08-19 | Completed the previously unspecified request/response control attributes, header representation/filter set, and independent per-request directional sequence domains from OpenAPI Section 2.4. | Proxy TDD needs one exact `headers/status/contentLength/deadline` shape and cannot safely infer whether sequence is connection-wide; this completion prevents the CLI from creating a second wire contract and supersedes its temporary connection-wide request-sequence assumption. |
| 2026-08-19 | Added the frozen public Agent Card authentication rewrite: discard source authentication/security declarations and publish only the credential-free standard `localRunnerBearer` HTTP Bearer scheme and requirement. | Public A2A consumers need a standard bearer declaration, while source-local authentication metadata and the actual one-time endpoint bearer must never be copied into the public snapshot. |
| 2026-08-19 | Added the post-rewrite recursive residual-loopback rejection for Agent Card fields outside the three callable URL locations. | Unknown, documentation, icon, or extension fields must not leak localhost URLs publicly, and guessing which such URL should be rewritten would corrupt card semantics. |
| 2026-08-19 | Corrected the four LocalRunner command declarations from pure `local` to `composite` interface mode. | Each command coordinates remote OAuth/OpenAPI and WSS capabilities with a local loopback proxy, so classifying it as a reviewed pure-local implementation was inaccurate and failed the Catalog contract gate. |
| 2026-08-19 | Recorded the post-implementation full-suite exit `1`: the unchanged workflow-file failures remained and `test/scripts` timed out at 10 minutes; the transient LocalRunner interface classification failure found in that run was corrected and the complete `internal/cli` package subsequently passed. | The focused-baseline exception requires preserving the failing full-suite evidence while separately proving that the new Schema regression was removed; rerunning the known 10-minute release-script timeout would not turn the repository baseline GREEN. |
| 2026-08-19 | Corrected the initial test-plan prose to reject unknown `connections/open` envelope/data fields and to require all four frozen `hello_ack` attributes. | The OpenAPI source of truth defines exact shapes; leaving an earlier forward-compatibility note or two-field example would contradict the implemented cross-project contract. |
| 2026-08-19 | Replaced the command runtime's unavailable default with production OAuth, encrypted keyring, non-sensitive atomic config, control/WSS/reconnect/proxy wiring, and a local TLS WebSocket plus SSE command lifecycle test. | A mounted command tree is not usable while the provider always returns `local_runner_command_runtime_unavailable`; the follow-up requires actual `expose/status/revoke/connect` behavior without relaxing secret boundaries. |
| 2026-08-19 | Restricted remote OpenAPI bases to HTTPS, rejected Card redirects outside lexical loopback, and derived the stored loopback origin from the validated callable URL rather than the Card document URL. | OAuth must never be sent over remote cleartext, redirect handling must not widen local Card fetch scope, and Card discovery and RPC endpoints may legitimately use different loopback origins. |
| 2026-08-19 | Added explicit lowerCamelCase JSON tags to the sanitized `CreatedRunner` command projection. | The `expose` command directly encodes this object, so Go's default exported field names violated the already-frozen `runnerId`/`endpointId`/`agentCardUrl`/`status` output contract. |
| 2026-08-19 | Changed the production default LocalRunner OpenAPI base from `https://api-deap.dingtalk.com` to `https://api.dingtalk.com`, retaining explicit `--openapi-base` overrides. | The user selected the already-open DingTalk API domain for the control plane; this changes only the CLI default origin and its Help/Schema projection, not API paths, server-selected WSS URLs, or tunnel wire semantics. |
| 2026-08-19 | Synchronized OpenAPI Section 2.8's shared public WSS default to `wss://api.dingtalk.com/v1/local-runners/connections/{runnerId}` while retaining opaque `webSocketUrl` consumption. | The public gateway now uses the already-open `api.dingtalk.com` hostname for both HTTP and WSS defaults; container Tengine Upgrade support does not prove outer-edge Upgrade preservation, so published-environment WSS remains an explicit release acceptance gate. |
| 2026-08-19 | Added the one-command `deap runtime start-local <agent-card-url>` orchestration contract while retaining the lower-level LocalRunner leaves. | Users need a copyable public A2A configuration and a maintained localhost bridge from one invocation; separating preparation/summary from the existing blocking connect path preserves secret boundaries, deterministic defaults, explicit revoke ownership, and compatibility. |
| 2026-08-19 | Superseded the URL-only positional with `start-local <agent-ref>`, adding the in-process `test-echo` A2A v0.3.0 acceptance agent while retaining loopback Card URLs. | The user requires one dws process with no Python sidecar; a narrowly bounded built-in Echo Agent provides deterministic local acceptance without pretending arbitrary agent IDs or process supervision are implemented. |
| 2026-08-20 | Migrated the default control and server-selected WSS origin from OpenAPI's `api.dingtalk.com` route to Studio pre-release `pre-deap.dingtalk.com`; every control call now sends active-profile corp/user selection headers in addition to the user OAuth bearer. | Studio owns an existing pre-release public domain and browser-cookie SSO cannot authenticate the CLI. Studio verifies the bearer, resolves its DingTalk identity, and compares it with both headers before accepting the owner context. |
| 2026-08-20 | Corrected persisted and HELLO `agentCardSha256` handling to require and preserve the frozen `sha256:<64 lowercase hex>` representation rather than a bare 64-character digest. | OpenAPI returns and compares the prefixed digest; accepting only bare hex caused `start-local` to fail locally after successful registration, while stripping or rebuilding the prefix would risk handshake identity drift. |
| 2026-08-20 | Superseded the control-header portion of the Studio migration: LocalRunner control calls now send only the DWS user OAuth bearer and omit optional `X-Dingtalk-Corp-Id` / `X-Dingtalk-User-Id` caller cross-check headers. | Pre-release traces proved bearer verification, trusted identity resolution, employee lookup, and corp conversion succeeded while both create attempts failed `dingtalk_user_header_mismatch`; CLI `TokenData.UserID` and Studio's trusted numeric `uid` have different semantics, so the client must not send an optional exact-match claim it cannot prove. |
| 2026-08-20 | Made the CLI actively send connection-scoped `heartbeat` frames every `heartbeatIntervalMs=15000` after `hello_ack`, accept sequenced `heartbeat_ack`, and stop the ticker with the WSS attempt lifecycle. | Pre-release evidence showed the server renews its 45-second lease only when it receives a client heartbeat; the prior passive-only client let the lease expire, made public RPC report offline, and then reconnected despite an otherwise healthy socket. |
| 2026-08-20 | Made `start-local` recover one unique valid `StoredRunnerConfig` by `localAgentId`, validate it against the current Card and authenticated Runner view, and skip CreateRunner; stored `test-echo` bindings reopen their original loopback origin. | Re-running the one-command UX previously attempted a duplicate registration and received `binding already exists`; idempotent recovery must reuse the existing one-to-one binding while failing closed on identity, target, Card, hash, origin, or control-base drift. |
| 2026-08-20 | Added the required top-level `version="1.0.0"` to the built-in `test-echo` Card while keeping A2A `protocolVersion="0.3.0"`, and allowed guarded in-place Card updates for an otherwise unchanged stored binding. | The official 0.3.0 Agent Card parser rejects a Card without the distinct agent version; an existing one-command binding must publish the corrected snapshot without duplicate registration, but only after the remote still matches the old stored digest and only when the update response proves the same identity, endpoint, URL, ACTIVE state, and exact new digest. |
| 2026-08-21 | Replaced the nested `httpAuthSecurityScheme` public Card declaration with the flat discriminated `localRunnerBearer={"type":"http","scheme":"bearer"}` shape and synchronized CLI digest/resume expectations. | The messaging A2A SDK selects security-scheme subtypes through the top-level `type` discriminator and rejects the old nested shape; keeping the CLI rewrite on that shape would also recompute a different digest after OpenAPI publishes the corrected Card and would trigger an erroneous resume PUT. |
| 2026-08-21 | Added one safe console-visible completion record at the shared tunnel-to-loopback proxy boundary for success, streaming, error, cancellation, rejection, and disconnect paths. | Successful pre-release RPC and SSE traffic was otherwise invisible after the initial configuration summary; fixed normalized metadata provides operational evidence without exposing request IDs, endpoint IDs, query, headers, credentials, bodies, response content, or raw error text, and does not alter tunnel frames. |
| 2026-08-21 | Changed stored-binding Card recovery to bind the server's raw public-Card digest while using bounded credential-free HTTPS fetches and JSON semantic equality for change detection and post-update verification. | Jackson and Go serialize equivalent object keys in different orders; comparing their raw digests triggered an unnecessary PUT and then rejected the unchanged server digest, so raw hashes remain concurrency evidence while key order is no longer treated as an A2A Card change. |
| 2026-08-21 | Removed CLI-authored `localRunnerBearer`, top-level authentication/security declarations, each `skills[*].security` override, and the bearer/keyring block from the published-Card expectation and `start-local` summary while retaining business `metadata.security` and redacted legacy endpoint-bearer response storage compatibility. | The Relay and the existing digital-employee A2A chain are now explicitly decoupled: public Card/RPC is unauthenticated at both Card and AgentSkill levels, WSS keeps OAuth plus its connection ticket, and a Relay endpoint bearer must not be presented as a local Agent startup requirement or exported configuration. |
| 2026-08-21 | Added `start-local opencode --workdir <project>` with an in-process A2A 0.3 adapter over the existing OpenCode serve/session/message lifecycle, stable workdir identity, optional model override, and guarded stored-binding recovery. | Users need one DWS command to expose a real project reasoning agent rather than an echo or separately managed HTTP service; the thin façade keeps OpenCode discovery and cleanup in the mature implementation while leaving Relay, control OAuth, WSS tickets, tunnel frames, and public RPC authentication semantics unchanged. |
| 2026-08-21 | Consolidated every LocalRunner leaf under the sole `deap runtime` group, removed public `--openapi-base` in favor of `deap_openapi_url` with production default `https://deap-open-api.dingtalk.com`, migrated the loopback Card/RPC server to official `a2a-go/v2` through `a2acompat/a2av0` while preserving `0.3.0`, and routed LocalRunner plus `dev connect` through one shared local-agent backend for `opencode`, `codex`, `claudecode`, `qoder`, `qoderwork`, `codebuddy`, `workbuddy`, and `custom`. | The product has one LocalRunner lifecycle surface and one environment resolver; official SDK types/handlers replace handwritten JSON-RPC/SSE envelopes without an incompatible wire upgrade, while reusing the existing agent registry, forwarder, process, session, streaming, attachment, and close lifecycle prevents a second OpenCode-specific launcher. `gemini` remains a `dev connect` remote-API channel and is explicitly excluded from LocalRunner. |
