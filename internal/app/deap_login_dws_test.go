package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/dwsauth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/testseam"
)

func TestLoginDWSUsesOperatorForGrantAndEmployeeForExchange(t *testing.T) {
	t.Setenv("DWS_CONFIG_DIR", t.TempDir())
	t.Setenv("DWS_PROFILE", "pending-employee-corp:pending-employee-uid")
	t.Cleanup(func() { authpkg.SetRuntimeProfile("") })
	testseam.Swap(t, &deapLoginDWSClientIDProvider,
		func(context.Context) (string, error) { return "mcp-client-id", nil })

	grantClient := &recordingDWSGrantIssuer{
		grant: &dwsauth.Grant{
			AssistantID:      "employee-1",
			CorpID:           "ding-corp",
			UID:              "987654",
			AuthCode:         "one-time-code",
			ExpiresInSeconds: 300,
		},
	}
	testseam.Swap(t, &deapLoginDWSGrantClientProvider,
		func(string) (deapLoginDWSGrantIssuer, error) { return grantClient, nil })
	var exchangeProfile, exchangeCode, exchangeUID string
	testseam.Swap(t, &deapLoginDWSExchangeAuthCode,
		func(_ context.Context, _, _, code, corpID, uid string) (*authpkg.TokenData, error) {
			exchangeProfile = authpkg.RuntimeProfile()
			exchangeCode = code
			exchangeUID = uid
			if corpID != "ding-corp" {
				t.Fatalf("exchange corpId = %q, want ding-corp", corpID)
			}
			return &authpkg.TokenData{
				AccessToken:  "employee-access-token",
				RefreshToken: "employee-refresh-token",
				CorpID:       "ding-corp",
				UserID:       "987654",
				ExpiresAt:    time.Now().Add(time.Hour),
			}, nil
		})

	var output bytes.Buffer
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"deep", "manage", "login-dws", "--assistant-id", "employee-1"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login-dws error = %v\noutput:\n%s", err, output.String())
	}

	if grantClient.profile != "" {
		t.Fatalf("grant profile = %q, want operator default selector", grantClient.profile)
	}
	if exchangeProfile != "ding-corp:987654" {
		t.Fatalf("exchange profile = %q, want exact employee selector", exchangeProfile)
	}
	if exchangeCode != "one-time-code" || exchangeUID != "987654" {
		t.Fatalf("exchange code/uid = %q/%q", exchangeCode, exchangeUID)
	}
	if got := authpkg.RuntimeProfile(); got != "pending-employee-corp:pending-employee-uid" {
		t.Fatalf("runtime profile after login = %q, want restored process selector", got)
	}
	for _, secret := range []string{"one-time-code", "employee-access-token", "employee-refresh-token"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("output leaked secret %q: %s", secret, output.String())
		}
	}
	for _, want := range []string{"employee-1", "ding-corp", "987654", "ding-corp:987654"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q: %s", want, output.String())
		}
	}
}

func TestLoginDWSCanonicalAndCompatibilityPathsExist(t *testing.T) {
	root := NewRootCommand()
	for _, path := range [][]string{
		{"deap", "manage", "login-dws"},
		{"deep", "manage", "login-dws"},
	} {
		command, remaining, err := root.Find(path)
		if err != nil || command == nil || len(remaining) != 0 {
			t.Fatalf("command %v resolution = command=%v remaining=%v error=%v",
				path, command, remaining, err)
		}
	}
}

func TestLoginDWSRequiresAssistantID(t *testing.T) {
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"deap", "manage", "login-dws"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "assistant-id") {
		t.Fatalf("error = %v, want required --assistant-id", err)
	}
}

func TestLoginDWSAcceptsInjectedAuthCodeGrant(t *testing.T) {
	t.Setenv("DWS_CONFIG_DIR", t.TempDir())
	t.Cleanup(func() { authpkg.SetRuntimeProfile("") })
	testseam.Swap(t, &deapLoginDWSClientIDProvider,
		func(context.Context) (string, error) { return "mcp-client-id", nil })

	testseam.Swap(t, &deapLoginDWSGrantClientProvider,
		func(string) (deapLoginDWSGrantIssuer, error) {
			t.Fatal("grant client must not be used for an injected auth code")
			return nil, nil
		})
	var exchangeCode, exchangeUID string
	testseam.Swap(t, &deapLoginDWSExchangeAuthCode,
		func(_ context.Context, _, _, code, corpID, uid string) (*authpkg.TokenData, error) {
			exchangeCode = code
			exchangeUID = uid
			if corpID != "ding-corp" {
				t.Fatalf("exchange corpId = %q, want ding-corp", corpID)
			}
			return &authpkg.TokenData{
				AccessToken: "employee-access-token",
				CorpID:      "ding-corp",
				UserID:      "987654",
				ExpiresAt:   time.Now().Add(time.Hour),
			}, nil
		})

	var output bytes.Buffer
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{
		"deap", "manage", "login-dws",
		"--auth-code", "one-time-code",
		"--corp-id", "ding-corp",
		"--uid", "987654",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("login-dws injected grant error = %v\noutput:\n%s", err, output.String())
	}
	if exchangeCode != "one-time-code" || exchangeUID != "987654" {
		t.Fatalf("exchange code/uid = %q/%q", exchangeCode, exchangeUID)
	}
	if strings.Contains(output.String(), "one-time-code") {
		t.Fatalf("output leaked auth code: %s", output.String())
	}
}

func TestLoginDWSFetchesMCPClientIDBeforeInjectedAuthCodeExchange(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("DWS_CONFIG_DIR", configDir)
	t.Cleanup(func() { authpkg.SetRuntimeProfile("") })

	var clientIDRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != authpkg.ClientIDPath {
			t.Errorf("MCP request path = %q, want %q", r.URL.Path, authpkg.ClientIDPath)
			http.NotFound(w, r)
			return
		}
		clientIDRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":"mcp-client-id"}`))
	}))
	defer server.Close()
	if err := os.WriteFile(filepath.Join(configDir, "mcp_url"), []byte(server.URL), 0o600); err != nil {
		t.Fatalf("write mcp_url: %v", err)
	}

	var exchangeClientID string
	testseam.Swap(t, &deapLoginDWSExchangeAuthCode,
		func(_ context.Context, _, clientID, _, corpID, uid string) (*authpkg.TokenData, error) {
			exchangeClientID = clientID
			return &authpkg.TokenData{
				AccessToken: "employee-access-token",
				CorpID:      corpID,
				UserID:      uid,
				ExpiresAt:   time.Now().Add(time.Hour),
			}, nil
		})

	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"deap", "manage", "login-dws",
		"--auth-code", "one-time-code",
		"--corp-id", "ding-corp",
		"--uid", "987654",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("login-dws injected grant error = %v", err)
	}
	if got := clientIDRequests.Load(); got != 1 {
		t.Fatalf("MCP client ID requests = %d, want 1", got)
	}
	if exchangeClientID != "mcp-client-id" {
		t.Fatalf("exchange clientId = %q, want mcp-client-id", exchangeClientID)
	}
}

type recordingDWSGrantIssuer struct {
	grant   *dwsauth.Grant
	profile string
}

func (i *recordingDWSGrantIssuer) Issue(context.Context, string) (*dwsauth.Grant, error) {
	i.profile = authpkg.RuntimeProfile()
	return i.grant, nil
}
