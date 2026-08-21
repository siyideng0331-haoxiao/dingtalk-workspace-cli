package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

type fakeLocalRunnerOpenCodeBackend struct {
	mu         sync.Mutex
	calls      []localRunnerOpenCodePromptCall
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

func (f *fakeLocalRunnerOpenCodeBackend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return nil
}

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
	backend := &fakeLocalRunnerOpenCodeBackend{reply: "OpenCode final answer"}
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
	if err != nil || strings.Count(string(streamBody), "data: ") != 1 || !strings.Contains(string(streamBody), `"text":"OpenCode final answer"`) {
		t.Fatalf("official SSE body = %q err=%v", streamBody, err)
	}
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
	const sensitive = "sensitive prompt backend body and password"
	backend := &fakeLocalRunnerOpenCodeBackend{err: errors.New(sensitive)}
	agent, err := startLocalRunnerOpenCodeAgentWithBackend(backend, "127.0.0.1:0")
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

	failed := []byte(`{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message","parts":[{"kind":"text","text":"` + sensitive + `"}]}}}`)
	response = postLocalRunnerOpenCode(t, agent.RPCURL(), failed)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"code":-32000`) || !strings.Contains(string(body), `"message":"Local agent request failed"`) {
		t.Fatalf("backend failure response status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(string(body), sensitive) {
		t.Fatalf("backend failure leaked sensitive content: %s", body)
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
