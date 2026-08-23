package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

type fakeLocalRunnerOpenCodeBackend struct {
	mu         sync.Mutex
	calls      []localRunnerOpenCodePromptCall
	streamCalls []localRunnerOpenCodePromptCall
	streamSnapshots []string
	streamReply string
	streamErr error
	reply      string
	err        error
	closeCalls int
}

type localRunnerOpenCodePromptCall struct {
	sessionKey string
	prompt     string
}

func (f *fakeLocalRunnerOpenCodeBackend) Prompt(_ context.Context, sessionKey, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, localRunnerOpenCodePromptCall{sessionKey: sessionKey, prompt: prompt})
	return f.reply, f.err
}

func (f *fakeLocalRunnerOpenCodeBackend) Stream(_ context.Context, sessionKey, prompt string, onDelta func(string)) (string, error) {
	f.mu.Lock()
	f.streamCalls = append(f.streamCalls, localRunnerOpenCodePromptCall{sessionKey: sessionKey, prompt: prompt})
	snapshots := append([]string(nil), f.streamSnapshots...)
	reply := f.streamReply
	err := f.streamErr
	if reply == "" && err == nil {
		reply = f.reply
	}
	f.mu.Unlock()
	for _, snapshot := range snapshots {
		if onDelta != nil {
			onDelta(snapshot)
		}
	}
	return reply, err
}

func (f *fakeLocalRunnerOpenCodeBackend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return nil
}

type controlledLocalRunnerStreamingBackend struct {
	started chan context.Context
	complete chan localRunnerA2AStreamResult
	stopped chan error
}

func newControlledLocalRunnerStreamingBackend() *controlledLocalRunnerStreamingBackend {
	return &controlledLocalRunnerStreamingBackend{
		started: make(chan context.Context, 1),
		complete: make(chan localRunnerA2AStreamResult, 1),
		stopped: make(chan error, 1),
	}
}

func (b *controlledLocalRunnerStreamingBackend) Prompt(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected synchronous prompt")
}

func (b *controlledLocalRunnerStreamingBackend) Stream(ctx context.Context, _, _ string, _ func(string)) (string, error) {
	b.started <- ctx
	select {
	case completed := <-b.complete:
		b.stopped <- nil
		return completed.reply, completed.err
	case <-ctx.Done():
		b.stopped <- context.Cause(ctx)
		return "", ctx.Err()
	}
}

func (*controlledLocalRunnerStreamingBackend) Close() error { return nil }

func TestLocalRunnerAgentExecutorUsesOfficialA2ATypes(t *testing.T) {
	backend := &fakeLocalRunnerOpenCodeBackend{reply: "official reply"}
	executor := &localRunnerA2AExecutor{backend: backend, defaultContext: "default-context"}
	var _ a2asrv.AgentExecutor = executor
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("first"), a2a.NewTextPart("second"))
	message.ContextID = "context-1"
	message.ID = "message-1"
	execContext := &a2asrv.ExecutorContext{Message: message, ContextID: message.ContextID}

	var events []a2a.Event
	for event, err := range executor.Execute(context.Background(), execContext) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("official executor events = %d, want 1", len(events))
	}
	reply, ok := events[0].(*a2a.Message)
	if !ok || reply.Role != a2a.MessageRoleAgent || reply.ContextID != "context-1" || len(reply.Parts) != 1 || reply.Parts[0].Text() != "official reply" {
		t.Fatalf("official executor reply = %#v", events[0])
	}
}

