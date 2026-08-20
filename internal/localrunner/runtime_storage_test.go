package localrunner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndpointBearerKeyringStoresLoadsAndRemovesWithoutExposingAccountMaterial(t *testing.T) {
	backend := &recordingSecretBackend{values: make(map[string]string)}
	store := NewEndpointBearerKeyring(backend)
	secret := []byte("endpoint-bearer-secret")

	if err := store.StoreEndpointBearer(context.Background(), "runner-1", "endpoint-1", secret); err != nil {
		t.Fatal(err)
	}
	if len(backend.values) != 1 {
		t.Fatalf("keyring entries = %d, want 1", len(backend.values))
	}
	for account, value := range backend.values {
		if strings.Contains(account, "runner-1") || strings.Contains(account, "endpoint-1") || strings.Contains(account, "bearer") && account == value {
			t.Fatalf("unsafe keyring account = %q", account)
		}
		if value != "endpoint-bearer-secret" {
			t.Fatal("keyring value did not preserve the endpoint bearer")
		}
	}
	loaded, err := store.LoadEndpointBearer(context.Background(), "runner-1", "endpoint-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded, secret) {
		t.Fatal("loaded endpoint bearer mismatch")
	}
	zeroBytes(loaded)
	if err := store.RemoveEndpointBearer(context.Background(), "runner-1", "endpoint-1"); err != nil {
		t.Fatal(err)
	}
	if len(backend.values) != 0 {
		t.Fatal("endpoint bearer remained after remove")
	}
}

func TestRunnerConfigStorePersistsOnlyNonSensitiveFields(t *testing.T) {
	root := filepath.Join(t.TempDir(), "local-runners")
	store := NewRunnerConfigStore(root)
	agentCardSHA256 := "sha256:" + strings.Repeat("a", 64)
	config := StoredRunnerConfig{
		RunnerID:        "runner-1",
		EndpointID:      "endpoint-1",
		LocalAgentID:    "agent-1",
		DisplayName:     "Local agent",
		AgentCardURL:    "http://127.0.0.1:8080/card",
		LoopbackBaseURL: "http://127.0.0.1:8080",
		OpenAPIBase:     "https://api.dingtalk.com",
		AgentCardSHA256: agentCardSHA256,
	}
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if *loaded != config {
		t.Fatalf("loaded config = %#v, want %#v", loaded, config)
	}
	if loaded.AgentCardSHA256 != agentCardSHA256 {
		t.Fatalf("loaded Agent Card digest = %q, want %q", loaded.AgentCardSHA256, agentCardSHA256)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("config entries = %d, error = %v", len(entries), err)
	}
	raw, err := os.ReadFile(filepath.Join(root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"endpointBearer", "connectionTicket", "Authorization", "endpoint-bearer-secret"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("config contains forbidden material %q", forbidden)
		}
	}
	info, err := os.Stat(filepath.Join(root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permission = %v, want 0600", info.Mode().Perm())
	}
	if err := store.Delete("runner-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("runner-1"); !errors.Is(err, ErrRunnerConfigNotFound) {
		t.Fatalf("load after delete error = %v", err)
	}
}

func TestRunnerConfigRejectsNonContractAgentCardSHA256(t *testing.T) {
	valid := StoredRunnerConfig{
		RunnerID:        "runner-1",
		EndpointID:      "endpoint-1",
		LocalAgentID:    "agent-1",
		DisplayName:     "Local agent",
		AgentCardURL:    "http://127.0.0.1:8080/card",
		LoopbackBaseURL: "http://127.0.0.1:8080",
		OpenAPIBase:     "https://api.dingtalk.com",
		AgentCardSHA256: "sha256:" + strings.Repeat("a", 64),
	}
	for name, digest := range map[string]string{
		"bare hex":     strings.Repeat("a", 64),
		"wrong prefix": "sha-256:" + strings.Repeat("a", 64),
		"wrong length": "sha256:" + strings.Repeat("a", 63),
		"uppercase":    "sha256:" + strings.Repeat("A", 64),
		"non hex":      "sha256:" + strings.Repeat("g", 64),
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.AgentCardSHA256 = digest
			if err := candidate.Validate(); !errors.Is(err, ErrRunnerConfigInvalid) {
				t.Fatalf("Validate() error = %v, want ErrRunnerConfigInvalid", err)
			}
		})
	}
}

type recordingSecretBackend struct {
	values map[string]string
}

func (b *recordingSecretBackend) Get(_, account string) (string, error) {
	return b.values[account], nil
}

func (b *recordingSecretBackend) Set(_, account, value string) error {
	b.values[account] = value
	return nil
}

func (b *recordingSecretBackend) Remove(_, account string) error {
	delete(b.values, account)
	return nil
}
