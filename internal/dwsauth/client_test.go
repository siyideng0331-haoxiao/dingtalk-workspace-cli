package dwsauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIssuesGrantWithOperatorBearer(t *testing.T) {
	var receivedMethod, receivedPath, receivedAuth, receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.EscapedPath()
		receivedAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		receivedBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"data":{"assistantId":"employee/1","corpId":"ding-corp","uid":"987654","authCode":"one-time-code","expiresInSeconds":300}}`)
	}))
	t.Cleanup(server.Close)
	tokens := &recordingTokenProvider{accessToken: "operator-token"}
	client, err := NewClient(server.URL, server.Client(), tokens)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	grant, err := client.Issue(context.Background(), "employee/1")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if receivedMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", receivedMethod)
	}
	if receivedPath != "/v1/assistant/digital-employees/employee%2F1/dws-auth-grants" {
		t.Fatalf("escaped path = %q", receivedPath)
	}
	if receivedAuth != "Bearer operator-token" {
		t.Fatalf("authorization = %q", receivedAuth)
	}
	if receivedBody != "" {
		t.Fatalf("body = %q, want empty", receivedBody)
	}
	if grant.AssistantID != "employee/1" || grant.CorpID != "ding-corp" ||
		grant.UID != "987654" || grant.AuthCode != "one-time-code" ||
		grant.ExpiresInSeconds != 300 {
		t.Fatalf("grant = %#v", grant)
	}
}

func TestClientRefreshesRejectedOperatorBearerOnce(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer stale-token" {
				t.Fatalf("first authorization = %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"unauthorized","message":"rejected"}}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
			t.Fatalf("retry authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"assistantId":"employee-1","corpId":"ding-corp","uid":"987654","authCode":"one-time-code","expiresInSeconds":300}}`)
	}))
	t.Cleanup(server.Close)
	tokens := &recordingTokenProvider{
		accessToken:  "stale-token",
		refreshToken: "fresh-token",
	}
	client, err := NewClient(server.URL, server.Client(), tokens)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if _, err := client.Issue(context.Background(), "employee-1"); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if requests != 2 || tokens.refreshCalls != 1 || tokens.rejected != "stale-token" {
		t.Fatalf("requests=%d refreshCalls=%d rejected=%q",
			requests, tokens.refreshCalls, tokens.rejected)
	}
}

func TestClientRejectsMalformedGrantWithoutLeakingResponse(t *testing.T) {
	const secret = "secret-auth-code-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":{"assistantId":"wrong","corpId":"ding-corp","uid":"987654","authCode":"`+secret+`","expiresInSeconds":300}}`)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, server.Client(),
		&recordingTokenProvider{accessToken: "operator-token"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Issue(context.Background(), "employee-1")
	if err == nil {
		t.Fatal("Issue() error = nil, want malformed response")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked auth code: %v", err)
	}
}

func TestClientReturnsStableServerErrorWithoutLeakingBody(t *testing.T) {
	const secret = "body-secret-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"dwsAuthUnavailable","message":"`+secret+`"}}`)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, server.Client(),
		&recordingTokenProvider{accessToken: "operator-token"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Issue(context.Background(), "employee-1")
	if err == nil {
		t.Fatal("Issue() error = nil, want server error")
	}
	if !strings.Contains(err.Error(), "dwsAuthUnavailable") || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %q", err)
	}
}

func TestGrantFormattingRedactsAuthCode(t *testing.T) {
	grant := &Grant{AssistantID: "employee-1", CorpID: "ding-corp", UID: "987654",
		AuthCode: "one-time-code", ExpiresInSeconds: 300}
	for _, formatted := range []string{fmt.Sprintf("%v", grant), fmt.Sprintf("%#v", grant)} {
		if strings.Contains(formatted, "one-time-code") || !strings.Contains(formatted, "[REDACTED]") {
			t.Fatalf("grant formatting was not redacted: %s", formatted)
		}
	}
	raw, err := json.Marshal(grant)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(raw), "one-time-code") || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("grant JSON was not redacted: %s", raw)
	}
}

type recordingTokenProvider struct {
	accessToken  string
	refreshToken string
	rejected     string
	refreshCalls int
}

func (p *recordingTokenProvider) AccessToken(context.Context) (string, error) {
	return p.accessToken, nil
}

func (p *recordingTokenProvider) RefreshRejectedAccessToken(_ context.Context, rejected string) (string, error) {
	p.refreshCalls++
	p.rejected = rejected
	return p.refreshToken, nil
}
