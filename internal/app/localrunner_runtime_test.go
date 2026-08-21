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
	"os"
	"path/filepath"
	"sort"
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

func TestProductionLocalRunnerRuntimeUsesDEAPOpenAPIConfigFile(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("DWS_CONFIG_DIR", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "deap_openapi_url"), []byte("https://pre-deap-open-api.dingtalk.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{ConfigDir: configDir})
	if got := runtime.openAPIBaseURL(); got != "https://pre-deap-open-api.dingtalk.com" {
		t.Fatalf("resolved DEAP OpenAPI base = %q", got)
	}
}

func TestProductionLocalRunnerExposeUsesPublicDingTalkDefaultBase(t *testing.T) {
	t.Setenv("DWS_CONFIG_DIR", t.TempDir())
	agentCardSHA256 := "sha256:" + strings.Repeat("a", 64)
	localAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Local agent","protocolVersion":"1.0","capabilities":{},"skills":[],"url":"` + localAgentURL(r) + `/rpc"}`))
	}))
	defer localAgent.Close()

	requestedURLs := make([]string, 0, 2)
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, request.URL.String())
		status := http.StatusOK
		body := `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","localAgentId":"agent-1","displayName":"Local agent","status":"ACTIVE","agentCardUrl":"https://deap-open-api.dingtalk.com/v1/a2a/local-runners/endpoint-1/card","agentCardSha256":"` + agentCardSHA256 + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
		if request.Method == http.MethodPost {
			status = http.StatusCreated
			body = `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","agentCardUrl":"https://deap-open-api.dingtalk.com/v1/a2a/local-runners/endpoint-1/card","endpointBearer":"endpoint-secret","status":"ACTIVE"}}`
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
		"deap", "runtime", "expose",
		"--local-agent-id", "agent-1",
		"--display-name", "Local agent",
		"--agent-card-url", localAgent.URL,
	})
	wantURLs := []string{
		"https://deap-open-api.dingtalk.com/v1/assistant/local-runners",
		"https://deap-open-api.dingtalk.com/v1/assistant/local-runners/runner-1",
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
	if stored.OpenAPIBase != "https://deap-open-api.dingtalk.com" {
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
	if result.Summary.Type != "A2A" || result.Summary.AgentCardURL == "" || result.Summary.LocalRunner.RunnerID != "runner-1" || result.Summary.LocalRunner.EndpointID != "endpoint-1" || result.Summary.LocalRunner.Status != "CONNECTING" {
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

func TestProductionLocalRunnerStartLocalReusesStoredBuiltInBindingWithoutCreate(t *testing.T) {
	previousAgent, err := startLocalRunnerTestEchoAgent()
	if err != nil {
		t.Fatal(err)
	}
	previousCardURL := previousAgent.CardURL()
	previousRPCURL := previousAgent.RPCURL()
	rawCard := readLocalRunnerTestCard(t, previousCardURL)
	snapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-existing", "https://pre-deap.dingtalk.com")
	if err != nil {
		t.Fatal(err)
	}
	serverDigest := localRunnerSnapshotDigest(snapshot.JSON)
	if err := previousAgent.Close(); err != nil {
		t.Fatal(err)
	}
	stored := localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing",
		LocalAgentID: "test-echo-20260820", DisplayName: "DWS Test Echo 20260820",
		AgentCardURL: previousCardURL, LoopbackBaseURL: strings.TrimSuffix(previousRPCURL, localRunnerTestEchoRPCPath),
		OpenAPIBase: "https://pre-deap-open-api.dingtalk.com", AgentCardSHA256: serverDigest,
	}
	createCalls := 0
	getCalls := 0
	updateCalls := 0
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodPost:
			createCalls++
			return nil, errors.New("create must not be called for stored binding")
		case http.MethodPut:
			updateCalls++
			return nil, errors.New("update must not be called when the server digest matches the current Card")
		}
		getCalls++
		body := `{"success":true,"data":{"runnerId":"runner-existing","endpointId":"endpoint-existing","localAgentId":"test-echo-20260820","displayName":"DWS Test Echo 20260820","status":"ACTIVE","agentCardUrl":"https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json","agentCardSha256":"` + stored.AgentCardSHA256 + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	credentials := localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(), ControlHTTPClient: controlClient,
		CardHTTPClient: localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme == "http" {
				return http.DefaultClient.Do(request)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(snapshot.JSON)),
				Request:    request,
			}, nil
		}),
		OAuth: staticLocalRunnerOAuth("oauth-token"), Credentials: credentials,
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})
	if err := runtime.configs.Save(stored); err != nil {
		t.Fatal(err)
	}
	if err := credentials.StoreEndpointBearer(context.Background(), stored.RunnerID, stored.EndpointID, []byte("existing-endpoint-secret")); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: localRunnerTestEchoRef, LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
		OpenAPIBase: stored.OpenAPIBase, MaxConcurrent: 3, Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	if createCalls != 0 || getCalls != 1 || updateCalls != 0 {
		t.Fatalf("control calls = create %d get %d update %d, want 0/1/0", createCalls, getCalls, updateCalls)
	}
	if result.Summary.LocalRunner.RunnerID != stored.RunnerID || result.Summary.LocalRunner.EndpointID != stored.EndpointID || result.ConnectOptions.RunnerID != stored.RunnerID || result.ConnectOptions.EndpointID != stored.EndpointID {
		t.Fatalf("recovered binding = summary %#v connect %#v", result.Summary.LocalRunner, result.ConnectOptions)
	}
	if result.ConnectOptions.TargetURL != previousRPCURL || result.ConnectOptions.AgentCardSHA256 != stored.AgentCardSHA256 {
		t.Fatalf("recovered connect options = %#v", result.ConnectOptions)
	}
	response := postLocalRunnerTestEcho(t, result.ConnectOptions.TargetURL, []byte(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"recovered"}]}}}`))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("recovered built-in RPC status = %d", response.StatusCode)
	}
}

func TestRecoverStoredLocalAgentCardIgnoresPublicObjectKeyOrder(t *testing.T) {
	rawCard := json.RawMessage(`{"name":"Stable agent","version":"1.0.0","protocolVersion":"0.3.0","capabilities":{"streaming":true},"skills":[],"url":"http://127.0.0.1:8080/rpc"}`)
	publicOrigin := "https://pre-deap.dingtalk.com"
	publicCardURL := publicOrigin + "/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json"
	snapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-existing", publicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	serverCard := localRunnerReorderedTopLevelCard(t, snapshot.JSON)
	var desiredValue any
	var serverValue any
	if json.Unmarshal(snapshot.JSON, &desiredValue) != nil || json.Unmarshal(serverCard, &serverValue) != nil {
		t.Fatal("fixture contains invalid JSON")
	}
	desiredCanonical, _ := json.Marshal(desiredValue)
	serverCanonical, _ := json.Marshal(serverValue)
	if !bytes.Equal(desiredCanonical, serverCanonical) {
		t.Fatalf("fixture Cards are not semantically equal:\ndesired=%s\nserver=%s", snapshot.JSON, serverCard)
	}
	serverHash := sha256.Sum256(serverCard)
	serverDigest := fmt.Sprintf("sha256:%x", serverHash[:])
	stored := localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing",
		LocalAgentID: "agent-existing", DisplayName: "Stable agent",
		AgentCardURL: "http://127.0.0.1:8080/.well-known/agent-card.json", LoopbackBaseURL: "http://127.0.0.1:8080",
		OpenAPIBase: "https://pre-deap-open-api.dingtalk.com", AgentCardSHA256: serverDigest,
	}
	updateCalls := 0
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPut {
			updateCalls++
			return nil, errors.New("semantically equal Card must not be updated")
		}
		return localRunnerStatusTestResponse(t, request, localrunner.RunnerStatusData{
			RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
			LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
			Status: localrunner.RunnerStatusActive, AgentCardURL: publicCardURL,
			AgentCardSHA256: serverDigest,
		}), nil
	})
	publicCardCalls := 0
	cardClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		publicCardCalls++
		if request.Method != http.MethodGet || request.URL.String() != publicCardURL || request.Header.Get("Authorization") != "" {
			t.Fatalf("public Card request = method %s url %s authorization=%t", request.Method, request.URL, request.Header.Get("Authorization") != "")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(serverCard)),
			Request:    request,
		}, nil
	})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(), ControlHTTPClient: controlClient, CardHTTPClient: cardClient,
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})
	control, err := runtime.controlClient(stored.OpenAPIBase)
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.recoverStoredLocalAgentCard(context.Background(), localRunnerExposeOptions{
		LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
		AgentCardURL: stored.AgentCardURL, OpenAPIBase: stored.OpenAPIBase,
	}, rawCard, "http://127.0.0.1:8080/rpc", &stored, control)
	if err != nil {
		t.Fatal(err)
	}
	if updateCalls != 0 || publicCardCalls != 1 {
		t.Fatalf("recovery calls = update %d public Card GET %d, want 0/1", updateCalls, publicCardCalls)
	}
	if created.RunnerID != stored.RunnerID || created.EndpointID != stored.EndpointID || stored.AgentCardSHA256 != serverDigest {
		t.Fatalf("recovered binding = %#v stored digest=%q", created, stored.AgentCardSHA256)
	}
}

func TestRecoverStoredLocalAgentCardRejectsUnsafePublicCardResponses(t *testing.T) {
	rawCard := json.RawMessage(`{"name":"Stable agent","version":"1.0.0","protocolVersion":"0.3.0","capabilities":{},"skills":[],"url":"http://127.0.0.1:8080/rpc"}`)
	publicOrigin := "https://pre-deap.dingtalk.com"
	publicCardURL := publicOrigin + "/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json"
	snapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-existing", publicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	digestValue := sha256.Sum256(snapshot.JSON)
	digest := fmt.Sprintf("sha256:%x", digestValue[:])
	tests := []struct {
		name        string
		cardURL     string
		status      int
		contentType string
		body        []byte
		finalURL    string
	}{
		{name: "non HTTPS URL", cardURL: "http://pre-deap.dingtalk.com/card", status: http.StatusOK, contentType: "application/json", body: snapshot.JSON},
		{name: "non 200", cardURL: publicCardURL, status: http.StatusBadGateway, contentType: "application/json", body: snapshot.JSON},
		{name: "non JSON media type", cardURL: publicCardURL, status: http.StatusOK, contentType: "text/plain", body: snapshot.JSON},
		{name: "redirected final URL", cardURL: publicCardURL, status: http.StatusOK, contentType: "application/json", body: snapshot.JSON, finalURL: "https://other.example.test/card"},
		{name: "oversized", cardURL: publicCardURL, status: http.StatusOK, contentType: "application/json", body: bytes.Repeat([]byte{'x'}, maxLocalAgentCardBytes+1)},
		{name: "invalid JSON", cardURL: publicCardURL, status: http.StatusOK, contentType: "application/json", body: []byte(`{"name":`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored := localrunner.StoredRunnerConfig{
				RunnerID: "runner-existing", EndpointID: "endpoint-existing",
				LocalAgentID: "agent-existing", DisplayName: "Stable agent",
				AgentCardURL: "http://127.0.0.1:8080/.well-known/agent-card.json", LoopbackBaseURL: "http://127.0.0.1:8080",
				OpenAPIBase: "https://pre-deap-open-api.dingtalk.com", AgentCardSHA256: digest,
			}
			updateCalls := 0
			controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPut {
					updateCalls++
					return nil, errors.New("unsafe public Card must not be updated")
				}
				return localRunnerStatusTestResponse(t, request, localrunner.RunnerStatusData{
					RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
					LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
					Status: localrunner.RunnerStatusActive, AgentCardURL: test.cardURL,
					AgentCardSHA256: digest,
				}), nil
			})
			cardClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				finalURL := request.URL
				if test.finalURL != "" {
					var parseErr error
					finalURL, parseErr = url.Parse(test.finalURL)
					if parseErr != nil {
						t.Fatal(parseErr)
					}
				}
				return &http.Response{
					StatusCode: test.status,
					Header:     http.Header{"Content-Type": []string{test.contentType}},
					Body:       io.NopCloser(bytes.NewReader(test.body)),
					Request:    &http.Request{Method: http.MethodGet, URL: finalURL},
				}, nil
			})
			runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
				ConfigDir: t.TempDir(), ControlHTTPClient: controlClient, CardHTTPClient: cardClient,
				OAuth: staticLocalRunnerOAuth("oauth-token"),
				Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
				OwnerIdentity: testLocalRunnerOwnerIdentity,
			})
			control, err := runtime.controlClient(stored.OpenAPIBase)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.recoverStoredLocalAgentCard(context.Background(), localRunnerExposeOptions{
				LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
				AgentCardURL: stored.AgentCardURL, OpenAPIBase: stored.OpenAPIBase,
			}, rawCard, "http://127.0.0.1:8080/rpc", &stored, control)
			if !errors.Is(err, ErrLocalRunnerRuntimeInvalid) {
				t.Fatalf("recoverStoredLocalAgentCard() error = %v, want ErrLocalRunnerRuntimeInvalid", err)
			}
			if updateCalls != 0 {
				t.Fatalf("UpdateAgentCard calls = %d, want 0", updateCalls)
			}
		})
	}
}

func TestRecoverStoredLocalAgentCardStoresServerDigestAfterSemanticUpdate(t *testing.T) {
	rawCard := json.RawMessage(`{"name":"Stable agent","version":"1.0.0","protocolVersion":"0.3.0","capabilities":{},"skills":[],"url":"http://127.0.0.1:8080/rpc"}`)
	legacyCard := json.RawMessage(`{"name":"Stable agent","protocolVersion":"0.3.0","capabilities":{},"skills":[],"url":"http://127.0.0.1:8080/rpc"}`)
	publicOrigin := "https://pre-deap.dingtalk.com"
	publicCardURL := publicOrigin + "/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json"
	legacySnapshot, err := localrunner.RewriteAgentCard(legacyCard, "endpoint-existing", publicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	desiredSnapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-existing", publicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	serverUpdatedCard := localRunnerReorderedTopLevelCard(t, desiredSnapshot.JSON)
	legacyHash := sha256.Sum256(legacySnapshot.JSON)
	legacyDigest := fmt.Sprintf("sha256:%x", legacyHash[:])
	updatedHash := sha256.Sum256(serverUpdatedCard)
	updatedDigest := fmt.Sprintf("sha256:%x", updatedHash[:])
	stored := localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing",
		LocalAgentID: "agent-existing", DisplayName: "Stable agent",
		AgentCardURL: "http://127.0.0.1:8080/.well-known/agent-card.json", LoopbackBaseURL: "http://127.0.0.1:8080",
		OpenAPIBase: "https://pre-deap-open-api.dingtalk.com", AgentCardSHA256: legacyDigest,
	}
	updateCalls := 0
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		status := localrunner.RunnerStatusData{
			RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
			LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
			Status: localrunner.RunnerStatusActive, AgentCardURL: publicCardURL,
			AgentCardSHA256: legacyDigest,
		}
		if request.Method == http.MethodPut {
			updateCalls++
			status.AgentCardSHA256 = updatedDigest
		}
		return localRunnerStatusTestResponse(t, request, status), nil
	})
	publicCardCalls := 0
	cardClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		publicCardCalls++
		body := legacySnapshot.JSON
		if publicCardCalls > 1 {
			body = serverUpdatedCard
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(), ControlHTTPClient: controlClient, CardHTTPClient: cardClient,
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})
	if err := runtime.configs.Save(stored); err != nil {
		t.Fatal(err)
	}
	control, err := runtime.controlClient(stored.OpenAPIBase)
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.recoverStoredLocalAgentCard(context.Background(), localRunnerExposeOptions{
		LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
		AgentCardURL: stored.AgentCardURL, OpenAPIBase: stored.OpenAPIBase,
	}, rawCard, "http://127.0.0.1:8080/rpc", &stored, control)
	if err != nil {
		t.Fatal(err)
	}
	if updateCalls != 1 || publicCardCalls != 2 {
		t.Fatalf("recovery calls = update %d public Card GET %d, want 1/2", updateCalls, publicCardCalls)
	}
	if created.RunnerID != stored.RunnerID || created.EndpointID != stored.EndpointID || stored.AgentCardSHA256 != updatedDigest {
		t.Fatalf("recovered binding = %#v stored digest=%q, want server digest %q", created, stored.AgentCardSHA256, updatedDigest)
	}
	loaded, err := runtime.configs.Load(stored.RunnerID)
	if err != nil || loaded.AgentCardSHA256 != updatedDigest {
		t.Fatalf("saved digest = %q error=%v, want server digest %q", loaded.AgentCardSHA256, err, updatedDigest)
	}
}

func TestProductionLocalRunnerStartLocalUpdatesStoredBuiltInCardWithoutCreate(t *testing.T) {
	stored, currentDigest, legacyPublicCard, currentPublicCard := newLocalRunnerStoredCardUpgradeFixture(t)
	createCalls := 0
	getCalls := 0
	updateCalls := 0
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodPost:
			createCalls++
			return nil, errors.New("create must not be called for stored Card upgrade")
		case http.MethodGet:
			getCalls++
			return localRunnerStatusTestResponse(t, request, localrunner.RunnerStatusData{
				RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
				LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
				Status: localrunner.RunnerStatusActive, AgentCardURL: "https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json",
				AgentCardSHA256: stored.AgentCardSHA256,
			}), nil
		case http.MethodPut:
			updateCalls++
			if request.URL.Path != "/v1/assistant/local-runners/runner-existing/agent-card" {
				t.Fatalf("UpdateAgentCard path = %q", request.URL.Path)
			}
			var update localrunner.UpdateAgentCardRequest
			if err := json.NewDecoder(request.Body).Decode(&update); err != nil {
				t.Fatal(err)
			}
			var card struct {
				Version         string `json:"version"`
				ProtocolVersion string `json:"protocolVersion"`
			}
			if err := json.Unmarshal(update.AgentCard, &card); err != nil || card.Version != localRunnerTestEchoAgentVersion || card.ProtocolVersion != "0.3.0" {
				t.Fatalf("updated Agent Card version = %#v err=%v", card, err)
			}
			return localRunnerStatusTestResponse(t, request, localrunner.RunnerStatusData{
				RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
				LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
				Status: localrunner.RunnerStatusActive, AgentCardURL: "https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json",
				AgentCardSHA256: currentDigest,
			}), nil
		default:
			return nil, fmt.Errorf("unexpected control method %s", request.Method)
		}
	})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(), ControlHTTPClient: controlClient,
		CardHTTPClient: localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme == "http" {
				return http.DefaultClient.Do(request)
			}
			body := legacyPublicCard
			if updateCalls > 0 {
				body = currentPublicCard
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})
	if err := runtime.configs.Save(stored); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: localRunnerTestEchoRef, LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
		OpenAPIBase: stored.OpenAPIBase, MaxConcurrent: 3, Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	if createCalls != 0 || getCalls != 1 || updateCalls != 1 {
		t.Fatalf("control calls = create %d get %d update %d, want 0/1/1", createCalls, getCalls, updateCalls)
	}
	if result.ConnectOptions.RunnerID != stored.RunnerID || result.ConnectOptions.EndpointID != stored.EndpointID || result.ConnectOptions.AgentCardSHA256 != currentDigest {
		t.Fatalf("upgraded connect options = %#v", result.ConnectOptions)
	}
	loaded, err := runtime.configs.Load(stored.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AgentCardSHA256 != currentDigest {
		t.Fatalf("stored upgraded digest = %q, want %q", loaded.AgentCardSHA256, currentDigest)
	}
}

func TestProductionLocalRunnerStartLocalRejectsAgentCardUpdateResponseDrift(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*localrunner.RunnerStatusData)
	}{
		{name: "endpoint identity", mutate: func(value *localrunner.RunnerStatusData) { value.EndpointID = "endpoint-other" }},
		{name: "status", mutate: func(value *localrunner.RunnerStatusData) { value.Status = localrunner.RunnerStatusRevoked }},
		{name: "digest", mutate: func(value *localrunner.RunnerStatusData) { value.AgentCardSHA256 = "sha256:" + strings.Repeat("c", 64) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stored, currentDigest, legacyPublicCard, currentPublicCard := newLocalRunnerStoredCardUpgradeFixture(t)
			updateCalls := 0
			controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPost {
					return nil, errors.New("create must not be called for stored Card upgrade")
				}
				if request.Method == http.MethodGet {
					return localRunnerStatusTestResponse(t, request, localrunner.RunnerStatusData{
						RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
						LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
						Status: localrunner.RunnerStatusActive, AgentCardURL: "https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json",
						AgentCardSHA256: stored.AgentCardSHA256,
					}), nil
				}
				updateCalls++
				updated := localrunner.RunnerStatusData{
					RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
					LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
					Status: localrunner.RunnerStatusActive, AgentCardURL: "https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json",
					AgentCardSHA256: currentDigest,
				}
				testCase.mutate(&updated)
				return localRunnerStatusTestResponse(t, request, updated), nil
			})
			runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
				ConfigDir: t.TempDir(), ControlHTTPClient: controlClient,
				CardHTTPClient: localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
					if request.URL.Scheme == "http" {
						return http.DefaultClient.Do(request)
					}
					body := legacyPublicCard
					if updateCalls > 0 {
						body = currentPublicCard
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(bytes.NewReader(body)),
						Request:    request,
					}, nil
				}),
				OAuth: staticLocalRunnerOAuth("oauth-token"),
				Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
				OwnerIdentity: testLocalRunnerOwnerIdentity,
			})
			if err := runtime.configs.Save(stored); err != nil {
				t.Fatal(err)
			}

			_, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
				AgentRef: localRunnerTestEchoRef, LocalAgentID: stored.LocalAgentID, DisplayName: stored.DisplayName,
				OpenAPIBase: stored.OpenAPIBase, MaxConcurrent: 1, Streaming: true,
			})
			if !errors.Is(err, ErrLocalRunnerRuntimeInvalid) {
				t.Fatalf("StartLocal() error = %v, want ErrLocalRunnerRuntimeInvalid", err)
			}
			if updateCalls != 1 {
				t.Fatalf("UpdateAgentCard calls = %d, want 1", updateCalls)
			}
			loaded, loadErr := runtime.configs.Load(stored.RunnerID)
			if loadErr != nil || loaded.AgentCardSHA256 != stored.AgentCardSHA256 {
				t.Fatalf("stored digest after rejected update = %q error=%v, want %q", loaded.AgentCardSHA256, loadErr, stored.AgentCardSHA256)
			}
		})
	}
}

func TestProductionLocalRunnerStartLocalRejectsStoredBindingDriftWithoutCreate(t *testing.T) {
	var localAgent *httptest.Server
	localAgent = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Stable agent","protocolVersion":"1.0","capabilities":{},"skills":[],"url":"` + localAgent.URL + `/rpc"}`))
	}))
	defer localAgent.Close()
	cardURL := localAgent.URL + "/card"
	rawCard := readLocalRunnerTestCard(t, cardURL)
	snapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-existing", "https://pre-deap.dingtalk.com")
	if err != nil {
		t.Fatal(err)
	}
	base := localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing",
		LocalAgentID: "agent-existing", DisplayName: "Stable agent",
		AgentCardURL: cardURL, LoopbackBaseURL: localAgent.URL,
		OpenAPIBase: "https://pre-deap-open-api.dingtalk.com", AgentCardSHA256: "sha256:" + snapshot.SHA256,
	}
	for _, testCase := range []struct {
		name                    string
		mutateConfig            func(*localrunner.StoredRunnerConfig)
		openAPIBase             string
		viewLocalAgentID        string
		viewAgentCardSHA256     string
	}{
		{name: "server local agent identity", viewLocalAgentID: "other-agent"},
		{name: "target origin", mutateConfig: func(value *localrunner.StoredRunnerConfig) { value.LoopbackBaseURL = "http://127.0.0.1:1" }},
		{name: "agent card location", mutateConfig: func(value *localrunner.StoredRunnerConfig) { value.AgentCardURL = localAgent.URL + "/other-card" }},
		{name: "remote concurrent card hash", viewAgentCardSHA256: "sha256:" + strings.Repeat("b", 64)},
		{name: "OpenAPI base", openAPIBase: "https://other-open-api.example.test"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stored := base
			if testCase.mutateConfig != nil {
				testCase.mutateConfig(&stored)
			}
			viewLocalAgentID := base.LocalAgentID
			if testCase.viewLocalAgentID != "" {
				viewLocalAgentID = testCase.viewLocalAgentID
			}
			openAPIBase := base.OpenAPIBase
			if testCase.openAPIBase != "" {
				openAPIBase = testCase.openAPIBase
			}
			createCalls := 0
			updateCalls := 0
			controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPost {
					createCalls++
					return nil, errors.New("create must not be called for stored binding drift")
				}
				if request.Method == http.MethodPut {
					updateCalls++
					return nil, errors.New("update must not be called for stored binding drift")
				}
				viewAgentCardSHA256 := stored.AgentCardSHA256
				if testCase.viewAgentCardSHA256 != "" {
					viewAgentCardSHA256 = testCase.viewAgentCardSHA256
				}
				body := `{"success":true,"data":{"runnerId":"runner-existing","endpointId":"endpoint-existing","localAgentId":"` + viewLocalAgentID + `","displayName":"Stable agent","status":"ACTIVE","agentCardUrl":"https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json","agentCardSha256":"` + viewAgentCardSHA256 + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
				ConfigDir: t.TempDir(), ControlHTTPClient: controlClient, CardHTTPClient: localAgent.Client(),
				OAuth: staticLocalRunnerOAuth("oauth-token"),
				Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
				OwnerIdentity: testLocalRunnerOwnerIdentity,
			})
			if err := runtime.configs.Save(stored); err != nil {
				t.Fatal(err)
			}
			_, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
				AgentRef: cardURL, LocalAgentID: base.LocalAgentID, DisplayName: base.DisplayName,
				OpenAPIBase: openAPIBase, MaxConcurrent: 1, Streaming: true,
			})
			if !errors.Is(err, ErrLocalRunnerRuntimeInvalid) {
				t.Fatalf("StartLocal() error = %v, want ErrLocalRunnerRuntimeInvalid", err)
			}
			if createCalls != 0 || updateCalls != 0 {
				t.Fatalf("control mutation calls = create %d update %d, want 0/0", createCalls, updateCalls)
			}
		})
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

