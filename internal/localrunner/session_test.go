package localrunner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSSDialAdapterUsesTicketOnlyInAuthorizationAndConsumesOnFailure(t *testing.T) {
	data := validOpenConnectionData(t)
	var gotURL string
	var gotHeader http.Header
	adapter := NewWSSDialAdapter(func(_ context.Context, rawURL string, header http.Header) (TunnelSocket, *http.Response, error) {
		gotURL = rawURL
		gotHeader = header.Clone()
		return nil, nil, errors.New("dial failed")
	})
	if _, err := adapter.Dial(context.Background(), *data, testIdentity(), time.Unix(100, 0)); !errors.Is(err, ErrWSSDialFailed) {
		t.Fatalf("dial error = %v", err)
	}
	if gotURL != data.WebSocketURL || gotHeader.Get("Authorization") == "" || len(gotHeader) != 1 {
		t.Fatalf("dial target/header shape mismatch: url=%q header_count=%d", gotURL, len(gotHeader))
	}
	if data.ConnectionTicket.empty() == false {
		t.Fatal("failed dial retained a reusable ticket")
	}
}

func TestTunnelSessionSendsHelloAcceptsAckAndAnswersHeartbeat(t *testing.T) {
	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	ack := validHelloAck()
	ack.Sequence = 0
	heartbeat := validFrame(FrameHeartbeat)
	heartbeat.Sequence = 1
	socket := &fakeTunnelSocket{reads: []socketMessage{
		encodeSocketMessage(t, codec, ack),
		encodeSocketMessage(t, codec, heartbeat),
	}}
	data := validOpenConnectionData(t)
	state := newTestMachine(t)
	handler := &recordingFrameHandler{}
	session := NewTunnelSession(testIdentity(), state, &staticSocketDialer{socket: socket}, codec)
	session.now = func() time.Time { return time.Unix(100, 0) }
	err := session.RunAttempt(context.Background(), *data, HelloConfig{
		AgentCardSHA256: "abc123",
		MaxConcurrent:   4,
		Streaming:       true,
	}, handler)
	if !errors.Is(err, ErrTunnelDisconnected) {
		t.Fatalf("session error = %v", err)
	}
	writes := socket.Writes()
	if len(writes) != 2 {
		t.Fatalf("writes = %d", len(writes))
	}
	hello, err := codec.DecodeText(writes[0].data)
	if err != nil || hello.Type != FrameHello || string(hello.Attributes["agentCardSha256"]) != `"abc123"` || string(hello.Attributes["maxConcurrent"]) != `4` || string(hello.Attributes["streaming"]) != `true` {
		t.Fatalf("hello = %#v, error = %v", hello, err)
	}
	heartbeatAck, err := codec.DecodeText(writes[1].data)
	if err != nil || heartbeatAck.Type != FrameHeartbeatAck || heartbeatAck.Sequence != 1 {
		t.Fatalf("heartbeat ack = %#v, error = %v", heartbeatAck, err)
	}
	if handler.failCalls != 1 || state.Snapshot().State != ConnectionStateDisconnected {
		t.Fatalf("fail calls=%d state=%s", handler.failCalls, state.Snapshot().State)
	}
}

func TestTunnelSessionRejectsDirectionOrSequenceViolation(t *testing.T) {
	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	for name, invalid := range map[string]TunnelFrame{
		"direction": func() TunnelFrame {
			frame := validFrame(FrameResponseStart)
			frame.Sequence = 1
			return frame
		}(),
		"sequence": func() TunnelFrame {
			frame := validFrame(FrameHeartbeat)
			frame.Sequence = 2
			return frame
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			socket := &fakeTunnelSocket{reads: []socketMessage{
				encodeSocketMessage(t, codec, validHelloAck()),
				encodeSocketMessage(t, codec, invalid),
			}}
			data := validOpenConnectionData(t)
			state := newTestMachine(t)
			session := NewTunnelSession(testIdentity(), state, &staticSocketDialer{socket: socket}, codec)
			session.now = func() time.Time { return time.Unix(100, 0) }
			err := session.RunAttempt(context.Background(), *data, HelloConfig{AgentCardSHA256: "sha", MaxConcurrent: 1}, &recordingFrameHandler{})
			if !errors.Is(err, ErrTunnelProtocol) || !socket.Closed() || state.Snapshot().State != ConnectionStateDisconnected {
				t.Fatalf("error=%v closed=%t state=%s", err, socket.Closed(), state.Snapshot().State)
			}
		})
	}
}

