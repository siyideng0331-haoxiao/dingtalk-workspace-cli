package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/localrunner"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/testseam"
	"github.com/gorilla/websocket"
)

func TestDefaultLocalRunnerRuntimeProviderIsProduction(t *testing.T) {
	runtime := localRunnerCommandRuntimeProvider()
	production, ok := runtime.(*productionLocalRunnerCommandRuntime)
	if !ok || production.credentials == nil || production.configs == nil || production.oauth == nil || production.ownerIdentity == nil {
		t.Fatal("default LocalRunner runtime dependencies are incomplete")
	}
}

func TestProductionLocalRunnerExposeUsesPublicDingTalkDefaultBase(t *testing.T) {
	agentCardSHA256 := "sha256:" + strings.Repeat("a", 64)
	localAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Local agent","protocolVersion":"1.0","capabilities":{},"skills":[],"url":"` + localAgentURL(r) + `/rpc"}`))
	}))
	defer localAgent.Close()

	requestedURLs := make([]string, 0, 2)
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, request.URL.String())
		status := http.StatusOK
		body := `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","localAgentId":"agent-1","displayName":"Local agent","status":"ACTIVE","agentCardUrl":"https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-1/card","agentCardSha256":"` + agentCardSHA256 + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
		if request.Method == http.MethodPost {
			status = http.StatusCreated
			body = `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","agentCardUrl":"https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-1/card","endpointBearer":"endpoint-secret","status":"ACTIVE"}}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	credentials := localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir:         t.TempDir(),
		ControlHTTPClient: controlClient,
		CardHTTPClient:    localAgent.Client(),
		OAuth:             staticLocalRunnerOAuth("oauth-token"),
		Credentials:       credentials,
		OwnerIdentity:     testLocalRunnerOwnerIdentity,
	})
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	executeLocalRunnerCommand(t, context.Background(), []string{
		"deap", "local-runner", "expose",
		"--local-agent-id", "agent-1",
		"--display-name", "Local agent",
		"--agent-card-url", localAgent.URL,
	})
	wantURLs := []string{
		"https://pre-deap.dingtalk.com/v1/assistant/local-runners",
		"https://pre-deap.dingtalk.com/v1/assistant/local-runners/runner-1",
	}
	if len(requestedURLs) != len(wantURLs) {
		t.Fatalf("control request URLs = %v", requestedURLs)
	}
	for index := range wantURLs {
		if requestedURLs[index] != wantURLs[index] {
			t.Fatalf("control request %d URL = %q, want %q", index, requestedURLs[index], wantURLs[index])
		}
	}
	stored, err := runtime.configs.Load("runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.OpenAPIBase != "https://pre-deap.dingtalk.com" {
		t.Fatalf("stored OpenAPI base = %q", stored.OpenAPIBase)
	}
}

func TestLocalRunnerStartLocalIdentityDefaultsAndOverrides(t *testing.T) {
	rawCard := json.RawMessage(`{"name":"Card default","protocolVersion":"1.0","capabilities":{},"skills":[],"url":"http://127.0.0.1:8080/rpc"}`)
	cardURL := "http://127.0.0.1:8080/card"
	digest := sha256.Sum256([]byte(cardURL))
	wantDefaultID := fmt.Sprintf("local-%x", digest[:8])

	localAgentID, displayName, err := resolveLocalRunnerStartIdentity(rawCard, cardURL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if localAgentID != wantDefaultID || displayName != "Card default" {
		t.Fatalf("default identity = %q/%q, want %q/Card default", localAgentID, displayName, wantDefaultID)
	}

	localAgentID, displayName, err = resolveLocalRunnerStartIdentity(rawCard, cardURL, "agent-override", "Display override")
	if err != nil {
		t.Fatal(err)
	}
	if localAgentID != "agent-override" || displayName != "Display override" {
		t.Fatalf("override identity = %q/%q", localAgentID, displayName)
	}

	for _, dynamicCard := range []json.RawMessage{
		json.RawMessage(`{"name":"DWS Test Echo","protocolVersion":"0.3.0","capabilities":{"streaming":true},"skills":[],"url":"http://127.0.0.1:31001/rpc"}`),
		json.RawMessage(`{"name":"DWS Test Echo","protocolVersion":"0.3.0","capabilities":{"streaming":true},"skills":[],"url":"http://127.0.0.1:32002/rpc"}`),
	} {
		localAgentID, displayName, err = resolveLocalRunnerStartIdentity(dynamicCard, localRunnerTestEchoRef, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if localAgentID != localRunnerTestEchoRef || displayName != localRunnerTestEchoDisplayName {
			t.Fatalf("built-in identity = %q/%q", localAgentID, displayName)
		}
	}
}

func TestProductionLocalRunnerStartLocalPreparesOneCardReadAndSanitizedConnect(t *testing.T) {
	agentCardSHA256 := "sha256:" + strings.Repeat("a", 64)
	cardRequests := 0
	localAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cardRequests++
		_, _ = w.Write([]byte(`{"name":"Card default","protocolVersion":"1.0","capabilities":{"streaming":true},"skills":[],"url":"` + localAgentURL(r) + `/rpc"}`))
	}))
	defer localAgent.Close()
	digest := sha256.Sum256([]byte(localAgent.URL))
	wantLocalAgentID := fmt.Sprintf("local-%x", digest[:8])
	var createRequest localrunner.CreateRunnerRequest

	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","localAgentId":"` + wantLocalAgentID + `","displayName":"Card default","status":"ACTIVE","agentCardUrl":"https://api.dingtalk.com/v1/a2a/local-runners/endpoint-1/.well-known/agent-card.json","agentCardSha256":"` + agentCardSHA256 + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
		if request.Method == http.MethodPost {
			status = http.StatusCreated
			body = `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","agentCardUrl":"https://api.dingtalk.com/v1/a2a/local-runners/endpoint-1/.well-known/agent-card.json","endpointBearer":"endpoint-secret","status":"ACTIVE"}}`
			if err := json.NewDecoder(request.Body).Decode(&createRequest); err != nil {
				t.Fatal(err)
			}
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	credentials := localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir:         t.TempDir(),
		ControlHTTPClient: controlClient,
		CardHTTPClient:    localAgent.Client(),
		OAuth:             staticLocalRunnerOAuth("oauth-token"),
		Credentials:       credentials,
		OwnerIdentity:     testLocalRunnerOwnerIdentity,
	})

	result, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef:     localAgent.URL,
		OpenAPIBase:  "https://api.dingtalk.com",
		MaxConcurrent: 7,
		Streaming:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cardRequests != 1 {
		t.Fatalf("Agent Card requests = %d, want 1", cardRequests)
	}
	if createRequest.LocalAgentID != wantLocalAgentID || createRequest.DisplayName != "Card default" {
		t.Fatalf("create request identity = %q/%q", createRequest.LocalAgentID, createRequest.DisplayName)
	}
	if result.Summary.Type != "A2A" || result.Summary.AgentCardURL == "" || result.Summary.Authentication.Scheme != "Bearer" || result.Summary.Authentication.CredentialStorage != "system-keyring" || result.Summary.Authentication.CredentialExported || result.Summary.LocalRunner.RunnerID != "runner-1" || result.Summary.LocalRunner.EndpointID != "endpoint-1" || result.Summary.LocalRunner.Status != "CONNECTING" {
		t.Fatalf("start-local summary = %#v", result.Summary)
	}
	if result.ConnectOptions.TargetURL != localAgent.URL+"/rpc" || result.ConnectOptions.AgentCardSHA256 != agentCardSHA256 || result.ConnectOptions.MaxConcurrent != 7 || !result.ConnectOptions.Streaming {
		t.Fatalf("connect options = %#v", result.ConnectOptions)
	}
	stored, err := runtime.configs.Load("runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentCardSHA256 != agentCardSHA256 {
		t.Fatalf("stored Agent Card digest = %q, want %q", stored.AgentCardSHA256, agentCardSHA256)
	}
	storedBearer, err := credentials.LoadEndpointBearer(context.Background(), "runner-1", "endpoint-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(storedBearer) != "endpoint-secret" {
		t.Fatal("endpoint bearer was not stored in system-keyring adapter")
	}
	encoded, err := json.Marshal(result.Summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "endpoint-secret") || strings.Contains(string(encoded), "connection-ticket") {
		t.Fatalf("summary exposed secret material: %s", encoded)
	}
}

