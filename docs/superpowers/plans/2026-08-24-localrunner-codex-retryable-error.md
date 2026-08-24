# LocalRunner Codex Retryable Error Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `test-driven-development` to implement each task and `verification-before-completion` before making any completion claim. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the LocalRunner-owned Codex app-server transport continue through retryable v2 `error` notifications and return a safe, actionable summary only for terminal errors.

**Architecture:** Decode the complete Codex v2 `ErrorNotification` in the existing `internal/app` transport, route it by `threadId`, and use `willRetry` as the sole retry/terminal decision. Keep raw prompt, thread, turn, credential, and structured provider details out of returned errors by passing only the human-readable message fields through `localRunnerHarnessSafeDetail`.

**Tech Stack:** Go 1.25.9, Codex app-server JSON-RPC v2 notifications, the repository's LocalRunner Harness transport, standard `encoding/json`, and Go `testing`.

---

### Task 1: Lock the Codex v2 error-notification contract

**Files:**
- Create: `internal/app/localrunner_harness_codex_test.go`
- Modify: `internal/app/localrunner_harness_codex.go`

- [ ] **Step 1: Write failing transport tests**

Construct an in-memory `localRunnerCodexClient` and enqueue real JSON notification shapes. Assert that `willRetry=true` is followed by delta/completed success, another thread's notification is ignored, `willRetry=false` returns sanitized `message` plus `additionalDetails`, and a notification missing required routing/decision fields fails with a static malformed-notification error. With explicit debug enabled, require one `localrunner.codex.retrying` record containing only sanitized detail and `will_retry=true`; require no retry record when debug is disabled.

```go
func TestLocalRunnerCodexTurnContinuesAfterRetryableError(t *testing.T) {
	client := newTestLocalRunnerCodexClient(t,
		codexNotification("thread-current", "turn-current", true, "connection reset", nil),
		codexDelta("thread-current", "recovered answer"),
		codexCompleted("thread-current", "recovered answer"),
	)
	got, err := client.turn(context.Background(), "thread-current", "private prompt", nil)
	if err != nil || got != "recovered answer" {
		t.Fatalf("turn = %q, %v", got, err)
	}
}
```

- [ ] **Step 2: Run the focused tests and record RED**

Run:

```bash
GOTOOLCHAIN=auto go test ./internal/app -run '^TestLocalRunnerCodexTurn' -count=1
```

Expected: FAIL because the current `case "error"` returns `Codex app-server error notification` before retry recovery and discards terminal details.

- [ ] **Step 3: Implement the minimum parser and turn handling**

Decode required `error`, `threadId`, `turnId`, and `willRetry` fields. Ignore valid notifications for another thread, fail closed on malformed current/identity-less notifications, continue on a matching retryable notification, and return only this sanitized terminal detail:

```go
detail := localRunnerHarnessSafeDetail(
	strings.TrimSpace(notification.Error.Message+": "+notification.Error.AdditionalDetails),
	[]string{prompt, threadID, notification.TurnID},
)
```

Do not serialize `codexErrorInfo` into the returned error.

For a matching retryable notification, reuse the same sanitized detail in a debug record only after checking `localRunnerA2AContentDebugEnabled`:

```go
if localRunnerA2AContentDebugEnabled.Load() {
	slog.DebugContext(ctx, "localrunner.codex.retrying", "detail", detail, "will_retry", true)
}
```

- [ ] **Step 4: Format only changed Go files and verify GREEN**

Run:

```bash
gofmt -w internal/app/localrunner_harness_codex.go internal/app/localrunner_harness_codex_test.go
GOTOOLCHAIN=auto go test ./internal/app -run '^TestLocalRunnerCodexTurn' -count=1
```

Expected: PASS with retry recovery, thread isolation, terminal redaction, and malformed-notification coverage.

### Task 2: Document, verify, and synchronize the fix

**Files:**
- Modify: `docs/localrunner-a2a-streaming.md`
- Create: `docs/superpowers/plans/2026-08-24-localrunner-codex-retryable-error.md`

- [ ] **Step 1: Append the protocol-adaptation history**

Add a dated history row explaining that Codex v2 retryable error notifications are progress signals rather than terminal Harness failures, while terminal summaries remain sanitized.

- [ ] **Step 2: Run the required A2A verification**

Run:

```bash
GOTOOLCHAIN=auto go test ./internal/app ./internal/helpers -count=1
GOTOOLCHAIN=auto make build
git diff --check
```

Expected: all commands exit 0. Confirm `git diff -- internal/helpers` is empty.

- [ ] **Step 3: Commit and non-force push the A2A branch**

Stage only the Codex production/test files, streaming documentation, and this plan (force-add the plan if the existing ignore rule requires it), then commit and push with:

```bash
git push origin HEAD:refs/heads/feature/20260819_localrunner_a2a_cli_1
```

Fresh-fetch and require local commit, tracking ref, and `git ls-remote` to match.

- [ ] **Step 4: Cherry-pick into the isolated profile worktree**

Create a second detached worktree from fresh `origin/feature/20260823_dws_profile_a2a_1`, cherry-pick the single A2A commit, repeat focused tests, app/helpers tests, build, and `git diff --check`, then non-force push:

```bash
git push origin HEAD:refs/heads/feature/20260823_dws_profile_a2a_1
```

Fresh-fetch both branches and compare stable patch IDs for the two commits. Remove both temporary worktrees with `git worktree remove`.