func TestTunnelSessionUsesIndependentPerRequestSequenceDirections(t *testing.T) {
	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	requestOneStart := validFrame(FrameRequestStart)
	requestOneStart.RequestID = "request-1"
	requestOneStart.Sequence = 0
	requestTwoStart := validFrame(FrameRequestStart)
	requestTwoStart.RequestID = "request-2"
	requestTwoStart.Sequence = 0
	requestOneChunk := validFrame(FrameRequestChunk)
	requestOneChunk.RequestID = "request-1"
	requestOneChunk.Sequence = 1
	requestOneChunk.Payload = []byte("one")
	requestTwoEnd := validFrame(FrameRequestEnd)
	requestTwoEnd.RequestID = "request-2"
	requestTwoEnd.Sequence = 1
	socket := &fakeTunnelSocket{reads: []socketMessage{
		encodeSocketMessage(t, codec, validHelloAck()),
		encodeSocketMessage(t, codec, requestOneStart),
		encodeSocketMessage(t, codec, requestTwoStart),
		encodeSocketMessage(t, codec, requestOneChunk),
		encodeSocketMessage(t, codec, requestTwoEnd),
	}}
	data := validOpenConnectionData(t)
	state := newTestMachine(t)
	handler := &sequenceReplyHandler{}
	session := NewTunnelSession(testIdentity(), state, &staticSocketDialer{socket: socket}, codec)
	session.now = func() time.Time { return time.Unix(100, 0) }
	if err := session.RunAttempt(context.Background(), *data, HelloConfig{AgentCardSHA256: "sha", MaxConcurrent: 2}, handler); !errors.Is(err, ErrTunnelDisconnected) {
		t.Fatalf("session error = %v", err)
	}
	writes := socket.Writes()
	if len(writes) != 5 {
		t.Fatalf("writes = %d", len(writes))
	}
	for index, want := range []struct {
		requestID string
		sequence  int64
	}{
		{requestID: "request-1", sequence: 0},
		{requestID: "request-2", sequence: 0},
		{requestID: "request-1", sequence: 1},
		{requestID: "request-2", sequence: 1},
	} {
		message := writes[index+1]
		var frame TunnelFrame
		var err error
		if message.typ == websocket.BinaryMessage {
			frame, err = codec.DecodeBinary(message.data)
		} else {
			frame, err = codec.DecodeText(message.data)
		}
		if err != nil || frame.RequestID != want.requestID || frame.Sequence != want.sequence {
			t.Fatalf("write[%d] frame=%#v error=%v", index, frame, err)
		}
	}
}