func TestProductionLocalRunnerStartLocalRunsBuiltInEchoAndClosesIt(t *testing.T) {
	agentCardSHA256 := "sha256:" + strings.Repeat("a", 64)
	var createRequest localrunner.CreateRunnerRequest
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := `{"success":true,"data":{"runnerId":"runner-echo","endpointId":"endpoint-echo","localAgentId":"test-echo","displayName":"DWS Test Echo","status":"ACTIVE","agentCardUrl":"https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-echo/.well-known/agent-card.json","agentCardSha256":"` + agentCardSHA256 + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
		if request.Method == http.MethodPost {
			status = http.StatusCreated
			body = `{"success":true,"data":{"runnerId":"runner-echo","endpointId":"endpoint-echo","agentCardUrl":"https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-echo/.well-known/agent-card.json","endpointBearer":"endpoint-secret","status":"ACTIVE"}}`
			if err := json.NewDecoder(request.Body).Decode(&createRequest); err != nil {
				t.Fatal(err)
			}
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(), ControlHTTPClient: controlClient,
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})

	result, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: localRunnerTestEchoRef, OpenAPIBase: defaultLocalRunnerOpenAPIBase,
		MaxConcurrent: 2, Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Close == nil {
		t.Fatal("built-in start result has no closer")
	}
	if createRequest.LocalAgentID != localRunnerTestEchoRef || createRequest.DisplayName != localRunnerTestEchoDisplayName {
		t.Fatalf("built-in create identity = %q/%q", createRequest.LocalAgentID, createRequest.DisplayName)
	}
	var card struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(createRequest.AgentCard, &card); err != nil || !localrunner.ValidateLoopbackHTTPURL(card.URL) || card.URL != result.ConnectOptions.TargetURL {
		t.Fatalf("built-in create Card URL=%q target=%q err=%v", card.URL, result.ConnectOptions.TargetURL, err)
	}
	response := postLocalRunnerTestEcho(t, result.ConnectOptions.TargetURL, []byte(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"ready"}]}}}`))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("built-in RPC status = %d", response.StatusCode)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	if _, err := client.Get(strings.TrimSuffix(result.ConnectOptions.TargetURL, "/rpc") + localRunnerTestEchoCardPath); err == nil {
		t.Fatal("built-in Agent remained available after close")
	}
}

func TestProductionLocalRunnerStartLocalClosesBuiltInEchoWhenRegistrationFails(t *testing.T) {
	var started *localRunnerBuiltInAgent
	testseam.Swap(t, &localRunnerTestEchoAgentStarter, func() (*localRunnerBuiltInAgent, error) {
		agent, err := startLocalRunnerTestEchoAgent()
		started = agent
		return agent, err
	})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(),
		ControlHTTPClient: localRunnerHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("control unavailable")
		}),
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})

	if _, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: localRunnerTestEchoRef, OpenAPIBase: defaultLocalRunnerOpenAPIBase,
		MaxConcurrent: 1, Streaming: true,
	}); err == nil {
		t.Fatal("built-in registration unexpectedly succeeded")
	}
	if started == nil {
		t.Fatal("built-in Agent did not start")
	}
	client := &http.Client{Timeout: time.Second}
	if _, err := client.Get(started.CardURL()); err == nil {
		t.Fatal("registration failure left built-in Agent running")
	}
}

func TestLocalRunnerCardFetchRejectsRedirectOutsideLoopback(t *testing.T) {
	runtime := &productionLocalRunnerCommandRuntime{
		cardHTTPClient: localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
			redirected := request.Clone(request.Context())
			redirected.URL, _ = url.Parse("https://external.example.test/card")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{"name":"Local agent"}`)),
				Request: redirected,
			}, nil
		}),
	}
	if _, err := runtime.readLocalAgentCard(context.Background(), "http://127.0.0.1:8080/card"); !errors.Is(err, ErrLocalRunnerRuntimeInvalid) {
		t.Fatalf("card fetch error = %v, want ErrLocalRunnerRuntimeInvalid", err)
	}
}

