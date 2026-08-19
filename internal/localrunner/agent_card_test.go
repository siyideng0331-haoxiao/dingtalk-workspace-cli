package localrunner

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRewriteAgentCardValidatesLoopbackAndRemovesLocalURLs(t *testing.T) {
	card := json.RawMessage(`{
		"name":"Local agent",
		"description":"test",
		"protocolVersion":"1.0",
		"url":"http://127.0.0.1:8080/rpc",
		"capabilities":{"streaming":true},
		"skills":[{"id":"answer","name":"Answer"}],
		"authentication":{"schemes":["source-secret"]},
		"securitySchemes":{"source":{"apiKeySecurityScheme":{"name":"source-secret"}}},
		"security":[{"source":[]}],
		"supportedInterfaces":[{"url":"http://localhost:8080/rpc","transport":"JSONRPC"}],
		"additionalInterfaces":[{"url":"http://[::1]:8080/rpc","transport":"HTTP+JSON"}]
	}`)
	snapshot, err := RewriteAgentCard(card, "endpoint-1", "https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "https://api.example.test/v1/a2a/local-runners/endpoint-1/rpc"
	if bytes.Contains(snapshot.JSON, []byte("127.0.0.1")) || bytes.Contains(snapshot.JSON, []byte("localhost")) || bytes.Contains(snapshot.JSON, []byte("::1")) {
		t.Fatal("public card retained a local URL")
	}
	if bytes.Count(snapshot.JSON, []byte(wantURL)) != 3 {
		t.Fatalf("rewritten card = %s", snapshot.JSON)
	}
	var published map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.JSON, &published); err != nil {
		t.Fatal(err)
	}
	if published["authentication"] != nil || string(published["securitySchemes"]) != `{"localRunnerBearer":{"httpAuthSecurityScheme":{"scheme":"Bearer"}}}` || string(published["security"]) != `[{"localRunnerBearer":[]}]` {
		t.Fatalf("published bearer declaration mismatch")
	}
	if bytes.Contains(snapshot.JSON, []byte("source-secret")) || bytes.Contains(snapshot.JSON, []byte("endpoint-secret")) {
		t.Fatal("public card retained source or endpoint credentials")
	}
	if len(snapshot.SHA256) != 64 || snapshot.ETag != `"sha256:`+snapshot.SHA256+`"` {
		t.Fatalf("hash=%q etag=%q", snapshot.SHA256, snapshot.ETag)
	}
	again, err := RewriteAgentCard(card, "endpoint-1", "https://api.example.test")
	if err != nil || !bytes.Equal(snapshot.JSON, again.JSON) || snapshot.SHA256 != again.SHA256 {
		t.Fatal("agent card snapshot was not deterministic")
	}
}

func TestRewriteAgentCardRejectsInvalidShapeOrNonLoopback(t *testing.T) {
	for name, card := range map[string]string{
		"protocol": `{"name":"n","protocolVersion":"2.0","url":"http://localhost/rpc","capabilities":{},"skills":[]}`,
		"remote": `{"name":"n","protocolVersion":"1.0","url":"https://example.com/rpc","capabilities":{},"skills":[]}`,
		"userinfo": `{"name":"n","protocolVersion":"1.0","url":"http://user@localhost/rpc","capabilities":{},"skills":[]}`,
		"missing callable": `{"name":"n","protocolVersion":"1.0","capabilities":{},"skills":[]}`,
		"skill scalar": `{"name":"n","protocolVersion":"0.3.0","url":"http://localhost/rpc","capabilities":{},"skills":["bad"]}`,
		"residual local documentation": `{"name":"n","protocolVersion":"1.0","url":"http://localhost/rpc","documentationUrl":"http://127.0.0.1:8080/docs","capabilities":{},"skills":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RewriteAgentCard(json.RawMessage(card), "endpoint-1", "https://api.example.test")
			if !errors.Is(err, ErrInvalidAgentCard) || strings.Contains(err.Error(), "example.com") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLocalAgentCardTargetUsesCallableURLRatherThanCardDocumentOrigin(t *testing.T) {
	card := json.RawMessage(`{"name":"n","protocolVersion":"1.0","url":"http://127.0.0.1:9000/rpc","capabilities":{},"skills":[]}`)
	target, err := LocalAgentCardTarget(card)
	if err != nil {
		t.Fatal(err)
	}
	if target != "http://127.0.0.1:9000/rpc" {
		t.Fatalf("target = %q", target)
	}
}
