package localrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreatedRunnerUsesFrozenLowerCamelCaseJSON(t *testing.T) {
	encoded, err := json.Marshal(CreatedRunner{
		RunnerID:     "runner-1",
		EndpointID:   "endpoint-1",
		AgentCardURL: "https://api.example.test/card",
		Status:       RunnerStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"runnerId":"runner-1","endpointId":"endpoint-1","agentCardUrl":"https://api.example.test/card","status":"ACTIVE"}`
	if string(encoded) != want {
		t.Fatalf("CreatedRunner JSON = %s, want %s", encoded, want)
	}
}

func TestHTTPControlClientCreatesRunnerWithOneOAuthRefreshAndStoresBearer(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/assistant/local-runners" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var object map[string]json.RawMessage
		if json.Unmarshal(body, &object) != nil || len(object) != 3 || object["localAgentId"] == nil || object["displayName"] == nil || object["agentCard"] == nil {
			t.Errorf("body = %s", body)
		}
		if requests == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer rejected-oauth" {
				t.Errorf("first authorization = %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"expired"}}`))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-oauth" {
			t.Errorf("retry authorization = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","agentCardUrl":"https://api.example.test/card","endpointBearer":"endpoint-secret","status":"ACTIVE"}}`))
	}))
	defer server.Close()

	provider := &recordingOAuthProvider{accessToken: "rejected-oauth", refreshedToken: "fresh-oauth"}
	sink := &recordingEndpointBearerSink{}
	client, err := NewHTTPControlClient(server.URL, server.Client(), provider)
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateRunner(context.Background(), CreateRunnerRequest{
		LocalAgentID: "agent-1",
		DisplayName:  "Local agent",
		AgentCard:    json.RawMessage(`{"protocolVersion":"1.0"}`),
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if created.RunnerID != "runner-1" || created.EndpointID != "endpoint-1" || provider.refreshCalls != 1 || requests != 2 {
		t.Fatalf("created=%#v refresh=%d requests=%d", created, provider.refreshCalls, requests)
	}
	if string(sink.secret) != "endpoint-secret" {
		t.Fatal("endpoint bearer was not transferred to sink")
	}
	if strings.Contains(fmt.Sprintf("%#v", created), "endpoint-secret") {
		t.Fatal("created result exposed endpoint bearer")
	}
}

func TestHTTPControlClientOpenConnectionUsesExactBinding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/assistant/local-runners/runner-1/connections/open" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"endpointId":"endpoint-1"}` {
			t.Errorf("body = %s", body)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","webSocketUrl":"wss://gateway.example.test/connect","connectionTicket":"lr1.payload.signature","ticketExpiresAtEpochSecond":200}}`))
	}))
	defer server.Close()

	client, err := NewHTTPControlClient(server.URL, server.Client(), &recordingOAuthProvider{accessToken: "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.OpenConnection(context.Background(), testIdentity(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if data.RunnerID != "runner-1" || data.EndpointID != "endpoint-1" || data.ConnectionTicket == nil {
		t.Fatalf("data = %#v", data)
	}
}

func TestHTTPControlClientReturnsStableFailureWithoutMessageLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"localRunnerNotFound","message":"endpointBearer=do-not-print"}}`))
	}))
	defer server.Close()

	client, err := NewHTTPControlClient(server.URL, server.Client(), &recordingOAuthProvider{accessToken: "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetRunner(context.Background(), "runner-1")
	var failure *ControlFailure
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusNotFound || failure.Detail.Code != "localRunnerNotFound" {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "do-not-print") {
		t.Fatal("failure exposed diagnostic message")
	}
}

func TestHTTPControlClientRejectsCleartextNonLoopbackBaseBeforeOAuth(t *testing.T) {
	provider := &recordingOAuthProvider{accessToken: "must-not-be-used"}
	if _, err := NewHTTPControlClient("http://api.example.test", http.DefaultClient, provider); !errors.Is(err, ErrControlClientInvalid) {
		t.Fatalf("constructor error = %v, want ErrControlClientInvalid", err)
	}
}

type recordingOAuthProvider struct {
	accessToken    string
	refreshedToken string
	refreshCalls   int
}

func (p *recordingOAuthProvider) AccessToken(context.Context) (string, error) {
	return p.accessToken, nil
}

func (p *recordingOAuthProvider) RefreshRejectedAccessToken(_ context.Context, rejected string) (string, error) {
	p.refreshCalls++
	if rejected != p.accessToken {
		return "", errors.New("unexpected rejected token")
	}
	return p.refreshedToken, nil
}