func TestProductionLocalRunnerStartLocalRunsSharedCodexBackendWithStableWorkDirIdentity(t *testing.T) {
	workDir := t.TempDir()
	backend := &fakeLocalRunnerOpenCodeBackend{reply: "Codex reply"}
	var started *localRunnerOpenCodeAgent
	var gotAgentRef string
	var gotWorkDir string
	var gotModel string
	var gotMemory bool
	var gotYolo bool
	var gotTimeout time.Duration
	testseam.Swap(t, &localRunnerLocalAgentStarter, func(_ context.Context, agentRef string, options localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
		gotAgentRef = agentRef
		gotWorkDir = options.WorkDir
		gotModel = options.Model
		gotMemory = options.Memory
		gotYolo = options.Yolo
		gotTimeout = options.Timeout
		agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", agentRef)
		started = agent
		return agent, err
	})
	digest := sha256.Sum256([]byte(workDir))
	wantLocalAgentID := fmt.Sprintf("codex-%x", digest[:8])
	agentCardSHA256 := "sha256:" + strings.Repeat("a", 64)
	var createRequest localrunner.CreateRunnerRequest
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := `{"success":true,"data":{"runnerId":"runner-codex","endpointId":"endpoint-codex","localAgentId":"` + wantLocalAgentID + `","displayName":"DWS Codex","status":"ACTIVE","agentCardUrl":"https://api.dingtalk.com/v1/a2a/local-runners/endpoint-codex/.well-known/agent-card.json","agentCardSha256":"` + agentCardSHA256 + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
		if request.Method == http.MethodPost {
			status = http.StatusCreated
			body = `{"success":true,"data":{"runnerId":"runner-codex","endpointId":"endpoint-codex","agentCardUrl":"https://api.dingtalk.com/v1/a2a/local-runners/endpoint-codex/.well-known/agent-card.json","endpointBearer":"endpoint-secret","status":"ACTIVE"}}`
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
		AgentRef: "codex", WorkDir: workDir, Model: "provider/model", Memory: true, Yolo: false, AgentTimeout: 11 * time.Second,
		OpenAPIBase: "https://api.dingtalk.com", MaxConcurrent: 3, Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAgentRef != "codex" || gotWorkDir != workDir || gotModel != "provider/model" || !gotMemory || gotYolo || gotTimeout != 11*time.Second {
		t.Fatalf("shared starter ref=%q workdir=%q model=%q memory=%v yolo=%v timeout=%v", gotAgentRef, gotWorkDir, gotModel, gotMemory, gotYolo, gotTimeout)
	}
	if createRequest.LocalAgentID != wantLocalAgentID || createRequest.DisplayName != "DWS Codex" {
		t.Fatalf("Codex create identity = %q/%q", createRequest.LocalAgentID, createRequest.DisplayName)
	}
	if result.ConnectOptions.TargetURL != started.RPCURL() {
		t.Fatalf("OpenCode connect target = %q, want %q", result.ConnectOptions.TargetURL, started.RPCURL())
	}
	stored, err := runtime.configs.Load("runner-codex")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentKind != "codex" || stored.WorkDir != workDir {
		t.Fatalf("stored Codex binding = %#v", stored)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	closeCalls := backend.closeCalls
	backend.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("Codex backend close calls = %d, want 1", closeCalls)
	}
}

func TestProductionLocalRunnerStartLocalClosesOpenCodeWhenRegistrationFails(t *testing.T) {
	backend := &fakeLocalRunnerOpenCodeBackend{reply: "reply"}
	testseam.Swap(t, &localRunnerLocalAgentStarter, func(context.Context, string, localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
		return startLocalRunnerOpenCodeAgentWithBackend(backend, "127.0.0.1:0")
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
		AgentRef: "opencode", WorkDir: t.TempDir(), OpenAPIBase: defaultLocalRunnerOpenAPIBase,
		MaxConcurrent: 1, Streaming: true,
	}); err == nil {
		t.Fatal("OpenCode registration unexpectedly succeeded")
	}
	backend.mu.Lock()
	closeCalls := backend.closeCalls
	backend.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("registration failure backend close calls = %d, want 1", closeCalls)
	}
}