func TestLocalRunnerA2ADebugLogsInboundAndSynchronousOutboundContentSafely(t *testing.T) {
	const sensitiveContext = "a2a-context-sensitive-123"
	const sensitiveMessage = "message-sensitive-456"
	const sensitiveToken = "token-value-789"
	const sensitiveCredential = "credential-value-012"
	const sensitiveCookie = "cookie-value-234"
	const sensitiveSession = "native-session-sensitive-345"
	parts := []string{
		"first line token=" + sensitiveToken,
		"second line credential=" + sensitiveCredential + " cookie=" + sensitiveCookie + " context_id=" + sensitiveContext,
	}
	reply := "actual reply Authorization: Bearer " + sensitiveToken + " session_id=" + sensitiveSession
	logs := captureLocalRunnerA2AMessageLogs(t, true, func() {
		backend := &fakeLocalRunnerOpenCodeBackend{reply: reply}
		executor := &localRunnerA2AExecutor{backend: backend, defaultContext: "default-context", harness: "qoder"}
		message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(parts[0]), a2a.NewTextPart(parts[1]))
		message.ContextID = sensitiveContext
		message.ID = sensitiveMessage
		execContext := &a2asrv.ExecutorContext{Message: message, ContextID: message.ContextID}
		for _, err := range executor.Execute(context.Background(), execContext) {
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	if len(logs.records) != 2 {
		t.Fatalf("content log records = %d, want inbound and outbound: %s", len(logs.records), logs.raw)
	}
	inbound, outbound := logs.records[0], logs.records[1]
	if inbound["msg"] != "localrunner.a2a.message.inbound" || inbound["harness"] != "qoder" || inbound["mode"] != "sync" || inbound["sequence"] != float64(0) {
		t.Fatalf("inbound content log = %#v", inbound)
	}
	inboundContent, _ := inbound["content"].(string)
	if !strings.Contains(inboundContent, "first line") || !strings.Contains(inboundContent, "second line") || inbound["content_bytes"] != float64(len(strings.Join(parts, "\n"))) || inbound["truncated"] != false {
		t.Fatalf("inbound content fields = %#v", inbound)
	}
	if outbound["msg"] != "localrunner.a2a.message.outbound" || outbound["mode"] != "sync" || outbound["sequence"] != float64(1) || outbound["append"] != false || outbound["last"] != true || outbound["content_bytes"] != float64(len(reply)) {
		t.Fatalf("outbound content log = %#v", outbound)
	}
	if content, _ := outbound["content"].(string); !strings.Contains(content, "actual reply") {
		t.Fatalf("outbound content = %q", content)
	}
	turnHash, _ := inbound["turn_hash"].(string)
	if !strings.HasPrefix(turnHash, "sha256:") || outbound["turn_hash"] != turnHash {
		t.Fatalf("turn hashes = %#v / %#v", inbound["turn_hash"], outbound["turn_hash"])
	}
	for _, forbidden := range []string{sensitiveContext, sensitiveMessage, sensitiveToken, sensitiveCredential, sensitiveCookie, sensitiveSession} {
		if strings.Contains(logs.raw, forbidden) {
			t.Fatalf("content logs leaked %q: %s", forbidden, logs.raw)
		}
	}
	if len(logs.eventRecords) != 1 {
		t.Fatalf("sync event logs = %#v", logs.eventRecords)
	}
	eventRecord := logs.eventRecords[0]
	if eventRecord["sequence"] != float64(1) || eventRecord["event_type"] != "message" || eventRecord["kind"] != "message" || eventRecord["truncated"] != false {
		t.Fatalf("sync event log = %#v", eventRecord)
	}
	var eventJSON map[string]any
	if raw, _ := eventRecord["event_json"].(string); json.Unmarshal([]byte(raw), &eventJSON) != nil || nestedString(eventJSON, "parts", "0", "text") == "" || eventJSON["contextId"] != "[redacted]" || eventJSON["messageId"] != "[redacted]" {
		t.Fatalf("sync event_json = %#v", eventRecord["event_json"])
	}
}

func TestLocalRunnerA2ADebugLogsEachDeliveredStreamingArtifact(t *testing.T) {
	type deliveredArtifact struct {
		text string
		appendPart bool
		last bool
	}
	var delivered []deliveredArtifact
	var deliveredEvents []map[string]any
	logs := captureLocalRunnerA2AMessageLogs(t, true, func() {
		backend := &fakeLocalRunnerOpenCodeBackend{
			streamSnapshots: []string{"alpha", "alpha beta"},
			streamReply: "alpha beta gamma",
		}
		agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", "codex")
		if err != nil {
			t.Fatal(err)
		}
		defer agent.Close()
		body := []byte(`{"jsonrpc":"2.0","id":"stream-1","method":"message/stream","params":{"message":{"kind":"message","role":"user","messageId":"message-1","contextId":"context-1","parts":[{"kind":"text","text":"stream input"}]}}}`)
		response := postLocalRunnerOpenCode(t, agent.RPCURL(), body)
		streamBody, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("stream status=%d body=%s error=%v", response.StatusCode, streamBody, readErr)
		}
		deliveredEvents = decodeLocalRunnerSSEEvents(t, streamBody)
		for _, event := range deliveredEvents {
			if event["kind"] != "artifact-update" {
				continue
			}
			appendPart, _ := event["append"].(bool)
			last, _ := event["lastChunk"].(bool)
			delivered = append(delivered, deliveredArtifact{
				text: nestedString(event, "artifact", "parts", "0", "text"),
				appendPart: appendPart,
				last: last,
			})
		}
	})
	if len(delivered) < 2 || len(logs.records) != len(delivered)+1 {
		t.Fatalf("delivered artifacts=%#v content logs=%#v", delivered, logs.records)
	}
	if logs.records[0]["msg"] != "localrunner.a2a.message.inbound" || logs.records[0]["mode"] != "stream" {
		t.Fatalf("stream inbound log = %#v", logs.records[0])
	}
	for index, artifact := range delivered {
		record := logs.records[index+1]
		if record["msg"] != "localrunner.a2a.message.outbound" || record["mode"] != "stream" || record["sequence"] != float64(index+1) || record["content"] != artifact.text || record["content_bytes"] != float64(len(artifact.text)) || record["append"] != artifact.appendPart || record["last"] != artifact.last {
			t.Fatalf("stream outbound log %d = %#v, artifact = %#v", index, record, artifact)
		}
	}
	for _, forbidden := range []string{"context-1", "message-1"} {
		if strings.Contains(logs.raw, forbidden) {
			t.Fatalf("stream logs leaked %q: %s", forbidden, logs.raw)
		}
	}
	if len(logs.eventRecords) != len(deliveredEvents) {
		t.Fatalf("stream event logs=%d delivered events=%d: %s", len(logs.eventRecords), len(deliveredEvents), logs.raw)
	}
	for index, deliveredEvent := range deliveredEvents {
		record := logs.eventRecords[index]
		if record["sequence"] != float64(index+1) || record["event_type"] != deliveredEvent["kind"] || record["kind"] != deliveredEvent["kind"] {
			t.Fatalf("stream event log %d = %#v delivered=%#v", index, record, deliveredEvent)
		}
		var loggedEvent map[string]any
		raw, _ := record["event_json"].(string)
		if json.Unmarshal([]byte(raw), &loggedEvent) != nil || loggedEvent["kind"] != deliveredEvent["kind"] || strings.Contains(raw, "stream input") {
			t.Fatalf("stream event_json %d = %q", index, raw)
		}
	}
}

func TestLocalRunnerA2AContentLogsRequireDebugAndBoundLongText(t *testing.T) {
	t.Run("disabled without debug even when handler accepts debug", func(t *testing.T) {
		logs := captureLocalRunnerA2AMessageLogs(t, false, func() {
			executor := &localRunnerA2AExecutor{backend: &fakeLocalRunnerOpenCodeBackend{reply: "visible reply"}, defaultContext: "default-context", harness: "claudecode"}
			message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("visible input"))
			execContext := &a2asrv.ExecutorContext{Message: message}
			for _, err := range executor.Execute(context.Background(), execContext) {
				if err != nil {
					t.Fatal(err)
				}
			}
		})
		if len(logs.records) != 0 || len(logs.eventRecords) != 0 || strings.Contains(logs.raw, "localrunner.a2a.event.outbound") {
			t.Fatalf("non-debug run emitted content logs: %s", logs.raw)
		}
	})

	t.Run("debug truncates long content", func(t *testing.T) {
		longInput := "visible-" + strings.Repeat("界", 9000) + " token=long-secret-value"
		longReply := "reply-" + strings.Repeat("界", 9000) + " cookie=long-cookie-value"
		logs := captureLocalRunnerA2AMessageLogs(t, true, func() {
			executor := &localRunnerA2AExecutor{backend: &fakeLocalRunnerOpenCodeBackend{reply: longReply}, defaultContext: "default-context", harness: "opencode"}
			message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(longInput))
			execContext := &a2asrv.ExecutorContext{Message: message}
			for _, err := range executor.Execute(context.Background(), execContext) {
				if err != nil {
					t.Fatal(err)
				}
			}
		})
		if len(logs.records) != 2 {
			t.Fatalf("long content records = %#v", logs.records)
		}
		inbound := logs.records[0]
		content, _ := inbound["content"].(string)
		if inbound["content_bytes"] != float64(len(longInput)) || inbound["truncated"] != true || utf8.RuneCountInString(content) > 8192 {
			t.Fatalf("bounded inbound content = %#v", inbound)
		}
		if strings.Contains(logs.raw, "long-secret-value") {
			t.Fatalf("bounded content log leaked token: %s", logs.raw)
		}
		if len(logs.eventRecords) != 1 || logs.eventRecords[0]["truncated"] != true || logs.eventRecords[0]["event_bytes"].(float64) <= float64(len(logs.eventRecords[0]["event_json"].(string))) {
			t.Fatalf("bounded event log = %#v", logs.eventRecords)
		}
		eventJSON, _ := logs.eventRecords[0]["event_json"].(string)
		var event map[string]any
		if utf8.RuneCountInString(eventJSON) > 8192 || json.Unmarshal([]byte(eventJSON), &event) != nil || strings.Contains(eventJSON, "long-cookie-value") {
			t.Fatalf("bounded event_json = %q", eventJSON)
		}
	})
}

