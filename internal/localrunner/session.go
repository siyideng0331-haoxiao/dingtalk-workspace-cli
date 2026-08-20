package localrunner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrTunnelDisconnected = errors.New("local_runner_tunnel_disconnected")
	ErrTunnelProtocol     = errors.New("local_runner_tunnel_protocol_error")
	ErrEndpointRevoked    = errors.New("local_runner_endpoint_revoked")
)

type HelloConfig struct {
	AgentCardSHA256 string
	MaxConcurrent   int
	Streaming       bool
}

func (c HelloConfig) Validate() error {
	if strings.TrimSpace(c.AgentCardSHA256) == "" || c.MaxConcurrent <= 0 {
		return ErrTunnelProtocol
	}
	return nil
}

type TunnelFrameWriter interface {
	WriteFrame(context.Context, TunnelFrame) error
}

type TunnelFrameHandler interface {
	HandleFrame(context.Context, TunnelFrame, TunnelFrameWriter) error
	FailAll(error)
}

type TunnelSession struct {
	identity RunnerEndpointIdentity
	state    EndpointConnectionState
	dialer   TunnelSocketDialer
	codec    *TunnelCodec
	now      func() time.Time
}

func NewTunnelSession(identity RunnerEndpointIdentity, state EndpointConnectionState, dialer TunnelSocketDialer, codec *TunnelCodec) *TunnelSession {
	if codec == nil {
		codec = NewTunnelCodec(DefaultMaxFrameBytes)
	}
	return &TunnelSession{
		identity: identity,
		state:    state,
		dialer:   dialer,
		codec:    codec,
		now:      time.Now,
	}
}

func (s *TunnelSession) RunAttempt(ctx context.Context, data OpenConnectionData, hello HelloConfig, handler TunnelFrameHandler) (result error) {
	if s == nil || s.state == nil || s.dialer == nil || s.codec == nil || hello.Validate() != nil {
		return ErrTunnelProtocol
	}
	now := s.now()
	if err := s.state.BeginOpen(data, now); err != nil {
		return err
	}
	socket, err := s.dialer.Dial(ctx, data, s.identity, now)
	if err != nil {
		_ = s.state.MarkDisconnected()
		if handler != nil {
			handler.FailAll(ErrTunnelDisconnected)
		}
		return err
	}
	terminal := false
	defer func() {
		_ = socket.Close()
		if !terminal {
			_ = s.state.MarkDisconnected()
		}
		if handler != nil {
			handler.FailAll(ErrTunnelDisconnected)
		}
	}()
	stopClose := make(chan struct{})
	defer close(stopClose)
	go func() {
		select {
		case <-ctx.Done():
			_ = socket.Close()
		case <-stopClose:
		}
	}()

	if err := s.state.MarkHandshakeStarted(); err != nil {
		return err
	}
	writer := &sessionFrameWriter{
		identity:            s.identity,
		socket:              socket,
		codec:               s.codec,
		now:                 s.now,
		connectionSequence:  -1,
		responseSequences:   make(map[string]int64),
	}
	helloFrame := TunnelFrame{
		Type: FrameHello,
		Attributes: map[string]json.RawMessage{
			"agentCardSha256": json.RawMessage(strconvQuote(hello.AgentCardSHA256)),
			"maxConcurrent":   json.RawMessage(intJSON(hello.MaxConcurrent)),
			"streaming":       json.RawMessage(boolJSON(hello.Streaming)),
		},
	}
	if err := writer.writeInternal(helloFrame, true); err != nil {
		return ErrTunnelDisconnected
	}
	ack, err := s.readFrame(socket)
	if err != nil || ack.Sequence != 0 || ack.Type != FrameHelloAck || !matchingFrameIdentity(ack, s.identity) {
		return ErrTunnelProtocol
	}
	if err := s.state.AcceptHelloAck(ack); err != nil {
		return ErrTunnelProtocol
	}
	_, _, heartbeatIntervalMs, _, ok := decodeHelloAckAttributes(ack.Attributes)
	if !ok || heartbeatIntervalMs <= 0 {
		return ErrTunnelProtocol
	}
	heartbeatTicker := time.NewTicker(time.Duration(heartbeatIntervalMs) * time.Millisecond)
	heartbeatStop := make(chan struct{})
	var heartbeatWait sync.WaitGroup
	heartbeatWait.Add(1)
	go func() {
		defer heartbeatWait.Done()
		for {
			select {
			case <-heartbeatTicker.C:
				if err := writer.writeInternal(TunnelFrame{Type: FrameHeartbeat}, false); err != nil {
					_ = socket.Close()
					return
				}
			case <-ctx.Done():
				return
			case <-heartbeatStop:
				return
			}
		}
	}()
	defer func() {
		heartbeatTicker.Stop()
		close(heartbeatStop)
		heartbeatWait.Wait()
	}()
	inboundConnectionSequence := int64(0)
	inboundRequests := newRequestSequenceTracker()
	writer.onRequestComplete = inboundRequests.complete

	for {
		frame, err := s.readFrame(socket)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrTunnelDisconnected
		}
		if !matchingFrameIdentity(frame, s.identity) || !allowedInboundFrame(frame.Type) {
			return ErrTunnelProtocol
		}
		if requestScopedInboundFrame(frame) {
			if !inboundRequests.accept(frame) {
				if requestFailureHandler, ok := handler.(TunnelRequestFailureHandler); ok {
					requestFailureHandler.FailRequest(frame.RequestID, ErrTunnelProtocol)
				}
				inboundRequests.fail(frame.RequestID)
				if err := writer.writeInternal(requestSequenceError(frame.RequestID), false); err != nil {
					return ErrTunnelDisconnected
				}
				continue
			}
		} else if !acceptNextSequence(&inboundConnectionSequence, frame.Sequence) {
			return ErrTunnelProtocol
		}
		switch frame.Type {
		case FrameHeartbeat:
			if err := writer.writeInternal(TunnelFrame{Type: FrameHeartbeatAck}, false); err != nil {
				return ErrTunnelDisconnected
			}
		case FrameHeartbeatAck:
		case FrameEndpointRevoke:
			s.state.Stop()
			terminal = true
			return ErrEndpointRevoked
		default:
			if handler == nil || handler.HandleFrame(ctx, frame, writer) != nil {
				return ErrTunnelProtocol
			}
			if frame.Type == FrameCancel {
				inboundRequests.complete(frame.RequestID)
			}
		}
	}
}