func TestProductionLocalRunnerStartLocalRejectsStoredOpenCodeWorkDirDriftBeforeStart(t *testing.T) {
	storedWorkDir := t.TempDir()
	requestedWorkDir := t.TempDir()
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(),
		ControlHTTPClient: localRunnerHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("workdir drift reached control plane")
			return nil, nil
		}),
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})
	if err := runtime.configs.Save(localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing", LocalAgentID: "stable-agent", DisplayName: localRunnerOpenCodeDisplayName,
		AgentCardURL: "http://127.0.0.1:32123/.well-known/agent-card.json", LoopbackBaseURL: "http://127.0.0.1:32123",
		OpenAPIBase: "https://api.dingtalk.com", AgentCardSHA256: "sha256:" + strings.Repeat("a", 64),
		AgentKind: "opencode", WorkDir: storedWorkDir,
	}); err != nil {
		t.Fatal(err)
	}
	startCalls := 0
	testseam.Swap(t, &localRunnerLocalAgentStarter, func(context.Context, string, localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
		startCalls++
		return nil, errors.New("must not start")
	})
	_, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: "opencode", WorkDir: requestedWorkDir, LocalAgentID: "stable-agent",
		OpenAPIBase: "https://api.dingtalk.com", MaxConcurrent: 1, Streaming: true,
	})
	if !errors.Is(err, ErrLocalRunnerRuntimeInvalid) {
		t.Fatalf("StartLocal error = %v, want ErrLocalRunnerRuntimeInvalid", err)
	}
	if startCalls != 0 {
		t.Fatalf("workdir drift started OpenCode %d times", startCalls)
	}
}

