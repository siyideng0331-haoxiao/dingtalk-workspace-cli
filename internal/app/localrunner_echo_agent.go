package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	localRunnerTestEchoRef          = "test-echo"
	localRunnerTestEchoDisplayName  = "DWS Test Echo"
	localRunnerTestEchoCardPath     = "/.well-known/agent-card.json"
	localRunnerTestEchoRPCPath      = "/rpc"
	localRunnerTestEchoMaxBodyBytes = 64 << 10
)

var localRunnerTestEchoAgentStarter = startLocalRunnerTestEchoAgent

type localRunnerBuiltInAgent struct {
	cardURL   string
	rpcURL    string
	server    *http.Server
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func startLocalRunnerTestEchoAgent() (*localRunnerBuiltInAgent, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	baseURL := "http://" + listener.Addr().String()
	card, err := json.Marshal(struct {
		Name               string                   `json:"name"`
		Description        string                   `json:"description"`
		URL                string                   `json:"url"`
		ProtocolVersion    string                   `json:"protocolVersion"`
		Capabilities       map[string]bool          `json:"capabilities"`
		DefaultInputModes  []string                 `json:"defaultInputModes"`
		DefaultOutputModes []string                 `json:"defaultOutputModes"`
		Skills             []map[string]interface{} `json:"skills"`
	}{
		Name: localRunnerTestEchoDisplayName, Description: "In-process LocalRunner A2A acceptance echo agent",
		URL: baseURL + localRunnerTestEchoRPCPath, ProtocolVersion: "0.3.0",
		Capabilities: map[string]bool{"streaming": true},
		DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []map[string]interface{}{{
			"id": "echo", "name": "Echo", "description": "Echo text message parts", "tags": []string{"test", "echo"},
		}},
	})
	if err != nil {
		listener.Close()
		return nil, err
	}
	agent := &localRunnerBuiltInAgent{
		cardURL: baseURL + localRunnerTestEchoCardPath,
		rpcURL: baseURL + localRunnerTestEchoRPCPath,
		done: make(chan struct{}),
	}
	agent.server = &http.Server{
		Handler: localRunnerTestEchoHandler{card: card},
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	go func() {
		_ = agent.server.Serve(listener)
		close(agent.done)
	}()
	return agent, nil
}

func (a *localRunnerBuiltInAgent) CardURL() string {
	if a == nil {
		return ""
	}
	return a.cardURL
}

func (a *localRunnerBuiltInAgent) RPCURL() string {
	if a == nil {
		return ""
	}
	return a.rpcURL
}

func (a *localRunnerBuiltInAgent) Close() error {
	if a == nil || a.server == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.closeErr = a.server.Close()
		<-a.done
	})
	return a.closeErr
}

type localRunnerTestEchoHandler struct {
	card []byte
}

func (h localRunnerTestEchoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case localRunnerTestEchoCardPath:
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(h.card)
	case localRunnerTestEchoRPCPath:
		h.serveRPC(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h localRunnerTestEchoHandler) serveRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > localRunnerTestEchoMaxBodyBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, localRunnerTestEchoMaxBodyBytes+1))
	if err != nil || len(body) > localRunnerTestEchoMaxBodyBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	request, err := decodeLocalRunnerTestEchoRequest(body)
	if err != nil {
		writeLocalRunnerTestEchoError(w, http.StatusBadRequest, nil, -32600, "Invalid Request")
		return
	}
	if request.Method != "message/send" && request.Method != "message/stream" {
		writeLocalRunnerTestEchoError(w, http.StatusOK, request.ID, -32601, "Method not found")
		return
	}
	response, err := encodeLocalRunnerTestEchoResponse(request)
	if err != nil {
		writeLocalRunnerTestEchoError(w, http.StatusBadRequest, request.ID, -32602, "Invalid params")
		return
	}
	if request.Method == "message/stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(response)
		_, _ = w.Write([]byte("\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(response)
}

type localRunnerTestEchoRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Message struct {
			Kind      string `json:"kind"`
			Role      string `json:"role"`
			MessageID string `json:"messageId"`
			ContextID string `json:"contextId"`
			Parts     []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	} `json:"params"`
}

func decodeLocalRunnerTestEchoRequest(body []byte) (*localRunnerTestEchoRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var request localRunnerTestEchoRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid_request")
	}
	id := bytes.TrimSpace(request.ID)
	if request.JSONRPC != "2.0" || len(id) == 0 || bytes.Equal(id, []byte("null")) || !json.Valid(id) || strings.TrimSpace(request.Method) == "" {
		return nil, errors.New("invalid_request")
	}
	request.ID = append(json.RawMessage(nil), id...)
	return &request, nil
}

func encodeLocalRunnerTestEchoResponse(request *localRunnerTestEchoRequest) ([]byte, error) {
	message := request.Params.Message
	if message.Kind != "message" || message.Role != "user" || strings.TrimSpace(message.MessageID) == "" || len(message.Parts) == 0 {
		return nil, errors.New("invalid_params")
	}
	parts := make([]map[string]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part.Kind != "text" {
			return nil, errors.New("invalid_params")
		}
		parts = append(parts, map[string]string{"kind": "text", "text": part.Text})
	}
	contextID := strings.TrimSpace(message.ContextID)
	if contextID == "" {
		contextID = "test-echo-context"
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  interface{}     `json:"result"`
	}{
		JSONRPC: "2.0", ID: request.ID,
		Result: map[string]interface{}{
			"kind": "message", "role": "agent", "messageId": strings.TrimSpace(message.MessageID) + "-echo",
			"contextId": contextID, "parts": parts,
		},
	})
}

func writeLocalRunnerTestEchoError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	payload, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   interface{}     `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: map[string]interface{}{"code": code, "message": message}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