func TestLocalRunnerCommandsCompleteLocalControlWSSAndSSELifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseSecondChunk := make(chan struct{})
	firstChunkAtGateway := make(chan struct{})
	responseEnded := make(chan struct{})
	localAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/card":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"Local agent","protocolVersion":"1.0","capabilities":{"streaming":true},"skills":[],"url":"` + localAgentURL(r) + `/rpc"}`))
		case "/rpc":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("local proxy forwarded a caller credential")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("local response is not flushable")
				return
			}
			_, _ = w.Write([]byte("data: first\n\n"))
			flusher.Flush()
			select {
			case <-releaseSecondChunk:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte("data: second\n\n"))
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer localAgent.Close()

	rawCard := json.RawMessage(`{"name":"Local agent","protocolVersion":"1.0","capabilities":{"streaming":true},"skills":[],"url":"` + localAgent.URL + `/rpc"}`)
	var stateMu sync.Mutex
	connected := false
	cardSHA := ""
	controlMux := http.NewServeMux()
	controlServer := httptest.NewTLSServer(controlMux)
	defer controlServer.Close()
	expectedSnapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-1", "https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	cardSHA = "sha256:" + expectedSnapshot.SHA256

	controlMux.HandleFunc("/v1/assistant/local-runners", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer oauth-token" {
			t.Error("create request did not use the DWS OAuth bearer")
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		var request localrunner.CreateRunnerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.LocalAgentID != "agent-1" || request.DisplayName != "Local agent" || !bytes.Equal(request.AgentCard, rawCard) {
			t.Error("create request body mismatch")
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		writeLocalRunnerJSON(w, http.StatusCreated, map[string]any{
			"success": true,
			"data": map[string]any{
				"runnerId": "runner-1", "endpointId": "endpoint-1",
				"agentCardUrl": controlServer.URL + "/v1/a2a/local-runners/endpoint-1/card",
				"endpointBearer": "endpoint-bearer-secret", "status": "ACTIVE",
			},
		})
	})
	controlMux.HandleFunc("/v1/assistant/local-runners/runner-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oauth-token" {
			t.Error("runner request did not use the DWS OAuth bearer")
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			stateMu.Lock()
			isConnected := connected
			stateMu.Unlock()
			writeLocalRunnerJSON(w, http.StatusOK, map[string]any{
				"success": true,
				"data": map[string]any{
					"runnerId": "runner-1", "endpointId": "endpoint-1", "localAgentId": "agent-1",
					"displayName": "Local agent", "status": "ACTIVE",
					"agentCardUrl": controlServer.URL + "/v1/a2a/local-runners/endpoint-1/card",
					"agentCardSha256": cardSHA, "connected": isConnected,
					"lastHeartbeatAtEpochSecond": nil,
				},
			})
		case http.MethodDelete:
			writeLocalRunnerJSON(w, http.StatusOK, map[string]any{
				"success": true,
				"data": map[string]any{"runnerId": "runner-1", "endpointId": "endpoint-1", "status": "REVOKED"},
			})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	controlMux.HandleFunc("/v1/assistant/local-runners/runner-1/connection", func(w http.ResponseWriter, r *http.Request) {
		stateMu.Lock()
		isConnected := connected
		stateMu.Unlock()
		var connectionID any
		var connectedAt any
		if isConnected {
			connectionID = "connection-1"
			connectedAt = time.Now().Unix()
		}
		writeLocalRunnerJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"data": map[string]any{
				"runnerId": "runner-1", "endpointId": "endpoint-1", "connected": isConnected,
				"connectionId": connectionID, "connectedAtEpochSecond": connectedAt,
				"lastHeartbeatAtEpochSecond": nil,
			},
		})
	})
	controlMux.HandleFunc("/v1/assistant/local-runners/runner-1/connections/open", func(w http.ResponseWriter, r *http.Request) {
		writeLocalRunnerJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"data": map[string]any{
				"runnerId": "runner-1", "endpointId": "endpoint-1",
				"webSocketUrl": "wss" + strings.TrimPrefix(controlServer.URL, "https") + "/local-runner-wss",
				"connectionTicket": "one-attempt-ticket", "ticketExpiresAtEpochSecond": time.Now().Add(time.Minute).Unix(),
			},
		})
	})
	controlMux.HandleFunc("/local-runner-wss", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer one-attempt-ticket" {
			t.Error("WSS handshake did not use the one-attempt ticket")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		codec := localrunner.NewTunnelCodec(localrunner.DefaultMaxFrameBytes)
		hello := readLocalRunnerFrame(t, conn, codec)
		if hello.Type != localrunner.FrameHello || hello.Sequence != 0 || string(hello.Attributes["agentCardSha256"]) != strconv.Quote(cardSHA) {
			t.Error("first WSS frame did not preserve the registered Agent Card digest")
			return
		}
		stateMu.Lock()
		connected = true
		stateMu.Unlock()
		defer func() {
			stateMu.Lock()
			connected = false
			stateMu.Unlock()
		}()
		writeLocalRunnerFrame(t, conn, codec, localrunner.TunnelFrame{
			Version: 1, Type: localrunner.FrameHelloAck, RunnerID: "runner-1", EndpointID: "endpoint-1", Sequence: 0, Timestamp: time.Now().UnixMilli(),
			Attributes: map[string]json.RawMessage{
				"accepted": json.RawMessage(`true`), "connectionId": json.RawMessage(`"connection-1"`),
				"heartbeatIntervalMs": json.RawMessage(`15000`), "maxFrameBytes": json.RawMessage(`262144`),
			},
		})
		writeLocalRunnerFrame(t, conn, codec, localrunner.TunnelFrame{
			Version: 1, Type: localrunner.FrameHeartbeat, RunnerID: "runner-1", EndpointID: "endpoint-1", Sequence: 1, Timestamp: time.Now().UnixMilli(),
		})
		if ack := readLocalRunnerFrame(t, conn, codec); ack.Type != localrunner.FrameHeartbeatAck || ack.Sequence != 1 {
			t.Error("heartbeat was not acknowledged in the connection sequence")
			return
		}
		requestID := "request-1"
		requestAttributes, _ := localRunnerRequestStartAttributes()
		writeLocalRunnerFrame(t, conn, codec, localrunner.TunnelFrame{
			Version: 1, Type: localrunner.FrameRequestStart, RunnerID: "runner-1", EndpointID: "endpoint-1", RequestID: requestID, Sequence: 0, Timestamp: time.Now().UnixMilli(), Attributes: requestAttributes,
		})
		writeLocalRunnerFrame(t, conn, codec, localrunner.TunnelFrame{
			Version: 1, Type: localrunner.FrameRequestEnd, RunnerID: "runner-1", EndpointID: "endpoint-1", RequestID: requestID, Sequence: 1, Timestamp: time.Now().UnixMilli(),
		})
		if start := readLocalRunnerFrame(t, conn, codec); start.Type != localrunner.FrameResponseStart || start.Sequence != 0 {
			t.Error("missing response_start seq=0")
			return
		}
		first := readLocalRunnerFrame(t, conn, codec)
		if first.Type != localrunner.FrameResponseChunk || first.Sequence != 1 || !bytes.Contains(first.Payload, []byte("data: first")) {
			t.Error("first SSE chunk was not forwarded before the stream completed")
			return
		}
		close(firstChunkAtGateway)
		close(releaseSecondChunk)
		for {
			frame := readLocalRunnerFrame(t, conn, codec)
			if frame.Type == localrunner.FrameResponseEnd {
				close(responseEnded)
				break
			}
		}
		<-r.Context().Done()
	})

	credentials := localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir:         t.TempDir(),
		ControlHTTPClient: controlServer.Client(),
		CardHTTPClient:    localAgent.Client(),
		ProxyHTTPClient:   localAgent.Client(),
		WSSDialer:         &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		OAuth:             staticLocalRunnerOAuth("oauth-token"),
		Credentials:       credentials,
		OwnerIdentity: func(context.Context) (string, string, error) {
			return "tenant-1", "operator-1", nil
		},
		ReconnectBackoff:  time.Millisecond,
	})
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	exposeOutput := executeLocalRunnerCommand(t, context.Background(), []string{
		"deap", "local-runner", "expose", "--local-agent-id", "agent-1", "--display-name", "Local agent",
		"--agent-card-url", localAgent.URL + "/card", "--openapi-base", controlServer.URL,
	})
	assertLocalRunnerOutputHasNoSecrets(t, exposeOutput)

	connectDone := make(chan error, 1)
	go func() {
		_, err := executeLocalRunnerCommandResult(ctx, []string{
			"deap", "local-runner", "connect", "--runner-id", "runner-1", "--endpoint-id", "endpoint-1",
			"--target-url", localAgent.URL + "/rpc", "--agent-card-sha256", cardSHA,
		})
		connectDone <- err
	}()
	select {
	case <-firstChunkAtGateway:
	case <-time.After(5 * time.Second):
		t.Fatal("first SSE chunk did not reach the WSS gateway")
	}
	statusOutput := executeLocalRunnerCommand(t, context.Background(), []string{"deap", "local-runner", "status", "--runner-id", "runner-1"})
	if !strings.Contains(statusOutput, `"connected":true`) || !strings.Contains(statusOutput, `"connectionId":"connection-1"`) {
		t.Fatalf("status did not combine runner and connection state: %s", statusOutput)
	}
	select {
	case <-responseEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE response did not finish")
	}
	cancel()
	select {
	case err := <-connectDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect did not stop after context cancellation")
	}

	revokeOutput := executeLocalRunnerCommand(t, context.Background(), []string{"--yes", "deap", "local-runner", "revoke", "--runner-id", "runner-1"})
	assertLocalRunnerOutputHasNoSecrets(t, revokeOutput)
	if _, err := runtime.configs.Load("runner-1"); !errors.Is(err, localrunner.ErrRunnerConfigNotFound) {
		t.Fatalf("local config remained after revoke: %v", err)
	}
	if _, err := credentials.LoadEndpointBearer(context.Background(), "runner-1", "endpoint-1"); !errors.Is(err, localrunner.ErrEndpointBearerNotFound) {
		t.Fatalf("endpoint bearer remained after revoke: %v", err)
	}
}

