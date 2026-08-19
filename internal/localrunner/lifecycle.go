package localrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrLifecycleResponseMalformed   = errors.New("lifecycle_response_malformed")
	ErrEndpointBearerAlreadyStored = errors.New("endpoint_bearer_already_stored")
	ErrEndpointBearerStoreFailed   = errors.New("endpoint_bearer_store_failed")
)

type RunnerStatus string

const (
	RunnerStatusActive  RunnerStatus = "ACTIVE"
	RunnerStatusRevoked RunnerStatus = "REVOKED"
)

type CreateRunnerRequest struct {
	LocalAgentID string          `json:"localAgentId"`
	DisplayName  string          `json:"displayName"`
	AgentCard    json.RawMessage `json:"agentCard"`
}

func (r CreateRunnerRequest) Validate() error {
	if strings.TrimSpace(r.LocalAgentID) == "" || strings.TrimSpace(r.DisplayName) == "" || !isJSONObject(r.AgentCard) {
		return ErrLifecycleResponseMalformed
	}
	return nil
}

type UpdateAgentCardRequest struct {
	AgentCard json.RawMessage `json:"agentCard"`
}

func (r UpdateAgentCardRequest) Validate() error {
	if !isJSONObject(r.AgentCard) {
		return ErrLifecycleResponseMalformed
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return len(raw) != 0 && json.Unmarshal(raw, &object) == nil && object != nil
}

type EndpointBearerSink interface {
	StoreEndpointBearer(context.Context, string, string, []byte) error
}

type EndpointBearer struct {
	raw    []byte
	stored bool
}

func (b *EndpointBearer) UnmarshalJSON(data []byte) error {
	if b == nil {
		return ErrLifecycleResponseMalformed
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil || strings.TrimSpace(raw) == "" {
		return ErrLifecycleResponseMalformed
	}
	b.discard()
	b.raw = append(b.raw[:0], raw...)
	b.stored = false
	return nil
}

func (b EndpointBearer) MarshalJSON() ([]byte, error) {
	return json.Marshal("[REDACTED]")
}

func (b EndpointBearer) String() string {
	return "[REDACTED]"
}

func (b EndpointBearer) GoString() string {
	return "[REDACTED]"
}

func (b *EndpointBearer) Store(ctx context.Context, sink EndpointBearerSink, runnerID, endpointID string) error {
	if b == nil || b.stored {
		return ErrEndpointBearerAlreadyStored
	}
	if sink == nil || len(b.raw) == 0 || strings.TrimSpace(runnerID) == "" || strings.TrimSpace(endpointID) == "" {
		return ErrLifecycleResponseMalformed
	}
	secret := append([]byte(nil), b.raw...)
	b.discard()
	defer zeroBytes(secret)
	return sink.StoreEndpointBearer(ctx, runnerID, endpointID, secret)
}

func (b *EndpointBearer) discard() {
	if b == nil {
		return
	}
	zeroBytes(b.raw)
	b.raw = nil
	b.stored = true
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

type CreateRunnerSuccess struct {
	Success bool              `json:"success"`
	Data    *CreateRunnerData `json:"data"`
}

type CreateRunnerData struct {
	RunnerID       string          `json:"runnerId"`
	EndpointID     string          `json:"endpointId"`
	AgentCardURL   string          `json:"agentCardUrl"`
	EndpointBearer *EndpointBearer `json:"endpointBearer"`
	Status         RunnerStatus    `json:"status"`
}

func DecodeCreateRunnerSuccess(data []byte) (*CreateRunnerSuccess, error) {
	var response CreateRunnerSuccess
	if err := decodeFrozenSuccess(data, []string{"runnerId", "endpointId", "agentCardUrl", "endpointBearer", "status"}, &response); err != nil {
		return nil, err
	}
	if response.Data == nil || strings.TrimSpace(response.Data.RunnerID) == "" || strings.TrimSpace(response.Data.EndpointID) == "" || strings.TrimSpace(response.Data.AgentCardURL) == "" || response.Data.EndpointBearer == nil || len(response.Data.EndpointBearer.raw) == 0 || response.Data.Status != RunnerStatusActive {
		return nil, ErrLifecycleResponseMalformed
	}
	return &response, nil
}

type RunnerStatusSuccess struct {
	Success bool              `json:"success"`
	Data    *RunnerStatusData `json:"data"`
}

type RunnerStatusData struct {
	RunnerID                       string       `json:"runnerId"`
	EndpointID                     string       `json:"endpointId"`
	LocalAgentID                   string       `json:"localAgentId"`
	DisplayName                    string       `json:"displayName"`
	Status                         RunnerStatus `json:"status"`
	AgentCardURL                   string       `json:"agentCardUrl"`
	AgentCardSHA256                string       `json:"agentCardSha256"`
	Connected                      bool         `json:"connected"`
	LastHeartbeatAtEpochSecond     *int64       `json:"lastHeartbeatAtEpochSecond"`
}

func DecodeRunnerStatusSuccess(data []byte) (*RunnerStatusSuccess, error) {
	var response RunnerStatusSuccess
	keys := []string{"runnerId", "endpointId", "localAgentId", "displayName", "status", "agentCardUrl", "agentCardSha256", "connected", "lastHeartbeatAtEpochSecond"}
	if err := decodeFrozenSuccess(data, keys, &response); err != nil {
		return nil, err
	}
	if response.Data == nil || !validRunnerStatusData(*response.Data) {
		return nil, ErrLifecycleResponseMalformed
	}
	return &response, nil
}

func validRunnerStatusData(data RunnerStatusData) bool {
	if strings.TrimSpace(data.RunnerID) == "" || strings.TrimSpace(data.EndpointID) == "" || strings.TrimSpace(data.LocalAgentID) == "" || strings.TrimSpace(data.DisplayName) == "" || strings.TrimSpace(data.AgentCardURL) == "" || strings.TrimSpace(data.AgentCardSHA256) == "" {
		return false
	}
	return data.Status == RunnerStatusActive || data.Status == RunnerStatusRevoked
}

type RevokeRunnerSuccess struct {
	Success bool              `json:"success"`
	Data    *RevokeRunnerData `json:"data"`
}

type RevokeRunnerData struct {
	RunnerID   string       `json:"runnerId"`
	EndpointID string       `json:"endpointId"`
	Status     RunnerStatus `json:"status"`
}

func DecodeRevokeRunnerSuccess(data []byte) (*RevokeRunnerSuccess, error) {
	var response RevokeRunnerSuccess
	if err := decodeFrozenSuccess(data, []string{"runnerId", "endpointId", "status"}, &response); err != nil {
		return nil, err
	}
	if response.Data == nil || strings.TrimSpace(response.Data.RunnerID) == "" || strings.TrimSpace(response.Data.EndpointID) == "" || response.Data.Status != RunnerStatusRevoked {
		return nil, ErrLifecycleResponseMalformed
	}
	return &response, nil
}

type ConnectionStatusSuccess struct {
	Success bool                  `json:"success"`
	Data    *ConnectionStatusData `json:"data"`
}

type ConnectionStatusData struct {
	RunnerID                       string  `json:"runnerId"`
	EndpointID                     string  `json:"endpointId"`
	Connected                      bool    `json:"connected"`
	ConnectionID                   *string `json:"connectionId"`
	ConnectedAtEpochSecond         *int64  `json:"connectedAtEpochSecond"`
	LastHeartbeatAtEpochSecond     *int64  `json:"lastHeartbeatAtEpochSecond"`
}

func DecodeConnectionStatusSuccess(data []byte) (*ConnectionStatusSuccess, error) {
	var response ConnectionStatusSuccess
	keys := []string{"runnerId", "endpointId", "connected", "connectionId", "connectedAtEpochSecond", "lastHeartbeatAtEpochSecond"}
	if err := decodeFrozenSuccess(data, keys, &response); err != nil {
		return nil, err
	}
	if response.Data == nil || strings.TrimSpace(response.Data.RunnerID) == "" || strings.TrimSpace(response.Data.EndpointID) == "" {
		return nil, ErrLifecycleResponseMalformed
	}
	if response.Data.Connected {
		if response.Data.ConnectionID == nil || strings.TrimSpace(*response.Data.ConnectionID) == "" || response.Data.ConnectedAtEpochSecond == nil {
			return nil, ErrLifecycleResponseMalformed
		}
	} else if response.Data.ConnectionID != nil || response.Data.ConnectedAtEpochSecond != nil {
		return nil, ErrLifecycleResponseMalformed
	}
	return &response, nil
}

type DisconnectSuccess struct {
	Success bool            `json:"success"`
	Data    *DisconnectData `json:"data"`
}

type DisconnectData struct {
	RunnerID    string `json:"runnerId"`
	EndpointID  string `json:"endpointId"`
	Disconnected bool  `json:"disconnected"`
}

func DecodeDisconnectSuccess(data []byte) (*DisconnectSuccess, error) {
	var response DisconnectSuccess
	if err := decodeFrozenSuccess(data, []string{"runnerId", "endpointId", "disconnected"}, &response); err != nil {
		return nil, err
	}
	if response.Data == nil || strings.TrimSpace(response.Data.RunnerID) == "" || strings.TrimSpace(response.Data.EndpointID) == "" {
		return nil, ErrLifecycleResponseMalformed
	}
	return &response, nil
}

func decodeFrozenSuccess(data []byte, dataKeys []string, target any) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || !hasExactKeys(envelope, []string{"success", "data"}) {
		return ErrLifecycleResponseMalformed
	}
	var success bool
	if err := json.Unmarshal(envelope["success"], &success); err != nil || !success {
		return ErrLifecycleResponseMalformed
	}
	var dataObject map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &dataObject); err != nil || !hasExactKeys(dataObject, dataKeys) {
		return ErrLifecycleResponseMalformed
	}
	if err := json.Unmarshal(data, target); err != nil {
		return ErrLifecycleResponseMalformed
	}
	return nil
}

func hasExactKeys(object map[string]json.RawMessage, keys []string) bool {
	if object == nil || len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

type ControlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ControlFailure struct {
	StatusCode int           `json:"-"`
	Detail     *ControlError `json:"error"`
}

func (f ControlFailure) String() string {
	code := "unknown"
	if f.Detail != nil {
		code = f.Detail.Code
	}
	return fmt.Sprintf("local runner control failed: status=%d code=%s", f.StatusCode, code)
}

func (f ControlFailure) Error() string {
	return f.String()
}
