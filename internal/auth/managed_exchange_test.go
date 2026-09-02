// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package auth

import (
	"context"
	"encoding/json"
	"errors"
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
		_, _ = io.WriteString(w, `{"accessToken":"access-secret","refreshToken":"refresh-secret","expiresIn":7200,"corpId":"corp-employee"}`)
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
	identityResolved := false
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
		ResolveIdentity: func(_ context.Context, accessToken, expectedOrgID string) (ManagedIdentity, error) {
			identityResolved = true
			if accessToken != "access-secret" || expectedOrgID != "corp-employee" {
				t.Fatalf("identity resolver input token=%q org=%q", accessToken, expectedOrgID)
			}
			return ManagedIdentity{
				CorpID: "corp-employee", CorpName: "Employee Corp",
				UserID: "user-employee", UserName: "Employee",
			}, nil
		},
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
	if !identityResolved || data.UserName != "Employee" || data.CorpName != "Employee Corp" {
		t.Fatalf("resolved identity = %#v called=%v", data, identityResolved)
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
		_, _ = io.WriteString(w, `{"accessToken":"access","refreshToken":"refresh","corpId":"expected-corp","userId":"wrong-user"}`)
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
		ResolveIdentity: func(context.Context, string, string) (ManagedIdentity, error) {
			return ManagedIdentity{CorpID: "expected-corp", UserID: "expected-user"}, nil
		},
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
		ResolveIdentity: func(context.Context, string, string) (ManagedIdentity, error) {
			return ManagedIdentity{CorpID: "expected-corp"}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("missing token identity error = %v", err)
	}
	if persisted {
		t.Fatal("token without a verified userId was persisted")
	}
}

func TestExchangeManagedAuthCodeRejectsResolvedIdentityMismatchBeforePersistence(t *testing.T) {
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
		ResolveIdentity: func(context.Context, string, string) (ManagedIdentity, error) {
			return ManagedIdentity{CorpID: "expected-corp", UserID: "other-user"}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("resolved identity mismatch error = %v", err)
	}
	if persisted {
		t.Fatal("mismatched resolved identity was persisted")
	}
}

func TestExchangeManagedAuthCodeRejectsResolvedOrganizationMismatchBeforePersistence(t *testing.T) {
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
		ResolveIdentity: func(context.Context, string, string) (ManagedIdentity, error) {
			return ManagedIdentity{CorpID: "other-corp", UserID: "expected-user"}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "organization") {
		t.Fatalf("resolved organization mismatch error = %v", err)
	}
	if persisted {
		t.Fatal("mismatched resolved organization was persisted")
	}
}

func TestExchangeManagedAuthCodeSanitizesIdentityLookupFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"accessToken":"access-secret","refreshToken":"refresh-secret","corpId":"expected-corp"}`)
	}))
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "mcp_url"), []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_CONFIG_DIR", configDir)
	testseam.Swap(t, &managedExchangePreparePersistence, func(string) error { return nil })

	_, err := ExchangeManagedAuthCode(context.Background(), configDir, ManagedExchangeRequest{
		ClientID:        "employee-client",
		AuthCode:        "one-time-secret",
		UID:             "expected-user",
		ExpectedOrgID:   "expected-corp",
		PreserveProfile: "supervisor-corp:supervisor-user",
		ResolveIdentity: func(context.Context, string, string) (ManagedIdentity, error) {
			return ManagedIdentity{}, errors.New("lookup failed with access-secret")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "identity lookup failed") {
		t.Fatalf("identity lookup error = %v", err)
	}
	if got := err.Error(); containsAny(got, "access-secret", "refresh-secret", "one-time-secret") {
		t.Fatalf("unsafe identity lookup error = %q", got)
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
