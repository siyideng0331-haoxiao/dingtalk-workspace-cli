package localrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const DefaultMaxFrameBytes = 262144

var (
	ErrFrameMalformed          = errors.New("frame_malformed")
	ErrFrameUnsupportedVersion = errors.New("frame_unsupported_version")
	ErrFrameTooLarge           = errors.New("frame_too_large")
	ErrFrameTypeMismatch       = errors.New("frame_type_mismatch")
	ErrSessionConflict         = errors.New("session_conflict")
)

type TunnelFrameType string

const (
	FrameHello          TunnelFrameType = "hello"
	FrameHelloAck       TunnelFrameType = "hello_ack"
	FrameRequestStart   TunnelFrameType = "request_start"
	FrameRequestChunk   TunnelFrameType = "request_chunk"
	FrameRequestEnd     TunnelFrameType = "request_end"
	FrameResponseStart  TunnelFrameType = "response_start"
	FrameResponseChunk  TunnelFrameType = "response_chunk"
	FrameResponseEnd    TunnelFrameType = "response_end"
	FrameCancel         TunnelFrameType = "cancel"
	FrameError          TunnelFrameType = "error"
	FrameHeartbeat      TunnelFrameType = "heartbeat"
	FrameHeartbeatAck   TunnelFrameType = "heartbeat_ack"
	FrameEndpointRevoke TunnelFrameType = "endpoint_revoke"
)

var allTunnelFrameTypes = []TunnelFrameType{
	FrameHello,
	FrameHelloAck,
	FrameRequestStart,
	FrameRequestChunk,
	FrameRequestEnd,
	FrameResponseStart,
	FrameResponseChunk,
	FrameResponseEnd,
	FrameCancel,
	FrameError,
	FrameHeartbeat,
	FrameHeartbeatAck,
	FrameEndpointRevoke,
}

func AllTunnelFrameTypes() []TunnelFrameType {
	return append([]TunnelFrameType(nil), allTunnelFrameTypes...)
}

type TunnelFrame struct {
	Version    int                        `json:"v"`
	Type       TunnelFrameType            `json:"type"`
	RunnerID   string                     `json:"runnerId"`
	EndpointID string                     `json:"endpointId"`
	RequestID  string                     `json:"requestId,omitempty"`
	Sequence   int64                      `json:"seq"`
	Timestamp  int64                      `json:"timestamp"`
	Attributes map[string]json.RawMessage `json:"-"`
	Payload    []byte                     `json:"-"`
}

type tunnelCommonHeader struct {
	Version    int             `json:"v"`
	Type       TunnelFrameType `json:"type"`
	RunnerID   string          `json:"runnerId"`
	EndpointID string          `json:"endpointId"`
	RequestID  string          `json:"requestId,omitempty"`
	Sequence   int64           `json:"seq"`
	Timestamp  int64           `json:"timestamp"`
}

func (f TunnelFrame) commonHeader() tunnelCommonHeader {
	return tunnelCommonHeader{
		Version:    f.Version,
		Type:       f.Type,
		RunnerID:   f.RunnerID,
		EndpointID: f.EndpointID,
		RequestID:  f.RequestID,
		Sequence:   f.Sequence,
		Timestamp:  f.Timestamp,
	}
}

var reservedFrameKeys = map[string]struct{}{
	"v":          {},
	"type":       {},
	"runnerId":   {},
	"endpointId": {},
	"requestId":  {},
	"seq":        {},
	"timestamp":  {},
}

func (f TunnelFrame) Validate() error {
	if f.Version != 1 {
		return ErrFrameUnsupportedVersion
	}
	if !knownTunnelFrameType(f.Type) {
		return ErrFrameMalformed
	}
	if strings.TrimSpace(f.RunnerID) == "" || strings.TrimSpace(f.EndpointID) == "" {
		return ErrInvalidIdentity
	}
	if f.Sequence < 0 {
		return ErrFrameMalformed
	}
	if frameRequiresRequestID(f.Type) && strings.TrimSpace(f.RequestID) == "" {
		return ErrFrameMalformed
	}
	if frameForbidsRequestID(f.Type) && f.RequestID != "" {
		return ErrFrameMalformed
	}
	for key := range f.Attributes {
		if _, reserved := reservedFrameKeys[key]; reserved {
			return ErrFrameMalformed
		}
	}
	if frameIsChunk(f.Type) {
		if len(f.Attributes) != 0 {
			return ErrFrameMalformed
		}
	} else if len(f.Payload) != 0 {
		return ErrFrameTypeMismatch
	}
	return nil
}

func knownTunnelFrameType(typ TunnelFrameType) bool {
	for _, candidate := range allTunnelFrameTypes {
		if candidate == typ {
			return true
		}
	}
	return false
}

func frameRequiresRequestID(typ TunnelFrameType) bool {
	switch typ {
	case FrameRequestStart,
		FrameRequestChunk,
		FrameRequestEnd,
		FrameResponseStart,
		FrameResponseChunk,
		FrameResponseEnd,
		FrameCancel:
		return true
	default:
		return false
	}
}

func frameForbidsRequestID(typ TunnelFrameType) bool {
	switch typ {
	case FrameHello, FrameHelloAck, FrameHeartbeat, FrameHeartbeatAck, FrameEndpointRevoke:
		return true
	default:
		return false
	}
}

func frameIsChunk(typ TunnelFrameType) bool {
	return typ == FrameRequestChunk || typ == FrameResponseChunk
}

func (f TunnelFrame) String() string {
	return fmt.Sprintf(
		"tunnel_frame type=%s runner_id=%s endpoint_id=%s request_id=%s seq=%d payload_bytes=%d",
		f.Type,
		f.RunnerID,
		f.EndpointID,
		f.RequestID,
		f.Sequence,
		len(f.Payload),
	)
}

func (f TunnelFrame) GoString() string {
	return f.String()
}