type localRunnerA2AMessageLogCapture struct {
	raw string
	records []map[string]any
	eventRecords []map[string]any
}

func captureLocalRunnerA2AMessageLogs(t *testing.T, debug bool, run func()) localRunnerA2AMessageLogCapture {
	t.Helper()
	t.Setenv("DWS_CONFIG_DIR", t.TempDir())
	previousLogger := slog.Default()
	configureLogLevel(&GlobalFlags{Debug: debug})
	var output bytes.Buffer
	slog.SetDefault(slog.New(localRunnerA2ASafeLogHandler{inner: slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})}))
	defer func() {
		configureLogLevel(&GlobalFlags{})
		CloseFileLogger()
		slog.SetDefault(previousLogger)
	}()
	run()
	raw := output.String()
	capture := localRunnerA2AMessageLogCapture{raw: raw}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil {
			switch record["msg"] {
			case "localrunner.a2a.message.inbound", "localrunner.a2a.message.outbound":
				capture.records = append(capture.records, record)
			case "localrunner.a2a.event.outbound":
				capture.eventRecords = append(capture.eventRecords, record)
			}
		}
	}
	return capture
}

func TestLocalRunnerOfficialA2AAdapterIsSharedAcrossLocalAgentChannels(t *testing.T) {
	for _, test := range []struct {
		ref  string
		name string
	}{
		{ref: "opencode", name: "DWS OpenCode"},
		{ref: "codex", name: "DWS Codex"},
		{ref: "claudecode", name: "DWS Claude Code"},
		{ref: "qoder", name: "DWS Qoder"},
		{ref: "qoderwork", name: "DWS QoderWork"},
		{ref: "codebuddy", name: "DWS CodeBuddy"},
		{ref: "workbuddy", name: "DWS WorkBuddy"},
		{ref: "custom", name: "DWS Custom Agent"},
	} {
		t.Run(test.ref, func(t *testing.T) {
			backend := &fakeLocalRunnerOpenCodeBackend{reply: "reply"}
			agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", test.ref)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			response, err := http.Get(agent.CardURL())
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			var card struct {
				Name   string `json:"name"`
				Skills []struct {
					ID string `json:"id"`
				} `json:"skills"`
			}
			if response.StatusCode != http.StatusOK || json.Unmarshal(body, &card) != nil || card.Name != test.name || len(card.Skills) != 1 || card.Skills[0].ID != test.ref {
				t.Fatalf("%s Card status=%d body=%s", test.ref, response.StatusCode, body)
			}
		})
	}
}

