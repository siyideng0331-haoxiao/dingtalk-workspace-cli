package app

import "testing"

func TestDeapDevEndpointRequiresExplicitSecureOverride(t *testing.T) {
	t.Setenv("DINGTALK_DEAP_DEV_MCP_URL", "")

	if _, ok := directRuntimeEndpoint("deap-dev", ""); ok {
		t.Fatal("expected deap-dev endpoint to remain unresolved without an explicit override")
	}

	const override = "https://pre-mcp-gw.example.test/server/deap-dev"
	t.Setenv("DINGTALK_DEAP_DEV_MCP_URL", override)

	endpoint, ok := directRuntimeEndpoint("deap-dev", "")
	if !ok {
		t.Fatal("expected explicit deap-dev endpoint override to resolve")
	}
	if endpoint != override {
		t.Fatalf("expected endpoint %q, got %q", override, endpoint)
	}
}
