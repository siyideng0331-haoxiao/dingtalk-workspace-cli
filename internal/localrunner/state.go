package localrunner

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

type ConnectionState string

const (
	ConnectionStateIdle         ConnectionState = "idle"
	ConnectionStateOpening      ConnectionState = "opening"
	ConnectionStateAwaitingAck  ConnectionState = "awaiting_ack"
	ConnectionStateReady        ConnectionState = "ready"
	ConnectionStateDisconnected ConnectionState = "disconnected"
	ConnectionStateStopped      ConnectionState = "stopped"
)

type ConnectionStateSnapshot struct {
	Identity     RunnerEndpointIdentity
	State        ConnectionState
	ConnectionID string
}

type EndpointConnectionState interface {
	BeginOpen(OpenConnectionData, time.Time) error
	MarkHandshakeStarted() error
	AcceptHelloAck(TunnelFrame) error
	MarkDisconnected() error
	Stop()
	Snapshot() ConnectionStateSnapshot
}

type SingleEndpointConnectionStateMachine struct {
	mu           sync.RWMutex
	identity     RunnerEndpointIdentity
	state        ConnectionState
	connectionID string
	pendingTicket *ConnectionTicket
}

func NewSingleEndpointConnectionStateMachine(config RunnerEndpointConfig) *SingleEndpointConnectionStateMachine {
	return &SingleEndpointConnectionStateMachine{
		identity: config.Identity(),
		state:    ConnectionStateIdle,
	}
}

func (m *SingleEndpointConnectionStateMachine) BeginOpen(data OpenConnectionData, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != ConnectionStateIdle && m.state != ConnectionStateDisconnected {
		return ErrSessionConflict
	}
	if err := data.ValidateFor(m.identity, now); err != nil {
		return err
	}
	m.connectionID = ""
	m.pendingTicket = data.ConnectionTicket
	m.state = ConnectionStateOpening
	return nil
}

func (m *SingleEndpointConnectionStateMachine) MarkHandshakeStarted() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != ConnectionStateOpening {
		return ErrSessionConflict
	}
	m.state = ConnectionStateAwaitingAck
	return nil
}

func (m *SingleEndpointConnectionStateMachine) AcceptHelloAck(frame TunnelFrame) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != ConnectionStateAwaitingAck {
		return ErrSessionConflict
	}
	if err := frame.Validate(); err != nil {
		return ErrSessionConflict
	}
	if frame.Type != FrameHelloAck || frame.RunnerID != m.identity.RunnerID || frame.EndpointID != m.identity.EndpointID {
		return ErrSessionConflict
	}
	accepted, connectionID, heartbeatInterval, maxFrameBytes, ok := decodeHelloAckAttributes(frame.Attributes)
	if !ok || !accepted || strings.TrimSpace(connectionID) == "" || heartbeatInterval != 15000 || maxFrameBytes != DefaultMaxFrameBytes {
		return ErrSessionConflict
	}
	if m.pendingTicket != nil {
		m.pendingTicket.discard()
	}
	m.pendingTicket = nil
	m.connectionID = connectionID
	m.state = ConnectionStateReady
	return nil
}

func decodeHelloAckAttributes(attributes map[string]json.RawMessage) (bool, string, int64, int64, bool) {
	if len(attributes) != 4 {
		return false, "", 0, 0, false
	}
	var accepted bool
	var connectionID string
	var heartbeatInterval int64
	var maxFrameBytes int64
	if err := json.Unmarshal(attributes["accepted"], &accepted); err != nil {
		return false, "", 0, 0, false
	}
	if err := json.Unmarshal(attributes["connectionId"], &connectionID); err != nil {
		return false, "", 0, 0, false
	}
	if err := json.Unmarshal(attributes["heartbeatIntervalMs"], &heartbeatInterval); err != nil {
		return false, "", 0, 0, false
	}
	if err := json.Unmarshal(attributes["maxFrameBytes"], &maxFrameBytes); err != nil {
		return false, "", 0, 0, false
	}
	return accepted, connectionID, heartbeatInterval, maxFrameBytes, true
}

func (m *SingleEndpointConnectionStateMachine) MarkDisconnected() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == ConnectionStateStopped {
		return ErrSessionConflict
	}
	if m.pendingTicket != nil {
		m.pendingTicket.discard()
	}
	m.pendingTicket = nil
	m.connectionID = ""
	m.state = ConnectionStateDisconnected
	return nil
}

func (m *SingleEndpointConnectionStateMachine) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pendingTicket != nil {
		m.pendingTicket.discard()
	}
	m.pendingTicket = nil
	m.connectionID = ""
	m.state = ConnectionStateStopped
}

func (m *SingleEndpointConnectionStateMachine) Snapshot() ConnectionStateSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return ConnectionStateSnapshot{
		Identity:     m.identity,
		State:        m.state,
		ConnectionID: m.connectionID,
	}
}