func TestLocalRunnerOpenCodeAgentServesCardSendAndFinalStream(t *testing.T) {
	backend := &fakeLocalRunnerOpenCodeBackend{
		reply: "OpenCode final answer",
		streamSnapshots: []string{"OpenCode", "OpenCode streaming"},
		streamReply: "OpenCode streaming answer",
	}
	agent, err := startLocalRunnerOpenCodeAgentWithBackend(backend, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	response, err := http.Get(agent.CardURL())
	if err != nil {
		t.Fatal(err)
	}
	cardBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var card struct {
		Name            string `json:"name"`
		Version         string `json:"version"`
		ProtocolVersion string `json:"protocolVersion"`
		URL             string `json:"url"`
		Capabilities    struct {
			Streaming bool `json:"streaming"`
		} `json:"capabilities"`
		Security        interface{} `json:"security"`
		SecuritySchemes interface{} `json:"securitySchemes"`
		Authentication  interface{} `json:"authentication"`
		SupportedInterfaces json.RawMessage `json:"supportedInterfaces"`
		PreferredTransport  string          `json:"preferredTransport"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(cardBody, &card) != nil {
		t.Fatalf("Card response status=%d body=%s", response.StatusCode, cardBody)
	}
	if card.Name != localRunnerOpenCodeDisplayName || card.Version != "1.0.0" || card.ProtocolVersion != "0.3.0" || card.URL != agent.RPCURL() || !card.Capabilities.Streaming {
		t.Fatalf("OpenCode Card = %#v", card)
	}
	if card.SupportedInterfaces != nil || card.PreferredTransport != "JSONRPC" {
		t.Fatalf("OpenCode Card mixed v1 interface fields into v0.3 shape: %s", cardBody)
	}
	if card.Security != nil || card.SecuritySchemes != nil || card.Authentication != nil {
		t.Fatalf("OpenCode Card unexpectedly declares authentication: %s", cardBody)
	}

	requestBody := []byte(`{"jsonrpc":"2.0","id":"request-1","method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message-1","contextId":"context-1","parts":[{"kind":"text","text":"first"},{"kind":"text","text":"second"}]}}}`)
	response = postLocalRunnerOpenCode(t, agent.RPCURL(), requestBody)
	sendBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var send struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  struct {
			Kind      string `json:"kind"`
			Role      string `json:"role"`
			MessageID string `json:"messageId"`
			ContextID string `json:"contextId"`
			Parts     []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"result"`
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || json.Unmarshal(sendBody, &send) != nil {
		t.Fatalf("send response status=%d contentType=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), sendBody)
	}
	if send.JSONRPC != "2.0" || string(send.ID) != `"request-1"` || send.Result.Kind != "message" || send.Result.Role != "agent" || send.Result.MessageID == "" || send.Result.MessageID == "message-1" || send.Result.ContextID != "context-1" || len(send.Result.Parts) != 1 || send.Result.Parts[0].Kind != "text" || send.Result.Parts[0].Text != "OpenCode final answer" {
		t.Fatalf("send response = %#v id=%s", send, send.ID)
	}
	backend.mu.Lock()
	if len(backend.calls) != 1 || backend.calls[0].sessionKey != "context-1" || backend.calls[0].prompt != "first\nsecond" {
		t.Fatalf("OpenCode calls = %#v", backend.calls)
	}
	backend.mu.Unlock()

	streamBody := bytes.Replace(requestBody, []byte(`message/send`), []byte(`message/stream`), 1)
	response = postLocalRunnerOpenCode(t, agent.RPCURL(), streamBody)
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response status=%d contentType=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	streamBody, err = io.ReadAll(bufio.NewReader(response.Body))
	response.Body.Close()
	if err != nil {
		t.Fatalf("official SSE body = %q err=%v", streamBody, err)
	}
	events := decodeLocalRunnerSSEEvents(t, streamBody)
	if len(events) < 5 {
		t.Fatalf("official SSE events = %d, want task lifecycle and incremental artifacts: %s", len(events), streamBody)
	}
	wantKinds := []string{"task", "status-update", "artifact-update", "artifact-update", "status-update"}
	for index, want := range wantKinds {
		if got := events[index]["kind"]; got != want {
			t.Fatalf("official SSE event %d kind = %#v, want %q: %s", index, got, want, streamBody)
		}
	}
	if state := nestedString(events[0], "status", "state"); state != "submitted" {
		t.Fatalf("submitted task state = %q: %s", state, streamBody)
	}
	if state := nestedString(events[1], "status", "state"); state != "working" {
		t.Fatalf("working task state = %q: %s", state, streamBody)
	}
	if text := nestedString(events[2], "artifact", "parts", "0", "text"); text != "OpenCode" {
		t.Fatalf("first artifact text = %q: %s", text, streamBody)
	}
	if text := nestedString(events[3], "artifact", "parts", "0", "text"); text != " streaming answer" || events[3]["append"] != true {
		t.Fatalf("incremental artifact = %#v: %s", events[3], streamBody)
	}
	if state := nestedString(events[len(events)-1], "status", "state"); state != "completed" {
		t.Fatalf("completed task state = %q: %s", state, streamBody)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.calls) != 1 || len(backend.streamCalls) != 1 || backend.streamCalls[0].sessionKey != "context-1" || backend.streamCalls[0].prompt != "first\nsecond" {
		t.Fatalf("prompt calls = %#v stream calls = %#v", backend.calls, backend.streamCalls)
	}
}

func TestLocalRunnerArtifactDeltaUsesSuffixAndFallsBackToReplacement(t *testing.T) {
	for _, test := range []struct {
		name string
		previous string
		next string
		wantText string
		wantAppend bool
	}{
		{name: "first snapshot", next: "hello", wantText: "hello"},
		{name: "prefix growth", previous: "hello", next: "hello world", wantText: " world", wantAppend: true},
		{name: "rewrite", previous: "hello world", next: "hello there", wantText: "hello there"},
	} {
		t.Run(test.name, func(t *testing.T) {
			text, appendPart := localRunnerArtifactDelta(test.previous, test.next)
			if text != test.wantText || appendPart != test.wantAppend {
				t.Fatalf("localRunnerArtifactDelta(%q, %q) = (%q, %v), want (%q, %v)", test.previous, test.next, text, appendPart, test.wantText, test.wantAppend)
			}
		})
	}
}

func TestLocalRunnerA2AStreamingHeartbeatRefreshesNinetySecondLeaseUntilTerminal(t *testing.T) {
	const streamActivityLease = 90 * time.Second
	if localRunnerA2AStreamHeartbeatInterval != 15*time.Second || localRunnerA2AStreamHeartbeatInterval*6 != streamActivityLease {
		t.Fatalf("stream heartbeat=%s lease=%s, want 15s heartbeat within 90s lease", localRunnerA2AStreamHeartbeatInterval, streamActivityLease)
	}

	backend := newControlledLocalRunnerStreamingBackend()
	executor := &localRunnerA2AExecutor{backend: backend, defaultContext: "default-context", harness: "qoder"}
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))
	message.ContextID = "context-1"
	execContext := &a2asrv.ExecutorContext{Message: message, ContextID: message.ContextID}
	events := make(chan a2a.Event, 8)
	done := make(chan struct{})
	go func() {
		executor.executeStream(context.Background(), execContext, message.ContextID, "hello", func(event a2a.Event, err error) bool {
			if err != nil {
				t.Errorf("stream event error: %v", err)
				return false
			}
			events <- event
			return true
		})
		close(done)
	}()

	if task, ok := (<-events).(*a2a.Task); !ok || task.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("first stream event = %#v, want submitted task", task)
	}
	if status, ok := (<-events).(*a2a.TaskStatusUpdateEvent); !ok || status.Status.State != a2a.TaskStateWorking {
		t.Fatalf("second stream event = %#v, want working status", status)
	}
	<-backend.started

	select {
	case event := <-events:
		status, ok := event.(*a2a.TaskStatusUpdateEvent)
		if !ok || status.Status.State != a2a.TaskStateWorking {
			t.Fatalf("heartbeat event = %#v, want working status", event)
		}
	case <-time.After(localRunnerA2AStreamHeartbeatInterval + 2*time.Second):
		t.Fatal("15s working heartbeat was not emitted")
	}

	backend.complete <- localRunnerA2AStreamResult{reply: "done"}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after terminal backend result")
	}

	var terminal a2a.TaskState
	for {
		select {
		case event := <-events:
			if status, ok := event.(*a2a.TaskStatusUpdateEvent); ok {
				terminal = status.Status.State
			}
		default:
			if terminal != a2a.TaskStateCompleted {
				t.Fatalf("terminal stream state = %q, want completed", terminal)
			}
			return
		}
	}
}

