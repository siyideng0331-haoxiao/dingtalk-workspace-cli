package localrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTunnelFrameTypeStableWireValues(t *testing.T) {
	want := []TunnelFrameType{
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
	got := AllTunnelFrameTypes()
	if len(got) != len(want) {
		t.Fatalf("type count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("type[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTunnelFrameRequiresIdentityOnEveryType(t *testing.T) {
	for _, typ := range AllTunnelFrameTypes() {
		frame := validFrame(typ)
		frame.RunnerID = ""
		if err := frame.Validate(); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("type %s error = %v, want invalid identity", typ, err)
		}
		frame = validFrame(typ)
		frame.EndpointID = ""
		if err := frame.Validate(); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("type %s accepted missing endpoint", typ)
		}
	}
}

func TestTunnelFrameRequestIDRules(t *testing.T) {
	required := []TunnelFrameType{
		FrameRequestStart,
		FrameRequestChunk,
		FrameRequestEnd,
		FrameResponseStart,
		FrameResponseChunk,
		FrameResponseEnd,
		FrameCancel,
	}
	for _, typ := range required {
		frame := validFrame(typ)
		frame.RequestID = ""
		if err := frame.Validate(); !errors.Is(err, ErrFrameMalformed) {
			t.Fatalf("type %s accepted missing requestId", typ)
		}
	}

	forbidden := []TunnelFrameType{
		FrameHello,
		FrameHelloAck,
		FrameHeartbeat,
		FrameHeartbeatAck,
		FrameEndpointRevoke,
	}
	for _, typ := range forbidden {
		frame := validFrame(typ)
		frame.RequestID = "request-1"
		if err := frame.Validate(); !errors.Is(err, ErrFrameMalformed) {
			t.Fatalf("type %s accepted requestId", typ)
		}
	}

	for _, requestID := range []string{"", "request-1"} {
		frame := validFrame(FrameError)
		frame.RequestID = requestID
		if err := frame.Validate(); err != nil {
			t.Fatalf("error frame requestId %q: %v", requestID, err)
		}
	}
}

func TestTunnelFrameRejectsInvalidVersionSequenceAndReservedAttributes(t *testing.T) {
	frame := validFrame(FrameHeartbeat)
	frame.Version = 2
	if err := frame.Validate(); !errors.Is(err, ErrFrameUnsupportedVersion) {
		t.Fatalf("version error = %v", err)
	}

	frame = validFrame(FrameHeartbeat)
	frame.Sequence = -1
	if err := frame.Validate(); !errors.Is(err, ErrFrameMalformed) {
		t.Fatalf("sequence error = %v", err)
	}

	frame = validFrame(FrameRequestStart)
	frame.Attributes = map[string]json.RawMessage{"runnerId": json.RawMessage(`"other"`)}
	if err := frame.Validate(); !errors.Is(err, ErrFrameMalformed) {
		t.Fatalf("reserved attribute error = %v", err)
	}
}

func TestTunnelFrameSafeSummaryOmitsAttributesAndPayload(t *testing.T) {
	frame := validFrame(FrameResponseChunk)
	frame.Attributes = map[string]json.RawMessage{"secret": json.RawMessage(`"attribute-value"`)}
	frame.Payload = []byte("opaque-payload-value")
	summary := fmt.Sprintf("%v %#v", frame, frame)
	if strings.Contains(summary, "attribute-value") || strings.Contains(summary, "opaque-payload-value") {
		t.Fatal("frame summary exposed attributes or payload")
	}
	if !strings.Contains(summary, "payload_bytes=20") {
		t.Fatalf("frame summary omitted payload length: %q", summary)
	}
}

func validFrame(typ TunnelFrameType) TunnelFrame {
	frame := TunnelFrame{
		Version:    1,
		Type:       typ,
		RunnerID:   "runner-1",
		EndpointID: "endpoint-1",
		Sequence:   0,
		Timestamp:  1787068800000,
	}
	if frameRequiresRequestID(typ) {
		frame.RequestID = "request-1"
	}
	return frame
}