func (s *TunnelSession) readFrame(socket TunnelSocket) (TunnelFrame, error) {
	messageType, data, err := socket.ReadMessage()
	if err != nil {
		return TunnelFrame{}, err
	}
	switch messageType {
	case websocket.TextMessage:
		return s.codec.DecodeText(data)
	case websocket.BinaryMessage:
		return s.codec.DecodeBinary(data)
	default:
		return TunnelFrame{}, ErrTunnelProtocol
	}
}

func matchingFrameIdentity(frame TunnelFrame, identity RunnerEndpointIdentity) bool {
	return frame.RunnerID == identity.RunnerID && frame.EndpointID == identity.EndpointID
}

func acceptNextSequence(previous *int64, next int64) bool {
	if previous == nil || next != *previous+1 {
		return false
	}
	*previous = next
	return true
}

func allowedInboundFrame(typ TunnelFrameType) bool {
	switch typ {
	case FrameRequestStart, FrameRequestChunk, FrameRequestEnd, FrameCancel, FrameError, FrameHeartbeat, FrameHeartbeatAck, FrameEndpointRevoke:
		return true
	default:
		return false
	}
}

func requestScopedInboundFrame(frame TunnelFrame) bool {
	if frame.RequestID == "" {
		return false
	}
	switch frame.Type {
	case FrameRequestStart, FrameRequestChunk, FrameRequestEnd, FrameCancel, FrameError:
		return true
	default:
		return false
	}
}

func requestSequenceError(requestID string) TunnelFrame {
	return TunnelFrame{
		Type:      FrameError,
		RequestID: requestID,
		Attributes: map[string]json.RawMessage{
			"code":      json.RawMessage(`"frame_malformed"`),
			"message":   json.RawMessage(`"Tunnel request sequence is invalid"`),
			"retryable": json.RawMessage(`false`),
		},
	}
}

type TunnelRequestFailureHandler interface {
	FailRequest(string, error)
}

