package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/testseam"
)

func TestAuthExchangeFetchesMCPClientIDBeforeExchange(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("DWS_CONFIG_DIR", configDir)

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

	testseam.Swap(t, &authOAuthExchange,
		func(*authpkg.OAuthProvider, context.Context, string, string) (*authpkg.TokenData, error) {
			return &authpkg.TokenData{
				AccessToken: "employee-access-token",
				CorpID:      "ding-corp",
				ExpiresAt:   time.Now().Add(time.Hour),
			}, nil
		})

	cmd := newAuthExchangeCommand(nil)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--code", "one-time-code"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("auth exchange error = %v", err)
	}
	if got := clientIDRequests.Load(); got != 1 {
		t.Fatalf("MCP client ID requests = %d, want 1", got)
	}
}