func localAgentURL(r *http.Request) string {
	return "http://" + r.Host
}

func localRunnerRequestStartAttributes() (map[string]json.RawMessage, error) {
	headers, _ := json.Marshal(map[string][]string{"accept": {"text/event-stream"}, "authorization": {"Bearer public-rpc-secret"}})
	return map[string]json.RawMessage{
		"method": json.RawMessage(`"POST"`), "path": json.RawMessage(`"/v1/a2a/local-runners/endpoint-1/rpc"`),
		"query": json.RawMessage(`""`), "headers": headers, "contentLength": json.RawMessage(`0`),
		"deadlineEpochMs": json.RawMessage(strconv.FormatInt(time.Now().Add(10*time.Second).UnixMilli(), 10)),
	}, nil
}

func writeLocalRunnerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func readLocalRunnerFrame(t *testing.T, conn *websocket.Conn, codec *localrunner.TunnelCodec) localrunner.TunnelFrame {
	t.Helper()
	kind, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind == websocket.BinaryMessage {
		frame, err := codec.DecodeBinary(raw)
		if err != nil {
			t.Fatal(err)
		}
		return frame
	}
	frame, err := codec.DecodeText(raw)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func writeLocalRunnerFrame(t *testing.T, conn *websocket.Conn, codec *localrunner.TunnelCodec, frame localrunner.TunnelFrame) {
	t.Helper()
	encoded, err := codec.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	kind := websocket.TextMessage
	if encoded.Kind == localrunner.MessageBinary {
		kind = websocket.BinaryMessage
	}
	if err := conn.WriteMessage(kind, encoded.Data); err != nil {
		t.Fatal(err)
	}
}

func executeLocalRunnerCommand(t *testing.T, ctx context.Context, args []string) string {
	t.Helper()
	output, err := executeLocalRunnerCommandResult(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func executeLocalRunnerCommandResult(ctx context.Context, args []string) (string, error) {
	var output bytes.Buffer
	root := newRootCommandWithEngine(ctx, nil, false, true)
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(args)
	err := root.Execute()
	return output.String(), err
}

func assertLocalRunnerOutputHasNoSecrets(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{"oauth-token", "endpoint-bearer-secret", "one-attempt-ticket", "public-rpc-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("command output exposed secret material")
		}
	}
}

func testLocalRunnerOwnerIdentity(context.Context) (string, string, error) {
	return "tenant-1", "operator-1", nil
}

type staticLocalRunnerOAuth string

func (s staticLocalRunnerOAuth) AccessToken(context.Context) (string, error) {
	return string(s), nil
}

func (s staticLocalRunnerOAuth) RefreshRejectedAccessToken(context.Context, string) (string, error) {
	return string(s), nil
}

type runtimeSecretBackend struct {
	mu     sync.Mutex
	values map[string]string
}

type localRunnerHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f localRunnerHTTPDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (b *runtimeSecretBackend) Get(_, account string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.values[account], nil
}

func (b *runtimeSecretBackend) Set(_, account, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[account] = value
	return nil
}

func (b *runtimeSecretBackend) Remove(_, account string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.values, account)
	return nil
}