func TestProductionLocalRunnerStartLocalResumesStoredOpenCodeWithoutCreateOrUpdate(t *testing.T) {
	workDir := t.TempDir()
	initialBackend := &fakeLocalRunnerOpenCodeBackend{reply: "initial"}
	initialAgent, err := startLocalRunnerOpenCodeAgentWithBackend(initialBackend, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cardURL := initialAgent.CardURL()
	rpcURL := initialAgent.RPCURL()
	rawCard := readLocalRunnerTestCard(t, cardURL)
	publicURL := "https://api.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json"
	publicSnapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-existing", "https://api.dingtalk.com")
	if err != nil {
		t.Fatal(err)
	}
	serverDigest := localRunnerAgentCardDigest(publicSnapshot.JSON)
	if err := initialAgent.Close(); err != nil {
		t.Fatal(err)
	}

	controlWrites := 0
	controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			controlWrites++
			t.Fatalf("stored OpenCode recovery sent %s %s", request.Method, request.URL)
		}
		body := `{"success":true,"data":{"runnerId":"runner-existing","endpointId":"endpoint-existing","localAgentId":"` + localRunnerOpenCodeDefaultID(workDir) + `","displayName":"DWS OpenCode","status":"ACTIVE","agentCardUrl":"` + publicURL + `","agentCardSha256":"` + serverDigest + `","connected":false,"lastHeartbeatAtEpochSecond":null}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	cardClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme == "https" {
			header := make(http.Header)
			header.Set("Content-Type", "application/json")
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(publicSnapshot.JSON)), Request: request}, nil
		}
		return http.DefaultClient.Do(request)
	})
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(), ControlHTTPClient: controlClient, CardHTTPClient: cardClient,
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})
	stored := localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing",
		LocalAgentID: localRunnerOpenCodeDefaultID(workDir), DisplayName: localRunnerOpenCodeDisplayName,
		AgentCardURL: cardURL, LoopbackBaseURL: strings.TrimSuffix(rpcURL, localRunnerOpenCodeRPCPath),
		OpenAPIBase: "https://api.dingtalk.com", AgentCardSHA256: serverDigest,
		AgentKind: localRunnerOpenCodeRef, WorkDir: workDir,
	}
	if err := runtime.configs.Save(stored); err != nil {
		t.Fatal(err)
	}
	resumedBackend := &fakeLocalRunnerOpenCodeBackend{reply: "resumed"}
	restartCalls := 0
	testseam.Swap(t, &localRunnerLocalAgentRestarter, func(_ context.Context, rawOrigin, agentRef string, options localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
		restartCalls++
		if rawOrigin != stored.LoopbackBaseURL || agentRef != localRunnerOpenCodeRef || options.WorkDir != workDir || options.Model != "provider/model" {
			t.Fatalf("OpenCode restart options = origin %q ref %q options %#v", rawOrigin, agentRef, options)
		}
		parsed, err := url.Parse(rawOrigin)
		if err != nil {
			return nil, err
		}
		return startLocalRunnerOpenCodeAgentWithBackend(resumedBackend, parsed.Host)
	})

	result, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: localRunnerOpenCodeRef, WorkDir: workDir, Model: "provider/model",
		OpenAPIBase: "https://api.dingtalk.com", MaxConcurrent: 2, Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restartCalls != 1 || controlWrites != 0 {
		t.Fatalf("recovery restart calls=%d control writes=%d", restartCalls, controlWrites)
	}
	if result.ConnectOptions.RunnerID != stored.RunnerID || result.ConnectOptions.EndpointID != stored.EndpointID || result.ConnectOptions.TargetURL != rpcURL || result.ConnectOptions.AgentCardSHA256 != serverDigest {
		t.Fatalf("recovered connect options = %#v", result.ConnectOptions)
	}
	reloaded, err := runtime.configs.Load(stored.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AgentCardSHA256 != serverDigest || reloaded.AgentKind != localRunnerOpenCodeRef || reloaded.WorkDir != workDir {
		t.Fatalf("reloaded stored OpenCode config = %#v", reloaded)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}

	explicitResult, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: localRunnerOpenCodeRef, WorkDir: workDir, Model: "provider/model",
		RunnerID: stored.RunnerID,
		OpenAPIBase: "https://api.dingtalk.com", MaxConcurrent: 2, Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer explicitResult.Close()
	if restartCalls != 2 || controlWrites != 0 {
		t.Fatalf("explicit recovery restart calls=%d control writes=%d", restartCalls, controlWrites)
	}
	if explicitResult.Summary.AgentCardURL != publicURL || explicitResult.ConnectOptions.RunnerID != stored.RunnerID || explicitResult.ConnectOptions.EndpointID != stored.EndpointID || explicitResult.ConnectOptions.TargetURL != rpcURL {
		t.Fatalf("explicit recovered binding = summary %#v connect %#v", explicitResult.Summary, explicitResult.ConnectOptions)
	}
}

func TestProductionLocalRunnerStartLocalExplicitRunnerRejectsStoredEndpointMismatchBeforeRestart(t *testing.T) {
	workDir := t.TempDir()
	controlCalls := 0
	runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
		ConfigDir: t.TempDir(),
		ControlHTTPClient: localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
			controlCalls++
			return localRunnerStatusTestResponse(t, request, localrunner.RunnerStatusData{
				RunnerID: "runner-existing", EndpointID: "endpoint-other",
				LocalAgentID: localRunnerOpenCodeDefaultID(workDir), DisplayName: localRunnerOpenCodeDisplayName,
				Status: localrunner.RunnerStatusActive,
				AgentCardURL: "https://api.dingtalk.com/v1/a2a/local-runners/endpoint-other/.well-known/agent-card.json",
				AgentCardSHA256: "sha256:" + strings.Repeat("a", 64),
			}), nil
		}),
		OAuth: staticLocalRunnerOAuth("oauth-token"),
		Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
		OwnerIdentity: testLocalRunnerOwnerIdentity,
	})
	stored := localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing",
		LocalAgentID: localRunnerOpenCodeDefaultID(workDir), DisplayName: localRunnerOpenCodeDisplayName,
		AgentCardURL: "http://127.0.0.1:32123/.well-known/agent-card.json", LoopbackBaseURL: "http://127.0.0.1:32123",
		OpenAPIBase: "https://api.dingtalk.com", AgentCardSHA256: "sha256:" + strings.Repeat("a", 64),
		AgentKind: localRunnerOpenCodeRef, WorkDir: workDir,
	}
	if err := runtime.configs.Save(stored); err != nil {
		t.Fatal(err)
	}
	startCalls := 0
	testseam.Swap(t, &localRunnerLocalAgentStarter, func(context.Context, string, localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
		startCalls++
		return nil, errors.New("must not start")
	})
	testseam.Swap(t, &localRunnerLocalAgentRestarter, func(context.Context, string, string, localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
		startCalls++
		return nil, errors.New("must not restart")
	})
	_, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
		AgentRef: localRunnerOpenCodeRef, WorkDir: workDir,
		RunnerID: stored.RunnerID,
		OpenAPIBase: stored.OpenAPIBase, MaxConcurrent: 1, Streaming: true,
	})
	if !errors.Is(err, ErrLocalRunnerRuntimeInvalid) {
		t.Fatalf("StartLocal() error = %v, want ErrLocalRunnerRuntimeInvalid", err)
	}
	if controlCalls != 1 || startCalls != 0 {
		t.Fatalf("stored endpoint mismatch calls = control %d start %d, want 1/0", controlCalls, startCalls)
	}
}

func TestProductionLocalRunnerStartLocalExplicitRunnerRebuildsMissingConfigWithoutCreate(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		mutateUpdate func(*localrunner.RunnerStatusData)
		wantError    bool
	}{
		{name: "success"},
		{name: "update identity drift", mutateUpdate: func(value *localrunner.RunnerStatusData) { value.EndpointID = "endpoint-other" }, wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workDir := t.TempDir()
			localAgentID := localRunnerOpenCodeDefaultID(workDir)
			publicOrigin := "https://api.dingtalk.com"
			publicURL := publicOrigin + "/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json"
			legacyRaw := json.RawMessage(`{"name":"DWS OpenCode","version":"0.9.0","protocolVersion":"0.3.0","description":"legacy","capabilities":{"streaming":true},"skills":[],"url":"http://127.0.0.1:1/rpc"}`)
			legacySnapshot, err := localrunner.RewriteAgentCard(legacyRaw, "endpoint-existing", publicOrigin)
			if err != nil {
				t.Fatal(err)
			}
			legacyDigest := localRunnerAgentCardDigest(legacySnapshot.JSON)
			var desiredSnapshot *localrunner.AgentCardSnapshot
			var desiredDigest string
			var started *localRunnerOpenCodeAgent
			backend := &fakeLocalRunnerOpenCodeBackend{reply: "recovered"}
			remoteRead := false
			getCalls := 0
			createCalls := 0
			updateCalls := 0
			controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				switch request.Method {
				case http.MethodPost:
					createCalls++
					return nil, errors.New("explicit recovery must not create a Runner")
				case http.MethodPut:
					updateCalls++
					if desiredSnapshot == nil || desiredDigest == "" {
						t.Fatal("Card update ran before the local Card was available")
					}
					updated := localrunner.RunnerStatusData{
						RunnerID: "runner-existing", EndpointID: "endpoint-existing",
						LocalAgentID: localAgentID, DisplayName: localRunnerOpenCodeDisplayName,
						Status: localrunner.RunnerStatusActive, AgentCardURL: publicURL,
						AgentCardSHA256: desiredDigest,
					}
					if testCase.mutateUpdate != nil {
						testCase.mutateUpdate(&updated)
					}
					return localRunnerStatusTestResponse(t, request, updated), nil
				default:
					getCalls++
					if getCalls == 1 {
						if started != nil {
							t.Fatal("explicit recovery started the local backend before authenticated runner validation")
						}
						remoteRead = true
					}
					return localRunnerStatusTestResponse(t, request, localrunner.RunnerStatusData{
						RunnerID: "runner-existing", EndpointID: "endpoint-existing",
						LocalAgentID: localAgentID, DisplayName: localRunnerOpenCodeDisplayName,
						Status: localrunner.RunnerStatusActive, AgentCardURL: publicURL,
						AgentCardSHA256: legacyDigest,
					}), nil
				}
			})
			publicCardCalls := 0
			cardClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme == "http" {
					return http.DefaultClient.Do(request)
				}
				publicCardCalls++
				body := legacySnapshot.JSON
				if publicCardCalls > 1 {
					body = desiredSnapshot.JSON
				}
				header := make(http.Header)
				header.Set("Content-Type", "application/json")
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
			})
			testseam.Swap(t, &localRunnerLocalAgentStarter, func(_ context.Context, agentRef string, options localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
				if !remoteRead {
					t.Fatal("local backend started before the explicit Runner view was validated")
				}
				agent, err := startLocalRunnerLocalAgentWithBackend(backend, "127.0.0.1:0", agentRef)
				if err != nil {
					return nil, err
				}
				started = agent
				rawCard := readLocalRunnerTestCard(t, agent.CardURL())
				snapshot, err := localrunner.RewriteAgentCard(rawCard, "endpoint-existing", publicOrigin)
				if err != nil {
					return nil, err
				}
				desiredSnapshot = snapshot
				desiredDigest = localRunnerAgentCardDigest(snapshot.JSON)
				return agent, nil
			})
			credentials := localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)})
			runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
				ConfigDir: t.TempDir(), ControlHTTPClient: controlClient, CardHTTPClient: cardClient,
				OAuth: staticLocalRunnerOAuth("oauth-token"), Credentials: credentials,
				OwnerIdentity: testLocalRunnerOwnerIdentity,
			})
			result, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
				AgentRef: localRunnerOpenCodeRef, WorkDir: workDir,
				RunnerID: "runner-existing",
				OpenAPIBase: publicOrigin, MaxConcurrent: 2, Streaming: true,
			})
			if testCase.wantError {
				if !errors.Is(err, ErrLocalRunnerRuntimeInvalid) {
					t.Fatalf("StartLocal() error = %v, want ErrLocalRunnerRuntimeInvalid", err)
				}
				if result != nil {
					t.Fatal("failed explicit recovery returned a result")
				}
				if _, loadErr := runtime.configs.Load("runner-existing"); !errors.Is(loadErr, localrunner.ErrRunnerConfigNotFound) {
					t.Fatalf("failed explicit recovery left config: %v", loadErr)
				}
				backend.mu.Lock()
				closeCalls := backend.closeCalls
				backend.mu.Unlock()
				if closeCalls != 1 {
					t.Fatalf("failed explicit recovery backend close calls = %d, want 1", closeCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer result.Close()
			if createCalls != 0 || getCalls != 2 || updateCalls != 1 || publicCardCalls != 2 {
				t.Fatalf("explicit recovery calls = create %d get %d update %d public-card %d", createCalls, getCalls, updateCalls, publicCardCalls)
			}
			if result.Summary.AgentCardURL != publicURL || result.Summary.LocalRunner.RunnerID != "runner-existing" || result.Summary.LocalRunner.EndpointID != "endpoint-existing" || result.ConnectOptions.EndpointID != "endpoint-existing" {
				t.Fatalf("explicit recovery result = summary %#v connect %#v", result.Summary, result.ConnectOptions)
			}
			stored, err := runtime.configs.Load("runner-existing")
			if err != nil {
				t.Fatal(err)
			}
			if stored.EndpointID != "endpoint-existing" || stored.LocalAgentID != localAgentID || stored.AgentKind != localRunnerOpenCodeRef || stored.WorkDir != workDir || stored.AgentCardURL != started.CardURL() || stored.AgentCardSHA256 != desiredDigest {
				t.Fatalf("rebuilt stored config = %#v", stored)
			}
			if _, err := credentials.LoadEndpointBearer(context.Background(), "runner-existing", "endpoint-existing"); !errors.Is(err, localrunner.ErrEndpointBearerNotFound) {
				t.Fatalf("explicit recovery unexpectedly required endpoint bearer: %v", err)
			}
		})
	}
}

func TestProductionLocalRunnerStartLocalExplicitRunnerRejectsRemoteDriftBeforeStart(t *testing.T) {
	workDir := t.TempDir()
	wantLocalAgentID := localRunnerOpenCodeDefaultID(workDir)
	for _, testCase := range []struct {
		name       string
		statusCode int
		mutate     func(*localrunner.RunnerStatusData)
	}{
		{name: "missing", statusCode: http.StatusNotFound},
		{name: "revoked", mutate: func(value *localrunner.RunnerStatusData) { value.Status = localrunner.RunnerStatusRevoked }},
		{name: "runner mismatch", mutate: func(value *localrunner.RunnerStatusData) { value.RunnerID = "runner-other" }},
		{name: "local agent mismatch", mutate: func(value *localrunner.RunnerStatusData) { value.LocalAgentID = "agent-other" }},
		{name: "display name mismatch", mutate: func(value *localrunner.RunnerStatusData) { value.DisplayName = "Other agent" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			startCalls := 0
			testseam.Swap(t, &localRunnerLocalAgentStarter, func(context.Context, string, localRunnerLocalAgentOptions) (*localRunnerOpenCodeAgent, error) {
				startCalls++
				return nil, errors.New("must not start")
			})
			createCalls := 0
			controlClient := localRunnerHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPost {
					createCalls++
					return nil, errors.New("must not create")
				}
				if testCase.statusCode == http.StatusNotFound {
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"localRunnerNotFound","message":"not found"}}`)), Request: request}, nil
				}
				view := localrunner.RunnerStatusData{
					RunnerID: "runner-existing", EndpointID: "endpoint-existing",
					LocalAgentID: wantLocalAgentID, DisplayName: localRunnerOpenCodeDisplayName,
					Status: localrunner.RunnerStatusActive,
					AgentCardURL: "https://api.dingtalk.com/v1/a2a/local-runners/endpoint-existing/.well-known/agent-card.json",
					AgentCardSHA256: "sha256:" + strings.Repeat("a", 64),
				}
				if testCase.mutate != nil {
					testCase.mutate(&view)
				}
				return localRunnerStatusTestResponse(t, request, view), nil
			})
			runtime := newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{
				ConfigDir: t.TempDir(), ControlHTTPClient: controlClient,
				OAuth: staticLocalRunnerOAuth("oauth-token"),
				Credentials: localrunner.NewEndpointBearerKeyring(&runtimeSecretBackend{values: make(map[string]string)}),
				OwnerIdentity: testLocalRunnerOwnerIdentity,
			})
			result, err := runtime.StartLocal(context.Background(), localRunnerStartLocalOptions{
				AgentRef: localRunnerOpenCodeRef, WorkDir: workDir,
				RunnerID: "runner-existing",
				OpenAPIBase: "https://api.dingtalk.com", MaxConcurrent: 1, Streaming: true,
			})
			if err == nil || result != nil {
				t.Fatalf("explicit remote drift result=%#v error=%v", result, err)
			}
			if startCalls != 0 || createCalls != 0 {
				t.Fatalf("remote drift calls = start %d create %d, want 0/0", startCalls, createCalls)
			}
			if _, loadErr := runtime.configs.Load("runner-existing"); !errors.Is(loadErr, localrunner.ErrRunnerConfigNotFound) {
				t.Fatalf("remote drift left local config: %v", loadErr)
			}
		})
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
		OpenAPIBaseURL:    func() string { return controlServer.URL },
		OwnerIdentity: func(context.Context) (string, string, error) {
			return "tenant-1", "operator-1", nil
		},
		ReconnectBackoff:  time.Millisecond,
	})
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	exposeOutput := executeLocalRunnerCommand(t, context.Background(), []string{
		"deap", "runtime", "expose", "--local-agent-id", "agent-1", "--display-name", "Local agent",
		"--agent-card-url", localAgent.URL + "/card",
	})
	assertLocalRunnerOutputHasNoSecrets(t, exposeOutput)

	connectDone := make(chan error, 1)
	go func() {
		_, err := executeLocalRunnerCommandResult(ctx, []string{
			"deap", "runtime", "connect", "--runner-id", "runner-1", "--endpoint-id", "endpoint-1",
			"--target-url", localAgent.URL + "/rpc", "--agent-card-sha256", cardSHA,
		})
		connectDone <- err
	}()
	select {
	case <-firstChunkAtGateway:
	case <-time.After(5 * time.Second):
		t.Fatal("first SSE chunk did not reach the WSS gateway")
	}
	statusOutput := executeLocalRunnerCommand(t, context.Background(), []string{"deap", "runtime", "status", "--runner-id", "runner-1"})
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

	revokeOutput := executeLocalRunnerCommand(t, context.Background(), []string{"--yes", "deap", "runtime", "revoke", "--runner-id", "runner-1"})
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

