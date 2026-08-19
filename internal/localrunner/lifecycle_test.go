package localrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestCreateRunnerRequestMatchesFrozenShape(t *testing.T) {
	request := CreateRunnerRequest{
		LocalAgentID: "agent-1",
		DisplayName:  "Local agent",
		AgentCard:    json.RawMessage(`{"protocolVersion":"1.0"}`),
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != 3 || object["localAgentId"] == nil || object["displayName"] == nil || object["agentCard"] == nil {
		t.Fatalf("request shape = %s", encoded)
	}
}

func TestCreateRunnerSuccessConsumesEndpointBearerIntoKeyringOnly(t *testing.T) {
	response, err := DecodeCreateRunnerSuccess([]byte(`{
		"success":true,
		"data":{
			"runnerId":"runner-1",
			"endpointId":"endpoint-1",
			"agentCardUrl":"https://api.example.test/v1/a2a/local-runners/endpoint-1/card",
			"endpointBearer":"endpoint-secret",
			"status":"ACTIVE"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if stringsContainSecret(fmt.Sprint(response.Data.EndpointBearer), fmt.Sprintf("%#v", response.Data.EndpointBearer)) {
		t.Fatal("formatted endpoint bearer was exposed")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("endpoint-secret")) {
		t.Fatal("JSON exposed endpoint bearer")
	}

	sink := &recordingEndpointBearerSink{}
	if err := response.Data.EndpointBearer.Store(context.Background(), sink, response.Data.RunnerID, response.Data.EndpointID); err != nil {
		t.Fatal(err)
	}
	if sink.runnerID != "runner-1" || sink.endpointID != "endpoint-1" || string(sink.secret) != "endpoint-secret" {
		t.Fatalf("sink = %#v", sink)
	}
	if err := response.Data.EndpointBearer.Store(context.Background(), sink, "runner-1", "endpoint-1"); !errors.Is(err, ErrEndpointBearerAlreadyStored) {
		t.Fatalf("second store error = %v", err)
	}
}

func TestRunnerLifecycleSuccessDTOsMatchFrozenFields(t *testing.T) {
	runner, err := DecodeRunnerStatusSuccess([]byte(`{
		"success":true,
		"data":{
			"runnerId":"runner-1",
			"endpointId":"endpoint-1",
			"localAgentId":"agent-1",
			"displayName":"Local agent",
			"status":"ACTIVE",
			"agentCardUrl":"https://api.example.test/card",
			"agentCardSha256":"abc123",
			"connected":true,
			"lastHeartbeatAtEpochSecond":123
		}
	}`))
	if err != nil || runner.Data.LastHeartbeatAtEpochSecond == nil || *runner.Data.LastHeartbeatAtEpochSecond != 123 {
		t.Fatalf("runner response = %#v, error = %v", runner, err)
	}

	connection, err := DecodeConnectionStatusSuccess([]byte(`{
		"success":true,
		"data":{
			"runnerId":"runner-1",
			"endpointId":"endpoint-1",
			"connected":false,
			"connectionId":null,
			"connectedAtEpochSecond":null,
			"lastHeartbeatAtEpochSecond":null
		}
	}`))
	if err != nil || connection.Data.ConnectionID != nil || connection.Data.ConnectedAtEpochSecond != nil {
		t.Fatalf("connection response = %#v, error = %v", connection, err)
	}

	revoked, err := DecodeRevokeRunnerSuccess([]byte(`{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","status":"REVOKED"}}`))
	if err != nil || revoked.Data.Status != RunnerStatusRevoked {
		t.Fatalf("revoke response = %#v, error = %v", revoked, err)
	}

	disconnected, err := DecodeDisconnectSuccess([]byte(`{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","disconnected":true}}`))
	if err != nil || !disconnected.Data.Disconnected {
		t.Fatalf("disconnect response = %#v, error = %v", disconnected, err)
	}
}

func TestLifecycleDTORejectsMissingOrUnknownFields(t *testing.T) {
	for name, decode := range map[string]func() error{
		"create unknown": func() error {
			_, err := DecodeCreateRunnerSuccess([]byte(`{"success":true,"data":{"runnerId":"r","endpointId":"e","agentCardUrl":"https://example.test/card","endpointBearer":"secret","status":"ACTIVE","extra":true}}`))
			return err
		},
		"runner missing nullable": func() error {
			_, err := DecodeRunnerStatusSuccess([]byte(`{"success":true,"data":{"runnerId":"r","endpointId":"e","localAgentId":"a","displayName":"n","status":"ACTIVE","agentCardUrl":"https://example.test/card","agentCardSha256":"sha","connected":false}}`))
			return err
		},
		"connection integer only": func() error {
			_, err := DecodeConnectionStatusSuccess([]byte(`{"success":true,"data":{"runnerId":"r","endpointId":"e","connected":true,"connectionId":"c","connectedAtEpochSecond":1.5,"lastHeartbeatAtEpochSecond":2}}`))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := decode(); !errors.Is(err, ErrLifecycleResponseMalformed) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type recordingEndpointBearerSink struct {
	runnerID   string
	endpointID string
	secret     []byte
}

func (s *recordingEndpointBearerSink) StoreEndpointBearer(_ context.Context, runnerID, endpointID string, secret []byte) error {
	s.runnerID = runnerID
	s.endpointID = endpointID
	s.secret = append(s.secret[:0], secret...)
	return nil
}

func stringsContainSecret(values ...string) bool {
	for _, value := range values {
		if bytes.Contains([]byte(value), []byte("endpoint-secret")) {
			return true
		}
	}
	return false
}
