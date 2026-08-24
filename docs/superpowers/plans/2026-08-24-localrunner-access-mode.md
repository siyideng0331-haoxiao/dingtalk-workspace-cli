# LocalRunner Access Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one LocalRunner-only `--access-mode workspace|full` contract for Codex, Claude Code, Qoder, and OpenCode while preserving and explaining the unchanged permission behavior of every other supported Harness.

**Architecture:** Parse and validate the new public CLI flag in `internal/app`, then carry a typed access mode only through the four dedicated LocalRunner transports. `workspace` limits mutations to the project plus the profile-scoped DWS config directory using each Harness's native policy; `full` maps to that Harness's native bypass/full-access mode. Shared DevConnect production code remains unchanged. For shared/non-dedicated Harnesses, keep the existing `Yolo` path untouched and emit a plain-language stderr notice before the machine-readable startup result.

**Tech Stack:** Go 1.25, Cobra, Codex app-server JSON-RPC, Claude/Qoder streaming JSON CLIs, OpenCode loopback HTTP server.

---

### Task 1: Pin the CLI contract and ignored-Harness notices

**Files:**
- Modify: `internal/app/localrunner_command_test.go`
- Modify: `internal/app/localrunner_command.go`

- [x] **Step 1: Write failing tests** for public Help/Schema enum delivery, default `workspace`, explicit `full`, invalid values, legacy `--yolo` conflict/compatibility, and stderr-only notices for `qoderwork`, `codebuddy`, `workbuddy`, and `custom`.
- [x] **Step 2: Run RED:** `GOTOOLCHAIN=auto go test ./internal/app -run '^(TestLocalRunnerRuntimeCommandContract|TestLocalRunnerStartLocalAccessModeContract|TestLocalRunnerStartLocalExplainsIgnoredAccessModeOnStderr|TestLocalRunnerDedicatedHarnessAccessModeMappings)$' -count=1` and confirm failures are caused by the missing flag, transport mappings, and notices.
- [x] **Step 3: Implement the minimum parser:** add `AccessMode string`, the public `--access-mode` enum contract, default `workspace`, explicit legacy `--yolo` compatibility, and a LocalRunner-owned policy description function that writes only to `cmd.ErrOrStderr()`.
- [x] **Step 4: Run the same focused test and require GREEN.**

### Task 2: Map the four dedicated Harnesses

**Files:**
- Modify: `internal/app/localrunner_harness_backend.go`
- Modify: `internal/app/localrunner_harness_codex.go`
- Modify: `internal/app/localrunner_harness_claude.go`
- Modify: `internal/app/localrunner_harness_qoder.go`
- Modify: `internal/app/localrunner_harness_opencode.go`
- Modify: focused `internal/app/localrunner_harness_*_test.go` files

- [x] **Step 1: Write failing transport tests** proving `workspace` and `full` argv/JSON/config for all four Harnesses, including the exact absolute `DWS_CONFIG_DIR` additional root in workspace mode.
- [x] **Step 2: Run RED:** use the combined focused command from Task 1 and confirm all four missing mappings fail for the expected reason.
- [x] **Step 3: Implement minimum mappings:** Codex `workspace-write` policy roots versus `danger-full-access`; Claude `acceptEdits --add-dir` versus bypass; Qoder `accept_edits --add-dir` versus bypass; OpenCode external-directory allowlist versus wildcard allow.
- [x] **Step 4: Run the focused tests and require GREEN.**

### Task 3: Preserve non-dedicated behavior and document the contract

**Files:**
- Modify: `internal/app/localrunner_runtime_test.go`
- Modify: `docs/localrunner-a2a-streaming.md`

- [x] **Step 1: Add a regression test** proving non-dedicated Harnesses still receive the pre-existing shared-backend `Yolo` setting regardless of `--access-mode`.
- [x] **Step 2: Append the access-mode contract and a dated audit row** to `docs/localrunner-a2a-streaming.md`, including why shared DevConnect code is untouched.
- [x] **Step 3: Run focused command/runtime/transport tests.**

### Task 4: Verify and deliver through the established branches

**Files:**
- Review all changed files; do not run a formatter.

- [x] **Step 1:** Run `GOTOOLCHAIN=auto go test ./internal/app ./internal/helpers -count=1`.
- [x] **Step 2:** Run `GOTOOLCHAIN=auto make build` and `git diff --check`.
- [ ] **Step 3:** Review the requirement checklist and diff, then commit and non-force push to `feature/20260819_localrunner_a2a_cli_1`.
- [ ] **Step 4:** Cherry-pick the single change in a fresh isolated worktree based on `feature/20260823_dws_profile_a2a_1`, repeat focused/full/build/diff verification, and non-force push the profile branch.
