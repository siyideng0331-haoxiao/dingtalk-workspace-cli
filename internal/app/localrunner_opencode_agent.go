// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/helpers"
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/uuid"
)

const (
	localRunnerOpenCodeRef          = "opencode"
	localRunnerOpenCodeDisplayName  = "DWS OpenCode"
	localRunnerOpenCodeAgentVersion = "1.0.0"
	localRunnerOpenCodeCardPath     = a2asrv.WellKnownAgentCardPath
	localRunnerOpenCodeRPCPath      = "/rpc"
	localRunnerOpenCodeMaxBodyBytes = 64 << 10
)

type localRunnerLocalAgentOptions struct {
	WorkDir string
	Model string
	Memory bool
	Yolo bool
	Timeout time.Duration
}

var localRunnerLocalAgentStarter = startLocalRunnerLocalAgent
var localRunnerLocalAgentRestarter = startLocalRunnerLocalAgentAt

type localRunnerOpenCodeBackend interface {
	Prompt(context.Context, string, string) (string, error)
	Close() error
}

type localRunnerA2AExecutor struct {
	backend        localRunnerOpenCodeBackend
	defaultContext string
}

var _ a2asrv.AgentExecutor = (*localRunnerA2AExecutor)(nil)

func (e *localRunnerA2AExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if e == nil || e.backend == nil || execCtx == nil || execCtx.Message == nil || execCtx.Message.Role != a2a.MessageRoleUser || len(execCtx.Message.Parts) == 0 {
			yield(nil, a2a.NewError(a2a.ErrInvalidParams, "Invalid params"))
			return
		}
		texts := make([]string, 0, len(execCtx.Message.Parts))
		for _, part := range execCtx.Message.Parts {
			if part == nil {
				yield(nil, a2a.NewError(a2a.ErrInvalidParams, "Invalid params"))
				return
			}
			text, ok := part.Content.(a2a.Text)
			if !ok {
				yield(nil, a2a.NewError(a2a.ErrInvalidParams, "Invalid params"))
				return
			}
			texts = append(texts, string(text))
		}
		prompt := strings.Join(texts, "\n")
		if strings.TrimSpace(prompt) == "" {
			yield(nil, a2a.NewError(a2a.ErrInvalidParams, "Invalid params"))
			return
		}
		contextID := execCtx.Message.ContextID
		if strings.TrimSpace(contextID) == "" {
			contextID = e.defaultContext
		}
		reply, err := e.backend.Prompt(ctx, contextID, prompt)
		if err != nil {
			yield(nil, localRunnerA2ASafeError(err))
			return
		}
		if strings.TrimSpace(reply) == "" {
			yield(nil, a2a.NewError(a2a.ErrServerError, "Local agent request failed"))
			return
		}
		message := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(reply))
		message.ContextID = contextID
		yield(message, nil)
	}
}

func (e *localRunnerA2AExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx == nil {
			yield(nil, a2a.NewError(a2a.ErrInvalidParams, "Invalid params"))
			return
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

func localRunnerA2ASafeError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return a2a.NewError(a2a.ErrServerError, "Local agent request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return a2a.NewError(a2a.ErrServerError, "Local agent request timed out")
	default:
		return a2a.NewError(a2a.ErrServerError, "Local agent request failed")
	}
}

type localRunnerOpenCodeAgent struct {
	cardURL   string
	rpcURL    string
	backend   localRunnerOpenCodeBackend
	server    *http.Server
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func startLocalRunnerLocalAgent(ctx context.Context, agentRef string, options localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
	return startLocalRunnerLocalAgentOn(ctx, agentRef, options, "127.0.0.1:0")
}

func startLocalRunnerLocalAgentAt(ctx context.Context, rawOrigin, agentRef string, options localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawOrigin))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid_local_runner_opencode_origin")
	}
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("invalid_local_runner_opencode_origin")
	}
	return startLocalRunnerLocalAgentOn(ctx, agentRef, options, parsed.Host)
}

func startLocalRunnerLocalAgentOn(ctx context.Context, agentRef string, options localRunnerLocalAgentOptions, listenAddress string) (*localRunnerOpenCodeAgent, error) {
	backend, err := helpers.StartLocalAgentBackend(ctx, helpers.LocalAgentBackendOptions{
		Channel: agentRef, WorkDir: options.WorkDir, Model: options.Model, Memory: options.Memory, Yolo: options.Yolo, Timeout: options.Timeout,
	})
	if err != nil {
		return nil, err
	}
	agent, err := startLocalRunnerLocalAgentWithBackend(backend, listenAddress, agentRef)
	if err != nil {
		_ = backend.Close()
		return nil, err
	}
	return agent, nil
}