type requestSequenceTracker struct {
	mu      sync.Mutex
	active  map[string]int64
	seen    map[string]bool
}

func newRequestSequenceTracker() *requestSequenceTracker {
	return &requestSequenceTracker{
		active: make(map[string]int64),
		seen:   make(map[string]bool),
	}
}

func (t *requestSequenceTracker) accept(frame TunnelFrame) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	previous, active := t.active[frame.RequestID]
	if frame.Type == FrameRequestStart {
		if active || t.seen[frame.RequestID] || frame.Sequence != 0 {
			return false
		}
		t.seen[frame.RequestID] = true
		t.active[frame.RequestID] = 0
		return true
	}
	if !active || frame.Sequence != previous+1 {
		return false
	}
	t.active[frame.RequestID] = frame.Sequence
	return true
}

func (t *requestSequenceTracker) fail(requestID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.active, requestID)
	t.seen[requestID] = true
}

func (t *requestSequenceTracker) complete(requestID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.active, requestID)
}

func allowedOutboundFrame(typ TunnelFrameType) bool {
	switch typ {
	case FrameResponseStart, FrameResponseChunk, FrameResponseEnd, FrameError, FrameHeartbeat, FrameHeartbeatAck:
		return true
	default:
		return false
	}
}

type sessionFrameWriter struct {
	mu                   sync.Mutex
	identity             RunnerEndpointIdentity
	socket               TunnelSocket
	codec                *TunnelCodec
	now                  func() time.Time
	connectionSequence   int64
	responseSequences    map[string]int64
	onRequestComplete    func(string)
}

func (w *sessionFrameWriter) WriteFrame(_ context.Context, frame TunnelFrame) error {
	return w.writeInternal(frame, false)
}

func (w *sessionFrameWriter) writeInternal(frame TunnelFrame, hello bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if (!hello && !allowedOutboundFrame(frame.Type)) || (hello && frame.Type != FrameHello) {
		return ErrTunnelProtocol
	}
	if frame.RunnerID != "" && frame.RunnerID != w.identity.RunnerID {
		return ErrTunnelProtocol
	}
	if frame.EndpointID != "" && frame.EndpointID != w.identity.EndpointID {
		return ErrTunnelProtocol
	}
	frame.Version = 1
	frame.RunnerID = w.identity.RunnerID
	frame.EndpointID = w.identity.EndpointID
	sequence, err := w.nextSequence(frame, hello)
	if err != nil {
		return err
	}
	frame.Sequence = sequence
	frame.Timestamp = w.now().UnixMilli()
	encoded, err := w.codec.Encode(frame)
	if err != nil {
		return err
	}
	messageType := websocket.TextMessage
	if encoded.Kind == MessageBinary {
		messageType = websocket.BinaryMessage
	}
	if err := w.socket.WriteMessage(messageType, encoded.Data); err != nil {
		return ErrTunnelDisconnected
	}
	if (frame.Type == FrameResponseEnd || frame.Type == FrameError) && frame.RequestID != "" {
		delete(w.responseSequences, frame.RequestID)
		if w.onRequestComplete != nil {
			w.onRequestComplete(frame.RequestID)
		}
	}
	return nil
}

func (w *sessionFrameWriter) nextSequence(frame TunnelFrame, hello bool) (int64, error) {
	if hello || frame.RequestID == "" {
		w.connectionSequence++
		return w.connectionSequence, nil
	}
	previous, exists := w.responseSequences[frame.RequestID]
	switch frame.Type {
	case FrameResponseStart:
		if exists {
			return 0, ErrTunnelProtocol
		}
		w.responseSequences[frame.RequestID] = 0
		return 0, nil
	case FrameError:
		if !exists {
			w.responseSequences[frame.RequestID] = 0
			return 0, nil
		}
		w.responseSequences[frame.RequestID] = previous + 1
		return previous + 1, nil
	case FrameResponseChunk, FrameResponseEnd:
		if !exists {
			return 0, ErrTunnelProtocol
		}
		w.responseSequences[frame.RequestID] = previous + 1
		return previous + 1, nil
	default:
		return 0, ErrTunnelProtocol
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func intJSON(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func boolJSON(value bool) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
