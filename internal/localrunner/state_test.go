package localrunner

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestSingleEndpointStateMachineNeedsMatchingHelloAck(t *testing.T) {
	machine := newTestMachine(t)
	data := validOpenConnectionData(t)
	if err := machine.BeginOpen(*data, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := machine.MarkHandshakeStarted(); err != nil {
		t.Fatal(err)
	}
	ack := validHelloAck()
	if err := machine.AcceptHelloAck(ack); err != nil {
		t.Fatal(err)
	}

	snapshot := machine.Snapshot()
	if snapshot.State != ConnectionStateReady || snapshot.ConnectionID != "connection-1" || snapshot.Identity != testIdentity() {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSingleEndpointStateMachineRejectsIdentityDrift(t *testing.T) {
	machine := newTestMachine(t)
	data := validOpenConnectionData(t)
	data.RunnerID = "other"
	if err := machine.BeginOpen(*data, time.Unix(100, 0)); !errors.Is(err, ErrTicketBindingMismatch) {
		t.Fatalf("open error = %v", err)
	}

	machine = newTestMachine(t)
	data = validOpenConnectionData(t)
	if err := machine.BeginOpen(*data, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := machine.MarkHandshakeStarted(); err != nil {
		t.Fatal(err)
	}
	ack := validHelloAck()
	ack.EndpointID = "other"
	if err := machine.AcceptHelloAck(ack); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("ack error = %v", err)
	}
}

func TestSingleEndpointStateMachineRejectsUnacceptedOrIncompleteAck(t *testing.T) {
	for _, attributes := range []map[string]json.RawMessage{
		{
			"accepted":            json.RawMessage(`false`),
			"connectionId":        json.RawMessage(`"connection-1"`),
			"heartbeatIntervalMs": json.RawMessage(`15000`),
			"maxFrameBytes":       json.RawMessage(`262144`),
		},
		{
			"accepted":            json.RawMessage(`true`),
			"heartbeatIntervalMs": json.RawMessage(`15000`),
			"maxFrameBytes":       json.RawMessage(`262144`),
		},
		{
			"accepted":     json.RawMessage(`true`),
			"connectionId": json.RawMessage(`"connection-1"`),
			"maxFrameBytes": json.RawMessage(`262144`),
		},
		{
			"accepted":            json.RawMessage(`true`),
			"connectionId":        json.RawMessage(`"connection-1"`),
			"heartbeatIntervalMs": json.RawMessage(`15000`),
			"maxFrameBytes":       json.RawMessage(`131072`),
		},
	} {
		machine := newTestMachine(t)
		data := validOpenConnectionData(t)
		if err := machine.BeginOpen(*data, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		if err := machine.MarkHandshakeStarted(); err != nil {
			t.Fatal(err)
		}
		ack := validHelloAck()
		ack.Attributes = attributes
		if err := machine.AcceptHelloAck(ack); !errors.Is(err, ErrSessionConflict) {
			t.Fatalf("ack error = %v", err)
		}
		if machine.Snapshot().State == ConnectionStateReady {
			t.Fatal("invalid ack made session ready")
		}
	}
}

func TestSingleEndpointStateMachineRejectsSecondActiveOpen(t *testing.T) {
	machine := newTestMachine(t)
	data := validOpenConnectionData(t)
	if err := machine.BeginOpen(*data, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	second := validOpenConnectionData(t)
	if err := machine.BeginOpen(*second, time.Unix(100, 0)); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("second open error = %v", err)
	}
}

func TestSingleEndpointStateMachineDisconnectDiscardsTicket(t *testing.T) {
	machine := newTestMachine(t)
	data := validOpenConnectionData(t)
	ticket := data.ConnectionTicket
	if err := machine.BeginOpen(*data, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := machine.MarkDisconnected(); err != nil {
		t.Fatal(err)
	}
	if snapshot := machine.Snapshot(); snapshot.State != ConnectionStateDisconnected || snapshot.ConnectionID != "" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := ticket.ApplyAuthorization(make(http.Header)); !errors.Is(err, ErrConnectionTicketAlreadyUsed) {
		t.Fatalf("discarded ticket error = %v", err)
	}
}

func TestSingleEndpointStateMachineStopIsTerminal(t *testing.T) {
	machine := newTestMachine(t)
	machine.Stop()
	if machine.Snapshot().State != ConnectionStateStopped {
		t.Fatal("machine did not stop")
	}
	data := validOpenConnectionData(t)
	if err := machine.BeginOpen(*data, time.Unix(100, 0)); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("open after stop error = %v", err)
	}
}

func newTestMachine(t *testing.T) *SingleEndpointConnectionStateMachine {
	t.Helper()
	config, err := NewRunnerEndpointConfig(testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	return NewSingleEndpointConnectionStateMachine(config)
}

func validHelloAck() TunnelFrame {
	frame := validFrame(FrameHelloAck)
	frame.Attributes = map[string]json.RawMessage{
		"accepted":            json.RawMessage(`true`),
		"connectionId":        json.RawMessage(`"connection-1"`),
		"heartbeatIntervalMs": json.RawMessage(`15000`),
		"maxFrameBytes":       json.RawMessage(`262144`),
	}
	return frame
}
