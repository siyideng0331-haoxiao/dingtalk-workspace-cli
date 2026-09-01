// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/testseam"
)

func TestExchangeManagedAuthCodeUsesExplicitClientAndPreservesRuntimeState(t *testing.T) {
	var requestBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"accessToken":"access-secret","refreshToken":"refresh-secret","expiresIn":7200,"corpId":"corp-employee","userId":"user-employee"}`)
	}))
	t.Cleanup(server.Close)

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "mcp_url"), []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_CONFIG_DIR", configDir)

	SetClientID("supervisor-client")
	SetClientSecret("supervisor-secret")
	SetRuntimeProfile("corp-supervisor:user-supervisor")
	t.Cleanup(func() {
		SetClientID("")
		SetClientSecret("")
		SetRuntimeProfile("")
	})

	var persistedSelector string
	var persisted *TokenData
	testseam.Swap(t, &managedExchangePreparePersistence, func(string) error { return nil })
	testseam.Swap(t, &managedExchangePersistToken, func(_ string, selector string, data *TokenData) error {
		persistedSelector = selector
		copy := *data
		persisted = &copy
		return nil
	})

	data, err := ExchangeManagedAuthCode(context.Background(), configDir, ManagedExchangeRequest{
		ClientID:        "employee-client",
		AuthCode:        "one-time-secret",
		UID:             "user-employee",
		ExpectedOrgID:   "corp-employee",
		PreserveProfile: "corp-supervisor:user-supervisor",
	})
	if err != nil {
		t.Fatalf("ExchangeManagedAuthCode() error = %v", err)
	}
	if requestBody["clientId"] != "employee-client" || requestBody["authCode"] != "one-time-secret" {
		t.Fatalf("exchange request = %#v", requestBody)
	}
	if requestBody["grantType"] != "authorization_code" {
		t.Fatalf("grantType = %q", requestBody["grantType"])
	}
	if data.UserID != "user-employee" || data.CorpID != "corp-employee" {
		t.Fatalf("token identity = %#v", data)
	}
	if data.ClientID != "employee-client" || data.Source != "mcp" || !data.FreshAuthorization {
		t.Fatalf("managed token metadata = %#v", data)
	}
	if persisted == nil || persistedSelector != "corp-supervisor:user-supervisor" {
		t.Fatalf("persisted token=%#v selector=%q", persisted, persistedSelector)
	}
	if ClientID() != "supervisor-client" || ClientSecret() != "supervisor-secret" {
		t.Fatalf("global app credentials changed: clientID=%q secret=%q", ClientID(), ClientSecret())
	}
	if RuntimeProfile() != "corp-supervisor:user-supervisor" {
		t.Fatalf("runtime profile changed to %q", RuntimeProfile())
	}
}

func TestExchangeManagedAuthCodeRejectsIdentityMismatchBeforePersistence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"accessToken":"access","refreshToken":"refresh","corpId":"wrong-corp","userId":"wrong-user"}`)
	}))
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "mcp_url"), []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_CONFIG_DIR", configDir)

	persisted := false
	testseam.Swap(t, &managedExchangePreparePersistence, func(string) error { return nil })
	testseam.Swap(t, &managedExchangePersistToken, func(string, string, *TokenData) error {
		persisted = true
		return nil
	})

	_, err := ExchangeManagedAuthCode(context.Background(), configDir, ManagedExchangeRequest{
		ClientID:        "employee-client",
		AuthCode:        "never-print-this-code",
		UID:             "expected-user",
		ExpectedOrgID:   "expected-corp",
		PreserveProfile: "supervisor-corp:supervisor-user",
	})
	if err == nil {
		t.Fatal("identity mismatch succeeded")
	}
	if persisted {
		t.Fatal("identity mismatch persisted token")
	}
	if got := err.Error(); got == "" || containsAny(got, "never-print-this-code", "access", "refresh") {
		t.Fatalf("unsafe mismatch error = %q", got)
	}
}

func TestExchangeManagedAuthCodeRejectsMissingTokenIdentityBeforePersistence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"accessToken":"access","refreshToken":"refresh","corpId":"expected-corp"}`)
	}))
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "mcp_url"), []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_CONFIG_DIR", configDir)

	persisted := false
	testseam.Swap(t, &managedExchangePreparePersistence, func(string) error { return nil })
	testseam.Swap(t, &managedExchangePersistToken, func(string, string, *TokenData) error {
		persisted = true
		return nil
	})

	_, err := ExchangeManagedAuthCode(context.Background(), configDir, ManagedExchangeRequest{
		ClientID:        "employee-client",
		AuthCode:        "one-time-secret",
		UID:             "expected-user",
		ExpectedOrgID:   "expected-corp",
		PreserveProfile: "supervisor-corp:supervisor-user",
	})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("missing token identity error = %v", err)
	}
	if persisted {
		t.Fatal("token without a verified userId was persisted")
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