func startLocalRunnerOpenCodeAgentWithBackend(backend localRunnerOpenCodeBackend, listenAddress string) (*localRunnerOpenCodeAgent, error) {
	return startLocalRunnerLocalAgentWithBackend(backend, listenAddress, localRunnerOpenCodeRef)
}

func startLocalRunnerLocalAgentWithBackend(backend localRunnerOpenCodeBackend, listenAddress, agentRef string) (*localRunnerOpenCodeAgent, error) {
	if backend == nil {
		return nil, errors.New("local_runner_opencode_backend_required")
	}
	agentRef = strings.ToLower(strings.TrimSpace(agentRef))
	displayName, ok := localRunnerLocalAgentDisplayName(agentRef)
	if !ok {
		return nil, errors.New("local_runner_agent_ref_unsupported")
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
	}
	baseURL := "http://" + listener.Addr().String()
	rpcURL := baseURL + localRunnerOpenCodeRPCPath
	card := &a2a.AgentCard{
		Name: displayName, Description: "Local project agent exposed through DWS LocalRunner",
		Version: localRunnerOpenCodeAgentVersion,
		SupportedInterfaces: []*a2a.AgentInterface{{
			URL: rpcURL, ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2av0.Version,
		}},
		Capabilities: a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{
			ID: agentRef, Name: displayName, Description: "Reason about and work with the configured local project", Tags: []string{"code", "project"},
		}},
	}
	cardProducer := localRunnerCompatCardProducer{AgentCardProducer: a2av0.NewStaticAgentCardProducer(card)}
	executor := &localRunnerA2AExecutor{backend: backend, defaultContext: "localrunner-agent-" + uuid.NewString()}
	rpcHandler := a2av0.NewJSONRPCHandler(a2asrv.NewHandler(executor))
	cardHandler := a2asrv.NewAgentCardHandler(cardProducer)

	agent := &localRunnerOpenCodeAgent{
		cardURL: baseURL + localRunnerOpenCodeCardPath,
		rpcURL: rpcURL,
		backend: backend,
		done: make(chan struct{}),
	}
	agent.server = &http.Server{
		Handler: localRunnerOfficialA2AHandler{card: cardHandler, rpc: rpcHandler},
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

func localRunnerLocalAgentDisplayName(agentRef string) (string, bool) {
	names := map[string]string{
		"opencode": "DWS OpenCode",
		"codex": "DWS Codex",
		"claudecode": "DWS Claude Code",
		"qoder": "DWS Qoder",
		"qoderwork": "DWS QoderWork",
		"codebuddy": "DWS CodeBuddy",
		"workbuddy": "DWS WorkBuddy",
		"custom": "DWS Custom Agent",
		"test-echo": "DWS Test Echo",
	}
	name, ok := names[agentRef]
	return name, ok
}

type localRunnerCompatCardProducer struct {
	a2asrv.AgentCardProducer
}

func (p localRunnerCompatCardProducer) CardJSON(ctx context.Context) ([]byte, error) {
	producer, ok := p.AgentCardProducer.(a2asrv.AgentCardJSONProducer)
	if !ok {
		return nil, errors.New("local_runner_agent_card_json_unavailable")
	}
	raw, err := producer.CardJSON(ctx)
	if err != nil {
		return nil, err
	}
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, err
	}
	card["protocolVersion"] = "0.3.0"
	if interfaces, ok := card["supportedInterfaces"].([]any); ok {
		for _, item := range interfaces {
			if supported, ok := item.(map[string]any); ok && supported["protocolVersion"] == "0.3" {
				supported["protocolVersion"] = "0.3.0"
			}
		}
	}
	return json.Marshal(card)
}

type localRunnerOfficialA2AHandler struct {
	card http.Handler
	rpc  http.Handler
}

func (h localRunnerOfficialA2AHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		h.card.ServeHTTP(w, r)
	case localRunnerOpenCodeRPCPath:
		h.serveRPC(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h localRunnerOfficialA2AHandler) serveRPC(w http.ResponseWriter, r *http.Request) {
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
	r.Body = io.NopCloser(bytes.NewReader(body))
	h.rpc.ServeHTTP(w, r)
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
