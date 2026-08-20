package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/helpers"
	"github.com/google/uuid"
)

const (
	localRunnerOpenCodeRef          = "opencode"
	localRunnerOpenCodeDisplayName  = "DWS OpenCode"
	localRunnerOpenCodeAgentVersion = "1.0.0"
	localRunnerOpenCodeCardPath     = "/.well-known/agent-card.json"
	localRunnerOpenCodeRPCPath      = "/rpc"
	localRunnerOpenCodeMaxBodyBytes = 64 << 10
)

var localRunnerOpenCodeAgentStarter = startLocalRunnerOpenCodeAgent
var localRunnerOpenCodeAgentRestarter = startLocalRunnerOpenCodeAgentAt

type localRunnerOpenCodeBackend interface {
	Prompt(context.Context, string, string) (string, error)
	Close() error
}

type localRunnerOpenCodeAgent struct {
	cardURL        string
	rpcURL         string
	defaultContext string
	backend        localRunnerOpenCodeBackend
	server         *http.Server
	done           chan struct{}
	closeOnce      sync.Once
	closeErr       error
}

func startLocalRunnerOpenCodeAgent(ctx context.Context, workDir, model string) (*localRunnerOpenCodeAgent, error) {
	return startLocalRunnerOpenCodeAgentOn(ctx, workDir, model, "127.0.0.1:0")
}

func startLocalRunnerOpenCodeAgentAt(ctx context.Context, rawOrigin, workDir, model string) (*localRunnerOpenCodeAgent, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawOrigin))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid_local_runner_opencode_origin")
	}
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("invalid_local_runner_opencode_origin")
	}
	return startLocalRunnerOpenCodeAgentOn(ctx, workDir, model, parsed.Host)
}

func startLocalRunnerOpenCodeAgentOn(ctx context.Context, workDir, model, listenAddress string) (*localRunnerOpenCodeAgent, error) {
	backend, err := helpers.StartOpenCodeLocal(ctx, helpers.OpenCodeLocalOptions{WorkDir: workDir, Model: model})
	if err != nil {
		return nil, err
	}
	agent, err := startLocalRunnerOpenCodeAgentWithBackend(backend, listenAddress)
	if err != nil {
		_ = backend.Close()
		return nil, err
	}
	return agent, nil
}

func startLocalRunnerOpenCodeAgentWithBackend(backend localRunnerOpenCodeBackend, listenAddress string) (*localRunnerOpenCodeAgent, error) {
	if backend == nil {
		return nil, errors.New("local_runner_opencode_backend_required")
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
	}
	baseURL := "http://" + listener.Addr().String()
	card, err := json.Marshal(struct {
		Name               string                   `json:"name"`
		Description        string                   `json:"description"`
		Version            string                   `json:"version"`
		URL                string                   `json:"url"`
		ProtocolVersion    string                   `json:"protocolVersion"`
		Capabilities       map[string]bool          `json:"capabilities"`
		DefaultInputModes  []string                 `json:"defaultInputModes"`
		DefaultOutputModes []string                 `json:"defaultOutputModes"`
		Skills             []map[string]interface{} `json:"skills"`
	}{
		Name: localRunnerOpenCodeDisplayName, Description: "OpenCode project agent exposed through DWS LocalRunner",
		Version: localRunnerOpenCodeAgentVersion, URL: baseURL + localRunnerOpenCodeRPCPath, ProtocolVersion: "0.3.0",
		Capabilities: map[string]bool{"streaming": true},
		DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []map[string]interface{}{{
			"id": "opencode", "name": "OpenCode", "description": "Reason about and work with the configured local project", "tags": []string{"code", "project"},
		}},
	})
	if err != nil {
		listener.Close()
		return nil, err
	}
	agent := &localRunnerOpenCodeAgent{
		cardURL: baseURL + localRunnerOpenCodeCardPath,
		rpcURL: baseURL + localRunnerOpenCodeRPCPath,
		defaultContext: "localrunner-opencode-" + uuid.NewString(),
		backend: backend,
		done: make(chan struct{}),
	}
	agent.server = &http.Server{
		Handler: localRunnerOpenCodeHandler{agent: agent, card: card},
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

func (a *localRunnerOpenCodeAgent) CardURL() string {
	if a == nil {
		return ""
	}
	return a.cardURL
}

func (a *localRunnerOpenCodeAgent) RPCURL() string {
	if a == nil {
		return ""
	}
	return a.rpcURL
}

func (a *localRunnerOpenCodeAgent) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		if a.server != nil {
			if err := a.server.Close(); err != nil {
				a.closeErr = errors.New("local_runner_opencode_close_failed")
			}
			<-a.done
		}
		if a.backend != nil {
			if err := a.backend.Close(); err != nil && a.closeErr == nil {
				a.closeErr = errors.New("local_runner_opencode_close_failed")
			}
		}
	})
	return a.closeErr
}