func TestLocalRunnerA2AStreamingCancellationStopsHeartbeatAndHarness(t *testing.T) {
	backend := newControlledLocalRunnerStreamingBackend()
	executor := &localRunnerA2AExecutor{backend: backend, defaultContext: "default-context", harness: "qoder"}
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))
	message.ContextID = "context-1"
	execContext := &a2asrv.ExecutorContext{Message: message, ContextID: message.ContextID}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan a2a.Event, 8)
	done := make(chan struct{})
	go func() {
		executor.executeStream(ctx, execContext, message.ContextID, "hello", func(event a2a.Event, err error) bool {
			if err != nil {
				t.Errorf("stream event error: %v", err)
				return false
			}
			events <- event
			return true
		})
		close(done)
	}()

	<-events
	<-events
	<-backend.started
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after explicit cancellation")
	}
	if cause := <-backend.stopped; !errors.Is(cause, context.Canceled) {
		t.Fatalf("backend stop cause = %v, want context canceled", cause)
	}

	var terminal a2a.TaskState
	for {
		select {
		case event := <-events:
			if status, ok := event.(*a2a.TaskStatusUpdateEvent); ok {
				terminal = status.Status.State
			}
		default:
			if terminal != a2a.TaskStateCanceled {
				t.Fatalf("terminal stream state = %q, want canceled", terminal)
			}
			return
		}
	}
}