func readLocalRunnerTestCard(t *testing.T, rawURL string) json.RawMessage {
	t.Helper()
	response, err := http.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("read Agent Card status=%d error=%v", response.StatusCode, err)
	}
	return json.RawMessage(raw)
}

func newLocalRunnerStoredCardUpgradeFixture(t *testing.T) (localrunner.StoredRunnerConfig, string, []byte, []byte) {
	t.Helper()
	agent, err := startLocalRunnerTestEchoAgent()
	if err != nil {
		t.Fatal(err)
	}
	cardURL := agent.CardURL()
	rpcURL := agent.RPCURL()
	currentCard := readLocalRunnerTestCard(t, cardURL)
	var legacyCard map[string]json.RawMessage
	if err := json.Unmarshal(currentCard, &legacyCard); err != nil {
		t.Fatal(err)
	}
	delete(legacyCard, "version")
	legacyRaw, err := json.Marshal(legacyCard)
	if err != nil {
		t.Fatal(err)
	}
	legacySnapshot, err := localrunner.RewriteAgentCard(legacyRaw, "endpoint-existing", "https://pre-deap.dingtalk.com")
	if err != nil {
		t.Fatal(err)
	}
	currentSnapshot, err := localrunner.RewriteAgentCard(currentCard, "endpoint-existing", "https://pre-deap.dingtalk.com")
	if err != nil {
		t.Fatal(err)
	}
	if legacySnapshot.SHA256 == currentSnapshot.SHA256 {
		t.Fatal("top-level Agent Card version did not change the public snapshot digest")
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	currentPublicCard := localRunnerReorderedTopLevelCard(t, currentSnapshot.JSON)
	currentPublicHash := sha256.Sum256(currentPublicCard)
	return localrunner.StoredRunnerConfig{
		RunnerID: "runner-existing", EndpointID: "endpoint-existing",
		LocalAgentID: "test-echo-20260820", DisplayName: "DWS Test Echo 20260820",
		AgentCardURL: cardURL, LoopbackBaseURL: strings.TrimSuffix(rpcURL, localRunnerTestEchoRPCPath),
		OpenAPIBase: "https://pre-deap-open-api.dingtalk.com", AgentCardSHA256: "sha256:" + legacySnapshot.SHA256,
	}, fmt.Sprintf("sha256:%x", currentPublicHash[:]), legacySnapshot.JSON, currentPublicCard
}

func localRunnerSnapshotDigest(published []byte) string {
	digest := sha256.Sum256(published)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func localRunnerReorderedTopLevelCard(t *testing.T, published []byte) []byte {
	t.Helper()
	var card map[string]json.RawMessage
	if err := json.Unmarshal(published, &card); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(card))
	for key := range card {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var reordered bytes.Buffer
	reordered.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			reordered.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		reordered.Write(encodedKey)
		reordered.WriteByte(':')
		reordered.Write(card[key])
	}
	reordered.WriteByte('}')
	if bytes.Equal(reordered.Bytes(), published) {
		t.Fatalf("fixture did not change the public Card key order: %s", published)
	}
	return reordered.Bytes()
}

func localRunnerStatusTestResponse(t *testing.T, request *http.Request, data localrunner.RunnerStatusData) *http.Response {
	t.Helper()
	raw, err := json.Marshal(struct {
		Success bool                         `json:"success"`
		Data    localrunner.RunnerStatusData `json:"data"`
	}{Success: true, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(bytes.NewReader(raw)), Request: request,
	}
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