type localRunnerOpenCodeHandler struct {
	agent *localRunnerOpenCodeAgent
	card  []byte
}

func (h localRunnerOpenCodeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case localRunnerOpenCodeCardPath:
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(h.card)
	case localRunnerOpenCodeRPCPath:
		h.serveRPC(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h localRunnerOpenCodeHandler) serveRPC(w http.ResponseWriter, r *http.Request) {
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
	if r.ContentLength > localRunnerOpenCodeMaxBodyBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, localRunnerOpenCodeMaxBodyBytes+1))
	if err != nil || len(body) > localRunnerOpenCodeMaxBodyBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	request, err := decodeLocalRunnerOpenCodeRequest(body)
	if err != nil {
		writeLocalRunnerOpenCodeError(w, http.StatusBadRequest, nil, -32600, "Invalid Request")
		return
	}
	if request.Method != "message/send" && request.Method != "message/stream" {
		writeLocalRunnerOpenCodeError(w, http.StatusOK, request.ID, -32601, "Method not found")
		return
	}
	prompt, contextID, err := h.agent.promptInput(request)
	if err != nil {
		writeLocalRunnerOpenCodeError(w, http.StatusBadRequest, request.ID, -32602, "Invalid params")
		return
	}
	reply, err := h.agent.backend.Prompt(r.Context(), contextID, prompt)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			writeLocalRunnerOpenCodeError(w, http.StatusOK, request.ID, -32001, "OpenCode request canceled")
		case errors.Is(err, context.DeadlineExceeded):
			writeLocalRunnerOpenCodeError(w, http.StatusOK, request.ID, -32002, "OpenCode request timed out")
		default:
			writeLocalRunnerOpenCodeError(w, http.StatusOK, request.ID, -32000, "OpenCode request failed")
		}
		return
	}
	response, err := encodeLocalRunnerOpenCodeResponse(request.ID, contextID, reply)
	if err != nil {
		writeLocalRunnerOpenCodeError(w, http.StatusOK, request.ID, -32000, "OpenCode request failed")
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

type localRunnerOpenCodeRequest struct {
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

func decodeLocalRunnerOpenCodeRequest(body []byte) (*localRunnerOpenCodeRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var request localRunnerOpenCodeRequest
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

func (a *localRunnerOpenCodeAgent) promptInput(request *localRunnerOpenCodeRequest) (string, string, error) {
	message := request.Params.Message
	if message.Kind != "message" || message.Role != "user" || strings.TrimSpace(message.MessageID) == "" || len(message.Parts) == 0 {
		return "", "", errors.New("invalid_params")
	}
	texts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part.Kind != "text" {
			return "", "", errors.New("invalid_params")
		}
		texts = append(texts, part.Text)
	}
	prompt := strings.Join(texts, "\n")
	if strings.TrimSpace(prompt) == "" {
		return "", "", errors.New("invalid_params")
	}
	contextID := message.ContextID
	if strings.TrimSpace(contextID) == "" {
		contextID = a.defaultContext
	}
	return prompt, contextID, nil
}

func encodeLocalRunnerOpenCodeResponse(id json.RawMessage, contextID, reply string) ([]byte, error) {
	if strings.TrimSpace(reply) == "" {
		return nil, errors.New("invalid_reply")
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  interface{}     `json:"result"`
	}{
		JSONRPC: "2.0", ID: id,
		Result: map[string]interface{}{
			"kind": "message", "role": "agent", "messageId": "msg_" + uuid.NewString(),
			"contextId": contextID, "parts": []map[string]string{{"kind": "text", "text": reply}},
		},
	})
}

func writeLocalRunnerOpenCodeError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
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