func TestLocalRunnerA2AStreamingRequestDeadlineDoesNotBecomeHarnessDeadline(t *testing.T) {
	backend := newControlledLocalRunnerStreamingBackend()
	agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", "qoder")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":"stream-1","method":"message/stream","params":{"message":{"kind":"message","role":"user","messageId":"message-1","parts":[{"kind":"text","text":"hello"}]}}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, agent.RPCURL(), body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	harnessContext := <-backend.started
	if deadline, ok := harnessContext.Deadline(); ok {
		t.Fatalf("Harness context inherited request deadline %s", deadline)
	}
	<-ctx.Done()
	select {
	case <-harnessContext.Done():
		t.Fatalf("Harness context stopped with request deadline: %v", context.Cause(harnessContext))
	case <-time.After(50 * time.Millisecond):
	}

	backend.complete <- localRunnerA2AStreamResult{reply: "done"}
	select {
	case cause := <-backend.stopped:
		if cause != nil {
			t.Fatalf("backend stopped with cause %v, want terminal completion", cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not finish after detached request deadline")
	}
}

func TestLocalRunnerRequiredHarnessesUseA2AStreamingExecutor(t *testing.T) {
	for _, harness := range []string{"qoder", "codex", "opencode", "claudecode"} {
		t.Run(harness, func(t *testing.T) {
			const prompt = "USER-PROMPT-MUST-NOT-BE-ECHOED"
			backend := &fakeLocalRunnerOpenCodeBackend{
				streamSnapshots: []string{"partial"},
				streamReply: "partial final",
			}
			agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", harness)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()

			body := []byte(`{"jsonrpc":"2.0","id":"stream-1","method":"message/stream","params":{"message":{"kind":"message","role":"user","messageId":"message-1","contextId":"context-1","parts":[{"kind":"text","text":"` + prompt + `"}]}}}`)
			response := postLocalRunnerOpenCode(t, agent.RPCURL(), body)
			streamBody, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("stream status=%d body=%s err=%v", response.StatusCode, streamBody, readErr)
			}
			events := decodeLocalRunnerSSEEvents(t, streamBody)
			if len(events) < 5 || events[0]["kind"] != "task" || nestedString(events[0], "status", "state") != "submitted" || nestedString(events[2], "artifact", "parts", "0", "text") != "partial" || nestedString(events[len(events)-1], "status", "state") != "completed" {
				t.Fatalf("%s stream lifecycle = %#v", harness, events)
			}
			if history, exists := events[0]["history"]; exists || bytes.Contains(streamBody, []byte(prompt)) {
				t.Fatalf("%s submitted task exposed inbound user history=%#v: %s", harness, history, streamBody)
			}
			backend.mu.Lock()
			streamCalls := len(backend.streamCalls)
			promptCalls := len(backend.calls)
			streamPrompt := ""
			if streamCalls == 1 {
				streamPrompt = backend.streamCalls[0].prompt
			}
			backend.mu.Unlock()
			if streamCalls != 1 || promptCalls != 0 || streamPrompt != prompt {
				t.Fatalf("%s stream calls=%d prompt calls=%d stream prompt=%q", harness, streamCalls, promptCalls, streamPrompt)
			}
		})
	}
}

func TestLocalRunnerA2AStreamingBackendFailureEndsTaskWithoutLeakingJSONRPCError(t *testing.T) {
	backend := &fakeLocalRunnerOpenCodeBackend{streamErr: errors.New("backend failed after stream start")}
	agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", "qoder")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	body := []byte(`{"jsonrpc":"2.0","id":"stream-1","method":"message/stream","params":{"message":{"kind":"message","role":"user","messageId":"message-1","parts":[{"kind":"text","text":"hello"}]}}}`)
	response := postLocalRunnerOpenCode(t, agent.RPCURL(), body)
	streamBody, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d body=%s err=%v", response.StatusCode, streamBody, readErr)
	}
	events := decodeLocalRunnerSSEEvents(t, streamBody)
	if len(events) != 3 || nestedString(events[2], "status", "state") != "failed" || nestedString(events[2], "status", "message", "parts", "0", "text") != "Local agent request failed" {
		t.Fatalf("failed stream lifecycle = %#v body=%s", events, streamBody)
	}
	if bytes.Contains(streamBody, []byte("backend failed")) || bytes.Contains(streamBody, []byte(`"error"`)) {
		t.Fatalf("failed stream exposed backend/JSON-RPC error: %s", streamBody)
	}
}

func decodeLocalRunnerSSEEvents(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var envelope struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope); err != nil {
			t.Fatalf("decode SSE event %q: %v", line, err)
		}
		events = append(events, envelope.Result)
	}
	return events
}