func TestTunnelSessionRequestSequenceFailureDoesNotFailOtherRequest(t *testing.T) {
	codec := NewTunnelCodec(DefaultMaxFrameBytes)
	first := validFrame(FrameRequestStart)
	first.RequestID = "bad-request"
	second := first
	second.Sequence = 0
	other := validFrame(FrameRequestStart)
	other.RequestID = "good-request"
	other.Sequence = 0
	socket := &fakeTunnelSocket{reads: []socketMessage{
		encodeSocketMessage(t, codec, validHelloAck()),
		encodeSocketMessage(t, codec, first),
		encodeSocketMessage(t, codec, second),
		encodeSocketMessage(t, codec, other),
	}}
	data := validOpenConnectionData(t)
	state := newTestMachine(t)
	handler := &recordingFrameHandler{}
	session := NewTunnelSession(testIdentity(), state, &staticSocketDialer{socket: socket}, codec)
	session.now = func() time.Time { return time.Unix(100, 0) }
	if err := session.RunAttempt(context.Background(), *data, HelloConfig{AgentCardSHA256: "sha", MaxConcurrent: 2}, handler); !errors.Is(err, ErrTunnelDisconnected) {
		t.Fatalf("session error = %v", err)
	}
	if len(handler.frames) != 2 || handler.frames[0].RequestID != "bad-request" || handler.frames[1].RequestID != "good-request" {
		t.Fatalf("handled frames = %#v", handler.frames)
	}
	writes := socket.Writes()
	if len(writes) != 2 {
		t.Fatalf("writes = %d", len(writes))
	}
	protocolError, err := codec.DecodeText(writes[1].data)
	if err != nil || protocolError.Type != FrameError || protocolError.RequestID != "bad-request" || protocolError.Sequence != 0 {
		t.Fatalf("protocol error = %#v, error = %v", protocolError, err)
	}
}

type socketMessage struct {
	typ  int
	data []byte
}

type fakeTunnelSocket struct {
	mu     sync.Mutex
	reads  []socketMessage
	writes []socketMessage
	closed bool
}

func (s *fakeTunnelSocket) ReadMessage() (int, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reads) == 0 {
		return 0, nil, io.EOF
	}
	message := s.reads[0]
	s.reads = s.reads[1:]
	return message.typ, append([]byte(nil), message.data...), nil
}

func (s *fakeTunnelSocket) WriteMessage(typ int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, socketMessage{typ: typ, data: append([]byte(nil), data...)})
	return nil
}

func (s *fakeTunnelSocket) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeTunnelSocket) Writes() []socketMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]socketMessage(nil), s.writes...)
}

func (s *fakeTunnelSocket) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func encodeSocketMessage(t *testing.T, codec *TunnelCodec, frame TunnelFrame) socketMessage {
	t.Helper()
	encoded, err := codec.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	typ := websocket.TextMessage
	if encoded.Kind == MessageBinary {
		typ = websocket.BinaryMessage
	}
	return socketMessage{typ: typ, data: encoded.Data}
}

type staticSocketDialer struct {
	socket TunnelSocket
}

func (d *staticSocketDialer) Dial(context.Context, OpenConnectionData, RunnerEndpointIdentity, time.Time) (TunnelSocket, error) {
	return d.socket, nil
}

type recordingFrameHandler struct {
	frames    []TunnelFrame
	failCalls int
}

type sequenceReplyHandler struct{}

func (h *sequenceReplyHandler) HandleFrame(ctx context.Context, frame TunnelFrame, writer TunnelFrameWriter) error {
	switch frame.Type {
	case FrameRequestStart:
		return writer.WriteFrame(ctx, TunnelFrame{Type: FrameResponseStart, RequestID: frame.RequestID, Attributes: map[string]json.RawMessage{
			"status":  json.RawMessage(`200`),
			"headers": json.RawMessage(`{}`),
		}})
	case FrameRequestChunk:
		return writer.WriteFrame(ctx, TunnelFrame{Type: FrameResponseChunk, RequestID: frame.RequestID, Payload: []byte("chunk")})
	case FrameRequestEnd:
		return writer.WriteFrame(ctx, TunnelFrame{Type: FrameResponseEnd, RequestID: frame.RequestID})
	default:
		return nil
	}
}

func (h *sequenceReplyHandler) FailAll(error) {}

func (h *recordingFrameHandler) HandleFrame(_ context.Context, frame TunnelFrame, _ TunnelFrameWriter) error {
	h.frames = append(h.frames, frame)
	return nil
}

func (h *recordingFrameHandler) FailAll(error) {
	h.failCalls++
}
