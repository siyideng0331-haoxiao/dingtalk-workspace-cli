package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLocalRunnerTestEchoAgentServesCardSendAndStream(t *testing.T) {
	agent, err := startLocalRunnerTestEchoAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	cardURL, err := url.Parse(agent.CardURL())
	if err != nil {
		t.Fatal(err)
	}
	if cardURL.Scheme != "http" || cardURL.Hostname() != "127.0.0.1" || cardURL.Port() == "" || cardURL.Path != localRunnerTestEchoCardPath {
		t.Fatalf("built-in Card URL = %q", cardURL.String())
	}
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
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || json.Unmarshal(cardBody, &card) != nil {
		t.Fatalf("Card response status=%d contentType=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), cardBody)
	}
	if card.Name != localRunnerTestEchoDisplayName || card.Version != "1.0.0" || card.ProtocolVersion != "0.3.0" || card.URL != agent.RPCURL() || !card.Capabilities.Streaming {
		t.Fatalf("built-in Card = %#v", card)
	}

	requestBody := []byte(`{"jsonrpc":"2.0","id":"request-1","method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"message-1","parts":[{"kind":"text","text":"hello"}]}}}`)
	response = postLocalRunnerTestEcho(t, agent.RPCURL(), requestBody)
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
			Parts     []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"result"`
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || json.Unmarshal(sendBody, &send) != nil {
		t.Fatalf("send response status=%d contentType=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), sendBody)
	}
	if send.JSONRPC != "2.0" || string(send.ID) != `"request-1"` || send.Result.Kind != "message" || send.Result.Role != "agent" || send.Result.MessageID == "" || send.Result.MessageID == "message-1" || len(send.Result.Parts) != 1 || send.Result.Parts[0].Kind != "text" || send.Result.Parts[0].Text != "hello" {
		t.Fatalf("send response = %#v id=%s", send, send.ID)
	}

	streamBody := bytes.Replace(requestBody, []byte(`message/send`), []byte(`message/stream`), 1)
	response = postLocalRunnerTestEcho(t, agent.RPCURL(), streamBody)
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response status=%d contentType=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	streamResponse, err := io.ReadAll(bufio.NewReader(response.Body))
	response.Body.Close()
	if err != nil || !strings.Contains(string(streamResponse), "id: ") || strings.Count(string(streamResponse), "data: ") != 1 || !strings.Contains(string(streamResponse), `"text":"hello"`) {
		t.Fatalf("official SSE response = %q err=%v", streamResponse, err)
	}
}

func TestLocalRunnerTestEchoUsesOfficialCompatCardAndSSEEnvelope(t *testing.T) {
	agent, err := startLocalRunnerTestEchoAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	response, err := http.Get(agent.CardURL())
	if err != nil {
		t.Fatal(err)
	}
	card, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(card, []byte(`"supportedInterfaces"`)) || !bytes.Contains(card, []byte(`"protocolVersion":"0.3.0"`)) {
		t.Fatalf("test-echo Card is not the official v0.3 compat projection: %s", card)
	}
	request := []byte(`{"jsonrpc":"2.0","id":"stream-1","method":"message/stream","params":{"message":{"kind":"message","role":"user","messageId":"message-1","parts":[{"kind":"text","text":"hello"}]}}}`)
	response = postLocalRunnerTestEcho(t, agent.RPCURL(), request)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("id: ")) || strings.Count(string(body), "data: ") != 1 {
		t.Fatalf("test-echo SSE is not the official compat envelope: %q", body)
	}
}

func TestLocalRunnerTestEchoAgentRestrictsSurfaceAndCloses(t *testing.T) {
	agent, err := startLocalRunnerTestEchoAgent()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}

	for _, request := range []*http.Request{
		mustLocalRunnerTestEchoRequest(t, http.MethodGet, agent.RPCURL(), nil, ""),
		mustLocalRunnerTestEchoRequest(t, http.MethodPost, agent.CardURL(), nil, "application/json"),
		mustLocalRunnerTestEchoRequest(t, http.MethodGet, strings.TrimSuffix(agent.CardURL(), localRunnerTestEchoCardPath)+"/unexpected", nil, ""),
	} {
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusNotFound {
			t.Fatalf("restricted request %s %s status = %d", request.Method, request.URL, response.StatusCode)
		}
	}

	response, err := client.Do(mustLocalRunnerTestEchoRequest(t, http.MethodPost, agent.RPCURL(), strings.NewReader(`{}`), "text/plain"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("unsupported media status = %d", response.StatusCode)
	}

	oversized := strings.Repeat("x", localRunnerTestEchoMaxBodyBytes+1)
	response, err = client.Do(mustLocalRunnerTestEchoRequest(t, http.MethodPost, agent.RPCURL(), strings.NewReader(oversized), "application/json"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d", response.StatusCode)
	}

	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := client.Get(agent.CardURL()); err == nil {
		t.Fatal("closed built-in Agent still accepts requests")
	}
}

func postLocalRunnerTestEcho(t *testing.T, target string, body []byte) *http.Response {
	t.Helper()
	request := mustLocalRunnerTestEchoRequest(t, http.MethodPost, target, bytes.NewReader(body), "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func mustLocalRunnerTestEchoRequest(t *testing.T, method, target string, body io.Reader, contentType string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request
}