func nestedString(value any, path ...string) string {
	current := value
	for _, key := range path {
		switch item := current.(type) {
		case map[string]any:
			current = item[key]
		case []any:
			if key != "0" || len(item) == 0 {
				return ""
			}
			current = item[0]
		default:
			return ""
		}
	}
	text, _ := current.(string)
	return text
}

func TestLocalRunnerOpenCodeAgentUsesStablePerInstanceDefaultContext(t *testing.T) {
	backend := &fakeLocalRunnerOpenCodeBackend{reply: "reply"}
	agent, err := startLocalRunnerOpenCodeAgentWithBackend(backend, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	for _, requestID := range []string{"request-1", "request-2"} {
		body := []byte(`{"jsonrpc":"2.0","id":"` + requestID + `","method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message","parts":[{"kind":"text","text":"hello"}]}}}`)
		response := postLocalRunnerOpenCode(t, agent.RPCURL(), body)
		response.Body.Close()
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.calls) != 2 || backend.calls[0].sessionKey == "" || backend.calls[0].sessionKey != backend.calls[1].sessionKey {
		t.Fatalf("default session calls = %#v", backend.calls)
	}
}

func TestLocalRunnerOpenCodeAgentPreservesOpaqueNonblankContextID(t *testing.T) {
	backend := &fakeLocalRunnerOpenCodeBackend{reply: "reply"}
	agent, err := startLocalRunnerOpenCodeAgentWithBackend(backend, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	response := postLocalRunnerOpenCode(t, agent.RPCURL(), []byte(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message","contextId":" context-with-spaces ","parts":[{"kind":"text","text":"hello"}]}}}`))
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"contextId":" context-with-spaces "`) {
		t.Fatalf("response status=%d body=%s", response.StatusCode, body)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.calls) != 1 || backend.calls[0].sessionKey != " context-with-spaces " {
		t.Fatalf("OpenCode calls = %#v", backend.calls)
	}
}

func TestLocalRunnerOpenCodeAgentRejectsUnsupportedPartsAndRedactsFailures(t *testing.T) {
	const sensitivePrompt = "sensitive prompt backend body"
	const sensitiveContext = "a2a-context-sensitive-123"
	const sensitiveSession = "native-session-sensitive-456"
	const sensitivePassword = "password-value-123"
	const sensitiveToken = "token-value-456"
	const sensitiveSecret = "secret-value-789"
	const sensitiveCredential = "credential-value-012"
	const sensitiveAPIKey = "api-key-value-789"
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	backend := &fakeLocalRunnerOpenCodeBackend{err: errors.New("Qoder process exited: not logged in; password=" + sensitivePassword + "; token=" + sensitiveToken + "; secret=" + sensitiveSecret + "; credential=" + sensitiveCredential + "; Authorization: Bearer " + sensitiveToken + "; api_key=" + sensitiveAPIKey + "; session_id=" + sensitiveSession + "; request=" + sensitivePrompt + "; conversation=" + sensitiveContext + "; " + strings.Repeat("diagnostic ", 80))}
	agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", "qoder")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	unsupported := []byte(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message","parts":[{"kind":"file","file":{"uri":"file:///secret"}}]}}}`)
	response := postLocalRunnerOpenCode(t, agent.RPCURL(), unsupported)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"code":-32602`) {
		t.Fatalf("unsupported part response status=%d body=%s", response.StatusCode, body)
	}

	failed := []byte(`{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message","contextId":"` + sensitiveContext + `","parts":[{"kind":"text","text":"` + sensitivePrompt + `"}]}}}`)
	response = postLocalRunnerOpenCode(t, agent.RPCURL(), failed)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"code":-32000`) || !strings.Contains(string(body), `"message":"Local agent request failed"`) {
		t.Fatalf("backend failure response status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(string(body), sensitivePrompt) || strings.Contains(string(body), sensitiveContext) || strings.Contains(string(body), sensitiveSession) || strings.Contains(string(body), sensitivePassword) || strings.Contains(string(body), sensitiveToken) || strings.Contains(string(body), sensitiveSecret) || strings.Contains(string(body), sensitiveCredential) || strings.Contains(string(body), sensitiveAPIKey) {
		t.Fatalf("backend failure leaked sensitive content: %s", body)
	}
	logged := logs.String()
	var failureRecord map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "localrunner.backend.failure" {
			failureRecord = record
			break
		}
	}
	if failureRecord == nil {
		t.Fatalf("backend failure log missing: %s", logged)
	}
	for key, want := range map[string]string{"harness": "qoder", "phase": "prompt", "category": "process_exit", "reason": "backend process exited"} {
		if failureRecord[key] != want {
			t.Fatalf("backend failure log %s = %#v, want %q: %#v", key, failureRecord[key], want, failureRecord)
		}
	}
	detail, _ := failureRecord["detail"].(string)
	if !strings.Contains(detail, "Qoder process exited") || !strings.Contains(detail, "not logged in") || utf8.RuneCountInString(detail) > 240 {
		t.Fatalf("backend failure detail = %q, want actionable bounded summary", detail)
	}
	if fingerprint, _ := failureRecord["fingerprint"].(string); !strings.HasPrefix(fingerprint, "sha256:") {
		t.Fatalf("backend failure fingerprint = %#v", failureRecord["fingerprint"])
	}
	for _, forbidden := range []string{sensitivePrompt, sensitiveContext, sensitiveSession, sensitivePassword, sensitiveToken, sensitiveSecret, sensitiveCredential, sensitiveAPIKey} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("backend logs leaked %q: %s", forbidden, logged)
		}
	}
	for _, forbidden := range []string{"password", "token", "secret", "credential", "api_key", "authorization", "bearer", "session_id", "prompt"} {
		if strings.Contains(strings.ToLower(detail), forbidden) {
			t.Fatalf("backend failure detail retained sensitive marker %q: %s", forbidden, detail)
		}
	}
}

func TestLocalRunnerQoderSessionMappingSurvivesRestartOnlyWithMemory(t *testing.T) {
	for _, test := range []struct {
		name      string
		memory    bool
		wantSame  bool
		wantStore bool
	}{
		{name: "memory enabled", memory: true, wantSame: true, wantStore: true},
		{name: "memory disabled", memory: false, wantSame: false, wantStore: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			workDir := t.TempDir()
			stubDir := t.TempDir()
			sessionLog := filepath.Join(stubDir, "sessions.log")
			scriptPath := filepath.Join(stubDir, "qoder-stub.py")
			script := `import json
import os
import sys

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    message = json.loads(raw)
    if message.get("type") == "control_request":
        print(json.dumps({"type":"control_response","response":{"subtype":"success","request_id":message.get("request_id", "")}}), flush=True)
        continue
    if message.get("type") == "user":
        with open(os.environ["DWS_QODER_SESSION_LOG"], "a", encoding="utf-8") as output:
            output.write(message.get("session_id", "") + "\n")
        print(json.dumps({"type":"result","subtype":"success","result":"ok"}), flush=True)
`
			if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			binPath := filepath.Join(stubDir, "qodercli")
			bin := "#!/bin/sh\nexec python3 \"$DWS_QODER_STUB\" \"$@\"\n"
			if err := os.WriteFile(binPath, []byte(bin), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DWS_CONFIG_DIR", configDir)
			t.Setenv("DWS_CONNECT_NO_INSTALL", "1")
			t.Setenv("DWS_AGENT_CMD", "")
			t.Setenv("DWS_QODER_STUB", scriptPath)
			t.Setenv("DWS_QODER_SESSION_LOG", sessionLog)
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			for requestID := 1; requestID <= 2; requestID++ {
				agent, err := startLocalRunnerLocalAgent(context.Background(), "qoder", localRunnerLocalAgentOptions{WorkDir: workDir, Memory: test.memory})
				if err != nil {
					t.Fatal(err)
				}
				response := postLocalRunnerOpenCode(t, agent.RPCURL(), []byte(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message","contextId":"a2a-context-1","parts":[{"kind":"text","text":"hello"}]}}}`))
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				_ = agent.Close()
				if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"result"`) {
					t.Fatalf("request %d status=%d body=%s err=%v", requestID, response.StatusCode, body, readErr)
				}
			}

			rawSessions, err := os.ReadFile(sessionLog)
			if err != nil {
				t.Fatal(err)
			}
			sessions := strings.Fields(string(rawSessions))
			if len(sessions) != 2 || (sessions[0] == sessions[1]) != test.wantSame {
				t.Fatalf("Qoder sessions after restart = %q, wantSame=%v", sessions, test.wantSame)
			}
			storePath := filepath.Join(configDir, "connect", "localrunner-"+localRunnerLocalAgentDefaultID("qoder", workDir), "sessions.json")
			_, statErr := os.Stat(storePath)
			if (statErr == nil) != test.wantStore {
				t.Fatalf("session store %q exists=%v error=%v, want %v", storePath, statErr == nil, statErr, test.wantStore)
			}
		})
	}
}

func TestLocalRunnerOpenCodeAgentMapsCancellationAndTimeout(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		code    string
		message string
	}{
		{name: "canceled", err: context.Canceled, code: `"code":-32000`, message: `"message":"Local agent request canceled"`},
		{name: "timeout", err: context.DeadlineExceeded, code: `"code":-32000`, message: `"message":"Local agent request timed out"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &fakeLocalRunnerOpenCodeBackend{err: test.err}
			agent, err := startLocalRunnerOpenCodeAgentWithBackend(backend, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			response := postLocalRunnerOpenCode(t, agent.RPCURL(), []byte(`{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message","parts":[{"kind":"text","text":"hello"}]}}}`))
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK || !strings.Contains(string(body), test.code) || !strings.Contains(string(body), test.message) {
				t.Fatalf("response status=%d body=%s", response.StatusCode, body)
			}
		})
	}
}

func TestLocalRunnerOpenCodeAgentCloseStopsHTTPAndBackendExactlyOnce(t *testing.T) {
	backend := &fakeLocalRunnerOpenCodeBackend{reply: "reply"}
	agent, err := startLocalRunnerOpenCodeAgentWithBackend(backend, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	closeCalls := backend.closeCalls
	backend.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", closeCalls)
	}
	if _, err := http.Get(agent.CardURL()); err == nil {
		t.Fatal("closed OpenCode agent still accepts requests")
	}
}

func postLocalRunnerOpenCode(t *testing.T, target string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
